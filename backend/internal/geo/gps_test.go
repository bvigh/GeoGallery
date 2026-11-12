package geo

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHaversineKm_ParisToLondon(t *testing.T) {
	// Paris: 48.8566, 2.3522 — London: 51.5074, -0.1278
	km := HaversineKm(48.8566, 2.3522, 51.5074, -0.1278)
	assert.InDelta(t, 341.0, km, 5.0)
}

// Rounding can leave the haversine term just above 1 at exact antipodes, where 1-a under a square
// root would give NaN: JSON cannot carry that, and the matcher's UPDATE breaks gps_confidence's CHECK.
func TestHaversineKm_ExactAntipodesAreHalfTheGlobe(t *testing.T) {
	halfCircumference := math.Pi * 6371.0
	for lat := -90.0; lat <= 90; lat += 0.5 {
		for lng := -180.0; lng < 0; lng += 0.5 {
			km := HaversineKm(lat, lng, -lat, lng+180)
			require.False(t, math.IsNaN(km), "(%v, %v) and its antipode", lat, lng)
			require.InDelta(t, halfCircumference, km, 1e-3, "(%v, %v) and its antipode", lat, lng)
		}
	}
}

func TestExplain_AntipodalPairIsFinite(t *testing.T) {
	at := time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)
	before := &Point{Lat: 10, Lng: 20, At: at.Add(-5 * time.Minute)}
	after := &Point{Lat: -10, Lng: -160, At: at.Add(5 * time.Minute)}

	e := Explain(before, after, at, time.Hour)

	assert.True(t, e.OK)
	assert.InDelta(t, math.Pi*6371.0, e.PairKm, 1e-3)
	require.NotNil(t, e.DistanceFactor)
	assert.Zero(t, *e.DistanceFactor)
	assert.Zero(t, e.Confidence)
	require.NotNil(t, e.ImpliedKmh)
	assert.False(t, math.IsNaN(*e.ImpliedKmh))
}

func TestInterpolate_ZeroTimeGap_ConfidenceOne(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	p := &Point{Lat: 47.0, Lng: 8.0, At: t0}

	res := Interpolate(p, p, t0, 60*time.Minute)

	assert.Equal(t, "interpolated", res.Source)
	assert.Equal(t, 1.0, res.Confidence)
}

func TestInterpolate_OnlyBefore_OutsideWindow_ReturnsNone(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	before := &Point{Lat: 47.1, Lng: 8.2, At: t0}
	photoAt := t0.Add(90 * time.Minute)

	res := Interpolate(before, nil, photoAt, 60*time.Minute)

	assert.Equal(t, "none", res.Source)
}

func TestInterpolate_OnlyBefore_WithinWindow(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	before := &Point{Lat: 47.1, Lng: 8.2, At: t0}
	photoAt := t0.Add(5 * time.Minute)

	res := Interpolate(before, nil, photoAt, 60*time.Minute)

	assert.Equal(t, "interpolated", res.Source)
	assert.Equal(t, 47.1, res.Lat)
	assert.Equal(t, 8.2, res.Lng)
}

func TestInterpolate_BothNil_ReturnsNone(t *testing.T) {
	res := Interpolate(nil, nil, time.Now(), 60*time.Minute)

	assert.Equal(t, "none", res.Source)
	assert.Equal(t, 0.0, res.Lat)
	assert.Equal(t, 0.0, res.Lng)
}

func TestInterpolate_FarGapLowConfidence(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	before := &Point{Lat: 47.0, Lng: 8.0, At: t0}
	photoAt := t0.Add(55 * time.Minute)

	res := Interpolate(before, nil, photoAt, 60*time.Minute)

	assert.Equal(t, "interpolated", res.Source)
	assert.LessOrEqual(t, res.Confidence, 0.10)
}

func TestIsValidGPS(t *testing.T) {
	cases := []struct {
		lat, lng float64
		valid    bool
	}{
		{0, 0, false},
		{0.001, 0.001, false}, // within null-island threshold
		{0.02, 0, true},       // only one axis needs to exceed threshold
		{0, 0.02, true},
		{47.5, 19.0, true},  // Budapest
		{-33.9, 18.4, true}, // Cape Town
		{91, 0, false},      // lat out of range
		{0, 181, false},     // lng out of range
		{math.NaN(), 20, false},
		{20, math.NaN(), false},
		{math.NaN(), math.NaN(), false},
		{math.Inf(1), 20, false},
		{20, math.Inf(-1), false},
	}
	for _, c := range cases {
		got := IsValidGPS(c.lat, c.lng)
		assert.Equal(t, c.valid, got, "lat=%v lng=%v", c.lat, c.lng)
	}
}

func TestInterpolate_MidpointHighConfidence(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	// ~100 m apart (0.0009° lat ≈ 100 m), 10 s apart
	before := &Point{Lat: 47.0000, Lng: 8.0000, At: t0}
	after := &Point{Lat: 47.0009, Lng: 8.0000, At: t0.Add(10 * time.Second)}
	midpoint := t0.Add(5 * time.Second)

	res := Interpolate(before, after, midpoint, 60*time.Minute)

	assert.Equal(t, "interpolated", res.Source)
	assert.GreaterOrEqual(t, res.Confidence, 0.99)
	assert.InDelta(t, 47.00045, res.Lat, 0.0001)
}

func TestExplain_MatchesInterpolate(t *testing.T) {
	at := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	window := time.Hour
	point := func(offset time.Duration, lat, lng float64) *Point {
		return &Point{Lat: lat, Lng: lng, At: at.Add(offset)}
	}
	tests := []struct {
		name          string
		before, after *Point
		wantOK        bool
		wantShape     Shape
	}{
		{"no points", nil, nil, false, ""},
		{"before only, inside the window", point(-10*time.Minute, 10, 20), nil, true, ShapeOneSided},
		{"before only, on the window's edge", point(-time.Hour, 10, 20), nil, true, ShapeOneSided},
		{"before only, outside the window", point(-90*time.Minute, 10, 20), nil, false, ""},
		{"after only, inside the window", nil, point(10*time.Minute, 10, 20), true, ShapeOneSided},
		{"after only, outside the window", nil, point(90*time.Minute, 10, 20), false, ""},
		{"same instant, same row", point(0, 10, 20), point(0, 10, 20), true, ShapeExact},
		{"same instant, different positions", point(0, 10, 20), point(0, 11, 21), true, ShapeExact},
		{"two sides, close", point(-5*time.Second, 10, 20), point(5*time.Second, 10.001, 20.001), true, ShapeTwoSided},
		{"two sides, far apart in space", point(-time.Minute, 10, 20), point(time.Minute, 12, 22), true, ShapeTwoSided},
		{"two sides, a full window apart", point(-30*time.Minute, 10, 20), point(30*time.Minute, 10.1, 20.1), true, ShapeTwoSided},
		{"two sides, over a window apart", point(-40*time.Minute, 10, 20), point(40*time.Minute, 10.1, 20.1), true, ShapeTwoSided},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Explain(tt.before, tt.after, at, window)
			r := Interpolate(tt.before, tt.after, at, window)

			assert.Equal(t, tt.wantOK, e.OK)
			assert.Equal(t, tt.wantShape, e.Shape)
			if !tt.wantOK {
				assert.Equal(t, InterpolationResult{Source: "none"}, r)
				return
			}
			assert.Equal(t, InterpolationResult{Lat: e.Lat, Lng: e.Lng, Confidence: e.Confidence, Source: "interpolated"}, r)
		})
	}
}

func TestExplain_Exact(t *testing.T) {
	at := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	p := &Point{Lat: 10, Lng: 20, At: at}

	e := Explain(p, p, at, time.Hour)

	assert.True(t, e.OK)
	assert.Equal(t, ShapeExact, e.Shape)
	assert.Equal(t, 10.0, e.Lat)
	assert.Equal(t, 20.0, e.Lng)
	assert.Zero(t, e.PairGapS)
	assert.Zero(t, e.PairKm)
	assert.Nil(t, e.T)
	assert.Nil(t, e.ImpliedKmh)
	assert.Equal(t, 1.0, e.TimeFactor)
	if assert.NotNil(t, e.DistanceFactor) {
		assert.Equal(t, 1.0, *e.DistanceFactor)
	}
	assert.Equal(t, 1.0, e.Confidence)
}

func TestExplain_OneSided(t *testing.T) {
	at := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	p := &Point{Lat: 10, Lng: 20, At: at.Add(15 * time.Minute)}

	e := Explain(nil, p, at, time.Hour)

	assert.True(t, e.OK)
	assert.Equal(t, ShapeOneSided, e.Shape)
	assert.Equal(t, 10.0, e.Lat)
	assert.Equal(t, 20.0, e.Lng)
	assert.Zero(t, e.PairGapS)
	assert.Zero(t, e.PairKm)
	assert.Nil(t, e.T)
	assert.Nil(t, e.DistanceFactor, "a copied point has no distance term")
	assert.Nil(t, e.ImpliedKmh)
	assert.InDelta(t, 0.75, e.TimeFactor, 1e-9)
	assert.InDelta(t, 0.75, e.Confidence, 1e-9)
}

func TestExplain_TwoSided(t *testing.T) {
	at := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	before := &Point{Lat: 10, Lng: 20, At: at.Add(-5 * time.Minute)}
	after := &Point{Lat: 10.2, Lng: 20, At: at.Add(15 * time.Minute)}
	km := HaversineKm(10, 20, 10.2, 20)

	e := Explain(before, after, at, time.Hour)

	assert.True(t, e.OK)
	assert.Equal(t, ShapeTwoSided, e.Shape)
	assert.InDelta(t, 10.05, e.Lat, 1e-9)
	assert.InDelta(t, 20.0, e.Lng, 1e-9)
	assert.InDelta(t, 1200.0, e.PairGapS, 1e-9)
	assert.InDelta(t, km, e.PairKm, 1e-9)
	if assert.NotNil(t, e.T) {
		assert.InDelta(t, 0.25, *e.T, 1e-9)
	}
	timeFactor := 1 - 1200.0/3600
	distanceFactor := 1 - km/100
	assert.InDelta(t, timeFactor, e.TimeFactor, 1e-9)
	if assert.NotNil(t, e.DistanceFactor) {
		assert.InDelta(t, distanceFactor, *e.DistanceFactor, 1e-9)
	}
	assert.InDelta(t, timeFactor*distanceFactor, e.Confidence, 1e-9)
	if assert.NotNil(t, e.ImpliedKmh) {
		assert.InDelta(t, km/(1200.0/3600), *e.ImpliedKmh, 1e-9)
	}
}
