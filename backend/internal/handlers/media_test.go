package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mocks ---

type mockRow struct {
	scanFn func(dest ...any) error
}

func (r *mockRow) Scan(dest ...any) error { return r.scanFn(dest...) }

type mockDB struct {
	queryRowFn func(ctx context.Context, sql string, args ...any) pgx.Row
	queryFn    func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (m *mockDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, sql, args...)
	}
	panic("queryFn not set")
}
func (m *mockDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return m.queryRowFn(ctx, sql, args...)
}
func (m *mockDB) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	panic("not expected in these tests")
}

type mockStorage struct {
	presignFn func(ctx context.Context, key string) (string, error)
}

func (m *mockStorage) Upload(_ context.Context, _ string, _ io.Reader, _ int64, _ string) error {
	panic("not expected in these tests")
}
func (m *mockStorage) PresignGet(ctx context.Context, key string) (string, error) {
	return m.presignFn(ctx, key)
}
func (m *mockStorage) Delete(_ context.Context, _ string) error {
	panic("not expected in these tests")
}

// --- helpers ---

func newChiRequest(method, path, paramKey, paramVal string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(paramKey, paramVal)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	return withTestUser(r)
}

// --- Cycle 1: happy path ---

func TestGetURL_ReturnsPresignedURL(t *testing.T) {
	const mediaID = "abc-123"
	const storageKey = "media/123/photo.jpg"
	const presignedURL = "https://minio.example/media/123/photo.jpg?X-Amz-Signature=sig"

	db := &mockDB{
		queryRowFn: func(_ context.Context, sql string, args ...any) pgx.Row {
			assert.Contains(t, sql, "FROM media m WHERE m.id=$1 AND "+readableMediaCond("m", 2))
			assert.Equal(t, []any{mediaID, testOwnerID}, args)
			return &mockRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = storageKey
				return nil
			}}
		},
	}
	store := &mockStorage{
		presignFn: func(_ context.Context, key string) (string, error) {
			assert.Equal(t, storageKey, key)
			return presignedURL, nil
		},
	}

	h := &MediaHandler{db: db, storage: store}
	w := httptest.NewRecorder()
	r := newChiRequest("GET", "/api/media/"+mediaID+"/url", "id", mediaID)

	h.GetURL(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var body map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, presignedURL, body["url"])
}

// --- Cycle 2: media not found ---

func TestGetURL_NotFound_Returns404(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			return &mockRow{scanFn: func(dest ...any) error {
				return pgx.ErrNoRows
			}}
		},
	}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	r := newChiRequest("GET", "/api/media/missing/url", "id", "missing")

	h.GetURL(w, r)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Cycle 3: presign error ---

func TestGetURL_PresignError_Returns500(t *testing.T) {
	db := &mockDB{
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			return &mockRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = "media/123/photo.jpg"
				return nil
			}}
		},
	}
	store := &mockStorage{
		presignFn: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("s3 unavailable")
		},
	}

	h := &MediaHandler{db: db, storage: store}
	w := httptest.NewRecorder()
	r := newChiRequest("GET", "/api/media/abc/url", "id", "abc")

	h.GetURL(w, r)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- List tests ---

// emptyRows returns a pgx.Rows with zero rows, reusing mockRows from gps_matcher_test.go.
func emptyRows() *mockRows { return &mockRows{} }

func listRequest(query string) *http.Request {
	return withTestUser(httptest.NewRequest("GET", "/api/media?"+query, nil))
}

func TestList_NoParams_ReturnsAll(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, withTestUser(httptest.NewRequest("GET", "/api/media", nil)))

	require.Equal(t, http.StatusOK, w.Code)
	sql := strings.Join(strings.Fields(capturedSQL), " ")
	from := strings.Join(strings.Fields("FROM media m JOIN users u ON u.id = m.owner_id WHERE "+readableMediaCond("m", 1)), " ")
	assert.Equal(t, 2, strings.Count(sql, from+" AND "), "read access starts both parts")
	assert.Contains(t, sql, from+" AND m.taken_at IS NOT NULL ORDER BY m.taken_at DESC, m.id LIMIT $2)",
		"dated items: read access is the only filter")
	assert.Contains(t, sql, from+" AND m.taken_at IS NULL ORDER BY m.id LIMIT $2)", "then the undated ones")
	assert.Regexp(t, `\) page ORDER BY page\.taken_at DESC NULLS LAST, page\.id LIMIT \$2$`, sql)
	assert.Equal(t, []any{testOwnerID, defaultListLimit + 1}, capturedArgs, "owner, and one row more than a page")
	assert.Empty(t, w.Header().Get(nextCursorHeader), "no rows: no next page")
}

func TestList_OwnerFieldsAndTripIDOnlyForOwnRows(t *testing.T) {
	var capturedSQL string
	takenAt := time.Date(2024, 5, 2, 12, 0, 0, 0, time.UTC)
	db := &mockDB{queryFn: func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
		capturedSQL = sql
		// id, filename, media_type, lat, lng, gps_confidence, gps_source, taken_at, storage_key,
		// trip_id, owner_id, owner_name, initial_yaw, initial_pitch, initial_fov
		return &mockRows{rows: [][]any{
			{"own", "a.jpg", "photo", 47.5, 19.0, 1.0, "exif", takenAt, "media/k1.jpg",
				"trip-1", testOwnerID, "Me", nil, nil, nil},
			{"shared", "b.jpg", "photo", nil, nil, nil, nil, nil, "media/k2.jpg",
				nil, "22222222-2222-4222-8222-222222222222", nil, nil, nil, nil},
		}}, nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, withTestUser(httptest.NewRequest("GET", "/api/media", nil)))

	require.Equal(t, http.StatusOK, w.Code)
	sql := strings.Join(strings.Fields(capturedSQL), " ")
	assert.Contains(t, sql, "CASE WHEN m.owner_id = $1 THEN m.trip_id END AS trip_id",
		"trips are personal: another owner's trip_id is never returned")
	assert.Contains(t, sql, "m.owner_id, COALESCE(u.display_name, u.email) AS owner_name")
	assert.Contains(t, sql, "FROM media m JOIN users u ON u.id = m.owner_id WHERE")
	assert.JSONEq(t, `[
		{"id":"own","filename":"a.jpg","media_type":"photo","lat":47.5,"lng":19,"gps_confidence":1,
		 "gps_source":"exif","taken_at":"2024-05-02T12:00:00Z","storage_key":"media/k1.jpg",
		 "trip_id":"trip-1","owner_id":"`+testOwnerID+`","owner_name":"Me",
		 "initial_yaw":null,"initial_pitch":null,"initial_fov":null},
		{"id":"shared","filename":"b.jpg","media_type":"photo","lat":null,"lng":null,"gps_confidence":null,
		 "gps_source":null,"taken_at":null,"storage_key":"media/k2.jpg",
		 "trip_id":null,"owner_id":"22222222-2222-4222-8222-222222222222","owner_name":null,
		 "initial_yaw":null,"initial_pitch":null,"initial_fov":null}
	]`, w.Body.String())
}

func TestList_FromDate_FilteredInSQL(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("from_date=2024-03-01"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "taken_at >=")
	assert.Equal(t, "2024-03-01", capturedArgs[1])
}

func TestList_ToDate_FilteredInSQL(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("to_date=2024-03-31"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "taken_at <")
	assert.Equal(t, "2024-03-31", capturedArgs[1])
}

func TestList_TripID_FilteredInSQL(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("trip_id=11111111-1111-1111-1111-111111111111"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "(m.owner_id = $1 AND m.trip_id = ANY($2))")
	assert.Equal(t, []string{"11111111-1111-1111-1111-111111111111"}, capturedArgs[1])
}

func TestList_TripIDUnassigned_FilteredInSQL(t *testing.T) {
	var capturedSQL string

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("trip_id=unassigned"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "(m.owner_id = $1 AND m.trip_id IS NULL)")
	assert.NotContains(t, capturedSQL, "trip_id = ANY")
}

func TestList_TripIDMixed_FilteredInSQL(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("trip_id=11111111-1111-1111-1111-111111111111&trip_id=unassigned"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "(m.owner_id = $1 AND (m.trip_id = ANY($2) OR m.trip_id IS NULL))")
	assert.Equal(t, []string{"11111111-1111-1111-1111-111111111111"}, capturedArgs[1])
}

func TestList_MediaType_FilteredInSQL(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("media_type=photo&media_type=video"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "media_type = ANY")
	assert.Equal(t, []string{"photo", "video"}, capturedArgs[1])
}

// A client paging with offset would get the first page forever if offset were ignored.
func TestList_Offset_Returns400(t *testing.T) {
	for _, query := range []string{"offset=500", "offset=0", "offset=", "limit=5000&offset=5000"} {
		t.Run(query, func(t *testing.T) {
			db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
				t.Fatal("no query with an offset")
				return nil, nil
			}}

			h := &MediaHandler{db: db, storage: &mockStorage{}}
			w := httptest.NewRecorder()
			h.List(w, listRequest(query))

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "offset is not supported: pass the X-Next-Cursor header of the previous page as cursor\n", w.Body.String())
		})
	}
}

func TestList_Cursor_BoundAfterTheFilters(t *testing.T) {
	takenAt := time.Date(2024, 5, 2, 12, 0, 0, 123456000, time.UTC)
	const id = "22222222-2222-4222-8222-222222222222"
	dated := mediaCursor{takenAt: &takenAt, id: id}.String()
	undated := mediaCursor{id: id}.String()

	tests := []struct {
		name     string
		query    string
		wantArgs []any
		want     []string // in the SQL, whitespace collapsed
		notWant  []string
	}{
		{"dated", "cursor=" + dated,
			[]any{testOwnerID, defaultListLimit + 1, takenAt, id},
			[]string{
				"AND m.taken_at <= $3 AND (m.taken_at < $3 OR m.id > $4) ORDER BY m.taken_at DESC, m.id LIMIT $2)",
				"AND m.taken_at IS NULL ORDER BY m.id LIMIT $2)", // every undated item follows a dated one
			},
			[]string{"m.taken_at IS NOT NULL", "m.taken_at IS NULL AND m.id"}},
		{"undated", "cursor=" + undated,
			[]any{testOwnerID, defaultListLimit + 1, id},
			[]string{"AND m.taken_at IS NULL AND m.id > $3 ORDER BY m.id LIMIT $2)"},
			[]string{"m.taken_at IS NOT NULL", "m.taken_at <=", "UNION ALL"}},
		{"after the filters and the limit", "from_date=2024-01-01&media_type=photo&limit=50&cursor=" + dated,
			[]any{testOwnerID, "2024-01-01", []string{"photo"}, 51, takenAt, id},
			[]string{"m.taken_at <= $5 AND (m.taken_at < $5 OR m.id > $6) ORDER BY m.taken_at DESC, m.id LIMIT $4)"},
			nil},
		{"an empty cursor is no cursor", "cursor=",
			[]any{testOwnerID, defaultListLimit + 1},
			[]string{"m.taken_at IS NOT NULL", "m.taken_at IS NULL ORDER BY m.id LIMIT $2)"},
			[]string{"m.taken_at <="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedSQL string
			var capturedArgs []any
			db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
				capturedSQL = sql
				capturedArgs = args
				return emptyRows(), nil
			}}

			h := &MediaHandler{db: db, storage: &mockStorage{}}
			w := httptest.NewRecorder()
			h.List(w, listRequest(tt.query))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Len(t, capturedArgs, len(tt.wantArgs))
			for i, want := range tt.wantArgs {
				if wantTime, ok := want.(time.Time); ok {
					got, ok := capturedArgs[i].(time.Time)
					require.True(t, ok, "arg %d is a time", i+1)
					assert.True(t, wantTime.Equal(got), "arg %d: the cursor's instant to the microsecond, got %v", i+1, got)
					continue
				}
				assert.Equal(t, want, capturedArgs[i], "arg %d", i+1)
			}
			sql := strings.Join(strings.Fields(capturedSQL), " ")
			for _, s := range tt.want {
				assert.Contains(t, sql, s)
			}
			for _, s := range tt.notWant {
				assert.NotContains(t, sql, s)
			}
		})
	}
}

// The cursor's ID is bound as auth.CanonicalUUID spells it, as filter IDs are: Postgres only takes
// a dash as a separator.
func TestList_Cursor_BindsCanonicalID(t *testing.T) {
	raw := "22222222x2222x4222x8222x222222222222"
	cursor := base64.RawURLEncoding.EncodeToString([]byte(" " + raw))
	var capturedArgs []any
	db := &mockDB{queryFn: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("cursor="+cursor))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []any{testOwnerID, defaultListLimit + 1, "22222222-2222-4222-8222-222222222222"}, capturedArgs)
}

func TestList_InvalidCursor_Returns400(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	const id = "22222222-2222-4222-8222-222222222222"
	tests := []struct{ name, cursor string }{
		{"not base64url", "not a cursor!"},
		{"padded", base64.URLEncoding.EncodeToString([]byte(" " + id))},
		{"no separator", enc(id)},
		{"not an ID", enc("2024-05-02T12:00:00Z 42")},
		{"no ID", enc("2024-05-02T12:00:00Z ")},
		{"not a time", enc("yesterday " + id)},
		{"no zone", enc("2024-05-02T12:00:00 " + id)},
		{"a year RFC 3339 cannot hold", enc("10000-01-01T00:00:00Z " + id)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
				t.Fatal("no query for an invalid cursor")
				return nil, nil
			}}

			h := &MediaHandler{db: db, storage: &mockStorage{}}
			w := httptest.NewRecorder()
			h.List(w, listRequest("cursor="+url.QueryEscape(tt.cursor)))

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Equal(t, "invalid cursor\n", w.Body.String())
		})
	}
}

// listRow is a row of the list query for mockRows, owned by the caller, with nothing set that the
// paging does not look at.
func listRow(id string, takenAt *time.Time) []any {
	var at any
	if takenAt != nil {
		at = *takenAt
	}
	return []any{id, id + ".jpg", "photo", nil, nil, nil, nil, at, "media/" + id,
		nil, testOwnerID, nil, nil, nil, nil}
}

// Item IDs for mockRows: the cursor carries an ID, so it must be one.
const (
	itemA = "00000000-0000-4000-8000-00000000000a"
	itemB = "00000000-0000-4000-8000-00000000000b"
	itemC = "00000000-0000-4000-8000-00000000000c"
)

func TestList_NextCursor(t *testing.T) {
	at := func(sec int) *time.Time {
		t := time.Date(2024, 5, 2, 12, 0, sec, 0, time.UTC)
		return &t
	}
	tests := []struct {
		name       string
		rows       [][]any
		wantIDs    []string
		wantCursor *mediaCursor // nil: the last page
	}{
		{"a row more than the page: the page ends at its last item", [][]any{
			listRow(itemA, at(3)), listRow(itemB, at(2)), listRow(itemC, at(1)),
		}, []string{itemA, itemB}, &mediaCursor{takenAt: at(2), id: itemB}},
		{"an undated last item", [][]any{
			listRow(itemA, at(3)), listRow(itemB, nil), listRow(itemC, nil),
		}, []string{itemA, itemB}, &mediaCursor{id: itemB}},
		{"exactly a page", [][]any{
			listRow(itemA, at(3)), listRow(itemB, at(2)),
		}, []string{itemA, itemB}, nil},
		{"less than a page", [][]any{listRow(itemA, at(3))}, []string{itemA}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &mockDB{queryFn: func(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
				assert.Equal(t, 3, args[len(args)-1], "a page of 2 asks for 3 rows")
				return &mockRows{rows: tt.rows}, nil
			}}

			h := &MediaHandler{db: db, storage: &mockStorage{}}
			w := httptest.NewRecorder()
			h.List(w, listRequest("limit=2"))

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.wantIDs, decodeListIDs(t, w.Body.Bytes()))
			header := w.Header().Get(nextCursorHeader)
			if tt.wantCursor == nil {
				assert.Empty(t, header)
				return
			}
			got, err := parseMediaCursor(header)
			require.NoError(t, err, header)
			assert.Equal(t, tt.wantCursor.id, got.id)
			if tt.wantCursor.takenAt == nil {
				assert.Nil(t, got.takenAt)
			} else {
				require.NotNil(t, got.takenAt)
				assert.True(t, tt.wantCursor.takenAt.Equal(*got.takenAt))
			}
		})
	}
}

// scanFailRows fails to scan its row number fail (1-based), as a row of an unexpected shape would.
type scanFailRows struct {
	*mockRows
	fail int
}

func (r *scanFailRows) Scan(dest ...any) error {
	if r.pos == r.fail {
		return errors.New("cannot scan")
	}
	return r.mockRows.Scan(dest...)
}

// The page is the rows read, so a row that cannot be scanned is logged and left out without
// ending the list: the cursor is the last item listed, and the next page reads the row again.
func TestList_NextCursor_AfterARowThatFailedToScan(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	at := time.Date(2024, 5, 2, 12, 0, 0, 0, time.UTC)
	db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return &scanFailRows{mockRows: &mockRows{rows: [][]any{
			listRow(itemA, &at), listRow(itemB, &at), listRow(itemC, &at),
		}}, fail: 2}, nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("limit=2"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{itemA}, decodeListIDs(t, w.Body.Bytes()))
	got, err := parseMediaCursor(w.Header().Get(nextCursorHeader))
	require.NoError(t, err)
	assert.Equal(t, itemA, got.id, "the next page starts after A, so B is read again")
	assert.Contains(t, logs.String(), "media: list row scan failed")
}

func decodeListIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var items []struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &items), string(body))
	ids := []string{}
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func TestList_MultipleFilters_Combined(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("from_date=2024-01-01&trip_id=11111111-1111-1111-1111-111111111111&media_type=photo"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "taken_at >=")
	assert.Contains(t, capturedSQL, "trip_id = ANY")
	assert.Contains(t, capturedSQL, "media_type = ANY")
	assert.Contains(t, capturedSQL, "WHERE "+readableMediaCond("m", 1)+" AND")
	assert.Equal(t, testOwnerID, capturedArgs[0])
	assert.Equal(t, "2024-01-01", capturedArgs[1])
	assert.Len(t, capturedArgs, 5) // owner, from_date, media_type, trip_id, limit
}

func TestList_Limit_PassedThrough(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("limit=5000"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 3, strings.Count(capturedSQL, "LIMIT $2"), "both parts and the merge")
	assert.Equal(t, []any{testOwnerID, 5001}, capturedArgs, "one row more than the page")
}

func TestList_InvalidLimit_Returns400(t *testing.T) {
	for _, limit := range []string{"0", "-1", "5001", "abc", "1.5"} {
		t.Run(limit, func(t *testing.T) {
			db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
				t.Fatal("no query for an invalid limit")
				return nil, nil
			}}

			h := &MediaHandler{db: db, storage: &mockStorage{}}
			w := httptest.NewRecorder()
			h.List(w, listRequest("limit="+limit))

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "invalid limit: want 1-5000")
		})
	}
}

func TestList_DBError_Returns500(t *testing.T) {
	db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return nil, errors.New("connection refused")
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, withTestUser(httptest.NewRequest("GET", "/api/media", nil)))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// pgx reports an error raised while the query runs (a failed cast, a dropped connection) from
// rows.Err, not from Query: the list must not turn it into 200 with what was read so far.
func TestList_RowsError_Returns500(t *testing.T) {
	db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return &mockRows{err: errors.New("invalid input syntax for type uuid")}, nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, withTestUser(httptest.NewRequest("GET", "/api/media", nil)))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "[]")
}

// A row stored before registerMedia refused such times can still hold a year time.Time does not
// marshal. Nothing can be sent then, and the reason has to reach the log.
func TestList_EncodeError_IsLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	takenAt := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return &mockRows{rows: [][]any{
			{"id-1", "a.jpg", "photo", nil, nil, nil, nil, takenAt, "media/k1.jpg",
				nil, testOwnerID, nil, nil, nil, nil},
		}}, nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	h.List(httptest.NewRecorder(), withTestUser(httptest.NewRequest("GET", "/api/media", nil)))

	assert.Contains(t, logs.String(), "media: encode list failed")
	assert.Contains(t, logs.String(), "year outside of range")
}

func TestList_EmptyResult_ReturnsEmptyArray(t *testing.T) {
	db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, withTestUser(httptest.NewRequest("GET", "/api/media", nil)))

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "[]") // must be array, not null
}

// --- KnownHashes tests ---

func knownHashesRequest(body string) *http.Request {
	return withTestUser(httptest.NewRequest(http.MethodPost, "/api/media/known-hashes", strings.NewReader(body)))
}

func TestKnownHashes_InvalidBody_Returns400(t *testing.T) {
	valid := strings.Repeat("a", 64)
	tooMany := `{"hashes":[` + strings.TrimSuffix(strings.Repeat(`"`+valid+`",`, maxKnownHashes+1), ",") + `]}`
	tests := []struct {
		name string
		body string
	}{
		{"not JSON", `nope`},
		{"no hashes", `{}`},
		{"empty list", `{"hashes":[]}`},
		{"too many", tooMany},
		{"too short", `{"hashes":["abc"]}`},
		{"upper case", `{"hashes":["` + strings.Repeat("A", 64) + `"]}`},
		{"not hex", `{"hashes":["` + strings.Repeat("g", 64) + `"]}`},
		{"one bad among good", `{"hashes":["` + valid + `","x"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &MediaHandler{db: &mockDB{}} // panics if queried
			w := httptest.NewRecorder()
			h.KnownHashes(w, knownHashesRequest(tt.body))
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestKnownHashes_ReturnsOwnMatches(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var gotSQL string
	var gotArgs []any
	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		gotSQL, gotArgs = sql, args
		return &mockRows{rows: [][]any{{a, "media-a"}}}, nil
	}}
	h := &MediaHandler{db: db}

	w := httptest.NewRecorder()
	h.KnownHashes(w, knownHashesRequest(`{"hashes":["`+a+`","`+b+`"]}`))

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"known":{"`+a+`":"media-a"}}`, w.Body.String())
	assert.Contains(t, gotSQL, "owner_id = $1 AND file_hash = ANY($2)")
	assert.Equal(t, []any{testOwnerID, []string{a, b}}, gotArgs)
}

func TestKnownHashes_DBErrors_Return500(t *testing.T) {
	valid := `{"hashes":["` + strings.Repeat("a", 64) + `"]}`
	tests := []struct {
		name    string
		queryFn func(context.Context, string, ...any) (pgx.Rows, error)
	}{
		{"query fails", func(context.Context, string, ...any) (pgx.Rows, error) { return nil, errors.New("db down") }},
		{"rows fail", func(context.Context, string, ...any) (pgx.Rows, error) {
			return &mockRows{err: errors.New("rows broke")}, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &MediaHandler{db: &mockDB{queryFn: tt.queryFn}}
			w := httptest.NewRecorder()
			h.KnownHashes(w, knownHashesRequest(valid))
			assert.Equal(t, http.StatusInternalServerError, w.Code)
		})
	}
}
