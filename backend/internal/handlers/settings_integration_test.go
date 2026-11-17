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

// settingsJSON is GET /api/me/settings from the test router, whose server zone is unset, with the
// caller's camera zone and video sound as JSON values.
func settingsJSON(cameraTimezone, videosStartMuted string) string {
	return `{"camera_timezone":` + cameraTimezone + `,"videos_start_muted":` + videosStartMuted +
		`,"defaults":{"camera_timezone":"server zone (unset)","videos_start_muted":false}}`
}

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
	assert.JSONEq(t, settingsJSON(`"Asia/Tokyo"`, "null"), rec.Body.String())

	rec = do(t, router, bob, http.MethodGet, "/api/me/settings", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, settingsJSON("null", "null"), rec.Body.String(), "alice's setting is not bob's")

	rec = do(t, router, alice, http.MethodPatch, "/api/me/settings", `{"camera_timezone":"Mars/Olympus"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = do(t, router, alice, http.MethodGet, "/api/me/settings", "")
	assert.JSONEq(t, settingsJSON(`"Asia/Tokyo"`, "null"), rec.Body.String(), "a refused change writes nothing")

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

// The video sound is the caller's own too: written to their row only, a key left out keeps its
// value, null returns it to the default, and a refused body writes nothing, the other key included.
func TestSettings_Integration_VideoSoundIsPerUser(t *testing.T) {
	pool := testdb.New(t)
	router := newOwnershipRouter(pool, &fakeObjectStore{objects: map[string][]byte{}})
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")

	patch := func(user, body string) string {
		t.Helper()
		rec := do(t, router, user, http.MethodPatch, "/api/me/settings", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	get := func(user string) string {
		t.Helper()
		rec := do(t, router, user, http.MethodGet, "/api/me/settings", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	stored := func(user string) *bool {
		t.Helper()
		var muted *bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT videos_start_muted FROM users WHERE id = $1`, user).Scan(&muted))
		return muted
	}

	assert.JSONEq(t, settingsJSON("null", "null"), get(alice), "nobody has chosen yet")
	assert.JSONEq(t, settingsJSON("null", "true"), patch(alice, `{"videos_start_muted":true}`))
	assert.Equal(t, ptr(true), stored(alice))
	assert.Nil(t, stored(bob), "alice's setting is not bob's")
	assert.JSONEq(t, settingsJSON("null", "null"), get(bob))

	assert.JSONEq(t, settingsJSON(`"Asia/Tokyo"`, "true"), patch(alice, `{"camera_timezone":"Asia/Tokyo"}`),
		"a key left out keeps its value")

	for _, body := range []string{`{"videos_start_muted":"yes"}`, `{"videos_start_muted":false,"camera_timezone":"Mars/Olympus"}`} {
		rec := do(t, router, alice, http.MethodPatch, "/api/me/settings", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
		assert.JSONEq(t, settingsJSON(`"Asia/Tokyo"`, "true"), get(alice), "%s writes nothing", body)
	}

	assert.JSONEq(t, settingsJSON(`"Asia/Tokyo"`, "false"), patch(alice, `{"videos_start_muted":false}`),
		"with sound, chosen: not the default's null")
	assert.Equal(t, ptr(false), stored(alice))
	assert.JSONEq(t, settingsJSON("null", "null"), patch(alice, `{"camera_timezone":null,"videos_start_muted":null}`))
	assert.Nil(t, stored(alice))
	assert.Nil(t, stored(bob))
}
