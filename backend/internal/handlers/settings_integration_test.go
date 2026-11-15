package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A camera zone setting is the caller's own: it is written to their row only, and dates their uploads
// only. An upload's own zone outranks it. The provenance view names the zone the owner's uploads get.
func TestSettings_Integration_CameraZoneIsPerUser(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeObjectStore{objects: map[string][]byte{}}
	router := newOwnershipRouter(pool, store)
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")

	rec := do(t, router, alice, http.MethodPatch, "/api/me/settings", `{"camera_timezone":"Asia/Tokyo"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"camera_timezone":"Asia/Tokyo","defaults":{"camera_timezone":"server zone (unset)"}}`, rec.Body.String())

	rec = do(t, router, bob, http.MethodGet, "/api/me/settings", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"camera_timezone":null,"defaults":{"camera_timezone":"server zone (unset)"}}`, rec.Body.String(),
		"alice's setting is not bob's")

	rec = do(t, router, alice, http.MethodPatch, "/api/me/settings", `{"camera_timezone":"Mars/Olympus"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = do(t, router, alice, http.MethodGet, "/api/me/settings", "")
	assert.JSONEq(t, `{"camera_timezone":"Asia/Tokyo","defaults":{"camera_timezone":"server zone (unset)"}}`, rec.Body.String(),
		"a refused change writes nothing")

	// One wall clock, 2020-01-02 03:04:05, in each zone. The test router's server zone is UTC.
	photo := string(exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2020:01:02 03:04:05"}))
	upload := func(user, name string, query url.Values) {
		t.Helper()
		rec, _ := uploadParts(t, router, user, query, uploadPart{name, photo + name})
		require.Equal(t, []string{name}, decodeUpload(t, rec).Uploaded)
		// The fake bucket keeps no bodies: the re-read below gets the photo from objects.
		var key string
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT storage_key FROM media WHERE owner_id = $1 AND filename = $2`, user, name).Scan(&key))
		store.objects[key] = []byte(photo + name)
	}
	upload(alice, "alice.jpg", nil)
	upload(bob, "bob.jpg", nil)
	upload(alice, "alice-ny.jpg", url.Values{"camera_timezone": {"America/New_York"}})

	stored := func(user, name string) (string, string, string) {
		t.Helper()
		var at time.Time
		var zone, from string
		require.NoError(t, pool.QueryRow(context.Background(), `
			SELECT taken_at, capture_evidence->>'camera_zone', capture_evidence->>'camera_zone_from'
			FROM media WHERE owner_id = $1 AND filename = $2`, user, name).Scan(&at, &zone, &from))
		return at.UTC().Format(time.RFC3339), zone, from
	}
	at, zone, from := stored(alice, "alice.jpg")
	assert.Equal(t, [3]string{"2020-01-01T18:04:05Z", "Asia/Tokyo", "user"}, [3]string{at, zone, from})
	at, zone, from = stored(bob, "bob.jpg")
	assert.Equal(t, [3]string{"2020-01-02T03:04:05Z", "server zone (unset)", "server"}, [3]string{at, zone, from})
	at, zone, from = stored(alice, "alice-ny.jpg")
	assert.Equal(t, [3]string{"2020-01-02T08:04:05Z", "America/New_York", "batch"}, [3]string{at, zone, from})

	zoneToday := func(user, name string) (string, string) {
		t.Helper()
		rec := do(t, router, user, http.MethodGet, "/api/media/"+mediaIDByName(t, pool, user, name)+"/provenance", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got ProvenanceResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got.Time.ServerCameraZone.Name, got.Time.ServerCameraZone.From
	}
	name, from := zoneToday(alice, "alice.jpg")
	assert.Equal(t, [2]string{"Asia/Tokyo", "user"}, [2]string{name, from})
	name, from = zoneToday(bob, "bob.jpg")
	assert.Equal(t, [2]string{"server zone (unset)", "server"}, [2]string{name, from})

	// The file re-read keeps the upload's zone, and reads the other in the owner's zone today.
	reread := func(user, name string) FileReread {
		t.Helper()
		rec := do(t, router, user, http.MethodGet, "/api/media/"+mediaIDByName(t, pool, user, name)+"/provenance/file", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got FileReread
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got
	}
	got := reread(alice, "alice-ny.jpg")
	require.NotNil(t, got.RuleToday)
	assert.Equal(t, ptr("America/New_York"), got.RuleToday.ZoneName)
	assert.Equal(t, ptr("batch"), got.RuleToday.ZoneFrom)
	assert.Equal(t, rereadSame, got.Class)
	got = reread(alice, "alice.jpg")
	assert.Equal(t, ptr("Asia/Tokyo"), got.RuleToday.ZoneName)
	assert.Equal(t, rereadSame, got.Class)

	_, err := pool.Exec(context.Background(), `UPDATE users SET camera_timezone = NULL WHERE id = $1`, alice)
	require.NoError(t, err)
	got = reread(alice, "alice.jpg")
	assert.Equal(t, ptr("server zone (unset)"), got.RuleToday.ZoneName, "today's zone, after the setting was cleared")
	assert.Equal(t, rereadWholeOffset, got.Class)
}
