package gpstrack

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParse(t *testing.T, name, content string) (string, []Point) {
	t.Helper()
	format, points, err := Parse(name, strings.NewReader(content))
	require.NoError(t, err)
	return format, points
}

func gzipped(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, err := zw.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return b.String()
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestParse_FormatByExtension(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"activity.gpx", gpxSample, FormatGPX},
		{"ACTIVITY.GPX", gpxSample, FormatGPX},
		{"ride.tcx", tcxSample, FormatTCX},
		{"history-2022-08-01.kml", kmlSample, FormatKML},
		{"history.geojson", geoJSONSample, FormatGeoJSON},
		{"FILE140101-051123.NMEA", nmeaSample, FormatNMEA},
		{"gps_balaton.log", esokoSample, FormatEsoko},
		{"Zeitachse.json", timelineSample, FormatGoogleTimeline},
		{"0020fcd6.com.samsung.health.exercise.location_data.json", samsungSample, FormatSamsungHealth},
		{"points.json", geoGallerySample, FormatGeoGallery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, points := mustParse(t, tt.name, tt.content)
			assert.Equal(t, tt.want, format)
			assert.NotEmpty(t, points)
		})
	}
}

func TestParse_Gzipped(t *testing.T) {
	// Loom reads .gpx.gz and .tcx.gz; every format is accepted gzipped here.
	tests := []struct {
		name    string
		content string
		want    string
		points  int
	}{
		{"activity.gpx.gz", gpxSample, FormatGPX, 3},
		{"ride.tcx.gz", tcxSample, FormatTCX, 2},
		{"drive.nmea.gz", nmeaSample, FormatNMEA, 2},
		{"Zeitachse.json.GZ", timelineSample, FormatGoogleTimeline, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, points := mustParse(t, tt.name, gzipped(t, tt.content))
			assert.Equal(t, tt.want, format)
			assert.Len(t, points, tt.points)
		})
	}
}

func TestParse_Unsupported(t *testing.T) {
	for _, name := range []string{"notes.txt", "activity.fit", "archive.gz", "noextension"} {
		_, _, err := Parse(name, strings.NewReader("x"))
		assert.ErrorIs(t, err, ErrUnsupported, name)
	}
}

func TestParse_NotGzip(t *testing.T) {
	_, _, err := Parse("activity.gpx.gz", strings.NewReader(gpxSample))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gzip")
}

func TestParse_ParseErrorNamesTheFormat(t *testing.T) {
	_, _, err := Parse("activity.gpx", strings.NewReader("<gpx><trk>"))
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "gpx: "), err.Error())
}

func TestParse_ExpandLimit(t *testing.T) {
	orig := maxExpandedBytes
	t.Cleanup(func() { maxExpandedBytes = orig })
	maxExpandedBytes = int64(len(gpxSample))

	_, points := mustParse(t, "exact.gpx.gz", gzipped(t, gpxSample))
	assert.Len(t, points, 3, "a file of exactly the limit is read")

	_, _, err := Parse("over.gpx.gz", strings.NewReader(gzipped(t, gpxSample+" ")))
	assert.ErrorIs(t, err, errTooLarge)
}

func TestParse_DropsPointsOutOfRange(t *testing.T) {
	gpx := `<gpx>
	  <trkpt lat="91" lon="19"><time>2022-04-03T13:08:40Z</time></trkpt>
	  <trkpt lat="47" lon="-180.5"><time>2022-04-03T13:08:41Z</time></trkpt>
	  <trkpt lat="NaN" lon="19"><time>2022-04-03T13:08:42Z</time></trkpt>
	  <trkpt lat="-90" lon="180"><time>2022-04-03T13:08:43Z</time></trkpt>
	</gpx>`
	_, points := mustParse(t, "edges.gpx", gpx)
	require.Len(t, points, 1)
	assert.Equal(t, utc("2022-04-03T13:08:43Z"), points[0].Time)
}

func TestParseISO8601(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"2024-05-30T10:48:32Z", "2024-05-30T10:48:32Z"},
		{"2015-09-02T08:00:00.000+02:00", "2015-09-02T06:00:00Z"},
		{"2015-09-02T08:00:00.000+0200", "2015-09-02T06:00:00Z"},
		{"2015-09-02T08:00:00-0130", "2015-09-02T09:30:00Z"},
		{" 2024-05-30T10:48:32.250Z\n", "2024-05-30T10:48:32.25Z"},
		{"2024-05-30T10:48:32", "2024-05-30T10:48:32Z"}, // no offset: UTC
	}
	for _, tt := range tests {
		got, err := parseISO8601(tt.in)
		require.NoError(t, err, tt.in)
		assert.Equal(t, utc(tt.want), got, tt.in)
		assert.Equal(t, time.UTC, got.Location(), tt.in)
	}
	for _, bad := range []string{"", "yesterday", "2024-13-01T00:00:00Z"} {
		_, err := parseISO8601(bad)
		assert.Error(t, err, bad)
	}
}

func TestUnixTime(t *testing.T) {
	got, ok := unixTime(1691051735.5)
	require.True(t, ok)
	assert.Equal(t, utc("2023-08-03T08:35:35.5Z"), got)

	for _, bad := range []float64{-1, 1e12} {
		_, ok := unixTime(bad)
		assert.False(t, ok, bad)
	}
}
