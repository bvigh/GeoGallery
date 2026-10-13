package handlers

// TODO: PostGIS integration tests (querying real gps_tracks rows) are deferred.
// These unit tests mock the DB to exercise RunGPSMatcher's control flow.

import (
	"context"
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
func (r *mockRows) Err() error                                   { return nil }
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

// trackPointRow is a pgx.Row that returns a single GPS track point.
type trackPointRow struct {
	lat, lng   float64
	recordedAt time.Time
}

func (r trackPointRow) Scan(dest ...any) error {
	*dest[0].(*float64) = r.lat
	*dest[1].(*float64) = r.lng
	*dest[2].(*time.Time) = r.recordedAt
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

	err := RunGPSMatcher(context.Background(), db, time.Hour, false)

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

	err := RunGPSMatcher(context.Background(), db, 2*time.Hour, false)

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

	err := RunGPSMatcher(context.Background(), db, time.Hour, false)

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

	err := RunGPSMatcher(context.Background(), db, time.Hour, false)

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

	err := RunGPSMatcher(context.Background(), db, time.Hour, false)

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

	err := RunGPSMatcher(context.Background(), db, time.Hour, true)

	require.NoError(t, err)
	assert.Contains(t, capturedSQL, "interpolated")
}
