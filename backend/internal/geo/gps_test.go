package geo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHaversineKm_ParisToLondon(t *testing.T) {
	// Paris: 48.8566, 2.3522 — London: 51.5074, -0.1278
	km := HaversineKm(48.8566, 2.3522, 51.5074, -0.1278)
	assert.InDelta(t, 341.0, km, 5.0)
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
		{0.001, 0.001, false},  // within null-island threshold
		{0.02, 0, true},        // only one axis needs to exceed threshold
		{0, 0.02, true},
		{47.5, 19.0, true},     // Budapest
		{-33.9, 18.4, true},    // Cape Town
		{91, 0, false},         // lat out of range
		{0, 181, false},        // lng out of range
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
