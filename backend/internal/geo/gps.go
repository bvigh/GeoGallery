package geo

import (
	"math"
	"time"
)

type Point struct {
	Lat float64
	Lng float64
	At  time.Time
}

type InterpolationResult struct {
	Lat        float64
	Lng        float64
	Confidence float64 // 0.0–1.0
	Source     string  // "exif", "interpolated", "none"
}

// Interpolate returns a GPS position for the given timestamp using the two
// bracketing track points. Confidence decays with time gap and distance jump.
func Interpolate(before, after *Point, at time.Time, maxWindow time.Duration) InterpolationResult {
	if before == nil && after == nil {
		return InterpolationResult{Source: "none"}
	}
	if before == nil {
		gap := after.At.Sub(at).Abs()
		if gap > maxWindow {
			return InterpolationResult{Source: "none"}
		}
		return InterpolationResult{
			Lat:        after.Lat,
			Lng:        after.Lng,
			Confidence: timeConfidence(gap, maxWindow),
			Source:     "interpolated",
		}
	}
	if after == nil {
		gap := at.Sub(before.At).Abs()
		if gap > maxWindow {
			return InterpolationResult{Source: "none"}
		}
		return InterpolationResult{
			Lat:        before.Lat,
			Lng:        before.Lng,
			Confidence: timeConfidence(gap, maxWindow),
			Source:     "interpolated",
		}
	}

	total := after.At.Sub(before.At).Seconds()
	if total == 0 {
		return InterpolationResult{Lat: before.Lat, Lng: before.Lng, Confidence: 1.0, Source: "interpolated"}
	}

	t := at.Sub(before.At).Seconds() / total
	lat := before.Lat + t*(after.Lat-before.Lat)
	lng := before.Lng + t*(after.Lng-before.Lng)

	timeFactor := timeConfidence(after.At.Sub(before.At), maxWindow)
	distFactor := distanceConfidence(HaversineKm(before.Lat, before.Lng, after.Lat, after.Lng))
	confidence := timeFactor * distFactor

	return InterpolationResult{Lat: lat, Lng: lng, Confidence: confidence, Source: "interpolated"}
}

func timeConfidence(gap, maxWindow time.Duration) float64 {
	if gap <= 0 {
		return 1.0
	}
	ratio := gap.Seconds() / maxWindow.Seconds()
	return math.Max(0, 1.0-ratio)
}

// confidence drops as distance between bracketing points grows
func distanceConfidence(km float64) float64 {
	const maxReasonableKm = 100.0
	return math.Max(0, 1.0-km/maxReasonableKm)
}

func HaversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371.0
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func toRad(deg float64) float64 {
	return deg * math.Pi / 180
}

// IsValidGPS returns false for coordinates that indicate no real GPS fix:
// exact or near-zero (Null Island placeholder), or out-of-range values.
func IsValidGPS(lat, lng float64) bool {
	// Written this way round so that NaN, which compares false with everything, is refused too.
	if !(math.Abs(lat) <= 90 && math.Abs(lng) <= 180) {
		return false
	}
	// Cameras with no fix typically write 0,0. Reject anything within ~1 km of
	// the origin — no consumer photo location is legitimately that close.
	const nullIslandThreshold = 0.01
	return math.Abs(lat) > nullIslandThreshold || math.Abs(lng) > nullIslandThreshold
}
