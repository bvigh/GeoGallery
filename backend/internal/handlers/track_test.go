package handlers

import (
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackFormatAndKind(t *testing.T) {
	tests := []struct {
		source, wantPrefix, wantFormat, wantKind string
	}{
		{"gpx:ride.gpx", "gpx", "gpx", "recorded"},
		{"tcx:run.tcx", "tcx", "tcx", "recorded"},
		{"nmea:log.nmea", "nmea", "nmea", "recorded"},
		{"esoko:car.log", "esoko", "esoko", "recorded"},
		{"samsung_health:a.json", "samsung_health", "samsung_health", "recorded"},
		{"google_timeline:records.json", "google_timeline", "google_timeline", "semantic"},
		{"google_timeline", "google_timeline", "google_timeline", "semantic"},
		{"kml:a.kml", "kml", "kml", "time_may_be_estimated"},
		{"kmz:a.kmz", "kmz", "kmz", "time_may_be_estimated"},
		{"geojson:a.geojson", "geojson", "geojson", "time_may_be_estimated"},
		{"exif", "exif", "exif", "derived"},
		{"manual", "manual", "manual", "derived"},
		{"geogallery:a.json", "geogallery", "geogallery", "other"},
		{"strava", "strava", "other", "other"},
		{"komoot:tour.gpx", "komoot", "other", "other"},
		{"gpx:name:with:colons.gpx", "gpx", "gpx", "recorded"},
		{"GPX:upper.gpx", "GPX", "other", "other"},
		{":leading.gpx", "", "other", "other"},
		{"", "", "other", "other"},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			assert.Equal(t, tt.wantPrefix, sourcePrefix(tt.source))
			assert.Equal(t, tt.wantFormat, trackFormat(tt.source))
			assert.Equal(t, tt.wantKind, trackKind(tt.source))
		})
	}
}

func TestTrackBucketSeconds(t *testing.T) {
	start := time.Date(2001, 2, 3, 0, 0, 0, 0, time.UTC)
	window := func(offset, length time.Duration) timeWindow {
		return timeWindow{from: start.Add(offset), to: start.Add(offset + length)}
	}
	twoHours := []timeWindow{window(0, 2*time.Hour)}

	tests := []struct {
		name                             string
		windows                          []timeWindow
		logFormats, logPoints, maxPoints int
		want                             int
	}{
		{"within the budget", twoHours, 1, 2000, 2000, 0},
		{"one over the budget", twoHours, 1, 2001, 2000, 4},
		{"formats share the budget", twoHours, 2, 5000, 2000, 8},
		{"rounds up", twoHours, 3, 8000, 7000, 4},
		{"windows add up", []timeWindow{window(0, time.Hour), window(10*time.Hour, 3*time.Hour)}, 1, 5000, 1000, 15},
		{"a zero-length window never divides by zero", []timeWindow{window(0, 0)}, 2, 50, 10, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, trackBucketSeconds(tt.windows, tt.logFormats, tt.logPoints, tt.maxPoints))
		})
	}
}

func TestNewTrackResponse(t *testing.T) {
	second := time.Date(2001, 2, 3, 12, 0, 5, 0, time.UTC)
	zone := time.FixedZone("TEST", 9*3600)
	window := timeWindow{from: second.Add(-time.Hour).In(zone), to: second.Add(time.Hour).In(zone)}

	t.Run("orders by time, then source, and never as text", func(t *testing.T) {
		// As RFC 3339 text "…:05.5Z" sorts before "…:05Z".
		points := []geo.Point{
			{Source: "nmea:b", At: second.Add(500 * time.Millisecond), Lat: 3},
			{Source: "nmea:b", At: second, Lat: 2},
			{Source: "gpx:a", At: second, Lat: 1},
		}

		resp := newTrackResponse([]timeWindow{window}, points)

		require.Len(t, resp.Points, 3)
		assert.Equal(t, []float64{1, 2, 3}, []float64{resp.Points[0].Lat, resp.Points[1].Lat, resp.Points[2].Lat})
		assert.Equal(t, 3, resp.ReturnedPoints)
		assert.Equal(t, []TrackSource{
			{Label: "gpx:a", Format: "gpx", Kind: "recorded"},
			{Label: "nmea:b", Format: "nmea", Kind: "recorded"},
		}, resp.Sources)
		assert.Equal(t, []int{0, 1, 1}, []int{resp.Points[0].S, resp.Points[1].S, resp.Points[2].S})
	})

	t.Run("every instant is UTC", func(t *testing.T) {
		resp := newTrackResponse([]timeWindow{window}, []geo.Point{{Source: "gpx:a", At: second.In(zone)}})

		assert.Equal(t, time.UTC, resp.Windows[0].From.Location())
		assert.Equal(t, time.UTC, resp.Windows[0].To.Location())
		assert.Equal(t, time.UTC, resp.Points[0].T.Location())
	})

	t.Run("no points is empty arrays", func(t *testing.T) {
		resp := newTrackResponse(nil, nil)

		assert.NotNil(t, resp.Windows)
		assert.NotNil(t, resp.Sources)
		assert.NotNil(t, resp.Points)
		assert.Zero(t, resp.ReturnedPoints)
	})
}
