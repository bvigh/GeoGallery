package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A date nobody photographed, at a point in open water.
var checksAt = time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)

func checksPoint(source string, offset time.Duration, lat, lng float64) *geo.Point {
	return &geo.Point{Source: source, At: checksAt.Add(offset), Lat: lat, Lng: lng}
}

// estimateAt recomputes one estimate at checksAt as the handler does. A stored position gets the
// recompute's confidence, so a position equal to the recompute's is current.
func estimateAt(id string, stored *position, before, after *geo.Point) checkedEstimate {
	row := estimateRow{id: id, takenAt: checksAt, stored: stored}
	if e := geo.Explain(before, after, checksAt, time.Hour); stored != nil && e.OK {
		row.confidence = &e.Confidence
	}
	return newCheckedEstimate(row, bracketPair{before: before, after: after}, time.Hour)
}

func distanceM(lat1, lng1, lat2, lng2 float64) float64 {
	return geo.HaversineKm(lat1, lng1, lat2, lng2) * 1000
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		sorted []float64
		p      float64
		want   float64
	}{
		{"one value is every quantile", []float64{42}, 0.9, 42},
		{"median of an odd count", []float64{1, 2, 3, 4, 5}, 0.5, 3},
		{"median of an even count interpolates", []float64{10, 20, 30, 40}, 0.5, 25},
		{"90th between ranks, as percentile_cont", []float64{10, 20, 30, 40}, 0.9, 37},
		{"90th of eleven is the tenth", []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.9, 9},
		{"two values", []float64{0, 100}, 0.9, 90},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, percentile(tt.sorted, tt.p), 1e-9)
		})
	}
}

func TestPairKey(t *testing.T) {
	assert.Equal(t, "gpx+nmea", pairKey("gpx", "nmea"))
	assert.Equal(t, "gpx+nmea", pairKey("nmea", "gpx"), "the same pair whichever side each is on")
	assert.Equal(t, "komoot+strava", pairKey("strava", "komoot"), "unknown prefixes as written")
}

func TestTopSource(t *testing.T) {
	tests := []struct {
		name      string
		estimates map[string]int
		wantLabel string
		wantN     int
		wantOK    bool
	}{
		{"none", map[string]int{}, "", 0, false},
		{"one", map[string]int{"gpx:a.gpx": 1}, "gpx:a.gpx", 1, true},
		{"the most estimates", map[string]int{"gpx:a.gpx": 1, "nmea:b.nmea": 2}, "nmea:b.nmea", 2, true},
		{"one ahead of a tie below it", map[string]int{"gpx:a.gpx": 1, "gpx:b.gpx": 1, "nmea:x.nmea": 2}, "nmea:x.nmea", 2, true},
		{"a tie at the top names none", map[string]int{"nmea:x.nmea": 2, "gpx:b.gpx": 2, "gpx:a.gpx": 1}, "", 0, false},
		{"two files under every estimate", map[string]int{"esoko:car.log": 5, "gpx:ride.gpx": 5}, "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A map's order changes from run to run: every order must agree.
			for range 20 {
				label, n, ok := topSource(tt.estimates)
				require.Equal(t, tt.wantOK, ok)
				if ok {
					assert.Equal(t, tt.wantLabel, label)
					assert.Equal(t, tt.wantN, n)
				}
			}
		})
	}
}

func TestWindowFits(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"an ordinary time", checksAt, true},
		{"the window ends in year 10000", time.Date(9999, 12, 31, 23, 30, 0, 0, time.UTC), false},
		{"the window starts in year 0", time.Date(1, 1, 1, 0, 30, 0, 0, time.UTC), true},
		{"the window starts in year -1", time.Date(0, 1, 1, 0, 30, 0, 0, time.UTC), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, windowFits(tt.at, time.Hour))
		})
	}
}

func TestNewCheckedEstimate(t *testing.T) {
	before := checksPoint("gpx:a.gpx", -10*time.Minute, 10, 20)
	after := checksPoint("gpx:a.gpx", 10*time.Minute, 10.01, 20.01)
	recomputed := position{lat: 10.005, lng: 20.005}

	tests := []struct {
		name          string
		stored        *position
		before, after *geo.Point
		wantState     string
		wantNow       bool
		wantReference *position
	}{
		{"current", &recomputed, before, after, placementCurrent, true, &recomputed},
		{"stale", &position{lat: 10.05, lng: 20}, before, after, placementStale, true, &position{lat: 10.05, lng: 20}},
		{"pending measures from the recompute", nil, before, after, placementPending, true, &recomputed},
		{"no support", &recomputed, nil, nil, placementNoSupport, false, &recomputed},
		{"no bracket", nil, nil, nil, placementNoBracket, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := estimateAt("e", tt.stored, tt.before, tt.after)

			assert.Equal(t, tt.wantState, e.state)
			assert.Equal(t, tt.wantNow, e.now != nil)
			assert.NotNil(t, e.warnings, "placementWarnings never returns nil")
			if tt.wantReference == nil {
				assert.Nil(t, e.reference())
				return
			}
			require.NotNil(t, e.reference())
			assert.InDelta(t, tt.wantReference.lat, e.reference().lat, 1e-9)
			assert.InDelta(t, tt.wantReference.lng, e.reference().lng, 1e-9)
		})
	}

	t.Run("the recompute is described as the evidence view's placement.now", func(t *testing.T) {
		e := estimateAt("e", &recomputed, before, after)
		require.NotNil(t, e.now)
		assert.Equal(t, "two_sided", e.now.Arithmetic.Shape)
		assert.Equal(t, "gpx:a.gpx", e.now.Before.Source)
		assert.Equal(t, bracketKindLog, e.now.After.Kind)
		assert.InDelta(t, 600, e.now.After.GapS, 1e-9)
		require.NotNil(t, e.now.DeltaM)
		assert.Less(t, *e.now.DeltaM, 1e-6)
	})
}

func TestCheckGroups(t *testing.T) {
	t.Run("nothing found is an empty list", func(t *testing.T) {
		got := checkGroups{}.check("code", checksSeverityLow, 0)
		assert.NotNil(t, got.Groups)
		assert.Empty(t, got.Groups)
	})

	t.Run("largest count first, then key; a group that counted nothing is dropped", func(t *testing.T) {
		groups := checkGroups{}
		groups.get("a").add("1")
		for _, key := range []string{"c", "b"} {
			for i := range 3 {
				groups.get(key).add(fmt.Sprint(i))
			}
		}
		groups.get("d").list("listed but not counted")

		got := groups.check("code", checksSeverityHigh, 7)

		var keys []string
		for _, g := range got.Groups {
			keys = append(keys, g.Key)
			assert.Equal(t, g.Key, g.Label)
		}
		assert.Equal(t, []string{"b", "c", "a"}, keys)
		assert.Equal(t, "code", got.Code)
		assert.Equal(t, checksSeverityHigh, got.Severity)
		assert.Equal(t, 7, got.Total, "the caller's total")
	})

	t.Run("media_ids keep the first 200", func(t *testing.T) {
		g := checkGroups{}.get("k")
		for i := range 250 {
			g.add(fmt.Sprint(i))
		}
		assert.Equal(t, 250, g.Count)
		require.Len(t, g.MediaIDs, checksMaxMediaIDs)
		assert.Equal(t, "0", g.MediaIDs[0])
		assert.Equal(t, "199", g.MediaIDs[199])
	})
}

func TestPairCheck(t *testing.T) {
	estimates := []checkedEstimate{
		// Impossible: e1 and e3 cover 55 km in a minute, e2 33 km; e3 the other way round.
		estimateAt("e1", nil, checksPoint("gpx:a.gpx", -30*time.Second, 10, 20), checksPoint("nmea:x.nmea", 30*time.Second, 10.5, 20)),
		estimateAt("e2", nil, checksPoint("gpx:b.gpx", -30*time.Second, 10, 20), checksPoint("nmea:x.nmea", 30*time.Second, 10.3, 20)),
		estimateAt("e3", nil, checksPoint("nmea:x.nmea", -30*time.Second, 10, 20), checksPoint("gpx:a.gpx", 30*time.Second, 10.5, 20)),
		// Far: 11 km in 40 minutes, twice between the same two files.
		estimateAt("e4", nil, checksPoint("gpx:a.gpx", -20*time.Minute, 10, 20), checksPoint("esoko:c.log", 20*time.Minute, 10.1, 20)),
		estimateAt("e6", nil, checksPoint("esoko:c.log", -20*time.Minute, 10, 20), checksPoint("gpx:a.gpx", 20*time.Minute, 10.05, 20)),
		// One format: never a pair warning.
		estimateAt("e5", nil, checksPoint("gpx:a.gpx", -30*time.Second, 10, 20), checksPoint("gpx:b.gpx", 30*time.Second, 10.5, 20)),
	}
	impossibleKm := geo.HaversineKm(10, 20, 10.5, 20)

	t.Run("pair_impossible", func(t *testing.T) {
		got := pairCheck(checksPairImpossible, checksSeverityHigh, estimates)

		assert.Equal(t, checksCheck{Code: "pair_impossible", Severity: "high", Total: 3, Groups: []checksGroup{{
			Key: "gpx+nmea", Label: "gpx+nmea", Count: 3, MediaIDs: []string{"e1", "e2", "e3"},
			Evidence: map[string]any{
				"max_km": impossibleKm, "max_kmh": impossibleKm / (60.0 / 3600),
				// Under all three, against gpx:a.gpx under two and gpx:b.gpx under one.
				"top_source": "nmea:x.nmea", "top_source_estimates": 3,
			},
		}}}, got)
	})

	t.Run("pair_far", func(t *testing.T) {
		got := pairCheck(checksPairFar, checksSeverityLow, estimates)

		assert.Equal(t, checksCheck{Code: "pair_far", Severity: "low", Total: 2, Groups: []checksGroup{{
			Key: "esoko+gpx", Label: "esoko+gpx", Count: 2, MediaIDs: []string{"e4", "e6"},
			Evidence: map[string]any{
				"max_km": geo.HaversineKm(10, 20, 10.1, 20),
				// Both files are under both estimates: neither stands out.
				"top_source": nil, "top_source_estimates": nil,
			},
		}}}, got)
	})
}

func TestStaleEstimatesCheck(t *testing.T) {
	before := checksPoint("gpx:a.gpx", -10*time.Minute, 10, 20)
	after := checksPoint("gpx:a.gpx", 10*time.Minute, 10, 20.02)
	recomputed := position{lat: 10, lng: 20.01}
	estimates := []checkedEstimate{
		estimateAt("current", &recomputed, before, after),
		estimateAt("near", &position{lat: 10, lng: 20.015}, before, after),
		estimateAt("far", &position{lat: 10, lng: 20.03}, before, after),
		estimateAt("unsupported", &recomputed, nil, nil),
		estimateAt("pending", nil, before, after),
	}

	got := staleEstimatesCheck(estimates)

	assert.Equal(t, "stale_estimates", got.Code)
	assert.Equal(t, "high", got.Severity)
	assert.Equal(t, 3, got.Total)
	require.Len(t, got.Groups, 2)
	stale, unsupported := got.Groups[0], got.Groups[1]
	assert.Equal(t, "stale", stale.Key)
	assert.Equal(t, 2, stale.Count)
	assert.Equal(t, []string{"near", "far"}, stale.MediaIDs)
	require.Contains(t, stale.Evidence, "max_delta_m")
	assert.InDelta(t, distanceM(10, 20.03, 10, 20.01), stale.Evidence["max_delta_m"], 1e-6)
	assert.Equal(t, checksGroup{Key: "no_support", Label: "no_support", Count: 1, MediaIDs: []string{"unsupported"},
		Evidence: map[string]any{}}, unsupported, "no evidence, but an object")
}

func TestFuturePointsCheck(t *testing.T) {
	plus9 := time.FixedZone("TEST+9", 9*3600)
	week := time.Date(2040, 6, 1, 9, 0, 0, 0, plus9)
	rows := []trackFileRow{
		{source: "gpx:a.gpx", points: 1, first: week, last: week},
		{source: "nmea:week.nmea", points: 3, first: week, last: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	got := futurePointsCheck(rows)

	weekUTC := week.UTC()
	assert.Equal(t, checksCheck{Code: "future_track_points", Severity: "high", Total: 4, Groups: []checksGroup{
		{Key: "nmea:week.nmea", Label: "nmea:week.nmea", Count: 3, MediaIDs: []string{},
			Evidence: map[string]any{"first": &weekUTC, "last": (*time.Time)(nil)}},
		{Key: "gpx:a.gpx", Label: "gpx:a.gpx", Count: 1, MediaIDs: []string{},
			Evidence: map[string]any{"first": &weekUTC, "last": &weekUTC}},
	}}, got)
}

func TestTakenAfterUploadCheck(t *testing.T) {
	items := []afterUploadItem{
		{id: "a", camera: "Model X", located: true, aheadS: 100},
		{id: "b", camera: "", aheadS: 50},
		{id: "c", camera: "Model X", aheadS: 3e10},
	}

	got := takenAfterUploadCheck(items)

	assert.Equal(t, checksCheck{Code: "taken_after_upload", Severity: "high", Total: 3, Groups: []checksGroup{
		{Key: "Model X", Label: "Model X", Count: 2, MediaIDs: []string{"a", "c"},
			Evidence: map[string]any{"max_ahead_s": 3e10, "located": 1}},
		{Key: "", Label: "", Count: 1, MediaIDs: []string{"b"},
			Evidence: map[string]any{"max_ahead_s": 50.0, "located": 0}},
	}}, got)
}

func TestOrphanPointsCheck(t *testing.T) {
	exifAt, manualAt := -time.Minute, time.Minute
	orphans := []orphanPoint{
		// The same instant in another zone is the same point.
		{source: "exif", at: checksAt.Add(exifAt).In(time.FixedZone("TEST+1", 3600))},
		{source: "manual", at: checksAt.Add(manualAt)},
		{source: "exif", at: checksAt.Add(2 * time.Hour)},
	}
	estimates := []checkedEstimate{
		estimateAt("both", nil, checksPoint("exif", exifAt, 10, 20), checksPoint("manual", manualAt, 10, 20)),
		estimateAt("one", nil, checksPoint("exif", exifAt, 10, 20), checksPoint("gpx:a.gpx", time.Minute, 10, 20)),
		// A linked exif point at another second, and an orphan's instant under another label.
		estimateAt("linked", nil, checksPoint("exif", -2*time.Minute, 10, 20), checksPoint("gpx:a.gpx", manualAt, 10, 20)),
	}

	got := orphanPointsCheck(orphans, estimates)

	assert.Equal(t, checksCheck{Code: "orphan_points", Severity: "low", Total: 3, Groups: []checksGroup{
		{Key: "exif", Label: "exif", Count: 2, MediaIDs: []string{"both", "one"}, Evidence: map[string]any{"estimates": 2}},
		{Key: "manual", Label: "manual", Count: 1, MediaIDs: []string{"both"}, Evidence: map[string]any{"estimates": 1}},
	}}, got)

	t.Run("an estimate on two orphans of one prefix counts once", func(t *testing.T) {
		twice := checkedEstimate{id: "twice", before: checksPoint("exif", exifAt, 10, 20), after: checksPoint("exif", 2*time.Hour, 10, 20)}

		got := orphanPointsCheck(orphans, []checkedEstimate{twice})

		require.Len(t, got.Groups, 2)
		assert.Equal(t, "exif", got.Groups[0].Key)
		assert.Equal(t, []string{"twice"}, got.Groups[0].MediaIDs)
		assert.Equal(t, 1, got.Groups[0].Evidence["estimates"])
	})
}

func TestTimeMayBeEstimatedCheck(t *testing.T) {
	alternative := func(p *geo.Point) *geo.Explanation {
		e := geo.Explain(p, nil, checksAt, time.Hour)
		return &e
	}
	none := &geo.Explanation{}
	gpx := checksPoint("gpx:g.gpx", 40*time.Minute, 10.02, 20)

	kml := estimateAt("kml", &position{lat: 10, lng: 20.005}, checksPoint("kml:k.kml", -10*time.Minute, 10, 20), checksPoint("gpx:g.gpx", 10*time.Minute, 10, 20.01))
	kml.withoutKMLGeoJSON = alternative(checksPoint("gpx:g.gpx", 10*time.Minute, 10, 20.01))
	// Rests on both formats of the first exclusion, so it is in both groups and counted once.
	both := estimateAt("both", nil, checksPoint("kml:k.kml", -10*time.Minute, 10, 20), checksPoint("geojson:j.json", 10*time.Minute, 10, 20))
	both.withoutKMLGeoJSON = alternative(gpx)
	noOther := estimateAt("no-other", nil, checksPoint("geojson:j.json", -10*time.Minute, 10, 20), nil)
	noOther.withoutKMLGeoJSON = none
	// Its kml point falls back on its kmz point, but without kmz too nothing is left.
	kmzAndKML := estimateAt("kmz-kml", nil, checksPoint("kmz:z.kmz", -10*time.Minute, 10, 20), checksPoint("kml:k.kml", 10*time.Minute, 10, 20))
	kmzAndKML.withoutKMLGeoJSON = alternative(checksPoint("kmz:z.kmz", -10*time.Minute, 10, 20))
	kmzAndKML.withoutKMZ = none
	kmz := estimateAt("kmz", &position{lat: 10, lng: 20}, checksPoint("kmz:z.kmz", -10*time.Minute, 10, 20), nil)
	kmz.withoutKMZ = alternative(gpx)
	untouched := estimateAt("gpx-only", nil, checksPoint("gpx:g.gpx", -10*time.Minute, 10, 20), nil)

	got := timeMayBeEstimatedCheck([]checkedEstimate{kml, both, noOther, kmzAndKML, kmz, untouched})

	moveKML := distanceM(10, 20.005, 10, 20.01)
	moveBoth := distanceM(10, 20, 10.02, 20) // from the recompute: it has no stored position
	assert.Equal(t, checksCheck{Code: "time_may_be_estimated_used", Severity: "low", Total: 4, Groups: []checksGroup{
		{Key: "kml", Label: "kml", Count: 3, MediaIDs: []string{"kml", "both", "kmz-kml"},
			Evidence: map[string]any{"using": 3, "over_100m": 2, "over_1km": 1, "max_move_m": moveBoth}},
		{Key: "geojson", Label: "geojson", Count: 1, MediaIDs: []string{"both"},
			Evidence: map[string]any{"using": 2, "over_100m": 1, "over_1km": 1, "max_move_m": moveBoth}},
		{Key: "kmz", Label: "kmz", Count: 1, MediaIDs: []string{"kmz"},
			Evidence: map[string]any{"using": 2, "over_100m": 1, "over_1km": 1, "max_move_m": distanceM(10, 20, 10.02, 20)}},
	}}, got)
	assert.Greater(t, moveKML, 100.0, "the fixture moves the kml estimate over 100 m and under 1 km")
	assert.Less(t, moveKML, 1000.0)
}

func TestNewChecksFiles(t *testing.T) {
	t1, t2 := checksAt.Add(-time.Hour), checksAt.Add(time.Hour)
	rows := []trackFileRow{
		{source: "exif", points: 2, first: t1, last: t2},
		{source: "gpx:a.gpx", points: 10, first: t1.In(time.FixedZone("TEST+9", 9*3600)), last: t2},
		{source: "kml:k.kml", points: 5, first: t1, last: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{source: "strava:ride", points: 1, first: t1, last: t1},
	}
	estimates := []checkedEstimate{
		// Both sides from one file: placed once.
		estimateAt("e1", nil, checksPoint("gpx:a.gpx", -time.Minute, 10, 20), checksPoint("gpx:a.gpx", time.Minute, 10, 20)),
		estimateAt("e2", nil, checksPoint("exif", -time.Minute, 10, 20), checksPoint("gpx:a.gpx", time.Minute, 10, 20)),
		estimateAt("e3", nil, nil, nil),
	}

	got := newChecksFiles(rows, estimates)

	t1UTC, t2UTC := t1.UTC(), t2.UTC()
	assert.Equal(t, []checksFile{
		{Source: "exif", Format: "exif", Kind: "derived", Points: 2, First: &t1UTC, Last: &t2UTC, ItemsPlaced: 1},
		{Source: "gpx:a.gpx", Format: "gpx", Kind: "recorded", Points: 10, First: &t1UTC, Last: &t2UTC, ItemsPlaced: 2},
		{Source: "kml:k.kml", Format: "kml", Kind: "time_may_be_estimated", Points: 5, First: &t1UTC, Last: nil},
		{Source: "strava:ride", Format: "strava", Kind: "other", Points: 1, First: &t1UTC, Last: &t1UTC},
	}, got)
}

func TestNewChecksResiduals(t *testing.T) {
	assert.Equal(t, []checksResidual{}, newChecksResiduals(nil))

	pairs := []residualPair{
		{format: "gpx", photoLat: 10, photoLng: 20, lat: 10, lng: 20.003},
		{format: "gpx", photoLat: 10, photoLng: 20, lat: 10, lng: 20.001},
		{format: "nmea", photoLat: 10, photoLng: 20, lat: 10.0001, lng: 20},
		// Equal medians are ordered by format.
		{format: "strava", photoLat: 10, photoLng: 20, lat: 10, lng: 20.01},
		{format: "komoot", photoLat: 10, photoLng: 20, lat: 10, lng: 20.01},
	}

	got := newChecksResiduals(pairs)

	near, far := distanceM(10, 20, 10, 20.001), distanceM(10, 20, 10, 20.003)
	nmea, strava := distanceM(10, 20, 10.0001, 20), distanceM(10, 20, 10, 20.01)
	want := []checksResidual{
		{Format: "nmea", N: 1, P50M: nmea, P90M: nmea},
		{Format: "gpx", N: 2, P50M: (near + far) / 2, P90M: near + 0.9*(far-near)},
		{Format: "komoot", N: 1, P50M: strava, P90M: strava},
		{Format: "strava", N: 1, P50M: strava, P90M: strava},
	}
	require.Len(t, got, len(want))
	for i := range want {
		assert.Equal(t, want[i].Format, got[i].Format)
		assert.Equal(t, want[i].N, got[i].N, want[i].Format)
		assert.InDelta(t, want[i].P50M, got[i].P50M, 1e-9, want[i].Format)
		assert.InDelta(t, want[i].P90M, got[i].P90M, 1e-9, want[i].Format)
	}
}

func TestSampleTargets(t *testing.T) {
	t.Run("a file that spans no time is sampled once", func(t *testing.T) {
		assert.Equal(t, []time.Time{checksAt}, sampleTargets(checksAt, checksAt))
	})

	t.Run("twenty instants from first to last", func(t *testing.T) {
		got := sampleTargets(checksAt, checksAt.Add(19*time.Minute))
		require.Len(t, got, agreementSamples)
		for k, target := range got {
			assert.Equal(t, checksAt.Add(time.Duration(k)*time.Minute), target, k)
		}
	})

	t.Run("the last target is the last point exactly", func(t *testing.T) {
		last := checksAt.Add(19*time.Second + 18)
		got := sampleTargets(checksAt, last)
		assert.Equal(t, checksAt.Add(18*time.Second), got[18])
		assert.Equal(t, last, got[19])
	})

	t.Run("a span beyond time.Duration still ends at the last point", func(t *testing.T) {
		first, last := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
		got := sampleTargets(first, last)
		require.Len(t, got, agreementSamples)
		assert.Equal(t, first, got[0])
		assert.Equal(t, last, got[19])
		for k := 1; k < len(got); k++ {
			assert.True(t, got[k].After(got[k-1]), k)
		}
	})
}

func TestSpaceSamples(t *testing.T) {
	sample := func(offset time.Duration) samplePoint { return samplePoint{at: checksAt.Add(offset)} }
	seconds := func(offsets ...float64) []samplePoint {
		var points []samplePoint
		for _, s := range offsets {
			points = append(points, sample(time.Duration(s*float64(time.Second))))
		}
		return points
	}
	// Twenty targets over two minutes of a 1 Hz file, each moved up to its next whole second.
	oneHertz := seconds(0, 7, 13, 19, 26, 32, 38, 45, 51, 57, 64, 70, 76, 83, 89, 95, 102, 108, 114, 120)

	tests := []struct {
		name   string
		points []samplePoint
		want   []samplePoint
	}{
		{"none", nil, nil},
		{"repeats go, and the order is by time", seconds(60, 0, 0, 30, 59), seconds(0, 30, 60)},
		{"30 s apart is kept", seconds(0, 30), seconds(0, 30)},
		{"just under 30 s is not", seconds(0, 29.999), seconds(0)},
		{"spaced from the last kept, not the last seen", seconds(0, 20, 40), seconds(0, 40)},
		{"two minutes of a 1 Hz file", oneHertz, seconds(0, 32, 64, 95)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, spaceSamples(tt.points))
		})
	}
}

func TestNearerPoint(t *testing.T) {
	at := checksAt
	point := func(offset time.Duration) *geo.Point { return &geo.Point{At: at.Add(offset)} }
	before5, after5, after10, before10 := point(-5*time.Second), point(5*time.Second), point(10*time.Second), point(-10*time.Second)
	exact := point(0)

	tests := []struct {
		name          string
		before, after *geo.Point
		want          *geo.Point
	}{
		{"neither", nil, nil, nil},
		{"before only", before5, nil, before5},
		{"after only", nil, after5, after5},
		{"after is nearer", before10, after5, after5},
		{"before is nearer", before5, after10, before5},
		{"a tie goes to the earlier", before5, after5, before5},
		{"a point at the instant is both sides", exact, exact, exact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Same(t, tt.want, nearerPoint(at, tt.before, tt.after))
		})
	}
}

func TestGapCountsShare(t *testing.T) {
	tests := []struct {
		name   string
		counts gapCounts
		want   *float64
	}{
		{"no gaps", gapCounts{}, nil},
		{"nine gaps are too few", gapCounts{gaps: 9, modal: 9}, nil},
		{"ten equal gaps", gapCounts{gaps: 10, modal: 10}, ptr(1.0)},
		{"a quarter", gapCounts{gaps: 20, modal: 5}, ptr(0.25)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.counts.share())
		})
	}
}

func TestNewAgreementFile(t *testing.T) {
	tests := []struct {
		name       string
		distances  []float64
		wantMedian *float64
	}{
		{"no match", nil, nil},
		{"four matches are too few", []float64{1, 2, 3, 4}, nil},
		{"five matches", []float64{50, 10, 40, 20, 30}, ptr(30.0)},
		{"an even count interpolates", []float64{6, 5, 4, 3, 2, 1}, ptr(3.5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newAgreementFile("gpx:a.gpx", 20, tt.distances, gapCounts{gaps: 10, modal: 10})

			assert.Equal(t, agreementFile{
				Source: "gpx:a.gpx", Samples: 20, Matched: len(tt.distances), MedianM: tt.wantMedian, ModalGapShare: ptr(1.0),
			}, got)
		})
	}
}

// failingChecksDB refuses every query and transaction, as a database that is down does.
type failingChecksDB struct{ err error }

func (d failingChecksDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, d.err }
func (d failingChecksDB) QueryRow(context.Context, string, ...any) pgx.Row        { return errRow{d.err} }
func (d failingChecksDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, d.err
}
func (d failingChecksDB) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("no batch follows a failed query")
}
func (d failingChecksDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { return nil, d.err }

func TestChecksHandler_DatabaseErrorIs500(t *testing.T) {
	h := &ChecksHandler{db: failingChecksDB{err: errors.New("connection refused")}, matchWindow: time.Hour}

	for name, handler := range map[string]http.HandlerFunc{"checks": h.Get, "agreement": h.Agreement} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/", nil)))

			assert.Equal(t, http.StatusInternalServerError, rec.Code)
			assert.NotContains(t, rec.Body.String(), "connection refused", "the error stays in the log")
		})
	}
}

// recordingChecksDB records every statement in order: "begin" and those of the transaction it
// began, then "rollback" or "commit", and those sent to the pool itself prefixed "pool: ". A query
// answers with rows[sql], none when absent; a batch with no row for any of its queries.
type recordingChecksDB struct {
	rows     map[string][][]any
	beginErr error
	during   func(sql string) // called on every statement, before it is answered
	opts     []pgx.TxOptions
	log      []string
}

func (d *recordingChecksDB) record(sql string) {
	if d.during != nil {
		d.during(sql)
	}
	d.log = append(d.log, sql)
}

func (d *recordingChecksDB) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	d.record("begin")
	d.opts = append(d.opts, opts)
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err // as the pool answers a request that has ended
	}
	return &recordingTx{db: d}, nil
}

func (d *recordingChecksDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	d.record("pool: " + sql)
	return &mockRows{rows: d.rows[sql]}, nil
}
func (d *recordingChecksDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	d.record("pool: " + sql)
	return noRowsRow{}
}
func (d *recordingChecksDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	d.record("pool: " + sql)
	return pgconn.CommandTag{}, nil
}
func (d *recordingChecksDB) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	d.record("pool: batch")
	return &recordedBatchResults{db: &recordingBatchDB{rowFor: noBracketRow}, queued: b.QueuedQueries}
}

func noBracketRow(*pgx.QueuedQuery) pgx.Row { return noRowsRow{} }

// recordingTx is a transaction of recordingChecksDB. A method the checks never call panics.
type recordingTx struct {
	pgx.Tx
	db *recordingChecksDB
}

func (tx *recordingTx) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	tx.db.record(sql)
	return &mockRows{rows: tx.db.rows[sql]}, nil
}
func (tx *recordingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.db.record(sql)
	return pgconn.CommandTag{}, nil
}
func (tx *recordingTx) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	tx.db.record("batch")
	return &recordedBatchResults{db: &recordingBatchDB{rowFor: noBracketRow}, queued: b.QueuedQueries}
}
func (tx *recordingTx) Commit(context.Context) error {
	tx.db.record("commit")
	return nil
}
func (tx *recordingTx) Rollback(context.Context) error {
	tx.db.record("rollback")
	return nil
}

func TestChecksReport_OneReadOnlySnapshotWithoutJIT(t *testing.T) {
	db := &recordingChecksDB{rows: map[string][][]any{
		// A derived label, so the orphan query runs too.
		trackFilesSQL: {{"exif", 1, checksAt, checksAt}},
		// An estimate, so its bracket is recomputed in a batch.
		checksEstimatesSQL: {{"e1", checksAt, nil, nil, nil}},
	}}
	h := &ChecksHandler{db: db, matchWindow: time.Hour}

	rec := httptest.NewRecorder()
	h.Get(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/checks", nil)))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []pgx.TxOptions{{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}}, db.opts,
		"one snapshot, and nothing written")
	assert.Equal(t, []string{
		"begin", "SET LOCAL jit = off",
		trackFilesSQL, checksEstimatesSQL, "batch", residualPairsSQL, futurePointsSQL, takenAfterUploadSQL, orphanPointsSQL,
		"rollback",
	}, db.log, "jit off before the first query, every query in the transaction, and none on the pool")
}

func TestChecksReport_TransactionFailures(t *testing.T) {
	t.Run("a transaction that cannot begin is a 500", func(t *testing.T) {
		db := &recordingChecksDB{beginErr: errors.New("too many clients already")}
		h := &ChecksHandler{db: db, matchWindow: time.Hour}

		rec := httptest.NewRecorder()
		h.Get(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/checks", nil)))

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "too many clients", "the error stays in the log")
		assert.Equal(t, []string{"begin"}, db.log)
	})

	t.Run("a deadline before it begins is a 504", func(t *testing.T) {
		db := &recordingChecksDB{}
		h := &ChecksHandler{db: db, matchWindow: time.Hour}
		ended, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		rec := httptest.NewRecorder()
		h.Get(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/checks", nil).WithContext(ended)))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.JSONEq(t, `{"error":"checks_timeout"}`, rec.Body.String())
		assert.Equal(t, []string{"begin"}, db.log)
	})
}

func TestAgreementFrom(t *testing.T) {
	tests := []struct {
		query  string
		want   int
		wantOK bool
	}{
		{"", 0, true},
		{"from=0", 0, true},
		{"from=7", 7, true},
		{"from=007", 7, true},
		{"from=1000000000", agreementMaxFrom, true},
		{"other=1", 0, true},
		{"from=1000000001", 0, false},
		{"from=99999999999999999999999", 0, false},
		{"from=", 0, false},
		{"from", 0, false},
		{"from=-1", 0, false},
		{"from=%2B1", 0, false}, // +1
		{"from=1.0", 0, false},
		{"from=1e3", 0, false},
		{"from=0x10", 0, false},
		{"from=%201", 0, false}, // " 1"
		{"from=1%20", 0, false},
		{"from=gpx%3Aa.gpx", 0, false}, // a label is never an index
		{"from=1&from=2", 0, false},
		{"from=1&from=1", 0, false},
		{"from=%zz", 0, false},
		{"from=1;x=2", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, ok := agreementFrom(httptest.NewRequest(http.MethodGet, "/api/checks/agreement?"+tt.query, nil))
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestChecksAgreement_BadFromIs400BeforeAnyDatabaseWork(t *testing.T) {
	// "%2B1" is "+1", which strconv.Atoi would take: only digits are a from.
	for _, query := range []string{"from=", "from=-1", "from=%2B1", "from=+1", "from=1.0", "from=x", "from=1&from=2", "from=1000000001", "from=%zz"} {
		t.Run(query, func(t *testing.T) {
			db := &recordingChecksDB{}
			h := NewChecksHandler(nil, time.Hour)
			h.db = db
			// Even with every run slot taken: a malformed request is refused for what it is.
			for range agreementMaxRuns {
				h.agreementRuns <- struct{}{}
			}

			rec := httptest.NewRecorder()
			h.Agreement(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/checks/agreement?"+query, nil)))

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.JSONEq(t, `{"error":"bad_from"}`, rec.Body.String())
			assert.Empty(t, db.log)
		})
	}
}

func TestChecksAgreement_RunSlots(t *testing.T) {
	get := func(h *ChecksHandler) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Agreement(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/checks/agreement", nil)))
		return rec
	}

	t.Run("NewChecksHandler bounds the runs", func(t *testing.T) {
		h := NewChecksHandler(nil, time.Hour)
		assert.Equal(t, agreementMaxRuns, cap(h.agreementRuns))
		assert.Equal(t, 2, agreementMaxRuns)
	})

	t.Run("a run holds a slot and gives it back", func(t *testing.T) {
		db := &recordingChecksDB{}
		h := NewChecksHandler(nil, time.Hour)
		h.db = db
		var taken []int
		db.during = func(string) { taken = append(taken, len(h.agreementRuns)) }

		rec := get(h)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []int{1}, taken, "the inventory ran in a slot")
		assert.Zero(t, len(h.agreementRuns), "free again")
	})

	t.Run("every slot taken is a 503 at once, before any database work", func(t *testing.T) {
		db := &recordingChecksDB{}
		h := NewChecksHandler(nil, time.Hour)
		h.db = db
		for range agreementMaxRuns {
			h.agreementRuns <- struct{}{}
		}

		rec := get(h)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"error":"checks_busy"}`, rec.Body.String())
		assert.Empty(t, db.log)
		assert.Equal(t, agreementMaxRuns, len(h.agreementRuns), "the runs in progress keep their slots")

		<-h.agreementRuns
		assert.Equal(t, http.StatusOK, get(h).Code, "a slot given back serves the next request")
	})

	t.Run("one slot taken still serves", func(t *testing.T) {
		h := NewChecksHandler(nil, time.Hour)
		h.db = &recordingChecksDB{}
		h.agreementRuns <- struct{}{}

		assert.Equal(t, http.StatusOK, get(h).Code)
	})

	t.Run("no channel sets no bound", func(t *testing.T) {
		h := &ChecksHandler{db: &recordingChecksDB{}, matchWindow: time.Hour}

		assert.Equal(t, http.StatusOK, get(h).Code)
	})
}

func TestAgreementChunkLen(t *testing.T) {
	files := func(points ...int) []trackFileRow {
		rows := make([]trackFileRow, len(points))
		for i, p := range points {
			rows[i] = trackFileRow{source: fmt.Sprint("gpx:", i), points: p}
		}
		return rows
	}
	tests := []struct {
		name                string
		files               []trackFileRow
		maxFiles, maxPoints int
		want                int
	}{
		{"none", nil, 3, 100, 0},
		{"every file fits", files(10, 20, 30), 3, 100, 3},
		{"at most maxFiles", files(1, 1, 1, 1), 3, 100, 3},
		{"up to maxPoints exactly", files(40, 60, 1), 3, 100, 2},
		{"stops before the file that would pass maxPoints", files(40, 61, 1), 3, 100, 1},
		{"a file over maxPoints is a chunk of its own", files(500, 1), 3, 100, 1},
		{"even after a small one, it waits for the next chunk", files(1, 500), 3, 100, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, agreementChunkLen(tt.files, tt.maxFiles, tt.maxPoints))
		})
	}
}
