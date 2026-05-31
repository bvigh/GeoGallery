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
}

func (m *mockDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("not expected in these tests")
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
