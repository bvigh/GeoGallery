package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockUploadStorage allows Upload to succeed without S3 and records the keys it was given and
// the keys it was asked to delete.
type mockUploadStorage struct {
	keys      []string
	deleted   []string
	deleteErr error // returned by Delete
}

func (m *mockUploadStorage) Upload(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	m.keys = append(m.keys, key)
	_, err := io.Copy(io.Discard, body)
	return err
}
func (m *mockUploadStorage) Delete(_ context.Context, key string) error {
	m.deleted = append(m.deleted, key)
	return m.deleteErr
}
func (m *mockUploadStorage) PresignGet(_ context.Context, _ string) (string, error) {
	panic("not expected")
}

// mockRegistrarDB implements registrarDB and records each call. A lookup by (owner, hash) returns
// "existing-<file hash>" for a hash in known or one whose INSERT conflicted, else no row. The media
// INSERT returns the ID "new-<filename>", or no row for a filename in raced.
type mockRegistrarDB struct {
	known      map[string]bool // file hashes already in the library
	raced      map[string]bool // filenames a concurrent call stores between the lookup and the INSERT
	insertErr  error           // returned by the media INSERT
	lookupErrs []error         // the i-th duplicate lookup returns lookupErrs[i] when it is set
	lookups    int
	conflicts  map[string]bool // file hashes whose INSERT hit ON CONFLICT DO NOTHING
	sqls       []string
	args       [][]any
}

func (m *mockRegistrarDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.sqls = append(m.sqls, sql)
	m.args = append(m.args, args)
	return makeTag(1), nil
}

func (m *mockRegistrarDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	m.sqls = append(m.sqls, sql)
	m.args = append(m.args, args)
	switch {
	case strings.Contains(sql, "INSERT INTO media"):
		if m.insertErr != nil {
			return &mockRow{scanFn: func(...any) error { return m.insertErr }}
		}
		if m.raced[args[1].(string)] {
			if m.conflicts == nil {
				m.conflicts = map[string]bool{}
			}
			m.conflicts[args[9].(string)] = true
			return noRowsRow{}
		}
		return scanStringRow("new-" + args[1].(string))
	case strings.Contains(sql, "SELECT id FROM media"):
		i := m.lookups
		m.lookups++
		if i < len(m.lookupErrs) && m.lookupErrs[i] != nil {
			return &mockRow{scanFn: func(...any) error { return m.lookupErrs[i] }}
		}
		hash := args[1].(string)
		if m.known[hash] || m.conflicts[hash] {
			return scanStringRow("existing-" + hash)
		}
		return noRowsRow{}
	}
	panic("unexpected QueryRow: " + sql)
}

// hashesOf returns the file hashes of small files with these contents, as mockRegistrarDB.known.
func hashesOf(contents ...string) map[string]bool {
	known := map[string]bool{}
	for _, c := range contents {
		known[partialHash([]byte(c))] = true
	}
	return known
}

// knownUploads returns the file hashes of the files uploadFiles builds for these names.
func knownUploads(filenames ...string) map[string]bool {
	contents := make([]string, len(filenames))
	for i, name := range filenames {
		contents[i] = "fake-content-of-" + name
	}
	return hashesOf(contents...)
}

// scanStringRow is a pgx.Row holding one string column.
func scanStringRow(v string) pgx.Row {
	return &mockRow{scanFn: func(dest ...any) error {
		*dest[0].(*string) = v
		return nil
	}}
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
		registrar: &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
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
	db := &mockRegistrarDB{}
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
	require.Len(t, db.sqls, 2)
	assert.Contains(t, db.sqls[0], "SELECT id FROM media WHERE owner_id = $1")
	assert.Equal(t, testOwnerID, db.args[0][0], "owner_id is the first lookup arg")
	assert.Contains(t, db.sqls[1], "ON CONFLICT (owner_id, file_hash) DO NOTHING")
	assert.Equal(t, testOwnerID, db.args[1][0], "owner_id is the first INSERT arg")
}

func TestUpload_OpaqueStorageKey(t *testing.T) {
	db := &mockRegistrarDB{}
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

	require.Len(t, db.args, 4, "a lookup and an INSERT per file")
	assert.Equal(t, name, db.args[1][1], "original filename is still stored in media.filename")
	assert.Equal(t, key, db.args[1][8], "storage_key arg matches the uploaded key")
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
		registrar:    &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
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
		registrar:  &MediaRegistrar{db: &mockRegistrarDB{known: knownUploads("photo.jpg")}, storage: &mockUploadStorage{}},
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
	assert.JSONEq(t, `{"uploaded":null,"skipped":["photo.jpg"],"failed":null}`, w.Body.String())
}

func TestUpload_TripDetectError_Returns500(t *testing.T) {
	h := &MediaHandler{
		registrar:  &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
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
		registrar:     &MediaRegistrar{db: &mockRegistrarDB{}, storage: store},
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

func TestUpload_RemovesSpooledParts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	orig := uploadFormMaxMemory
	uploadFormMaxMemory = 1 // spool every part to disk
	t.Cleanup(func() { uploadFormMaxMemory = orig })

	tests := []struct {
		name       string
		runMatcher func(context.Context, matcherDB, string, time.Duration, bool) error
		wantCode   int
	}{
		{"success", noopMatcher, http.StatusOK},
		{"matcher error", func(context.Context, matcherDB, string, time.Duration, bool) error {
			return errors.New("boom")
		}, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &MediaHandler{
				registrar:     &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
				runMatcher:    tt.runMatcher,
				runTripDetect: noopTripDetect,
			}

			w := httptest.NewRecorder()
			h.Upload(w, uploadFiles("a.jpg", "b.jpg"))

			require.Equal(t, tt.wantCode, w.Code)
			left, err := os.ReadDir(tmp)
			require.NoError(t, err)
			assert.Empty(t, left)
		})
	}
}

func TestUpload_ReportsFailedFiles(t *testing.T) {
	h := &MediaHandler{
		registrar:     &MediaRegistrar{db: &mockRegistrarDB{}, storage: &failOnceStorage{failAt: 1}},
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

func TestUpload_WithoutAlbumIDLeavesResponseUnchanged(t *testing.T) {
	db := &uploadAlbumDB{}
	h := &MediaHandler{
		db:            db,
		registrar:     &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
		runMatcher:    noopMatcher,
		runTripDetect: noopTripDetect,
	}

	w := httptest.NewRecorder()
	h.Upload(w, uploadFiles("photo.jpg"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"uploaded":["photo.jpg"],"skipped":null,"failed":null}`, w.Body.String(), "no album_added")
	assert.Empty(t, db.lookups)
	assert.Empty(t, db.inserts)
}

// --- registerMedia ---

// notSentError is a pgx error raised before the statement reached the server.
type notSentError struct{}

func (notSentError) Error() string     { return "dial: connection refused" }
func (notSentError) SafeToRetry() bool { return true }

func TestRegisterMedia_ReturnsMediaID(t *testing.T) {
	const content = "fake photo bytes"
	hash := partialHash([]byte(content))
	dbDown := errors.New("db down")
	rejected := &pgconn.PgError{Code: "23514", Message: "violates check constraint"}
	known := map[string]bool{hash: true}
	raced := map[string]bool{"photo.jpg": true}
	const lookup, insert = "lookup", "insert"
	tests := []struct {
		name         string
		db           *mockRegistrarDB
		deleteErr    error
		wantID       string
		wantInserted bool
		wantErr      error
		wantSQL      []string
		wantUploaded bool
		wantDeleted  bool // the uploaded object is deleted again
	}{
		{"new file returns the inserted ID", &mockRegistrarDB{}, nil,
			"new-photo.jpg", true, nil, []string{lookup, insert}, true, false},
		{"duplicate returns the existing ID without uploading", &mockRegistrarDB{known: known}, nil,
			"existing-" + hash, false, nil, []string{lookup}, false, false},
		{"duplicate lookup fails before uploading", &mockRegistrarDB{lookupErrs: []error{dbDown}}, nil,
			"", false, dbDown, []string{lookup}, false, false},
		{"concurrent duplicate returns the winner's ID", &mockRegistrarDB{raced: raced}, nil,
			"existing-" + hash, false, nil, []string{lookup, insert, lookup}, true, true},
		{"concurrent duplicate still counts when deleting its object fails", &mockRegistrarDB{raced: raced}, errors.New("s3 down"),
			"existing-" + hash, false, nil, []string{lookup, insert, lookup}, true, true},
		{"insert rejected by the server", &mockRegistrarDB{insertErr: rejected}, nil,
			"", false, rejected, []string{lookup, insert}, true, true},
		{"insert never sent", &mockRegistrarDB{insertErr: notSentError{}}, nil,
			"", false, notSentError{}, []string{lookup, insert}, true, true},
		{"insert outcome unknown keeps the object", &mockRegistrarDB{insertErr: dbDown}, nil,
			"", false, dbDown, []string{lookup, insert}, true, false},
		{"lookup after a concurrent duplicate fails", &mockRegistrarDB{raced: raced, lookupErrs: []error{nil, pgx.ErrNoRows}}, nil,
			"", false, pgx.ErrNoRows, []string{lookup, insert, lookup}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockUploadStorage{deleteErr: tt.deleteErr}
			reg := &MediaRegistrar{db: tt.db, storage: store}

			id, inserted, err := reg.registerMedia(context.Background(), testOwnerID,
				strings.NewReader(content), int64(len(content)), "photo.jpg", nil)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantID, id)
			assert.Equal(t, tt.wantInserted, inserted)
			if tt.wantUploaded {
				require.Len(t, store.keys, 1)
			} else {
				assert.Empty(t, store.keys, "nothing is uploaded")
			}
			if tt.wantDeleted {
				assert.Equal(t, store.keys, store.deleted, "exactly the object this call uploaded is deleted")
			} else {
				assert.Empty(t, store.deleted)
			}

			require.Len(t, tt.db.sqls, len(tt.wantSQL))
			for i, kind := range tt.wantSQL {
				if kind == lookup {
					assert.Contains(t, tt.db.sqls[i], "SELECT id FROM media WHERE owner_id = $1 AND file_hash = $2")
					assert.Equal(t, []any{testOwnerID, hash}, tt.db.args[i], "the lookup is scoped to the owner")
					continue
				}
				assert.Contains(t, tt.db.sqls[i], "ON CONFLICT (owner_id, file_hash) DO NOTHING")
				assert.Contains(t, tt.db.sqls[i], "RETURNING id")
				assert.Equal(t, testOwnerID, tt.db.args[i][0])
				assert.Equal(t, hash, tt.db.args[i][9], "file_hash is the tenth INSERT arg")
			}
		})
	}
}

// --- Upload into an album ---

const testAlbumID = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d" // lower case, with letters, so canonicalising is visible

// uploadAlbumDB stands in for MediaHandler.db on an upload with ?album_id: it answers the
// membership lookup and records the album_media insert.
type uploadAlbumDB struct {
	member    bool
	lookupErr error
	insertErr error
	added     int // rows the album insert reports
	lookups   [][]any
	insertSQL string
	inserts   [][]any
}

func (d *uploadAlbumDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("not expected in upload")
}

func (d *uploadAlbumDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	d.lookups = append(d.lookups, args)
	return &mockRow{scanFn: func(dest ...any) error {
		if d.lookupErr != nil {
			return d.lookupErr
		}
		*dest[0].(*bool) = d.member
		return nil
	}}
}

func (d *uploadAlbumDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.insertSQL = sql
	d.inserts = append(d.inserts, args)
	if d.insertErr != nil {
		return pgconn.CommandTag{}, d.insertErr
	}
	return makeTag(d.added), nil
}

// countingReader counts the reads made on a request body.
type countingReader struct {
	r     io.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// uploadToAlbum is uploadFiles with ?album_id=albumID and a body that counts its reads.
func uploadToAlbum(albumID string, filenames ...string) (*http.Request, *countingReader) {
	r := uploadFiles(filenames...)
	body := &countingReader{r: r.Body}
	r.Body = io.NopCloser(body)
	r.URL.RawQuery = url.Values{"album_id": {albumID}}.Encode()
	return r, body
}

func TestUpload_AlbumIDRejectedBeforeBodyIsRead(t *testing.T) {
	tests := []struct {
		name       string
		albumID    string
		db         *uploadAlbumDB
		wantStatus int
		wantLookup bool
	}{
		{"malformed", "not-a-uuid", &uploadAlbumDB{}, http.StatusNotFound, false},
		{"empty", "", &uploadAlbumDB{}, http.StatusNotFound, false},
		{"not a member", testAlbumID, &uploadAlbumDB{member: false}, http.StatusNotFound, true},
		{"lookup fails", testAlbumID, &uploadAlbumDB{lookupErr: errors.New("db down")}, http.StatusInternalServerError, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &mockRegistrarDB{}
			store := &mockUploadStorage{}
			matcherCalled := false
			h := &MediaHandler{
				db:        tt.db,
				registrar: &MediaRegistrar{db: reg, storage: store},
				runMatcher: func(context.Context, matcherDB, string, time.Duration, bool) error {
					matcherCalled = true
					return nil
				},
				runTripDetect: noopTripDetect,
			}
			req, body := uploadToAlbum(tt.albumID, "photo.jpg")

			w := httptest.NewRecorder()
			h.Upload(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Zero(t, body.reads, "the body must not be read")
			assert.Empty(t, store.keys)
			assert.Empty(t, reg.sqls)
			assert.Empty(t, tt.db.inserts)
			assert.False(t, matcherCalled)
			if tt.wantLookup {
				require.Len(t, tt.db.lookups, 1)
				assert.Equal(t, []any{testAlbumID, testOwnerID}, tt.db.lookups[0])
			} else {
				assert.Empty(t, tt.db.lookups, "a malformed ID never reaches the DB")
			}
		})
	}
}

func TestUpload_AlbumIDAddsUploadedAndDuplicates(t *testing.T) {
	db := &uploadAlbumDB{member: true, added: 2}
	reg := &mockRegistrarDB{known: knownUploads("dup.jpg")}
	h := &MediaHandler{
		db:            db,
		registrar:     &MediaRegistrar{db: reg, storage: &failOnceStorage{failAt: 1}}, // dup.jpg is never uploaded
		runMatcher:    noopMatcher,
		runTripDetect: noopTripDetect,
	}
	req, body := uploadToAlbum(strings.ToUpper(testAlbumID), "a.jpg", "dup.jpg", "broken.jpg")

	w := httptest.NewRecorder()
	h.Upload(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Positive(t, body.reads)
	assert.JSONEq(t, `{"uploaded":["a.jpg"],"skipped":["dup.jpg"],"failed":["broken.jpg"],"album_added":2}`, w.Body.String())
	require.Len(t, db.lookups, 1)
	assert.Equal(t, []any{testAlbumID, testOwnerID}, db.lookups[0], "the album ID is canonicalised")
	require.Len(t, db.inserts, 1, "one insert for the whole request")
	dupID := "existing-" + partialHash([]byte("fake-content-of-dup.jpg"))
	assert.Equal(t, []any{testAlbumID, []string{"new-a.jpg", dupID}, testOwnerID}, db.inserts[0],
		"uploaded and duplicate files go in, failed ones do not")
	assert.Contains(t, db.insertSQL, "INSERT INTO album_media (album_id, media_id, owner_id)")
	assert.Contains(t, db.insertSQL, "SELECT $1, unnest($2::uuid[]), $3")
	assert.Contains(t, db.insertSQL, "ON CONFLICT DO NOTHING")
}

func TestUpload_AlbumIDOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		storage     registrarStorage
		known       map[string]bool
		insertErr   error
		matcherErr  error
		wantStatus  int
		wantBody    string // exact JSON on 200
		wantInserts int
	}{
		{"every file fails", &failOnceStorage{failAt: 0}, nil, nil, nil, http.StatusOK,
			`{"uploaded":null,"skipped":null,"failed":["a.jpg"],"album_added":0}`, 0},
		{"only duplicates", &mockUploadStorage{}, knownUploads("a.jpg"), nil, nil, http.StatusOK,
			`{"uploaded":null,"skipped":["a.jpg"],"failed":null,"album_added":1}`, 1},
		{"album insert fails", &mockUploadStorage{}, nil, errors.New("db down"), nil, http.StatusInternalServerError, "", 1},
		{"matcher fails before the album insert", &mockUploadStorage{}, nil, nil, errors.New("matcher down"),
			http.StatusInternalServerError, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &uploadAlbumDB{member: true, added: 1, insertErr: tt.insertErr}
			h := &MediaHandler{
				db:        db,
				registrar: &MediaRegistrar{db: &mockRegistrarDB{known: tt.known}, storage: tt.storage},
				runMatcher: func(context.Context, matcherDB, string, time.Duration, bool) error {
					return tt.matcherErr
				},
				runTripDetect: noopTripDetect,
			}
			req, _ := uploadToAlbum(testAlbumID, "a.jpg")

			w := httptest.NewRecorder()
			h.Upload(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantBody != "" {
				assert.JSONEq(t, tt.wantBody, w.Body.String())
			}
			assert.Len(t, db.inserts, tt.wantInserts)
		})
	}
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
		registrar: &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
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
		registrar:    &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
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
		registrar:  &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}},
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
		name  string
		known map[string]bool
		want  ScanResult
	}{
		{"new files are ingested", nil, ScanResult{Ingested: 2, Failed: 1}},
		{"deduplicated files are skipped", hashesOf("fake-a", "fake-b"), ScanResult{Skipped: 2, Failed: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "a.jpg", "fake-a")
			writeFile(t, dir, "b.PNG", "fake-b")
			writeFile(t, dir, "empty.mp4", "") // zero bytes: registerMedia refuses an empty file
			writeFile(t, dir, "notes.txt", "unsupported extension, ignored")

			db := &mockRegistrarDB{known: tt.known}
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
		registrar: &MediaRegistrar{db: &mockRegistrarDB{}, storage: store},
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

// takeExifSlots occupies every EXIF decode slot until the returned function, or the end of the
// test, frees them.
func takeExifSlots(t *testing.T) (release func()) {
	t.Helper()
	for range cap(exifDecodeSlots) {
		exifDecodeSlots <- struct{}{}
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			for range cap(exifDecodeSlots) {
				<-exifDecodeSlots
			}
		})
	}
	t.Cleanup(release)
	return release
}

func TestRegisterMedia_WaitsForAnExifSlot(t *testing.T) {
	release := takeExifSlots(t)
	reg := &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}}
	photo := []byte("a small photo")

	done := make(chan error, 1)
	go func() {
		_, _, err := reg.registerMedia(context.Background(), testOwnerID, bytes.NewReader(photo), int64(len(photo)), "a.jpg", nil)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("finished while every slot was taken: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the slots were freed")
	}
	assert.Empty(t, exifDecodeSlots, "the slot is given back")
}

func TestRegisterMedia_StopsWaitingForAnExifSlotWhenCancelled(t *testing.T) {
	takeExifSlots(t)
	db := &mockRegistrarDB{}
	store := &mockUploadStorage{}
	reg := &MediaRegistrar{db: db, storage: store}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	photo := []byte("a small photo")

	done := make(chan error, 1)
	go func() {
		_, _, err := reg.registerMedia(ctx, testOwnerID, bytes.NewReader(photo), int64(len(photo)), "a.jpg", nil)
		done <- err
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the context was cancelled")
	}
	assert.Empty(t, db.sqls, "the database is not reached")
	assert.Empty(t, store.keys, "nothing is stored")
}
