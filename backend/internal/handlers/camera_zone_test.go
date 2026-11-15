package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The zones of these tests: the server's is Warsaw; the owner's setting Tokyo; a batch's New York.
// photoInCameraZone's wall clock, 2020-01-02 03:04:05, reads as a different instant in each.
const (
	inWarsaw  = "2020-01-02T02:04:05Z"
	inTokyo   = "2020-01-01T18:04:05Z"
	inNewYork = "2020-01-02T08:04:05Z"
)

func warsawServerZone(t *testing.T) ingest.CameraZone {
	return ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, From: ingest.CameraZoneFromServer,
		Rules: panasonicOffsetRule}
}

func TestIngestCameraZone(t *testing.T) {
	server := warsawServerZone(t)
	tokyo, newYork := mustLoadLocation(t, "Asia/Tokyo"), mustLoadLocation(t, "America/New_York")
	tests := []struct {
		name      string
		setting   *string
		batch     *time.Location
		wantName  string
		wantFrom  string
		wantLoads int
	}{
		{"the server's, without a setting or a batch zone", nil, nil, "Europe/Warsaw", ingest.CameraZoneFromServer, 1},
		{"the owner's setting over the server's", ptr("Asia/Tokyo"), nil, "Asia/Tokyo", ingest.CameraZoneFromUser, 1},
		{"the batch's over the owner's, which is not read", ptr("Asia/Tokyo"), newYork, "America/New_York", ingest.CameraZoneFromBatch, 0},
		{"the batch's over the server's", nil, tokyo, "Asia/Tokyo", ingest.CameraZoneFromBatch, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &mockRegistrarDB{zoneSetting: tt.setting}
			got, err := ingestCameraZone(context.Background(), db, server, testOwnerID, tt.batch)
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, got.Name())
			assert.Equal(t, tt.wantFrom, got.Source())
			assert.True(t, got.Set)
			assert.Equal(t, server.Rules, got.Rules, "the camera rules stay")
			assert.Len(t, db.zoneLoads, tt.wantLoads)
			for _, args := range db.zoneLoads {
				assert.Equal(t, []any{testOwnerID}, args, "the owner's own row")
			}
		})
	}

	t.Run("a stored zone this server cannot load is an error", func(t *testing.T) {
		db := &mockRegistrarDB{zoneSetting: ptr("Gone/Zone")}
		_, err := ingestCameraZone(context.Background(), db, server, testOwnerID, nil)
		assert.ErrorContains(t, err, `unknown time zone "Gone/Zone"`)
	})
	t.Run("a failed lookup is an error", func(t *testing.T) {
		db := &mockRegistrarDB{zoneErr: errors.New("down")}
		_, err := ingestCameraZone(context.Background(), db, server, testOwnerID, nil)
		assert.ErrorContains(t, err, "down")
	})
}

func TestStoredItemCameraZone(t *testing.T) {
	owner := warsawServerZone(t).In(mustLoadLocation(t, "Asia/Tokyo"), ingest.CameraZoneFromUser)
	tests := []struct {
		name, from, zone string
		wantName         string
		wantFrom         string
	}{
		{"a zone its upload named stays with it", "batch", "America/New_York", "America/New_York", ingest.CameraZoneFromBatch},
		{"one read in the owner's zone then is read in the owner's zone now", "user", "Europe/Paris", "Asia/Tokyo", ingest.CameraZoneFromUser},
		{"one read in the server's zone too", "server", "Europe/Warsaw", "Asia/Tokyo", ingest.CameraZoneFromUser},
		{"a row recorded before per-user zones", "", "Europe/Warsaw", "Asia/Tokyo", ingest.CameraZoneFromUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := storedItemCameraZone(owner, tt.from, tt.zone)
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, got.Name())
			assert.Equal(t, tt.wantFrom, got.Source())
			assert.Equal(t, owner.Rules, got.Rules)
		})
	}
	t.Run("a batch zone this server cannot load is an error", func(t *testing.T) {
		_, err := storedItemCameraZone(owner, "batch", "Gone/Zone")
		assert.Error(t, err)
	})
}

// uploadInZone uploads photoInCameraZone through the handler, the owner's setting being setting, with
// ?camera_timezone=batch when batch is not "". It returns the response, the media INSERT's arguments
// (nil without one) and the database.
func uploadInZone(t *testing.T, setting *string, batch string) (*httptest.ResponseRecorder, []any, *mockRegistrarDB) {
	t.Helper()
	db := &mockRegistrarDB{zoneSetting: setting}
	h := &MediaHandler{
		registrar:     &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: warsawServerZone(t)},
		runMatcher:    noopMatcher,
		runTripDetect: noopTripDetect,
	}
	req := uploadTimedFiles(t, timedFile{"P1000001.JPG", photoInCameraZone, uploadMtime})
	if batch != "" {
		req.URL.RawQuery = url.Values{"camera_timezone": {batch}}.Encode()
	}
	w := httptest.NewRecorder()
	h.Upload(w, req)
	for i, sql := range db.sqls {
		if strings.Contains(sql, "INSERT INTO media") {
			return w, db.args[i], db
		}
	}
	return w, nil, db
}

// The issue's order of the last fallback: the batch's zone, then the owner's, then CAMERA_TIMEZONE.
func TestUpload_CameraZonePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		setting  *string
		batch    string
		want     string
		wantZone string
		wantFrom string
	}{
		{"the server's zone", nil, "", inWarsaw, "Europe/Warsaw", "server"},
		{"the owner's zone over the server's", ptr("Asia/Tokyo"), "", inTokyo, "Asia/Tokyo", "user"},
		{"the upload's zone over the owner's", ptr("Asia/Tokyo"), "America/New_York", inNewYork, "America/New_York", "batch"},
		{"the upload's zone over the server's", nil, "America/New_York", inNewYork, "America/New_York", "batch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, insert, _ := uploadInZone(t, tt.setting, tt.batch)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.NotNil(t, insert)

			assert.Equal(t, tt.want, takenAtUTC(t, insert))
			evidence := decodeEvidence(t, insert)
			assert.Equal(t, "camera", evidence["zone"])
			assert.Equal(t, tt.wantZone, evidence["camera_zone"])
			assert.Equal(t, tt.wantFrom, evidence["camera_zone_from"])

			var resp struct {
				TimeReport TimeReport `json:"time_report"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantZone, resp.TimeReport.CameraZone)
			assert.Equal(t, tt.wantFrom, resp.TimeReport.CameraZoneFrom)
		})
	}

	t.Run("the zone is read once per request, not per file", func(t *testing.T) {
		db := &mockRegistrarDB{zoneSetting: ptr("Asia/Tokyo")}
		h := &MediaHandler{
			registrar:     &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: warsawServerZone(t)},
			runMatcher:    noopMatcher,
			runTripDetect: noopTripDetect,
		}
		w := httptest.NewRecorder()
		h.Upload(w, uploadFiles("a.jpg", "b.jpg", "c.jpg"))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, [][]any{{testOwnerID}}, db.zoneLoads)
	})
}

func TestUpload_CameraTimezoneRefusedBeforeBodyIsRead(t *testing.T) {
	for _, zone := range []string{"Europe/Nowhere", "", "Local", "../etc/passwd"} {
		t.Run(zone, func(t *testing.T) {
			db := &mockRegistrarDB{}
			store := &mockUploadStorage{}
			h := &MediaHandler{db: &uploadAlbumDB{member: true}, registrar: &MediaRegistrar{db: db, storage: store},
				runMatcher: noopMatcher, runTripDetect: noopTripDetect}
			req, body := uploadToAlbum(testAlbumID, "photo.jpg")
			req.URL.RawQuery = url.Values{"camera_timezone": {zone}, "album_id": {testAlbumID}}.Encode()

			w := httptest.NewRecorder()
			h.Upload(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "camera_timezone: ")
			assert.Zero(t, body.reads, "the body must not be read")
			assert.Empty(t, h.db.(*uploadAlbumDB).lookups, "checked before the album")
			assert.Empty(t, db.zoneLoads)
			assert.Empty(t, store.keys)
		})
	}
}

func TestUpload_CameraZoneSettingFailure(t *testing.T) {
	for name, db := range map[string]*mockRegistrarDB{
		"the lookup fails":                {zoneErr: errors.New("down")},
		"the stored zone no longer loads": {zoneSetting: ptr("Gone/Zone")},
	} {
		t.Run(name+": 500 with the body unread", func(t *testing.T) {
			store := &mockUploadStorage{}
			h := &MediaHandler{registrar: &MediaRegistrar{db: db, storage: store}, runMatcher: noopMatcher, runTripDetect: noopTripDetect}
			req := uploadFiles("photo.jpg")
			body := &countingReader{r: req.Body}
			req.Body = io.NopCloser(body)

			w := httptest.NewRecorder()
			h.Upload(w, req)

			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.Zero(t, body.reads)
			assert.Empty(t, store.keys)
		})
	}
}

func TestRunIngestScan_CameraZone(t *testing.T) {
	scan := func(t *testing.T, db *mockRegistrarDB, zone *time.Location) (ScanResult, error) {
		t.Helper()
		dir := t.TempDir()
		writeFile(t, dir, "P1000001.JPG", string(photoInCameraZone))
		deps := IngestDeps{
			registrar:     &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: warsawServerZone(t)},
			runMatcher:    noopMatcher,
			runTripDetect: noopTripDetect,
		}
		return RunIngestScan(context.Background(), deps, dir, testOwnerID, zone)
	}
	stored := func(t *testing.T, db *mockRegistrarDB) string {
		t.Helper()
		for i, sql := range db.sqls {
			if strings.Contains(sql, "INSERT INTO media") {
				return takenAtUTC(t, db.args[i])
			}
		}
		require.Fail(t, "no media INSERT")
		return ""
	}

	t.Run("the owner's zone", func(t *testing.T) {
		db := &mockRegistrarDB{zoneSetting: ptr("Asia/Tokyo")}
		res, err := scan(t, db, nil)
		require.NoError(t, err)
		assert.Equal(t, inTokyo, stored(t, db))
		assert.Equal(t, "Asia/Tokyo", res.TimeReport.CameraZone)
		assert.Equal(t, "user", res.TimeReport.CameraZoneFrom)
	})
	t.Run("--tz over the owner's zone", func(t *testing.T) {
		db := &mockRegistrarDB{zoneSetting: ptr("Asia/Tokyo")}
		res, err := scan(t, db, mustLoadLocation(t, "America/New_York"))
		require.NoError(t, err)
		assert.Equal(t, inNewYork, stored(t, db))
		assert.Equal(t, "America/New_York", res.TimeReport.CameraZone)
		assert.Equal(t, "batch", res.TimeReport.CameraZoneFrom)
		assert.Empty(t, db.zoneLoads, "the owner's setting is not needed")
	})
	t.Run("a failed lookup stops the scan before any file", func(t *testing.T) {
		db := &mockRegistrarDB{zoneErr: errors.New("down")}
		res, err := scan(t, db, nil)
		assert.ErrorContains(t, err, "down")
		assert.Equal(t, ScanResult{}, res)
		assert.Empty(t, db.sqls)
	})
}

// A zone named for the batch was chosen for those very files, so its being UTC is no warning; the
// owner's setting and the server's zone at UTC are.
func TestNewTimeReport_UTCWarning(t *testing.T) {
	server := ingest.CameraZone{Loc: time.UTC, UTCFallback: true}
	tests := []struct {
		name string
		zone ingest.CameraZone
		want bool
	}{
		{"the server's zone, unset on a UTC server", server, true},
		{"the owner's zone at UTC", server.In(time.UTC, ingest.CameraZoneFromUser), true},
		{"the batch's zone at UTC", server.In(time.UTC, ingest.CameraZoneFromBatch), false},
		{"the owner's zone elsewhere", server.In(mustLoadLocation(t, "Asia/Tokyo"), ingest.CameraZoneFromUser), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTimeReport(tt.zone)
			r.add("a.jpg", registerResult{src: captureSrcExifOriginal, zone: ingest.ZoneCamera})
			assert.Equal(t, tt.want, r.AssumedZoneIsUTC)
		})
	}
}
