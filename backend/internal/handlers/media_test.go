package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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

func (m *mockStorage) Upload(_ context.Context, _ string, _ io.Reader, _ string) error {
	panic("not expected in these tests")
}
func (m *mockStorage) PresignGet(ctx context.Context, key string) (string, error) {
	return m.presignFn(ctx, key)
}

// --- helpers ---

func newChiRequest(method, path, paramKey, paramVal string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(paramKey, paramVal)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	return r
}

// --- Cycle 1: happy path ---

func TestGetURL_ReturnsPresignedURL(t *testing.T) {
	const mediaID = "abc-123"
	const storageKey = "media/123/photo.jpg"
	const presignedURL = "https://minio.example/media/123/photo.jpg?X-Amz-Signature=sig"

	db := &mockDB{
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
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
	return httptest.NewRequest("GET", "/api/media?"+query, nil)
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
	h.List(w, httptest.NewRequest("GET", "/api/media", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, capturedSQL, "WHERE")
	// only arg is offset=0
	assert.Equal(t, []any{0}, capturedArgs)
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
	assert.Equal(t, "2024-03-01", capturedArgs[0])
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
	assert.Equal(t, "2024-03-31", capturedArgs[0])
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
	h.List(w, listRequest("trip_id=uuid-1"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "trip_id = ANY")
	assert.Equal(t, []string{"uuid-1"}, capturedArgs[0])
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
	assert.Contains(t, capturedSQL, "trip_id IS NULL")
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
	h.List(w, listRequest("trip_id=uuid-1&trip_id=unassigned"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "trip_id = ANY")
	assert.Contains(t, capturedSQL, "trip_id IS NULL")
	assert.Equal(t, []string{"uuid-1"}, capturedArgs[0])
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
	assert.Equal(t, []string{"photo", "video"}, capturedArgs[0])
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
	h.List(w, listRequest("from_date=2024-01-01&trip_id=uuid-1&media_type=photo"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "taken_at >=")
	assert.Contains(t, capturedSQL, "trip_id = ANY")
	assert.Contains(t, capturedSQL, "media_type = ANY")
	assert.Equal(t, "2024-01-01", capturedArgs[0])
	assert.Len(t, capturedArgs, 4) // from_date, media_type, trip_id, offset
}

func TestList_DBError_Returns500(t *testing.T) {
	db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return nil, errors.New("connection refused")
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/media", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestList_EmptyResult_ReturnsEmptyArray(t *testing.T) {
	db := &mockDB{queryFn: func(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
		return emptyRows(), nil
	}}

	h := &MediaHandler{db: db, storage: &mockStorage{}}
	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/media", nil))

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "[]") // must be array, not null
}
