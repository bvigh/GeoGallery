package handlers

// These unit tests mock the DB to exercise RunGPSMatcher's control flow; owner isolation
// against real gps_tracks rows is covered in ownership_integration_test.go.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Cycle 1: no unpositioned media → returns nil, no Exec ---

type mockRows struct {
	rows [][]any
	pos  int
	err  error // returned by Err once the rows are exhausted
}

func (r *mockRows) Next() bool {
	r.pos++
	return r.pos <= len(r.rows)
}

func (r *mockRows) Scan(dest ...any) error {
	row := r.rows[r.pos-1]
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = row[i].(string)
		case **string:
			if row[i] == nil {
				*p = nil
			} else {
				v := row[i].(string)
				*p = &v
			}
		case *time.Time:
			*p = row[i].(time.Time)
		case **float64:
			if row[i] == nil {
				*p = nil
			} else {
				v := row[i].(float64)
				*p = &v
			}
		case **time.Time:
			if row[i] == nil {
				*p = nil
			} else {
				v := row[i].(time.Time)
				*p = &v
			}
		}
	}
	return nil
}

func (r *mockRows) Close()                                       {}
func (r *mockRows) Err() error                                   { return r.err }
func (r *mockRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *mockRows) Values() ([]any, error)                       { return nil, nil }
func (r *mockRows) RawValues() [][]byte                          { return nil }
func (r *mockRows) Conn() *pgx.Conn                              { return nil }

type mockMatcherDB struct {
	queryFn    func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	queryRowFn func(ctx context.Context, sql string, args ...any) pgx.Row
	execFn     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (m *mockMatcherDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return m.queryFn(ctx, sql, args...)
}
func (m *mockMatcherDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return m.queryRowFn(ctx, sql, args...)
}
func (m *mockMatcherDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return m.execFn(ctx, sql, args...)
}

// noRowsRow is a pgx.Row that always returns ErrNoRows.
type noRowsRow struct{}

func (noRowsRow) Scan(...any) error { return pgx.ErrNoRows }

// trackPointRow is a pgx.Row that returns a single GPS track point, with bracketingPoints' columns.
type trackPointRow struct {
	lat, lng             float64
	recordedAt           time.Time
	source               string
	accuracyM, altitudeM *float64
}

func (r trackPointRow) Scan(dest ...any) error {
	*dest[0].(*float64) = r.lat
	*dest[1].(*float64) = r.lng
	*dest[2].(*time.Time) = r.recordedAt
	*dest[3].(*string) = r.source
	*dest[4].(**float64) = r.accuracyM
	*dest[5].(**float64) = r.altitudeM
	return nil
}

func TestRunGPSMatcher_NoMedia_ReturnsNil(t *testing.T) {
	execCalled := false
	db := &mockMatcherDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &mockRows{}, nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			panic("not expected")
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return pgconn.CommandTag{}, nil
		},
	}

	err := RunGPSMatcher(context.Background(), db, testOwnerID, time.Hour, false)

	require.NoError(t, err)
	assert.False(t, execCalled)
}

// --- Cycle 2: media bracketed by before+after → Exec called with interpolated coords ---

func TestRunGPSMatcher_BracketedMedia_UpdatesLocation(t *testing.T) {
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	beforeAt := takenAt.Add(-30 * time.Minute)
	afterAt := takenAt.Add(30 * time.Minute)

	// before point: Budapest
	const beforeLat, beforeLng = 47.49801, 19.03991
	// after point: slightly north-east
	const afterLat, afterLng = 47.50801, 19.05991

	queryRowCall := 0
	var execLng, execLat, execConf float64
	var execID string

	db := &mockMatcherDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &mockRows{rows: [][]any{
				{"media-1", takenAt},
			}}, nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			queryRowCall++
			if queryRowCall == 1 {
				return trackPointRow{lat: beforeLat, lng: beforeLng, recordedAt: beforeAt}
			}
			return trackPointRow{lat: afterLat, lng: afterLng, recordedAt: afterAt}
		},
		execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
			execLng = args[0].(float64)
			execLat = args[1].(float64)
			execConf = args[2].(float64)
			execID = args[3].(string)
			return pgconn.CommandTag{}, nil
		},
	}

	err := RunGPSMatcher(context.Background(), db, testOwnerID, 2*time.Hour, false)

	require.NoError(t, err)
	assert.Equal(t, "media-1", execID)
	// midpoint between before and after
	assert.InDelta(t, (beforeLat+afterLat)/2, execLat, 0.0001)
	assert.InDelta(t, (beforeLng+afterLng)/2, execLng, 0.0001)
	assert.Greater(t, execConf, 0.0)
	assert.LessOrEqual(t, execConf, 1.0)
}

// --- Cycle 3: no GPS tracks within window → Exec NOT called ---

func TestRunGPSMatcher_NoGPSTracks_SkipsMedia(t *testing.T) {
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	execCalled := false

	db := &mockMatcherDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &mockRows{rows: [][]any{
				{"media-2", takenAt},
			}}, nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return noRowsRow{}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return pgconn.CommandTag{}, nil
		},
	}

	err := RunGPSMatcher(context.Background(), db, testOwnerID, time.Hour, false)

	require.NoError(t, err)
	assert.False(t, execCalled)
}

// --- Cycle 4: only a before point within window → Exec called ---

func TestRunGPSMatcher_OnlyBeforePoint_UpdatesLocation(t *testing.T) {
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	beforeAt := takenAt.Add(-20 * time.Minute)
	const beforeLat, beforeLng = 47.49801, 19.03991

	queryRowCall := 0
	execCalled := false

	db := &mockMatcherDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &mockRows{rows: [][]any{
				{"media-3", takenAt},
			}}, nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			queryRowCall++
			if queryRowCall == 1 {
				return trackPointRow{lat: beforeLat, lng: beforeLng, recordedAt: beforeAt}
			}
			return noRowsRow{}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return pgconn.CommandTag{}, nil
		},
	}

	err := RunGPSMatcher(context.Background(), db, testOwnerID, time.Hour, false)

	require.NoError(t, err)
	assert.True(t, execCalled)
}

// --- Cycle 5: only an after point within window → Exec called ---

func TestRunGPSMatcher_OnlyAfterPoint_UpdatesLocation(t *testing.T) {
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	afterAt := takenAt.Add(20 * time.Minute)
	const afterLat, afterLng = 47.50801, 19.05991

	queryRowCall := 0
	execCalled := false

	db := &mockMatcherDB{
		queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
			return &mockRows{rows: [][]any{
				{"media-4", takenAt},
			}}, nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			queryRowCall++
			if queryRowCall == 1 {
				return noRowsRow{}
			}
			return trackPointRow{lat: afterLat, lng: afterLng, recordedAt: afterAt}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return pgconn.CommandTag{}, nil
		},
	}

	err := RunGPSMatcher(context.Background(), db, testOwnerID, time.Hour, false)

	require.NoError(t, err)
	assert.True(t, execCalled)
}

// --- Cycle 6: reprocess=true includes already-interpolated media --- reprocess=true includes already-interpolated media ---

func TestRunGPSMatcher_Reprocess_IncludesInterpolatedMedia(t *testing.T) {
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	beforeAt := takenAt.Add(-10 * time.Minute)
	const beforeLat, beforeLng = 47.49801, 19.03991

	var capturedSQL string
	queryRowCall := 0

	db := &mockMatcherDB{
		queryFn: func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
			capturedSQL = sql
			return &mockRows{rows: [][]any{
				{"media-5", takenAt},
			}}, nil
		},
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			queryRowCall++
			if queryRowCall == 1 {
				return trackPointRow{lat: beforeLat, lng: beforeLng, recordedAt: beforeAt}
			}
			return noRowsRow{}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, nil
		},
	}

	err := RunGPSMatcher(context.Background(), db, testOwnerID, time.Hour, true)

	require.NoError(t, err)
	assert.Contains(t, capturedSQL, "interpolated")
}

// --- Owner scoping: media select and both bracketing lookups filter on the owner ---

func TestRunGPSMatcher_ScopesQueriesToOwner(t *testing.T) {
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	for _, reprocess := range []bool{false, true} {
		t.Run(fmt.Sprintf("reprocess=%v", reprocess), func(t *testing.T) {
			var querySQL string
			var queryArgs []any
			var rowSQLs []string
			var rowArgs [][]any
			db := &mockMatcherDB{
				queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
					querySQL, queryArgs = sql, args
					return &mockRows{rows: [][]any{{"media-1", takenAt}}}, nil
				},
				queryRowFn: func(_ context.Context, sql string, args ...any) pgx.Row {
					rowSQLs = append(rowSQLs, sql)
					rowArgs = append(rowArgs, args)
					return noRowsRow{}
				},
				execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
					t.Fatal("no track points, so no update expected")
					return pgconn.CommandTag{}, nil
				},
			}

			require.NoError(t, RunGPSMatcher(context.Background(), db, testOwnerID, time.Hour, reprocess))

			assert.Contains(t, querySQL, "owner_id = $1")
			assert.Equal(t, []any{testOwnerID}, queryArgs)
			require.Len(t, rowSQLs, 2)
			for i := range rowSQLs {
				assert.Contains(t, rowSQLs[i], "owner_id = $3")
				require.Len(t, rowArgs[i], 4)
				assert.Equal(t, testOwnerID, rowArgs[i][2])
				assert.Equal(t, []string{}, rowArgs[i][3], "the matcher excludes nothing")
			}
		})
	}
}

// --- bracketingPoints: the column list, the tie-break and the exclusions ---

// recordBracketQueries answers both bracket queries with rows and records what they were sent.
func recordBracketQueries(rows ...pgx.Row) (db *mockMatcherDB, sqls *[]string, args *[][]any) {
	sqls, args = &[]string{}, &[][]any{}
	db = &mockMatcherDB{
		queryRowFn: func(_ context.Context, sql string, a ...any) pgx.Row {
			*sqls = append(*sqls, sql)
			*args = append(*args, a)
			return rows[len(*sqls)-1]
		},
	}
	return db, sqls, args
}

func TestBracketingPoints_ReturnsEveryColumn(t *testing.T) {
	at := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	accuracy, altitude := 4.5, 120.0
	beforeRow := trackPointRow{lat: 10, lng: 20, recordedAt: at.Add(-time.Minute), source: "gpx:ride.gpx", accuracyM: &accuracy, altitudeM: &altitude}
	afterRow := trackPointRow{lat: 11, lng: 21, recordedAt: at.Add(time.Minute), source: "exif"}
	db, _, _ := recordBracketQueries(beforeRow, afterRow)

	before, after, err := bracketingPoints(context.Background(), db, testOwnerID, at, time.Hour, bracketOptions{})

	require.NoError(t, err)
	require.NotNil(t, before)
	require.NotNil(t, after)
	assert.Equal(t, 10.0, before.Lat)
	assert.Equal(t, 20.0, before.Lng)
	assert.Equal(t, at.Add(-time.Minute), before.At)
	assert.Equal(t, "gpx:ride.gpx", before.Source)
	assert.Equal(t, &accuracy, before.AccuracyM)
	assert.Equal(t, &altitude, before.AltitudeM)
	assert.Equal(t, "exif", after.Source)
	assert.Nil(t, after.AccuracyM)
	assert.Nil(t, after.AltitudeM)
}

func TestBracketingPoints_TieBreakIsDeterministic(t *testing.T) {
	db, sqls, _ := recordBracketQueries(noRowsRow{}, noRowsRow{})

	_, _, err := bracketingPoints(context.Background(), db, testOwnerID, time.Now(), time.Hour, bracketOptions{})

	require.NoError(t, err)
	require.Len(t, *sqls, 2)
	assert.Contains(t, (*sqls)[0], "ORDER BY recorded_at DESC, source ASC")
	assert.Contains(t, (*sqls)[1], "ORDER BY recorded_at ASC, source ASC")
	for _, sql := range *sqls {
		assert.Contains(t, sql, "LIMIT 1")
		assert.Contains(t, sql, "accuracy_m, altitude_m")
	}
}

func TestBracketingPoints_Exclusions(t *testing.T) {
	tests := []struct {
		name string
		opts bracketOptions
		want []string
	}{
		// x <> ALL(NULL) matches nothing, so nil must arrive as an empty, non-nil list.
		{"nil is normalised", bracketOptions{}, []string{}},
		{"empty stays empty", bracketOptions{excludeFormats: []string{}}, []string{}},
		{"formats are passed through", bracketOptions{excludeFormats: []string{"exif", "manual"}}, []string{"exif", "manual"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, sqls, args := recordBracketQueries(noRowsRow{}, noRowsRow{})

			_, _, err := bracketingPoints(context.Background(), db, testOwnerID, time.Now(), time.Hour, tt.opts)

			require.NoError(t, err)
			require.Len(t, *sqls, 2)
			for i := range *sqls {
				assert.Contains(t, (*sqls)[i], "split_part(source, ':', 1) <> ALL($4::text[])")
				require.Len(t, (*args)[i], 4)
				got, ok := (*args)[i][3].([]string)
				require.True(t, ok)
				assert.NotNil(t, got)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func TestBracketingPoints_QueryErrorIsReturned(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		rows []pgx.Row
	}{
		{"before query", []pgx.Row{errRow{boom}, noRowsRow{}}},
		{"after query", []pgx.Row{noRowsRow{}, errRow{boom}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, _, _ := recordBracketQueries(tt.rows...)

			before, after, err := bracketingPoints(context.Background(), db, testOwnerID, time.Now(), time.Hour, bracketOptions{})

			require.ErrorIs(t, err, boom)
			assert.Nil(t, before)
			assert.Nil(t, after)
		})
	}
}
