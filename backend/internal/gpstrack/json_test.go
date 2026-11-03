package gpstrack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Loom's sample plus an activity segment.
const timelineSample = `{
  "semanticSegments": [
    {
      "startTime": "2024-06-01T08:00:00.000+00:00",
      "endTime": "2024-06-01T09:00:00.000+00:00",
      "timelinePath": [{"point": "47.4936672°, 19.1074134°", "time": "2024-06-01T08:30:00.000+00:00"}]
    },
    {
      "startTime": "2024-06-01T09:00:00.000+00:00",
      "endTime": "2024-06-01T10:00:00.000+00:00",
      "visit": {"topCandidate": {"placeLocation": {"latLng": "47.4933726°, 19.1078253°"}}}
    },
    {
      "startTime": "2024-06-01T10:00:00.000+02:00",
      "endTime": "2024-06-01T11:45:00.000+02:00",
      "activity": {"start": {"latLng": "47.5°, 19.1°"}, "end": {"latLng": "48.2°, 16.4°"}, "distanceMeters": 1}
    }
  ],
  "rawSignals": [{"position": {"LatLng": "1.0°, 2.0°"}}],
  "userLocationProfile": {}
}`

func TestGoogleTimeline(t *testing.T) {
	_, points := mustParse(t, "Zeitachse.json", timelineSample)
	require.Len(t, points, 4)

	assert.Equal(t, Point{Time: utc("2024-06-01T08:30:00Z"), Lat: 47.4936672, Lng: 19.1074134}, points[0],
		"timelinePath point at its own time")
	assert.Equal(t, Point{Time: utc("2024-06-01T09:30:00Z"), Lat: 47.4933726, Lng: 19.1078253}, points[1],
		"visit at the middle of the segment")
	assert.Equal(t, Point{Time: utc("2024-06-01T08:00:00Z"), Lat: 47.5, Lng: 19.1}, points[2],
		"activity start at the segment start")
	assert.Equal(t, Point{Time: utc("2024-06-01T09:45:00Z"), Lat: 48.2, Lng: 16.4}, points[3],
		"activity end at the segment end")
}

func TestGoogleTimeline_SkipsWhatItCannotRead(t *testing.T) {
	doc := `{"semanticSegments": [
	  {"timelinePath": [
	    {"point": "47.1°, 19.1°", "time": "2024-06-01T08:30:00Z"},
	    {"point": "47°, 19°", "time": "2024-06-01T08:31:00Z"},
	    {"point": "47.2°, 19.2°", "time": "not a time"},
	    {"point": 5, "time": "2024-06-01T08:32:00Z"}
	  ]},
	  {"startTime": "2024-06-01T09:00:00Z", "visit": {"topCandidate": {"placeLocation": {"latLng": "47.3°, 19.3°"}}}},
	  {"startTime": "2024-06-01T09:00:00Z", "endTime": 7, "activity": {"start": {"latLng": "47.4°, 19.4°"}}},
	  {"startTime": "2024-06-01T10:00:00Z", "endTime": "2024-06-01T11:00:00Z", "visit": {"topCandidate": {"placeLocation": {"latLng": "47.5°, 19.5°"}}}}
	]}`
	_, points := mustParse(t, "Timeline.json", doc)
	require.Len(t, points, 2)
	assert.Equal(t, 47.1, points[0].Lat)
	assert.Equal(t, 47.5, points[1].Lat, "a segment without both times places no visit or activity")
}

func TestGoogleTimeline_NotAnExport(t *testing.T) {
	for _, doc := range []string{`{"locations": []}`, `{}`, `"text"`, `{"semanticSegments": {}}`} {
		_, _, err := Parse("Records.json", strings.NewReader(doc))
		require.Error(t, err, doc)
		assert.Contains(t, err.Error(), "google_timeline: ", doc)
	}
}

const samsungSample = `[
  {"start_time": 1653060801000, "latitude": 47.504338, "longitude": 19.058940, "altitude": 110.96, "accuracy": 4.0, "version": 2},
  {"start_time": 1653060802000, "latitude": 47.504400, "longitude": 19.059000, "accuracy": 3.0, "version": 2}
]`

func TestSamsungHealth(t *testing.T) {
	_, points := mustParse(t, "x.location_data.json", samsungSample)
	require.Len(t, points, 2)
	assert.Equal(t, utc("2022-05-20T15:33:21Z"), points[0].Time)
	assert.Equal(t, 47.504338, points[0].Lat)
	assert.Equal(t, 19.058940, points[0].Lng)
	require.NotNil(t, points[0].AltitudeM)
	assert.Equal(t, 110.96, *points[0].AltitudeM)
	require.NotNil(t, points[0].AccuracyM)
	assert.Equal(t, 4.0, *points[0].AccuracyM)
	assert.Nil(t, points[1].AltitudeM, "altitude is optional")
	assert.Empty(t, points[0].Source)
}

func TestSamsungHealth_SkipsBadEntries(t *testing.T) {
	doc := `[
	  {"start_time": 1653060801000, "latitude": 47.5},
	  {"start_time": "soon", "latitude": 47.5, "longitude": 19.0},
	  {"latitude": 47.5, "longitude": 19.0},
	  null,
	  {"start_time": 1653060801500, "latitude": 47.6, "longitude": 19.1}
	]`
	_, points := mustParse(t, "x.json", doc)
	require.Len(t, points, 1)
	assert.Equal(t, utc("2022-05-20T15:33:21.5Z"), points[0].Time)
}

const geoGallerySample = `[
  {"recorded_at": "2024-06-15T12:00:00+02:00", "lat": 47.5, "lng": 19.0, "source": "gpx:tracks/Strava/1.gpx",
   "altitude_m": 104.5, "accuracy_m": 3, "activity": "cycling"},
  {"recorded_at": "2024-06-15T12:00:01Z", "lat": 47.6, "lng": 19.1},
  {"recorded_at": "yesterday", "lat": 47.7, "lng": 19.2, "source": "s"},
  {"recorded_at": "2024-06-15T12:00:02Z", "lat": 47.8, "source": "s"}
]`

func TestGeoGalleryJSON(t *testing.T) {
	format, points := mustParse(t, "points.json", geoGallerySample)
	assert.Equal(t, FormatGeoGallery, format)
	require.Len(t, points, 2, "bad recorded_at and a missing lng are skipped")

	p := points[0]
	assert.Equal(t, utc("2024-06-15T10:00:00Z"), p.Time)
	assert.Equal(t, "gpx:tracks/Strava/1.gpx", p.Source, "the row's own source is kept")
	require.NotNil(t, p.AltitudeM)
	assert.Equal(t, 104.5, *p.AltitudeM)
	require.NotNil(t, p.AccuracyM)
	assert.Equal(t, 3.0, *p.AccuracyM)
	require.NotNil(t, p.Activity)
	assert.Equal(t, "cycling", *p.Activity)

	assert.Empty(t, points[1].Source, "no source: the caller labels it")
}

func TestJSON_Detection(t *testing.T) {
	tests := []struct {
		label   string
		content string
		want    string
		points  int
	}{
		{"BOM and whitespace before an array", "\xef\xbb\xbf \n\t" + samsungSample, FormatSamsungHealth, 2},
		{"whitespace before an object", "\r\n " + timelineSample, FormatGoogleTimeline, 4},
		{"empty array", "[]", FormatSamsungHealth, 0},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			format, points := mustParse(t, "x.json", tt.content)
			assert.Equal(t, tt.want, format)
			assert.Len(t, points, tt.points)
		})
	}

	_, _, err := Parse("x.json", strings.NewReader("  \n"))
	assert.EqualError(t, err, "json: empty file")
	_, _, err = Parse("x.json", strings.NewReader(`[{"start_time": 1}`))
	assert.Error(t, err, "truncated array")
}

const geoJSONSample = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature",
     "geometry": {"type": "Point", "coordinates": [19.0581674, 47.5044304, 0]},
     "properties": {"name": "Home", "timespan": {"begin": "2022-07-30T15:08:24Z", "end": "2022-07-31T05:00:04Z"}}},
    {"type": "Feature",
     "geometry": {"type": "LineString", "coordinates": [[19.0581674, 47.5044304, 0], [19.0550264, 47.5035547, 0]]},
     "properties": {"name": "Walking", "timespan": {"begin": "2022-07-31T05:00:04Z", "end": "2022-07-31T05:04:28Z"}}}
  ]
}`

func TestGeoJSON(t *testing.T) {
	_, points := mustParse(t, "history.geojson", geoJSONSample)
	require.Len(t, points, 3)

	assert.Equal(t, utc("2022-07-30T15:08:24Z"), points[0].Time, "a Point at the begin time")
	assert.Equal(t, 47.5044304, points[0].Lat)
	assert.Equal(t, 19.0581674, points[0].Lng)
	require.NotNil(t, points[0].AltitudeM)

	assert.Equal(t, utc("2022-07-31T05:00:04Z"), points[1].Time, "LineString starts at begin")
	assert.Equal(t, utc("2022-07-31T05:04:28Z"), points[2].Time, "and ends at end")
	assert.Equal(t, 47.5035547, points[2].Lat)
}

func TestGeoJSON_SkipsWhatItCannotRead(t *testing.T) {
	doc := `{"features": [
	  {"geometry": {"type": "Point", "coordinates": [19.0, 47.0]}, "properties": {}},
	  {"geometry": null, "properties": {"timespan": {"begin": "2022-07-30T15:00:00Z"}}},
	  {"geometry": {"type": "Polygon", "coordinates": [[[1, 2]]]}, "properties": {"timespan": {"begin": "2022-07-30T15:00:00Z"}}},
	  {"geometry": {"type": "LineString", "coordinates": [[19.0, 47.0], ["x", 47.1], [19.2], [19.3, 47.3]]},
	   "properties": {"timespan": {"begin": "2022-07-30T15:00:00Z"}}}
	]}`
	_, points := mustParse(t, "x.geojson", doc)
	require.Len(t, points, 2, "only the readable LineString vertices")
	assert.Equal(t, 47.0, points[0].Lat)
	assert.Equal(t, 47.3, points[1].Lat)
	assert.Equal(t, utc("2022-07-30T15:00:00Z"), points[1].Time, "no end: every vertex at begin")
}

func TestSpread(t *testing.T) {
	begin, end := utc("2022-08-04T19:00:00Z"), utc("2022-08-04T20:00:00Z")
	assert.Equal(t, begin, spread(begin, end, 0, 1))
	assert.Equal(t, begin, spread(begin, end, 0, 3))
	assert.Equal(t, utc("2022-08-04T19:30:00Z"), spread(begin, end, 1, 3))
	assert.Equal(t, end, spread(begin, end, 2, 3))
}
