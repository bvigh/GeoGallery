package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest/crafted"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Positions of the new columns in the media INSERT's arguments.
const (
	insertArgTakenAt     = 7
	insertArgFileHash    = 9
	insertArgCameraModel = 10
	insertArgWidth       = 11
	insertArgHeight      = 12
	insertArgDuration    = 13
	insertArgEvidence    = 14
)

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

// registerFile runs content through registerMedia against a mock database and returns the arguments
// of the media INSERT.
func registerFile(t *testing.T, zone ingest.CameraZone, content []byte, filename string, mtime *time.Time) []any {
	t.Helper()
	db := &mockRegistrarDB{}
	reg := &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: zone}
	_, inserted, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(content), int64(len(content)), filename, mtime)
	require.NoError(t, err)
	require.True(t, inserted)
	for i, sql := range db.sqls {
		if strings.Contains(sql, "INSERT INTO media") {
			return db.args[i]
		}
	}
	require.Fail(t, "no media INSERT")
	return nil
}

func decodeEvidence(t *testing.T, insert []any) map[string]any {
	t.Helper()
	raw, ok := insert[insertArgEvidence].([]byte)
	require.True(t, ok, "capture_evidence is sent as JSON bytes")
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	return got
}

// evidenceWith returns the object for a file with no EXIF and no video data, plus overrides. The
// comparison is on the whole object, so a key that goes missing fails it: all keys are always present.
func evidenceWith(overrides map[string]any) map[string]any {
	want := map[string]any{
		"v": 1.0, "by": "ingest", "src": "none", "zone": nil, "wall": nil, "gps_utc": nil, "wall_minus_gps_s": nil,
		"offset_tag": nil, "offset_ignored": false, "offset_used_min": nil, "camera_zone": "Europe/Warsaw",
		"camera_zone_utc_fallback": false, "make": nil, "model": nil, "exif_partial": false, "creation_time": nil,
		"file_mtime": nil, "duration_s": nil, "encoder": nil, "codec": nil, "transcoded": false,
	}
	for k, v := range overrides {
		want[k] = v
	}
	return want
}

// TestRegisterMedia_ExifTimeZone pins that the stored taken_at and the EXIF GPS track point come
// from the registrar's camera zone (or the photo's GPS clock), never from the server's own zone.
func TestRegisterMedia_ExifTimeZone(t *testing.T) {
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true}
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		photo     exiftest.Photo
		zone      ingest.CameraZone
		want      string
		wantTrack bool
	}{
		{"camera zone", exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53"}, zone, "2018-10-23T09:50:53Z", false},
		{"a zero camera zone reads UTC", exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53"}, ingest.CameraZone{}, "2018-10-23T11:50:53Z", false},
		{"GPS clock abroad, track point at the same instant",
			exiftest.Photo{DateTimeOriginal: "2023:05:01 18:00:00", GPS: &exiftest.GPS{Lat: 35.68, Lng: 139.76, Time: time.Date(2023, 5, 1, 9, 0, 3, 0, time.UTC)}},
			zone, "2023-05-01T09:00:00Z", true},
		{"EXIF GPS without a GPS clock, track point in the camera zone",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", GPS: &exiftest.GPS{Lat: 47.4979, Lng: 19.0402}},
			zone, "2018-10-23T09:50:53Z", true},
		{"a phone abroad by OffsetTimeOriginal, track point at the same instant",
			exiftest.Photo{Make: "samsung", DateTimeOriginal: "2025:07:14 15:30:00", OffsetTimeOriginal: "+01:00", GPS: &exiftest.GPS{Lat: 51.5074, Lng: -0.1278}},
			zone, "2025-07-14T14:30:00Z", true},
		{"no EXIF time falls back to the file time", exiftest.Photo{}, zone, "2026-01-01T00:00:00Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &mockRegistrarDB{}
			reg := &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: tt.zone}
			photo := exiftest.JPEG(tt.photo)
			_, inserted, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(photo), int64(len(photo)), "P1011841.JPG", &mtime)
			require.NoError(t, err)
			require.True(t, inserted)

			want, err := time.Parse(time.RFC3339, tt.want)
			require.NoError(t, err)
			var takenAt, trackAt *time.Time
			for i, sql := range db.sqls {
				switch {
				case strings.Contains(sql, "INSERT INTO media"):
					takenAt = db.args[i][7].(*time.Time)
				case strings.Contains(sql, "INSERT INTO gps_tracks"):
					at := db.args[i][1].(time.Time)
					trackAt = &at
				}
			}
			require.NotNil(t, takenAt)
			assert.True(t, want.Equal(*takenAt), "taken_at %s, want %s", takenAt.UTC(), tt.want)
			if !tt.wantTrack {
				assert.Nil(t, trackAt)
				return
			}
			require.NotNil(t, trackAt, "EXIF GPS becomes a track point")
			assert.True(t, want.Equal(*trackAt), "track point at %s, want %s", trackAt.UTC(), tt.want)
		})
	}
}

func TestRegisterMedia_CaptureEvidence(t *testing.T) {
	warsaw := mustLoadLocation(t, "Europe/Warsaw")
	set := ingest.CameraZone{Loc: warsaw, Set: true, Rules: panasonicOffsetRule}
	unset := ingest.CameraZone{Loc: mustLoadLocation(t, "Asia/Tokyo")}
	utcServer := ingest.CameraZone{Loc: time.UTC, UTCFallback: true}
	mtime := time.Date(2001, 2, 3, 4, 5, 6, 789_000_000, time.UTC)
	const mtimeUTC = "2001-02-03T04:05:06.789Z"
	const wall = "2001:02:03 04:05:06"
	fix := func(s string) *exiftest.GPS {
		at, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return &exiftest.GPS{Lat: 10, Lng: 20, Time: at}
	}

	tests := []struct {
		name  string
		photo exiftest.Photo
		zone  ingest.CameraZone
		mtime *time.Time
		want  map[string]any
	}{
		{"DateTimeOriginal in the camera zone", exiftest.Photo{Make: "Acme", Model: "One", DateTimeOriginal: wall}, set, &mtime, map[string]any{
			"src": "exif_original", "zone": "camera", "wall": wall, "offset_used_min": 60.0, "make": "Acme", "model": "One", "file_mtime": mtimeUTC}},
		{"the camera zone's daylight saving is the offset used", exiftest.Photo{DateTimeOriginal: "2001:07:03 04:05:06"}, set, &mtime, map[string]any{
			"src": "exif_original", "zone": "camera", "wall": "2001:07:03 04:05:06", "offset_used_min": 120.0, "file_mtime": mtimeUTC}},
		{"IFD0 DateTime", exiftest.Photo{DateTime: wall}, set, &mtime, map[string]any{
			"src": "exif_ifd0", "zone": "camera", "wall": wall, "offset_used_min": 60.0, "file_mtime": mtimeUTC}},
		{"the GPS clock", exiftest.Photo{DateTimeOriginal: wall, GPS: fix("2001-02-02T19:05:03Z")}, set, &mtime, map[string]any{
			"src": "exif_original", "zone": "gps", "wall": wall, "gps_utc": "2001-02-02T19:05:03Z", "wall_minus_gps_s": 32403.0,
			"offset_used_min": 540.0, "file_mtime": mtimeUTC}},
		{"a refused GPS clock is recorded", exiftest.Photo{DateTimeOriginal: wall, GPS: fix("1970-01-01T00:00:00Z")}, set, &mtime, map[string]any{
			"src": "exif_original", "zone": "camera", "wall": wall, "gps_utc": "1970-01-01T00:00:00Z", "wall_minus_gps_s": 981173106.0,
			"offset_used_min": 60.0, "file_mtime": mtimeUTC}},
		{"OffsetTimeOriginal", exiftest.Photo{DateTimeOriginal: wall, OffsetTimeOriginal: "-03:30"}, set, &mtime, map[string]any{
			"src": "exif_original", "zone": "offset", "wall": wall, "offset_tag": "-03:30", "offset_used_min": -210.0, "file_mtime": mtimeUTC}},
		{"an ignored offset is recorded, and the camera zone applies", exiftest.Photo{Make: "Panasonic", DateTimeOriginal: wall, OffsetTimeOriginal: "+09:00"}, set, &mtime, map[string]any{
			"src": "exif_original", "zone": "camera", "wall": wall, "offset_tag": "+09:00", "offset_ignored": true, "offset_used_min": 60.0, "make": "Panasonic", "file_mtime": mtimeUTC}},
		{"old Sony's zero-length tag is not a partial read", exiftest.Photo{DateTime: wall, BrokenExifIFD: true}, set, &mtime, map[string]any{
			"src": "exif_ifd0", "zone": "camera", "wall": wall, "offset_used_min": 60.0, "file_mtime": mtimeUTC}},

		{"file time", exiftest.Photo{}, set, &mtime, map[string]any{"src": "mtime", "file_mtime": mtimeUTC}},
		{"no time at all", exiftest.Photo{}, set, nil, map[string]any{"src": "none"}},
		{"a wall tag that does not parse records the origin that applied", exiftest.Photo{DateTimeOriginal: "0000:00:00 00:00:00", DateTime: wall}, set, &mtime, map[string]any{
			"src": "mtime", "wall": "0000:00:00 00:00:00", "file_mtime": mtimeUTC}},
		{"an IFD0 wall tag that does not parse is not exif_ifd0", exiftest.Photo{DateTime: "0000:00:00 00:00:00"}, set, &mtime, map[string]any{
			"src": "mtime", "wall": "0000:00:00 00:00:00", "file_mtime": mtimeUTC}},
		{"a GPS stamp with no wall clock", exiftest.Photo{GPS: fix("2001-02-02T19:05:03Z")}, set, &mtime, map[string]any{
			"src": "mtime", "gps_utc": "2001-02-02T19:05:03Z", "file_mtime": mtimeUTC}},

		{"unset, on a server with a zone", exiftest.Photo{DateTimeOriginal: wall}, unset, &mtime, map[string]any{
			"src": "exif_original", "zone": "camera", "wall": wall, "offset_used_min": 540.0, "camera_zone": "server zone (unset)", "file_mtime": mtimeUTC}},
		{"unset, with a UTC server", exiftest.Photo{DateTimeOriginal: wall}, utcServer, &mtime, map[string]any{
			"src": "exif_original", "zone": "camera", "wall": wall, "offset_used_min": 0.0, "camera_zone": "server zone (unset)",
			"camera_zone_utc_fallback": true, "file_mtime": mtimeUTC}},
		{"a zero CameraZone reads as UTC", exiftest.Photo{DateTimeOriginal: wall}, ingest.CameraZone{}, &mtime, map[string]any{
			"src": "exif_original", "zone": "camera", "wall": wall, "offset_used_min": 0.0, "camera_zone": "server zone (unset)", "file_mtime": mtimeUTC}},
		{"the camera zone is written for a file without EXIF too", exiftest.Photo{}, utcServer, nil, map[string]any{
			"camera_zone": "server zone (unset)", "camera_zone_utc_fallback": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			insert := registerFile(t, tt.zone, exiftest.JPEG(tt.photo), "photo.jpg", tt.mtime)
			assert.Equal(t, evidenceWith(tt.want), decodeEvidence(t, insert))
		})
	}

	t.Run("a partial read", func(t *testing.T) {
		file := crafted.JPEG(crafted.PhotoTIFF(crafted.Entry{ID: 0x9286, Type: 50, Count: 1}))
		insert := registerFile(t, set, file, "photo.jpg", &mtime)
		assert.Equal(t, evidenceWith(map[string]any{
			"src": "exif_ifd0", "zone": "camera", "wall": crafted.DateTime, "offset_used_min": 60.0, "model": crafted.Model,
			"exif_partial": true, "file_mtime": mtimeUTC}), decodeEvidence(t, insert))
	})

	// jsonCanCarry refuses the time, and the file time is not tried in its place. The evidence names no
	// source and no zone for a time that was not stored, and keeps what the file said.
	t.Run("a time JSON cannot carry names no source", func(t *testing.T) {
		const far = "9999:06:01 12:00:00" // 10:00 UTC in Warsaw's summer, in year 9999
		insert := registerFile(t, set, exiftest.JPEG(exiftest.Photo{DateTimeOriginal: far}), "photo.jpg", &mtime)

		assert.Nil(t, insert[insertArgTakenAt])
		assert.Equal(t, evidenceWith(map[string]any{"src": "none", "wall": far, "file_mtime": mtimeUTC}), decodeEvidence(t, insert))
	})
}

func TestNewCaptureEvidence_Video(t *testing.T) {
	zone := ingest.CameraZone{Loc: time.UTC, Set: true}
	mtime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	created := time.Date(2002, 3, 4, 5, 6, 7, 0, time.FixedZone("", 3600))
	exifTime := time.Date(2003, 4, 5, 6, 7, 8, 0, time.FixedZone("", 7200))
	duration := 12.5
	const createdUTC, mtimeUTC = "2002-03-04T04:06:07Z", "2001-02-03T04:05:06Z"

	tests := []struct {
		name  string
		exif  ingest.ExifData
		probe ingest.VideoProbeResult
		mtime *time.Time
		want  map[string]any
	}{
		{"the container's time",
			ingest.ExifData{},
			ingest.VideoProbeResult{CreatedAt: &created, CodecName: "h264", Encoder: "Lavf60.16.100", DurationS: &duration},
			&mtime,
			map[string]any{"src": "container", "creation_time": createdUTC, "file_mtime": mtimeUTC, "duration_s": 12.5, "encoder": "Lavf60.16.100", "codec": "h264"}},
		{"the container's time and the file time are kept when EXIF wins",
			ingest.ExifData{TakenAt: &exifTime, TakenAtZone: ingest.ZoneCamera, Raw: ingest.RawExif{WallTag: ingest.WallTagOriginal, WallClock: "2003:04:05 06:07:08"}},
			ingest.VideoProbeResult{CreatedAt: &created, CodecName: "hevc"},
			&mtime,
			map[string]any{"src": "exif_original", "zone": "camera", "wall": "2003:04:05 06:07:08", "offset_used_min": 120.0, "creation_time": createdUTC, "file_mtime": mtimeUTC, "codec": "hevc"}},
		{"a re-encoded video",
			ingest.ExifData{},
			ingest.VideoProbeResult{CreatedAt: &created, CodecName: "mpeg4"},
			nil,
			map[string]any{"src": "container", "creation_time": createdUTC, "codec": "mpeg4", "transcoded": true}},
		{"no container time, so the file time",
			ingest.ExifData{},
			ingest.VideoProbeResult{CodecName: "h264"},
			&mtime,
			map[string]any{"src": "mtime", "file_mtime": mtimeUTC, "codec": "h264"}},
		{"the codec name is cleaned",
			ingest.ExifData{},
			ingest.VideoProbeResult{CodecName: "h2\xff64"},
			nil,
			map[string]any{"codec": "h264", "transcoded": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := newCaptureEvidence(tt.exif, tt.probe, tt.mtime, zone).encode()
			var got map[string]any
			require.NoError(t, json.Unmarshal(raw, &got))
			want := evidenceWith(tt.want)
			want["camera_zone"] = "UTC"
			assert.Equal(t, want, got)
		})
	}
}

func TestCaptureEvidence_ValuesThatCannotBeWritten(t *testing.T) {
	t.Run("a file time RFC 3339 cannot express is null", func(t *testing.T) {
		mtime := time.UnixMilli(math.MaxInt64)
		ev := newCaptureEvidence(ingest.ExifData{}, ingest.VideoProbeResult{}, &mtime, ingest.CameraZone{})
		assert.Nil(t, ev.FileMtime)
		assert.NotNil(t, ev.encode())
	})
	t.Run("a GPS stamp RFC 3339 cannot express takes its difference with it", func(t *testing.T) {
		stamp := time.Date(20000, 1, 1, 0, 0, 0, 0, time.UTC)
		diff := time.Hour
		ev := newCaptureEvidence(ingest.ExifData{Raw: ingest.RawExif{GPSStamp: &stamp, WallMinusGPS: &diff}}, ingest.VideoProbeResult{}, nil, ingest.CameraZone{})
		assert.Nil(t, ev.GPSUTC)
		assert.Nil(t, ev.WallMinusGPSS)
	})
	t.Run("evidence that cannot be encoded is NULL, not a failure", func(t *testing.T) {
		nan := math.NaN()
		ev := newCaptureEvidence(ingest.ExifData{}, ingest.VideoProbeResult{DurationS: &nan}, nil, ingest.CameraZone{})
		assert.Nil(t, ev.encode())
	})
}

func TestRegisterMedia_ColumnsFromExif(t *testing.T) {
	zone := ingest.CameraZone{Loc: time.UTC, Set: true}
	insert := registerFile(t, zone, exiftest.JPEG(exiftest.Photo{Model: "One", PixelX: 4000, PixelY: 3000}), "photo.jpg", nil)

	model := insert[insertArgCameraModel].(*string)
	require.NotNil(t, model)
	assert.Equal(t, "One", *model)
	assert.Equal(t, 4000, *insert[insertArgWidth].(*int))
	assert.Equal(t, 3000, *insert[insertArgHeight].(*int))
	assert.Nil(t, insert[insertArgDuration], "a photo has no duration")
}

func TestRegisterMedia_NoCameraModelIsNull(t *testing.T) {
	insert := registerFile(t, ingest.CameraZone{}, exiftest.JPEG(exiftest.Photo{}), "photo.jpg", nil)
	assert.Nil(t, insert[insertArgCameraModel])
	assert.Nil(t, insert[insertArgWidth])
	assert.Nil(t, insert[insertArgHeight])
}

func TestRegisterMedia_InvalidBytesInTagsStillIngest(t *testing.T) {
	photo := exiftest.JPEG(exiftest.Photo{
		Make:               "Ac\xffme",
		Model:              "O\xfe\xffne",
		DateTimeOriginal:   "2001:02:03 04:05:06",
		OffsetTimeOriginal: "\xff+01:00",
	})
	insert := registerFile(t, ingest.CameraZone{}, photo, "photo.jpg", nil)

	assert.Equal(t, "One", *insert[insertArgCameraModel].(*string))
	got := decodeEvidence(t, insert)
	assert.Equal(t, "Acme", got["make"])
	assert.Equal(t, "One", got["model"])
	assert.Equal(t, "+01:00", got["offset_tag"])
}

// ffprobe finds nothing in these bytes, so its width and height are 0. No ffprobe at all gives the
// same zero result, so this test needs neither tool.
func TestRegisterMedia_ProbeWidthZeroIsNull(t *testing.T) {
	insert := registerFile(t, ingest.CameraZone{}, []byte("not a video"), "clip.mp4", nil)
	assert.Nil(t, insert[insertArgWidth])
	assert.Nil(t, insert[insertArgHeight])
	assert.Nil(t, insert[insertArgDuration])
	assert.Nil(t, decodeEvidence(t, insert)["codec"])
}

func TestDimension(t *testing.T) {
	v := func(n int) *int { return &n }
	tests := []struct {
		name  string
		exif  *int
		probe int
		want  *int
	}{
		{"EXIF wins", v(4000), 1920, v(4000)},
		{"the probe when EXIF has none", nil, 1920, v(1920)},
		{"0 from the probe is unknown", nil, 0, nil},
		{"a zero EXIF value leaves the probe", v(0), 1920, v(1920)},
		{"a negative EXIF value is unknown", v(-1), 0, nil},
		{"the largest value INT holds", v(math.MaxInt32), 0, v(math.MaxInt32)},
		{"a value INT cannot hold is unknown", v(math.MaxInt32 + 1), 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dimension(tt.exif, tt.probe))
		})
	}
}

// writeClip encodes a synthetic clip with a creation_time and returns its bytes.
func writeClip(t *testing.T, codec string) []byte {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	path := filepath.Join(t.TempDir(), "clip.mp4")
	out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=32x16:d=1:r=10",
		"-c:v", codec, "-metadata", "creation_time=2001-02-03T04:05:06Z", path).CombinedOutput()
	require.NoError(t, err, string(out))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return content
}

// keepingUploadStorage keeps the bytes of the last upload, so a test can read the stored object.
type keepingUploadStorage struct {
	mockUploadStorage
	body []byte
}

func (s *keepingUploadStorage) Upload(_ context.Context, _ string, body io.Reader, _ int64, _ string) error {
	var err error
	s.body, err = io.ReadAll(body)
	return err
}

// The time ingest read reaches the transcode, so the stored object still says when it was recorded.
func TestRegisterMedia_TranscodedObjectKeepsTheTimeIngestRead(t *testing.T) {
	content := writeClip(t, "mpeg4")
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &keepingUploadStorage{}
	reg := &MediaRegistrar{db: &mockRegistrarDB{}, storage: store, cameraZone: ingest.CameraZone{Loc: time.UTC, Set: true}}

	_, inserted, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(content), int64(len(content)), "clip.mp4", &mtime)
	require.NoError(t, err)
	require.True(t, inserted)

	tmp, stored, err := ingest.ProbeVideo(bytes.NewReader(store.body))
	require.NoError(t, err)
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	assert.Equal(t, "h264", stored.CodecName, "the object is the transcode")
	require.NotNil(t, stored.CreatedAt)
	assert.True(t, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC).Equal(*stored.CreatedAt), "creation_time %s", stored.CreatedAt)
}

// TestRegisterMedia_VideoEvidence runs real ffmpeg and ffprobe, from the container to the INSERT.
func TestRegisterMedia_VideoEvidence(t *testing.T) {
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		codec      string
		wantCodec  string
		transcoded bool
	}{
		{"kept as uploaded", "libx264", "h264", false},
		{"re-encoded at upload, describing the original", "mpeg4", "mpeg4", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			insert := registerFile(t, ingest.CameraZone{Loc: time.UTC, Set: true}, writeClip(t, tt.codec), "clip.mp4", &mtime)

			assert.Equal(t, 32, *insert[insertArgWidth].(*int))
			assert.Equal(t, 16, *insert[insertArgHeight].(*int))
			require.NotNil(t, insert[insertArgDuration].(*float64))
			assert.InDelta(t, 1.0, *insert[insertArgDuration].(*float64), 0.2)
			assert.Nil(t, insert[insertArgCameraModel], "a video's model is not read from its container")

			got := decodeEvidence(t, insert)
			assert.Equal(t, "container", got["src"])
			assert.Equal(t, "2001-02-03T04:05:06Z", got["creation_time"])
			assert.Equal(t, "2026-01-01T00:00:00Z", got["file_mtime"])
			assert.Equal(t, tt.wantCodec, got["codec"])
			assert.Equal(t, tt.transcoded, got["transcoded"])
			assert.InDelta(t, 1.0, got["duration_s"], 0.2)
			assert.Contains(t, got["encoder"], "Lavf")
			assert.Nil(t, got["zone"])
			assert.Nil(t, got["offset_used_min"])
		})
	}
}

func TestJSONCanCarry(t *testing.T) {
	east, west := time.FixedZone("", 14*3600), time.FixedZone("", -12*3600)
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"an ordinary date", time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC), true},
		{"the first instant of year 1", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"the last second of year 0", time.Date(0, 12, 31, 23, 59, 59, 0, time.UTC), false},
		{"the last second of year 9998", time.Date(9998, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"the first instant of year 9999", time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"west of UTC, past the end in UTC", time.Date(9998, 12, 31, 23, 0, 0, 0, west), false},
		{"east of UTC, still inside in UTC", time.Date(9999, 1, 1, 0, 0, 0, 0, east), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, jsonCanCarry(tt.t))
		})
	}
}
