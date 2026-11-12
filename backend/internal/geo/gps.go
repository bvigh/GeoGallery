package geo

import (
	"math"
	"time"
)

// Point is one track point. Source, AccuracyM and AltitudeM are carried along for the provenance
// view; the arithmetic reads only Lat, Lng and At.
type Point struct {
	Lat       float64
	Lng       float64
	At        time.Time
	Source    string
	AccuracyM *float64
	AltitudeM *float64
}

type InterpolationResult struct {
	Lat        float64
	Lng        float64
	Confidence float64 // 0.0–1.0
	Source     string  // "exif", "interpolated", "none"
}

// Shape says how a bracket produced a position.
type Shape string

const (
	ShapeTwoSided Shape = "two_sided" // interpolated between two points
	ShapeOneSided Shape = "one_sided" // copied from the only point
	ShapeExact    Shape = "exact"     // a point at the same instant
)

// Explanation is the arithmetic of one bracket, with its intermediate values.
type Explanation struct {
	OK             bool // false where Interpolate returns Source "none"
	Lat, Lng       float64
	Shape          Shape
	PairGapS       float64
	PairKm         float64
	T              *float64 // position along the pair, two_sided only
	TimeFactor     float64
	DistanceFactor *float64 // nil for one_sided: there is no distance term
	Confidence     float64
	ImpliedKmh     *float64 // two_sided only
}

// Explain computes the position and confidence for at from its two bracketing track points. It is
// the only copy of this arithmetic: Interpolate wraps it, so the matcher and the "Why here?" panel
// cannot drift apart.
func Explain(before, after *Point, at time.Time, window time.Duration) Explanation {
	if before == nil && after == nil {
		return Explanation{}
	}
	if before == nil || after == nil {
		only := before
		if only == nil {
			only = after
		}
		gap := only.At.Sub(at).Abs()
		if gap > window {
			return Explanation{}
		}
		confidence := timeConfidence(gap, window)
		return Explanation{
			OK:         true,
			Lat:        only.Lat,
			Lng:        only.Lng,
			Shape:      ShapeOneSided,
			TimeFactor: confidence,
			Confidence: confidence,
		}
	}

	total := after.At.Sub(before.At).Seconds()
	if total == 0 {
		distanceFactor := 1.0
		return Explanation{
			OK:             true,
			Lat:            before.Lat,
			Lng:            before.Lng,
			Shape:          ShapeExact,
			TimeFactor:     1,
			DistanceFactor: &distanceFactor,
			Confidence:     1,
		}
	}

	t := at.Sub(before.At).Seconds() / total
	km := HaversineKm(before.Lat, before.Lng, after.Lat, after.Lng)
	timeFactor := timeConfidence(after.At.Sub(before.At), window)
	distanceFactor := distanceConfidence(km)
	impliedKmh := km / (total / 3600)

	return Explanation{
		OK:             true,
		Lat:            before.Lat + t*(after.Lat-before.Lat),
		Lng:            before.Lng + t*(after.Lng-before.Lng),
		Shape:          ShapeTwoSided,
		PairGapS:       total,
		PairKm:         km,
		T:              &t,
		TimeFactor:     timeFactor,
		DistanceFactor: &distanceFactor,
		Confidence:     timeFactor * distanceFactor,
		ImpliedKmh:     &impliedKmh,
	}
}

// Interpolate returns a GPS position for the given timestamp using the two
// bracketing track points. Confidence decays with time gap and distance jump.
func Interpolate(before, after *Point, at time.Time, maxWindow time.Duration) InterpolationResult {
	e := Explain(before, after, at, maxWindow)
	if !e.OK {
		return InterpolationResult{Source: "none"}
	}
	return InterpolationResult{Lat: e.Lat, Lng: e.Lng, Confidence: e.Confidence, Source: "interpolated"}
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
	a = math.Min(1, a) // rounding can pass 1 at exact antipodes, and 1-a under the root would be NaN
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
