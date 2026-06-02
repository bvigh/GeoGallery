package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockUploadStorage allows Upload to succeed without S3.
type mockUploadStorage struct{}

func (m *mockUploadStorage) Upload(_ context.Context, _ string, _ io.Reader, _ string) error {
	return nil
}
func (m *mockUploadStorage) PresignGet(_ context.Context, _ string) (string, error) {
	panic("not expected")
}

// uploadOneFile builds a minimal multipart/form-data request with one file.
func uploadOneFile(filename, content string) *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", filename)
	fw.Write([]byte(content))
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/media/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

// --- Cycle 4: Upload calls matcher with reprocess=true after inserting ---

func TestUpload_CallsMatcherWithReprocess(t *testing.T) {
	execDB := &mockMediaExecDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil
		},
	}

	var capturedReprocess bool
	matcherCalled := false

	h := &MediaHandler{
		db:      execDB,
		storage: &mockUploadStorage{},
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, reprocess bool) error {
			matcherCalled = true
			capturedReprocess = reprocess
			return nil
		},
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ int) error { return nil },
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadOneFile("photo.jpg", "fake-jpeg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, matcherCalled)
	assert.True(t, capturedReprocess)
}

// --- Cycle 5: Scan calls matcher with reprocess=true after walking directory ---

func TestScan_CallsMatcherWithReprocess(t *testing.T) {
	var capturedReprocess bool
	matcherCalled := false

	// watchDir with no files → walk finishes immediately, matcher still called.
	h := &IngestHandler{
		db:       &mockIngestDB{},
		storage:  nil,
		watchDir: t.TempDir(),
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, reprocess bool) error {
			matcherCalled = true
			capturedReprocess = reprocess
			return nil
		},
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ int) error { return nil },
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/ingest/scan", nil)
	h.Scan(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, matcherCalled)
	assert.True(t, capturedReprocess)
}

// --- Cycle 6: Scan calls trip detector after matcher ---

func TestScan_CallsTripDetect(t *testing.T) {
	var capturedGapHours int
	tripDetectCalled := false

	h := &IngestHandler{
		db:       &mockIngestDB{},
		storage:  nil,
		watchDir: t.TempDir(),
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
			return nil
		},
		tripGapHours: 24,
		runTripDetect: func(_ context.Context, _ tripDetectDB, gapHours int) error {
			tripDetectCalled = true
			capturedGapHours = gapHours
			return nil
		},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/ingest/scan", nil)
	h.Scan(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, tripDetectCalled)
	assert.Equal(t, 24, capturedGapHours)
}

// --- Cycle 7: Scan returns 500 if trip detector fails ---

func TestScan_TripDetectError_Returns500(t *testing.T) {
	h := &IngestHandler{
		db:       &mockIngestDB{},
		storage:  nil,
		watchDir: t.TempDir(),
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
			return nil
		},
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ int) error {
			return fmt.Errorf("db error")
		},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/ingest/scan", nil)
	h.Scan(w, r)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- Cycle 8: Upload calls trip detector when files are uploaded ---

func TestUpload_CallsTripDetect(t *testing.T) {
	execDB := &mockMediaExecDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil
		},
	}

	tripDetectCalled := false
	h := &MediaHandler{
		db:      execDB,
		storage: &mockUploadStorage{},
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
			return nil
		},
		tripGapHours: 24,
		runTripDetect: func(_ context.Context, _ tripDetectDB, gapHours int) error {
			tripDetectCalled = true
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadOneFile("photo.jpg", "fake-jpeg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, tripDetectCalled)
}

// --- Cycle 9: Upload skips trip detector when nothing was uploaded ---

func TestUpload_SkipsTripDetectWhenNothingUploaded(t *testing.T) {
	execDB := &mockMediaExecDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(0), nil // dedup: 0 rows affected
		},
	}

	tripDetectCalled := false
	h := &MediaHandler{
		db:      execDB,
		storage: &mockUploadStorage{},
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
			return nil
		},
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ int) error {
			tripDetectCalled = true
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadOneFile("photo.jpg", "fake-jpeg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.False(t, tripDetectCalled)
}

// --- Cycle 10: Upload returns 500 if trip detector fails ---

func TestUpload_TripDetectError_Returns500(t *testing.T) {
	execDB := &mockMediaExecDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil
		},
	}

	h := &MediaHandler{
		db:      execDB,
		storage: &mockUploadStorage{},
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
			return nil
		},
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ int) error {
			return fmt.Errorf("db error")
		},
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadOneFile("photo.jpg", "fake-jpeg"))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- support types ---

// mockMediaExecDB implements mediaDB with controllable Exec.
type mockMediaExecDB struct {
	execFn func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (m *mockMediaExecDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("not expected")
}
func (m *mockMediaExecDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	panic("not expected")
}
func (m *mockMediaExecDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return m.execFn(ctx, sql, args...)
}

// mockIngestDB implements ingestDB; no files in temp dir so these are never called.
type mockIngestDB struct{}

func (m *mockIngestDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	panic("not expected")
}
func (m *mockIngestDB) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	panic("not expected")
}
