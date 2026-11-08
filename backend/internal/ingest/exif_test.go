package ingest

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bvigh/geogallery/internal/ingest/exiftest"
)

func TestDetectMediaType_Filename360_IsPhoto360(t *testing.T) {
	result := DetectMediaType(ExifData{}, "photo360.jpg", ".jpg")
	assert.Equal(t, "photo_360", result)
}

func TestDetectMediaType_StandardJpg_IsPhoto(t *testing.T) {
	result := DetectMediaType(ExifData{}, "image.jpg", ".jpg")
	assert.Equal(t, "photo", result)
}

func TestDetectMediaType_PanoMov_IsVideo360(t *testing.T) {
	result := DetectMediaType(ExifData{}, "pano.mov", ".mov")
	assert.Equal(t, "video_360", result)
}

func TestDetectMediaType_Mp4_IsVideo(t *testing.T) {
	result := DetectMediaType(ExifData{}, "clip.mp4", ".mp4")
	assert.Equal(t, "video", result)
}

func TestDetectMediaType_3gp_IsVideo(t *testing.T) {
	result := DetectMediaType(ExifData{}, "clip.3gp", ".3gp")
	assert.Equal(t, "video", result)
}

func TestDetectMediaType_NonSquareRatio_IsPhoto(t *testing.T) {
	w, h := 3000, 1600
	e := ExifData{WidthPx: &w, HeightPx: &h}

	result := DetectMediaType(e, "image.jpg", ".jpg")

	assert.Equal(t, "photo", result)
}

func TestDetectMediaType_2to1Ratio_IsPhoto360(t *testing.T) {
	w, h := 4000, 2000
	e := ExifData{WidthPx: &w, HeightPx: &h}

	result := DetectMediaType(e, "image.jpg", ".jpg")

	assert.Equal(t, "photo_360", result)
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func gpsAt(t *testing.T, s string) *exiftest.GPS {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return &exiftest.GPS{Lat: 47.4979, Lng: 19.0402, Time: at}
}

func TestExtractExif_TakenAt(t *testing.T) {
	warsaw := mustZone(t, "Europe/Warsaw")
	tests := []struct {
		name       string
		photo      exiftest.Photo
		zone       *time.Location
		want       string // RFC3339 instant; empty: no TakenAt
		wantZone   string
		wantOffset int // seconds east of UTC in TakenAt's location, checked for ZoneGPS and ZoneOffset
	}{
		{"summer photo reads the camera zone with DST",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53"}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"winter photo reads the camera zone without DST",
			exiftest.Photo{DateTimeOriginal: "2018:12:23 11:50:53"}, warsaw, "2018-12-23T10:50:53Z", ZoneCamera, 0},
		{"nil zone reads UTC, whatever the server's zone",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53"}, nil, "2018-10-23T11:50:53Z", ZoneCamera, 0},
		{"IFD0 DateTime when DateTimeOriginal is missing",
			exiftest.Photo{DateTime: "2018:10:23 11:50:53"}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"DateTimeOriginal wins over DateTime",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", DateTime: "2019:01:01 00:00:00"}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"an unparseable DateTimeOriginal does not fall back to DateTime",
			exiftest.Photo{DateTimeOriginal: "0000:00:00 00:00:00", DateTime: "2018:10:23 11:50:53"}, warsaw, "", "", 0},
		{"no capture time",
			exiftest.Photo{GPS: gpsAt(t, "2018-10-23T09:50:53Z")}, warsaw, "", "", 0},

		{"GPS clock at home agrees with the camera zone",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", GPS: gpsAt(t, "2018-10-23T09:50:52Z")}, warsaw, "2018-10-23T09:50:53Z", ZoneGPS, 2 * 3600},
		{"GPS clock abroad (Tokyo) overrides the camera zone",
			exiftest.Photo{DateTimeOriginal: "2023:05:01 18:00:00", GPS: gpsAt(t, "2023-05-01T09:00:04Z")}, warsaw, "2023-05-01T09:00:00Z", ZoneGPS, 9 * 3600},
		{"GPS clock west of UTC (Arizona)",
			exiftest.Photo{DateTimeOriginal: "2020:06:01 12:00:00", GPS: gpsAt(t, "2020-06-01T19:00:01Z")}, warsaw, "2020-06-01T19:00:00Z", ZoneGPS, -7 * 3600},
		{"GPS clock in India (+05:30)",
			exiftest.Photo{DateTimeOriginal: "2019:01:10 12:00:00", GPS: gpsAt(t, "2019-01-10T06:30:30Z")}, warsaw, "2019-01-10T06:30:00Z", ZoneGPS, 5*3600 + 1800},
		{"GPS clock in Nepal (+05:45)",
			exiftest.Photo{DateTimeOriginal: "2019:01:10 12:00:00", GPS: gpsAt(t, "2019-01-10T06:15:00Z")}, warsaw, "2019-01-10T06:15:00Z", ZoneGPS, 5*3600 + 45*60},
		{"GPS clock in Newfoundland (-03:30)",
			exiftest.Photo{DateTimeOriginal: "2019:01:10 12:00:00", GPS: gpsAt(t, "2019-01-10T15:30:00Z")}, warsaw, "2019-01-10T15:30:00Z", ZoneGPS, -(3*3600 + 1800)},
		{"fractional GPS seconds",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", GPS: gpsAt(t, "2018-10-23T09:50:52.5Z")}, warsaw, "2018-10-23T09:50:53Z", ZoneGPS, 2 * 3600},
		{"GPS fix 1m59s off is kept",
			exiftest.Photo{DateTimeOriginal: "2023:05:01 18:00:00", GPS: gpsAt(t, "2023-05-01T09:01:59Z")}, warsaw, "2023-05-01T09:00:00Z", ZoneGPS, 9 * 3600},

		{"stale GPS fix 2m29s off a real offset falls back to the camera zone",
			exiftest.Photo{DateTimeOriginal: "2012:08:27 10:12:44", GPS: gpsAt(t, "2012-08-27T08:00:13Z")}, warsaw, "2012-08-27T08:12:44Z", ZoneCamera, 0},
		{"GPS offset of +02:15 is not a real one",
			exiftest.Photo{DateTimeOriginal: "2012:08:27 10:14:00", GPS: gpsAt(t, "2012-08-27T07:59:30Z")}, warsaw, "2012-08-27T08:14:00Z", ZoneCamera, 0},
		{"GPS timestamp 1970-01-01 is ignored",
			exiftest.Photo{DateTimeOriginal: "2022:08:01 20:17:09", GPS: gpsAt(t, "1970-01-01T00:00:00Z")}, warsaw, "2022-08-01T18:17:09Z", ZoneCamera, 0},
		{"GPS fix a day off is ignored",
			exiftest.Photo{DateTimeOriginal: "2017:08:11 10:20:11", GPS: gpsAt(t, "2017-08-12T00:54:18Z")}, warsaw, "2017-08-11T08:20:11Z", ZoneCamera, 0},
		{"GPS position without a timestamp uses the camera zone",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", GPS: &exiftest.GPS{Lat: 47.4979, Lng: 19.0402}}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},

		{"a phone in London writes its real offset; the camera zone says +02:00",
			exiftest.Photo{Make: "samsung", DateTimeOriginal: "2025:07:14 15:30:00", OffsetTimeOriginal: "+01:00", GPS: &exiftest.GPS{Lat: 51.5074, Lng: -0.1278}},
			warsaw, "2025-07-14T14:30:00Z", ZoneOffset, 3600},
		{"OffsetTimeOriginal is trusted without a make",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "+01:00"}, warsaw, "2018-10-23T10:50:53Z", ZoneOffset, 3600},
		{"OffsetTimeOriginal is trusted from an unknown make",
			exiftest.Photo{Make: "Acme", DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "-03:30"}, warsaw, "2018-10-23T15:20:53Z", ZoneOffset, -(3*3600 + 1800)},
		{"a phone at home agrees with the camera zone",
			exiftest.Photo{Make: "samsung", DateTimeOriginal: "2026:01:10 12:00:00", OffsetTimeOriginal: "+01:00"}, warsaw, "2026-01-10T11:00:00Z", ZoneOffset, 3600},
		{"a zero offset (London in winter) is a real one",
			exiftest.Photo{DateTimeOriginal: "2026:01:10 12:00:00", OffsetTimeOriginal: "+00:00"}, warsaw, "2026-01-10T12:00:00Z", ZoneOffset, 0},
		{"a fractional offset (Nepal)",
			exiftest.Photo{DateTimeOriginal: "2019:01:10 12:00:00", OffsetTimeOriginal: "+05:45"}, warsaw, "2019-01-10T06:15:00Z", ZoneOffset, 5*3600 + 45*60},
		{"the Panasonic's all-year +01:00 is ignored",
			exiftest.Photo{Make: "Panasonic", DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "+01:00"}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"the exemption ignores case and padding",
			exiftest.Photo{Make: " PANASONIC ", DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "+01:00"}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"the GPS clock outranks OffsetTimeOriginal",
			exiftest.Photo{Make: "samsung", DateTimeOriginal: "2023:05:01 18:00:00", OffsetTimeOriginal: "+01:00", GPS: gpsAt(t, "2023-05-01T09:00:04Z")}, warsaw, "2023-05-01T09:00:00Z", ZoneGPS, 9 * 3600},
		{"the GPS clock still applies to an exempt make",
			exiftest.Photo{Make: "Panasonic", DateTimeOriginal: "2023:05:01 18:00:00", OffsetTimeOriginal: "+01:00", GPS: gpsAt(t, "2023-05-01T09:00:04Z")}, warsaw, "2023-05-01T09:00:00Z", ZoneGPS, 9 * 3600},
		{"a stale GPS fix falls through to OffsetTimeOriginal",
			exiftest.Photo{DateTimeOriginal: "2022:08:01 20:17:09", OffsetTimeOriginal: "+01:00", GPS: gpsAt(t, "1970-01-01T00:00:00Z")}, warsaw, "2022-08-01T19:17:09Z", ZoneOffset, 3600},
		{"blank OffsetTimeOriginal uses the camera zone",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "      "}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"a camera's empty OffsetTimeOriginal pattern uses the camera zone",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "   :  "}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"an offset nobody uses is refused",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "+01:17"}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},
		{"an offset out of range is refused",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "+15:00"}, warsaw, "2018-10-23T09:50:53Z", ZoneCamera, 0},

		{"a broken Exif IFD still leaves IFD0 DateTime readable (old Sony)",
			exiftest.Photo{DateTime: "2009:04:15 10:20:30", DateTimeOriginal: "2009:04:15 10:20:30", BrokenExifIFD: true}, warsaw, "2009-04-15T08:20:30Z", ZoneCamera, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractExif(bytes.NewReader(exiftest.JPEG(tt.photo)), tt.zone)
			if tt.want == "" {
				assert.Nil(t, got.TakenAt)
				assert.Empty(t, got.TakenAtZone)
				return
			}
			require.NotNil(t, got.TakenAt)
			want, err := time.Parse(time.RFC3339, tt.want)
			require.NoError(t, err)
			assert.True(t, want.Equal(*got.TakenAt), "got %s, want %s", got.TakenAt.UTC().Format(time.RFC3339), tt.want)
			assert.Equal(t, tt.wantZone, got.TakenAtZone)
			if tt.wantZone == ZoneGPS || tt.wantZone == ZoneOffset {
				_, offset := got.TakenAt.Zone()
				assert.Equal(t, tt.wantOffset, offset)
			}
		})
	}
}

func TestExtractExif_LatLng(t *testing.T) {
	tests := []struct {
		name     string
		lat, lng float64
	}{
		{"north east", 47.4979, 19.0402},
		{"south west", -33.4489, -70.6693},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractExif(bytes.NewReader(exiftest.JPEG(exiftest.Photo{
				DateTimeOriginal: "2018:10:23 11:50:53",
				GPS:              &exiftest.GPS{Lat: tt.lat, Lng: tt.lng},
			})), time.UTC)
			require.NotNil(t, got.Lat)
			require.NotNil(t, got.Lng)
			assert.InDelta(t, tt.lat, *got.Lat, 1e-6)
			assert.InDelta(t, tt.lng, *got.Lng, 1e-6)
		})
	}
}

func TestExtractExif_BrokenExifIFDKeepsTheRest(t *testing.T) {
	got := ExtractExif(bytes.NewReader(exiftest.JPEG(exiftest.Photo{
		Make:          "SONY",
		DateTime:      "2009:04:15 10:20:30",
		BrokenExifIFD: true,
		GPS:           &exiftest.GPS{Lat: 47.4979, Lng: 19.0402},
	})), time.UTC)
	require.NotNil(t, got.TakenAt)
	require.NotNil(t, got.Lat, "the GPS IFD is read although the Exif IFD is not")
	assert.InDelta(t, 47.4979, *got.Lat, 1e-6)
}

func TestExtractExif_NotExif(t *testing.T) {
	got := ExtractExif(bytes.NewReader([]byte("not a photo at all")), time.UTC)
	assert.Equal(t, ExifData{}, got)
}

func TestIsRealOffset(t *testing.T) {
	tests := []struct {
		offset time.Duration
		want   bool
	}{
		{0, true},
		{2 * time.Hour, true},
		{-12 * time.Hour, true},
		{14 * time.Hour, true},
		{14*time.Hour + time.Hour, false},
		{-13 * time.Hour, false},
		{5*time.Hour + 30*time.Minute, true},
		{12*time.Hour + 45*time.Minute, true},
		{-(9*time.Hour + 30*time.Minute), true},
		{2*time.Hour + 15*time.Minute, false},
		{30 * time.Minute, false},
	}
	for _, tt := range tests {
		t.Run(tt.offset.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, isRealOffset(tt.offset))
		})
	}
}

// time.Parse hands back time.Local when an offset matches it, so OffsetTimeOriginal is pinned too.
func TestExtractExif_OffsetIndependentOfProcessZone(t *testing.T) {
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })

	photo := exiftest.JPEG(exiftest.Photo{Make: "samsung", DateTimeOriginal: "2025:07:14 15:30:00", OffsetTimeOriginal: "+01:00"})
	for _, name := range []string{"UTC", "Europe/London", "Europe/Warsaw", "Asia/Tokyo"} {
		time.Local = mustZone(t, name)
		got := ExtractExif(bytes.NewReader(photo), mustZone(t, "Europe/Warsaw"))
		require.NotNil(t, got.TakenAt, name)
		assert.Equal(t, "2025-07-14T14:30:00Z", got.TakenAt.UTC().Format(time.RFC3339), name)
		assert.Equal(t, ZoneOffset, got.TakenAtZone, name)
	}
}

// The result must not depend on the process's own zone. Ingest read EXIF times in the server's zone
// before, so the same photo got a different time on a native server (Warsaw) and in Docker (UTC).
func TestExtractExif_IndependentOfProcessZone(t *testing.T) {
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })

	photo := exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53"})
	warsaw := mustZone(t, "Europe/Warsaw")
	var withZone, withNil *time.Time
	for _, name := range []string{"UTC", "Europe/Warsaw", "Asia/Tokyo", "America/New_York"} {
		time.Local = mustZone(t, name)
		gotZone := ExtractExif(bytes.NewReader(photo), warsaw).TakenAt
		gotNil := ExtractExif(bytes.NewReader(photo), nil).TakenAt
		require.NotNil(t, gotZone)
		require.NotNil(t, gotNil)
		if withZone == nil {
			withZone, withNil = gotZone, gotNil
			continue
		}
		assert.True(t, withZone.Equal(*gotZone), "process zone %s changed the camera-zone result", name)
		assert.True(t, withNil.Equal(*gotNil), "process zone %s changed the UTC result", name)
	}
	assert.Equal(t, "2018-10-23T09:50:53Z", withZone.UTC().Format(time.RFC3339))
	assert.Equal(t, "2018-10-23T11:50:53Z", withNil.UTC().Format(time.RFC3339))
}
