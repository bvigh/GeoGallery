package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	assert.Regexp(t, `FROM media m JOIN users u ON u\.id = m\.owner_id\s+WHERE `+
		regexp.QuoteMeta(readableMediaCond("m", 1))+`\s+ORDER BY`, capturedSQL, "read access is the only condition")
	assert.Equal(t, []any{testOwnerID, defaultListLimit, 0}, capturedArgs, "owner, limit, offset")
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

func TestList_Offset_PassedThrough(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any

	db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		capturedSQL = sql
		capturedArgs = args
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, listRequest("offset=500"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "OFFSET")
	assert.Equal(t, 500, capturedArgs[len(capturedArgs)-1])
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
	assert.Len(t, capturedArgs, 6) // owner, from_date, media_type, trip_id, limit, offset
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
	h.List(w, listRequest("limit=5000&offset=5000"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "LIMIT $2 OFFSET $3")
	assert.Equal(t, []any{testOwnerID, 5000, 5000}, capturedArgs)
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
