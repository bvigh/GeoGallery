package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type uploadPart struct {
	name, content string
}

type uploadResult struct {
	Uploaded   []string `json:"uploaded"`
	Skipped    []string `json:"skipped"`
	Failed     []string `json:"failed"`
	AlbumAdded *int64   `json:"album_added"`
}

// uploadParts posts one multipart file per part to /api/media/upload?<query> as userID, through
// a body that counts its reads.
func uploadParts(t *testing.T, h http.Handler, userID string, query url.Values, parts ...uploadPart) (*httptest.ResponseRecorder, *countingReader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		fw, err := mw.CreateFormFile("files", p.name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(p.content))
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	body := &countingReader{r: &buf}
	target := "/api/media/upload"
	if query != nil {
		target += "?" + query.Encode()
	}
	req := httptest.NewRequest(http.MethodPost, target, body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(testUserHeader, userID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, body
}

func decodeUpload(t *testing.T, rec *httptest.ResponseRecorder) uploadResult {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res uploadResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res), rec.Body.String())
	return res
}

func mediaIDByName(t *testing.T, pool *pgxpool.Pool, ownerID, filename string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id::text FROM media WHERE owner_id = $1 AND filename = $2`, ownerID, filename).Scan(&id))
	return id
}

// assertNoOrphans checks that the objects uploaded to store and not deleted again are exactly the
// ones a media row references: a duplicate's upload must not stay behind in the bucket.
func assertNoOrphans(t *testing.T, pool *pgxpool.Pool, store *fakeObjectStore) {
	t.Helper()
	require.NotEmpty(t, store.keys)
	deleted := map[string]bool{}
	for _, key := range store.deleted {
		deleted[key] = true
	}
	live := []string{}
	for _, key := range store.keys {
		if !deleted[key] {
			live = append(live, key)
		}
	}
	assert.Equal(t, sorted(live...), queryIDs(t, pool,
		`SELECT storage_key FROM media WHERE storage_key = ANY($1)`, store.keys))
}

func TestUploadToAlbum_RejectedBeforeBodyIsRead(t *testing.T) {
	f := newSharingFixture(t)
	familyBefore := albumContents(t, f.pool, f.family)
	privateBefore := albumContents(t, f.pool, f.private)

	tests := []struct {
		name    string
		userID  string
		albumID string
	}{
		{"not a member of any album", f.carol, f.family},
		{"member of another album only", f.userB, f.private},
		{"unknown album", f.userA, unknownUUID},
		{"malformed album_id", f.userA, "not-a-uuid"},
		{"empty album_id", f.userA, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, body := uploadParts(t, f.router, tt.userID, url.Values{"album_id": {tt.albumID}},
				uploadPart{"photo.jpg", "bytes that must never be stored"})

			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Zero(t, body.reads, "the body must not be read")
		})
	}

	assert.Empty(t, f.store.keys, "nothing reached S3")
	assert.Equal(t, 0, countMedia(t, f.pool, f.carol))
	assert.Equal(t, 1, countMedia(t, f.pool, f.userB))
	assert.Equal(t, 2, countMedia(t, f.pool, f.userA))
	assert.Equal(t, familyBefore, albumContents(t, f.pool, f.family))
	assert.Equal(t, privateBefore, albumContents(t, f.pool, f.private))

	// Positive control: the same request from a member reads the body and stores the file.
	rec, body := uploadParts(t, f.router, f.userB, url.Values{"album_id": {f.family}},
		uploadPart{"photo.jpg", "bytes that must never be stored"})
	assert.Equal(t, int64(1), *decodeUpload(t, rec).AlbumAdded)
	assert.Positive(t, body.reads)
}

func TestUploadToAlbum_AddsNewFilesAndDuplicates(t *testing.T) {
	f := newSharingFixture(t)
	familyQuery := url.Values{"album_id": {f.family}}

	// Without album_id an upload is unchanged: no album_added, and no album gains anything.
	rec, _ := uploadParts(t, f.router, f.userB, nil, uploadPart{"old.jpg", "bytes B uploaded earlier"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"failed", "skipped", "uploaded"}, jsonKeys(t, rec.Body.Bytes()))
	assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), albumContents(t, f.pool, f.family))
	oldID := mediaIDByName(t, f.pool, f.userB, "old.jpg")

	// Into the album, with the ID in upper case: a duplicate of old.jpg, two new files, a copy of
	// one of them in the same request, and a zero-byte video that fails.
	rec, body := uploadParts(t, f.router, f.userB, url.Values{"album_id": {strings.ToUpper(f.family)}},
		uploadPart{"old-again.jpg", "bytes B uploaded earlier"},
		uploadPart{"new1.jpg", "first new photo"},
		uploadPart{"new2.jpg", "second new photo"},
		uploadPart{"new1-copy.jpg", "first new photo"},
		uploadPart{"empty.mp4", ""})
	assert.Positive(t, body.reads)
	assert.Equal(t, uploadResult{
		Uploaded:   []string{"new1.jpg", "new2.jpg"},
		Skipped:    []string{"old-again.jpg", "new1-copy.jpg"},
		Failed:     []string{"empty.mp4"},
		AlbumAdded: ptr(int64(3)),
	}, decodeUpload(t, rec))
	assert.Equal(t, 4, countMedia(t, f.pool, f.userB), "mediaB, old, new1 and new2: duplicates add no rows")
	new1 := mediaIDByName(t, f.pool, f.userB, "new1.jpg")
	new2 := mediaIDByName(t, f.pool, f.userB, "new2.jpg")
	want := sorted(f.mediaA, f.mediaAUnlocated, oldID, new1, new2)
	assert.Equal(t, want, albumContents(t, f.pool, f.family))
	assert.Equal(t, []string{f.userB, f.userB, f.userB}, queryIDs(t, f.pool,
		`SELECT owner_id::text FROM album_media WHERE album_id = $1 AND media_id = ANY($2::uuid[])`,
		f.family, []string{oldID, new1, new2}), "the uploader is the contributor")
	assert.Equal(t, sorted(f.mediaA), albumContents(t, f.pool, f.private), "other albums are untouched")

	// The album owner sees B's uploads through the album filter.
	rec = do(t, f.router, f.userA, http.MethodGet, "/api/media?album_id="+f.family, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, want, sorted(decodeIDs(t, rec)...))

	// Uploading the same files into the album again adds nothing.
	rec, _ = uploadParts(t, f.router, f.userB, familyQuery,
		uploadPart{"old.jpg", "bytes B uploaded earlier"},
		uploadPart{"new1.jpg", "first new photo"},
		uploadPart{"new2.jpg", "second new photo"})
	assert.Equal(t, uploadResult{
		Skipped:    []string{"old.jpg", "new1.jpg", "new2.jpg"},
		AlbumAdded: ptr(int64(0)),
	}, decodeUpload(t, rec))
	assert.Equal(t, want, albumContents(t, f.pool, f.family))

	// A file that fails leaves album_added at 0.
	rec, _ = uploadParts(t, f.router, f.userB, familyQuery, uploadPart{"empty.mp4", ""})
	assert.JSONEq(t, `{"uploaded":null,"skipped":null,"failed":["empty.mp4"],"album_added":0}`, rec.Body.String())

	// No cross-user dedup: the album owner uploading B's bytes gets her own row, and the album
	// holds both copies.
	rec, _ = uploadParts(t, f.router, f.userA, familyQuery, uploadPart{"old.jpg", "bytes B uploaded earlier"})
	assert.Equal(t, uploadResult{Uploaded: []string{"old.jpg"}, AlbumAdded: ptr(int64(1))}, decodeUpload(t, rec))
	aCopy := mediaIDByName(t, f.pool, f.userA, "old.jpg")
	assert.NotEqual(t, oldID, aCopy)
	assert.Equal(t, sorted(append(want, aCopy)...), albumContents(t, f.pool, f.family))

	assert.Len(t, f.store.keys, 4, "old, new1, new2 and A's old: skipped files never reach S3")
	assert.Empty(t, f.store.deleted)
	assertNoOrphans(t, f.pool, f.store)
}

func TestRegisterMedia_Integration_ReturnsExistingIDForDuplicates(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeObjectStore{}
	reg := &MediaRegistrar{db: pool, storage: store}
	userA := createUser(t, pool, "alice")
	userB := createUser(t, pool, "bob")
	const content = "the same bytes every time"

	register := func(ownerID, filename string) (string, bool) {
		t.Helper()
		id, inserted, err := reg.registerMedia(context.Background(), ownerID,
			strings.NewReader(content), int64(len(content)), filename, nil)
		require.NoError(t, err)
		return id, inserted
	}

	first, inserted := register(userA, "one.jpg")
	assert.True(t, inserted)
	assert.Equal(t, mediaIDByName(t, pool, userA, "one.jpg"), first, "the returned ID is the new row's")

	again, inserted := register(userA, "renamed.jpg")
	assert.False(t, inserted)
	assert.Equal(t, first, again, "a duplicate returns the existing row's ID")

	other, inserted := register(userB, "one.jpg")
	assert.True(t, inserted, "dedup is per owner")
	assert.NotEqual(t, first, other)

	assert.Equal(t, 1, countMedia(t, pool, userA))
	assert.Equal(t, 1, countMedia(t, pool, userB))
	var filename, key string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT filename, storage_key FROM media WHERE id = $1`, first).Scan(&filename, &key))
	assert.Equal(t, "one.jpg", filename, "the duplicate leaves the existing row alone")
	assert.Equal(t, store.keys[0], key)
	assert.Len(t, store.keys, 2, "A's and B's one.jpg: the duplicate is never uploaded")
	assert.Empty(t, store.deleted)
	assertNoOrphans(t, pool, store)
}
