package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timedFile is one file of an upload, with the file_mtime the browser sends for it: milliseconds
// since the epoch, or "" for a value that gives no fallback time.
type timedFile struct {
	name    string
	content []byte
	mtime   string
}

// uploadTimedFiles builds an authenticated upload of files, in order.
func uploadTimedFiles(t *testing.T, files ...timedFile) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		fw, err := mw.CreateFormFile("files", f.name)
		require.NoError(t, err)
		_, err = fw.Write(f.content)
		require.NoError(t, err)
	}
	for _, f := range files {
		require.NoError(t, mw.WriteField("file_mtime", f.mtime))
	}
	require.NoError(t, mw.Close())
	r := httptest.NewRequest(http.MethodPost, "/api/media/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return withTestUser(r)
}

// uploadTimeReport uploads files through a handler whose registrar is reg, and returns the response's
// time_report as JSON.
func uploadTimeReport(t *testing.T, reg *MediaRegistrar, files ...timedFile) string {
	t.Helper()
	h := &MediaHandler{registrar: reg, runMatcher: noopMatcher, runTripDetect: noopTripDetect}
	w := httptest.NewRecorder()

	h.Upload(w, uploadTimedFiles(t, files...))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Contains(t, resp, "time_report")
	return string(resp["time_report"])
}

// Synthetic photos, one per way a capture time is read, and when it is not.
var (
	photoInCameraZone = exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2020:01:02 03:04:05"})
	photoIFD0         = exiftest.JPEG(exiftest.Photo{DateTime: "2020:01:02 03:04:05"})
	photoGPSClock     = exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2023:05:01 18:00:00",
		GPS: &exiftest.GPS{Lat: 35.68, Lng: 139.76, Time: time.Date(2023, 5, 1, 9, 0, 3, 0, time.UTC)}})
	photoOffsetTag = exiftest.JPEG(exiftest.Photo{Make: "samsung", DateTimeOriginal: "2025:07:14 15:30:00", OffsetTimeOriginal: "+01:00"})
	// 10:00 UTC in year 9999, which JSON cannot carry: no time is stored, and the file time is not tried.
	photoTooFar = exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "9999:06:01 12:00:00"})
)

// uploadMtime is a file_mtime as the browser sends it.
var uploadMtime = strconv.FormatInt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), 10)

// The receipt counts what capture_evidence records for each file the upload stored as a new row:
// here one file per source and per zone rule. A duplicate was dated when it was first stored, and a
// file that failed was never stored.
func TestUpload_TimeReport(t *testing.T) {
	warsaw := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicOffsetRule}

	t.Run("each source and zone rule, new files only", func(t *testing.T) {
		db := &mockRegistrarDB{clockRules: ingest.ClockRules{tz200ClockRule}, known: hashesOf("no exif: dup")}
		reg := &MediaRegistrar{db: db, storage: &failOnceStorage{failAt: 0}, cameraZone: warsaw}

		got := uploadTimeReport(t, reg,
			timedFile{"broken.jpg", []byte("no exif: broken"), uploadMtime}, // the first upload to S3 fails
			timedFile{"camera.jpg", photoInCameraZone, uploadMtime},
			timedFile{"ifd0.jpg", photoIFD0, uploadMtime},
			timedFile{"gps.jpg", photoGPSClock, uploadMtime},
			timedFile{"rule.jpg", exiftest.JPEG(tz200Winter), uploadMtime},
			timedFile{"offset.jpg", photoOffsetTag, uploadMtime},
			timedFile{"z-copied.jpg", []byte("no exif: z"), uploadMtime},
			timedFile{"undated.jpg", []byte("no exif: undated"), ""},
			timedFile{"too-far.jpg", photoTooFar, uploadMtime},
			timedFile{"a-copied.jpg", []byte("no exif: a"), uploadMtime},
			timedFile{"dup.jpg", []byte("no exif: dup"), uploadMtime},
		)

		assert.JSONEq(t, `{
			"by_source": {"exif_original": 4, "exif_ifd0": 1, "container": 0, "mtime": 2, "none": 2},
			"by_zone": {"gps": 1, "clock_rule": 1, "offset": 1, "camera": 2},
			"camera_zone": "Europe/Warsaw",
			"camera_zone_from": "server",
			"assumed_zone_is_utc": false,
			"file_date_names": ["z-copied.jpg", "a-copied.jpg"]
		}`, got)
	})
	t.Run("a video dated by its container", func(t *testing.T) {
		reg := &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}, cameraZone: warsaw}

		got := uploadTimeReport(t, reg, timedFile{"clip.mp4", writeClip(t, "libx264"), uploadMtime})

		assert.JSONEq(t, `{
			"by_source": {"exif_original": 0, "exif_ifd0": 0, "container": 1, "mtime": 0, "none": 0},
			"by_zone": {"gps": 0, "clock_rule": 0, "offset": 0, "camera": 0},
			"camera_zone": "Europe/Warsaw",
			"camera_zone_from": "server",
			"assumed_zone_is_utc": false,
			"file_date_names": []
		}`, got)
	})
	t.Run("every key is there when nothing new was stored", func(t *testing.T) {
		reg := &MediaRegistrar{db: &mockRegistrarDB{known: hashesOf("no exif: dup")}, storage: &failOnceStorage{failAt: 0}}

		got := uploadTimeReport(t, reg,
			timedFile{"dup.jpg", []byte("no exif: dup"), uploadMtime},
			timedFile{"broken.jpg", photoInCameraZone, uploadMtime},
		)

		assert.JSONEq(t, `{
			"by_source": {"exif_original": 0, "exif_ifd0": 0, "container": 0, "mtime": 0, "none": 0},
			"by_zone": {"gps": 0, "clock_rule": 0, "offset": 0, "camera": 0},
			"camera_zone": "server zone (unset)",
			"camera_zone_from": "server",
			"assumed_zone_is_utc": false,
			"file_date_names": []
		}`, got, "a zero zone is UTC, but no file was read in it")
	})
}

// assumed_zone_is_utc warns of a camera zone that reads every camera clock as UTC, which few cameras
// show. One file read in that zone is enough. A zone at UTC for part of the year only, or a file
// dated by anything but the camera zone, gives no warning.
func TestUpload_TimeReportAssumedZoneIsUTC(t *testing.T) {
	london := mustLoadLocation(t, "Europe/London")
	tests := []struct {
		name       string
		zone       ingest.CameraZone
		content    []byte
		wantZone   string
		wantCamera int
		want       bool
	}{
		{"unset, on a server in UTC", ingest.CameraZone{Loc: time.UTC, UTCFallback: true}, photoInCameraZone, "server zone (unset)", 1, true},
		{"set to UTC", ingest.CameraZone{Loc: time.UTC, Set: true}, photoInCameraZone, "UTC", 1, true},
		{"a zero zone reads as UTC", ingest.CameraZone{}, photoInCameraZone, "server zone (unset)", 1, true},
		{"UTC in winter only, for a winter photo", ingest.CameraZone{Loc: london, Set: true}, photoInCameraZone, "Europe/London", 1, false},
		{"unset, on a server in London", ingest.CameraZone{Loc: london}, photoInCameraZone, "server zone (unset)", 1, false},
		{"UTC, but the photo's GPS clock gave the offset", ingest.CameraZone{Loc: time.UTC, Set: true}, photoGPSClock, "UTC", 0, false},
		{"UTC, but the photo's offset tag gave it", ingest.CameraZone{Loc: time.UTC, Set: true}, photoOffsetTag, "UTC", 0, false},
		{"UTC, but no EXIF time", ingest.CameraZone{Loc: time.UTC, Set: true}, []byte("no exif"), "UTC", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &MediaRegistrar{db: &mockRegistrarDB{}, storage: &mockUploadStorage{}, cameraZone: tt.zone}

			var got TimeReport
			require.NoError(t, json.Unmarshal([]byte(uploadTimeReport(t, reg, timedFile{"photo.jpg", tt.content, uploadMtime})), &got))

			assert.Equal(t, tt.wantZone, got.CameraZone)
			assert.Equal(t, tt.wantCamera, got.ByZone.Camera)
			assert.Equal(t, tt.want, got.AssumedZoneIsUTC)
		})
	}
}

// A scan's receipt counts the same way. Its file dates come in the order the walk met them.
func TestRunIngestScan_TimeReport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "b.jpg", string(photoInCameraZone))
	writeFile(t, dir, "c.jpg", "no exif: c")
	writeFile(t, dir, "a.jpg", "no exif: a")
	writeFile(t, dir, "dup.jpg", "no exif: dup")
	writeFile(t, dir, "empty.jpg", "") // registerMedia refuses an empty file
	zone := ingest.CameraZone{Loc: time.UTC, UTCFallback: true}
	deps := IngestDeps{
		registrar:     &MediaRegistrar{db: &mockRegistrarDB{known: hashesOf("no exif: dup")}, storage: &mockUploadStorage{}, cameraZone: zone},
		runMatcher:    noopMatcher,
		runTripDetect: noopTripDetect,
	}

	res, err := RunIngestScan(context.Background(), deps, dir, testOwnerID, nil)

	require.NoError(t, err)
	assert.Equal(t, ScanResult{Ingested: 3, Skipped: 1, Failed: 1}, scanCounts(res))
	want := newTimeReport(zone)
	want.BySource = TimeSourceCounts{ExifOriginal: 1, Mtime: 2}
	want.ByZone = TimeZoneCounts{Camera: 1}
	want.AssumedZoneIsUTC = true
	want.FileDateNames = []string{"a.jpg", "c.jpg"}
	assert.Equal(t, want, res.TimeReport)
}
