package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockUploadStorage allows Upload to succeed without S3 and records the keys it was given.
type mockUploadStorage struct {
	keys []string
}

func (m *mockUploadStorage) Upload(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	m.keys = append(m.keys, key)
	_, err := io.Copy(io.Discard, body)
	return err
}
func (m *mockUploadStorage) PresignGet(_ context.Context, _ string) (string, error) {
	panic("not expected")
}

// mockRegistrarDB implements registrarDB with a controllable Exec outcome and records each call.
type mockRegistrarDB struct {
	rowsAffected int64
	sqls         []string
	args         [][]any
}

func (m *mockRegistrarDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.sqls = append(m.sqls, sql)
	m.args = append(m.args, args)
	return makeTag(int(m.rowsAffected)), nil
}

// uploadFiles builds an authenticated multipart/form-data request with one part per filename.
func uploadFiles(filenames ...string) *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, name := range filenames {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write([]byte("fake-content-of-" + name))
	}
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/media/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return withTestUser(r)
}

func noopMatcher(context.Context, matcherDB, string, time.Duration, bool) error { return nil }
func noopTripDetect(context.Context, tripDetectDB, string, int) error           { return nil }

// --- Upload ---

func TestUpload_CallsMatcherWithReprocess(t *testing.T) {
	var capturedReprocess bool
	matcherCalled := false

	h := &MediaHandler{
		registrar: &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: &mockUploadStorage{}},
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, reprocess bool) error {
			matcherCalled = true
			capturedReprocess = reprocess
			return nil
		},
		runTripDetect: noopTripDetect,
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles("photo.jpg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, matcherCalled)
	assert.True(t, capturedReprocess)
}

func TestUpload_PropagatesOwner(t *testing.T) {
	db := &mockRegistrarDB{rowsAffected: 1}
	var matcherOwner, tripOwner string
	h := &MediaHandler{
		registrar: &MediaRegistrar{db: db, storage: &mockUploadStorage{}},
		runMatcher: func(_ context.Context, _ matcherDB, ownerID string, _ time.Duration, _ bool) error {
			matcherOwner = ownerID
			return nil
		},
		runTripDetect: func(_ context.Context, _ tripDetectDB, ownerID string, _ int) error {
			tripOwner = ownerID
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles("photo.jpg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, testOwnerID, matcherOwner)
	assert.Equal(t, testOwnerID, tripOwner)
	require.Len(t, db.sqls, 1)
	assert.Contains(t, db.sqls[0], "ON CONFLICT (owner_id, file_hash) DO NOTHING")
	assert.Equal(t, testOwnerID, db.args[0][0], "owner_id is the first INSERT arg")
}

func TestUpload_OpaqueStorageKey(t *testing.T) {
	db := &mockRegistrarDB{rowsAffected: 1}
	store := &mockUploadStorage{}
	h := &MediaHandler{
		registrar:     &MediaRegistrar{db: db, storage: store},
		runMatcher:    noopMatcher,
		runTripDetect: noopTripDetect,
	}

	const name = "IMG_20230729_210400_Budapest.JPG"
	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles(name, "other.jpg"))

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, store.keys, 2)
	key := store.keys[0]
	assert.Regexp(t, `^media/`+regexp.QuoteMeta(testOwnerID)+`/[0-9a-f]{32}\.jpg$`, key)
	for _, leak := range []string{"IMG", "20230729", "210400", "Budapest"} {
		assert.NotContains(t, key, leak)
	}
	assert.NotEqual(t, store.keys[0], store.keys[1])

	require.Len(t, db.args, 2)
	assert.Equal(t, name, db.args[0][1], "original filename is still stored in media.filename")
	assert.Equal(t, key, db.args[0][8], "storage_key arg matches the uploaded key")
}

func TestNewStorageKey(t *testing.T) {
	tests := []struct {
		ext     string
		wantExt string
	}{
		{".jpg", ".jpg"},
		{".JPG", ".jpg"},
		{".Jpeg", ".jpeg"},
		{".MP4", ".mp4"},
		{".heic", ".heic"},
		{".exe", ""},
		{".html", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			key := newStorageKey("owner-1", tt.ext)
			assert.Regexp(t, `^media/owner-1/[0-9a-f]{32}`+regexp.QuoteMeta(tt.wantExt)+`$`, key)
		})
	}
	assert.NotEqual(t, newStorageKey("o", ".jpg"), newStorageKey("o", ".jpg"))
}

func TestUpload_CallsTripDetect(t *testing.T) {
	tripDetectCalled := false
	var capturedGapHours int

	h := &MediaHandler{
		registrar:    &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: &mockUploadStorage{}},
		runMatcher:   noopMatcher,
		tripGapHours: 24,
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ string, gapHours int) error {
			tripDetectCalled = true
			capturedGapHours = gapHours
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles("photo.jpg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, tripDetectCalled)
	assert.Equal(t, 24, capturedGapHours)
}

func TestUpload_SkipsTripDetectWhenNothingUploaded(t *testing.T) {
	tripDetectCalled := false

	h := &MediaHandler{
		registrar:  &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 0}, storage: &mockUploadStorage{}}, // 0 = dedup
		runMatcher: noopMatcher,
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ string, _ int) error {
			tripDetectCalled = true
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles("photo.jpg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.False(t, tripDetectCalled)
}

func TestUpload_TripDetectError_Returns500(t *testing.T) {
	h := &MediaHandler{
		registrar:  &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: &mockUploadStorage{}},
		runMatcher: noopMatcher,
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ string, _ int) error {
			return fmt.Errorf("db error")
		},
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles("photo.jpg"))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestUpload_BodyTooLarge_Returns413(t *testing.T) {
	orig := maxUploadBodyBytes
	maxUploadBodyBytes = 1024
	t.Cleanup(func() { maxUploadBodyBytes = orig })

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", "big.jpg")
	fw.Write(bytes.Repeat([]byte("x"), 4096))
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/media/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())

	store := &mockUploadStorage{}
	h := &MediaHandler{
		registrar:     &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: store},
		runMatcher:    noopMatcher,
		runTripDetect: noopTripDetect,
	}
	w := httptest.NewRecorder()
	h.Upload(w, withTestUser(r))

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Empty(t, store.keys)
}

// failOnceStorage fails the upload of the file whose key matches the nth call (0-based).
type failOnceStorage struct {
	mockUploadStorage
	failAt int
	calls  int
}

func (m *failOnceStorage) Upload(ctx context.Context, key string, body io.Reader, size int64, ct string) error {
	defer func() { m.calls++ }()
	if m.calls == m.failAt {
		return fmt.Errorf("s3 down")
	}
	return m.mockUploadStorage.Upload(ctx, key, body, size, ct)
}

func TestUpload_ReportsFailedFiles(t *testing.T) {
	h := &MediaHandler{
		registrar:     &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: &failOnceStorage{failAt: 1}},
		runMatcher:    noopMatcher,
		runTripDetect: noopTripDetect,
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles("a.jpg", "b.jpg", "c.jpg"))

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Uploaded []string `json:"uploaded"`
		Failed   []string `json:"failed"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, []string{"a.jpg", "c.jpg"}, resp.Uploaded)
	assert.Equal(t, []string{"b.jpg"}, resp.Failed)
}

// --- RunIngestScan (fake deps) ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestRunIngestScan_CallsMatcherWithReprocess(t *testing.T) {
	var capturedReprocess bool
	var capturedOwner string
	matcherCalled := false

	deps := IngestDeps{
		registrar: &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: &mockUploadStorage{}},
		runMatcher: func(_ context.Context, _ matcherDB, ownerID string, _ time.Duration, reprocess bool) error {
			matcherCalled = true
			capturedOwner = ownerID
			capturedReprocess = reprocess
			return nil
		},
		runTripDetect: noopTripDetect,
	}

	// Empty dir → walk finishes immediately, matcher still called.
	res, err := RunIngestScan(context.Background(), deps, t.TempDir(), testOwnerID)

	require.NoError(t, err)
	assert.Equal(t, ScanResult{}, res)
	assert.True(t, matcherCalled)
	assert.True(t, capturedReprocess)
	assert.Equal(t, testOwnerID, capturedOwner)
}

func TestRunIngestScan_CallsTripDetect(t *testing.T) {
	var capturedGapHours int
	var capturedOwner string
	tripDetectCalled := false

	deps := IngestDeps{
		registrar:    &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: &mockUploadStorage{}},
		runMatcher:   noopMatcher,
		tripGapHours: 24,
		runTripDetect: func(_ context.Context, _ tripDetectDB, ownerID string, gapHours int) error {
			tripDetectCalled = true
			capturedOwner = ownerID
			capturedGapHours = gapHours
			return nil
		},
	}

	_, err := RunIngestScan(context.Background(), deps, t.TempDir(), testOwnerID)

	require.NoError(t, err)
	assert.True(t, tripDetectCalled)
	assert.Equal(t, 24, capturedGapHours)
	assert.Equal(t, testOwnerID, capturedOwner)
}

func TestRunIngestScan_TripDetectError_ReturnsError(t *testing.T) {
	deps := IngestDeps{
		registrar:  &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: &mockUploadStorage{}},
		runMatcher: noopMatcher,
		runTripDetect: func(_ context.Context, _ tripDetectDB, _ string, _ int) error {
			return fmt.Errorf("db error")
		},
	}

	_, err := RunIngestScan(context.Background(), deps, t.TempDir(), testOwnerID)

	assert.ErrorContains(t, err, "trip detect")
}

func TestRunIngestScan_CountsIngestedSkippedFailed(t *testing.T) {
	tests := []struct {
		name         string
		rowsAffected int64
		want         ScanResult
	}{
		{"new files are ingested", 1, ScanResult{Ingested: 2, Failed: 1}},
		{"deduplicated files are skipped", 0, ScanResult{Skipped: 2, Failed: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.jpg", "fake-a")
			writeFile(t, dir, "b.PNG", "fake-b")
			writeFile(t, dir, "empty.mp4", "") // zero bytes: registerMedia fails reading the head
			writeFile(t, dir, "notes.txt", "unsupported extension, ignored")

			db := &mockRegistrarDB{rowsAffected: tt.rowsAffected}
			deps := IngestDeps{
				registrar:     &MediaRegistrar{db: db, storage: &mockUploadStorage{}},
				runMatcher:    noopMatcher,
				runTripDetect: noopTripDetect,
			}

			res, err := RunIngestScan(context.Background(), deps, dir, testOwnerID)

			require.NoError(t, err)
			assert.Equal(t, tt.want, res)
			for _, args := range db.args {
				assert.Equal(t, testOwnerID, args[0])
			}
		})
	}
}

func TestRunIngestScan_PreCancelledContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.jpg", "fake-a")

	store := &mockUploadStorage{}
	matcherCalled, tripDetectCalled := false, false
	deps := IngestDeps{
		registrar: &MediaRegistrar{db: &mockRegistrarDB{rowsAffected: 1}, storage: store},
		runMatcher: func(context.Context, matcherDB, string, time.Duration, bool) error {
			matcherCalled = true
			return nil
		},
		runTripDetect: func(context.Context, tripDetectDB, string, int) error {
			tripDetectCalled = true
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RunIngestScan(ctx, deps, dir, testOwnerID)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, ScanResult{}, res)
	assert.Empty(t, store.keys, "no file may be uploaded")
	assert.False(t, matcherCalled)
	assert.False(t, tripDetectCalled)
}

func TestRunIngestScan_MissingDir_ReturnsWalkError(t *testing.T) {
	matcherCalled := false
	deps := IngestDeps{
		registrar: &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
		runMatcher: func(context.Context, matcherDB, string, time.Duration, bool) error {
			matcherCalled = true
			return nil
		},
		runTripDetect: noopTripDetect,
	}

	_, err := RunIngestScan(context.Background(), deps, filepath.Join(t.TempDir(), "missing"), testOwnerID)

	assert.ErrorContains(t, err, "walk")
	assert.False(t, matcherCalled)
}
