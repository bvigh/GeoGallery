package gpstrack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"
)

// decodeLenient decodes the next value into v. A field of the wrong type keeps its zero value,
// so v must hold only strings (and structs and slices of them): "" reads as missing and only that
// point is skipped, as in Loom. A pointer field would be allocated and read as 0.
func decodeLenient(dec *json.Decoder, v any) error {
	err := dec.Decode(v)
	if isTypeError(err) {
		return nil
	}
	return err
}

func isTypeError(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

// --- Google Maps Timeline ("Zeitachse") export ---

type latLngField struct {
	LatLng string `json:"latLng"`
}

type timelineSegment struct {
	StartTime    string `json:"startTime"`
	EndTime      string `json:"endTime"`
	TimelinePath []struct {
		Point string `json:"point"`
		Time  string `json:"time"`
	} `json:"timelinePath"`
	Visit struct {
		TopCandidate struct {
			PlaceLocation latLngField `json:"placeLocation"`
		} `json:"topCandidate"`
	} `json:"visit"`
	Activity struct {
		Start latLngField `json:"start"`
		End   latLngField `json:"end"`
	} `json:"activity"`
}

// parseGoogleTimeline streams semanticSegments one at a time (exports run to hundreds of MB).
// Each timelinePath point has its own time. A visit's place is put at the middle of the
// segment. An activity's start and end are put at the segment's start and end time; Loom puts
// both at the middle, where the unique (owner, source, time) key would drop the end.
func parseGoogleTimeline(r io.Reader) ([]Point, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	var points []Point
	found := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if key != "semanticSegments" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
			continue
		}
		found = true
		if err := expectDelim(dec, '['); err != nil {
			return nil, err
		}
		for dec.More() {
			var seg timelineSegment
			if err := decodeLenient(dec, &seg); err != nil {
				return nil, err
			}
			points = seg.appendPoints(points)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, errors.New("no semanticSegments: not a Google Timeline export")
	}
	return points, nil
}

func (seg *timelineSegment) appendPoints(points []Point) []Point {
	for _, pt := range seg.TimelinePath {
		t, err := parseISO8601(pt.Time)
		if err != nil {
			continue
		}
		if lat, lng, ok := parseGoogleLatLng(pt.Point); ok {
			points = append(points, Point{Time: t, Lat: lat, Lng: lng})
		}
	}

	start, err := parseISO8601(seg.StartTime)
	if err != nil {
		return points
	}
	end, err := parseISO8601(seg.EndTime)
	if err != nil {
		return points
	}
	for _, place := range []struct {
		latLng string
		at     time.Time
	}{
		{seg.Visit.TopCandidate.PlaceLocation.LatLng, start.Add(end.Sub(start) / 2)},
		{seg.Activity.Start.LatLng, start},
		{seg.Activity.End.LatLng, end},
	} {
		if lat, lng, ok := parseGoogleLatLng(place.latLng); ok {
			points = append(points, Point{Time: place.at, Lat: lat, Lng: lng})
		}
	}
	return points
}

var googleDecimal = regexp.MustCompile(`[+-]?\d+\.\d+`)

// parseGoogleLatLng reads "47.4936672°, 19.1074134°".
func parseGoogleLatLng(s string) (lat, lng float64, ok bool) {
	parts := googleDecimal.FindAllString(s, 2)
	if len(parts) < 2 {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(parts[0], 64)
	lng, err2 := strconv.ParseFloat(parts[1], 64)
	return lat, lng, err1 == nil && err2 == nil
}

// --- JSON arrays: Samsung Health location_data and GeoGallery's import format ---

type arrayEntry struct {
	// Samsung Health exercise location_data: start_time is unix milliseconds.
	StartTime *float64 `json:"start_time"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Altitude  *float64 `json:"altitude"`
	Accuracy  *float64 `json:"accuracy"`

	// GeoGallery: one element of the POST /api/gps-tracks/import body.
	RecordedAt *string  `json:"recorded_at"`
	Lat        *float64 `json:"lat"`
	Lng        *float64 `json:"lng"`
	Source     string   `json:"source"`
	AltitudeM  *float64 `json:"altitude_m"`
	AccuracyM  *float64 `json:"accuracy_m"`
	Activity   *string  `json:"activity"`
}

// parseJSONArray reads a Samsung Health file, or GeoGallery's format when the first readable
// entry has recorded_at. Entries are decoded one at a time; one with a value of the wrong type is
// skipped whole (its number fields are pointers, see decodeLenient).
func parseJSONArray(r io.Reader) (string, []Point, error) {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '['); err != nil {
		return FormatSamsungHealth, nil, err
	}
	format := ""
	var points []Point
	for dec.More() {
		var e arrayEntry
		if err := dec.Decode(&e); isTypeError(err) {
			continue
		} else if err != nil {
			return format, nil, err
		}
		if format == "" {
			format = FormatSamsungHealth
			if e.RecordedAt != nil {
				format = FormatGeoGallery
			}
		}
		if p, ok := e.point(format); ok {
			points = append(points, p)
		}
	}
	if _, err := dec.Token(); err != nil {
		return format, nil, err
	}
	if format == "" {
		format = FormatSamsungHealth
	}
	return format, points, nil
}

func (e *arrayEntry) point(format string) (Point, bool) {
	if format == FormatGeoGallery {
		if e.RecordedAt == nil || e.Lat == nil || e.Lng == nil {
			return Point{}, false
		}
		t, err := time.Parse(time.RFC3339, *e.RecordedAt)
		if err != nil {
			return Point{}, false
		}
		return Point{Time: t.UTC(), Lat: *e.Lat, Lng: *e.Lng, AltitudeM: e.AltitudeM,
			AccuracyM: e.AccuracyM, Activity: e.Activity, Source: e.Source}, true
	}
	if e.StartTime == nil || e.Latitude == nil || e.Longitude == nil {
		return Point{}, false
	}
	t, ok := unixTime(*e.StartTime / 1000)
	if !ok {
		return Point{}, false
	}
	return Point{Time: t, Lat: *e.Latitude, Lng: *e.Longitude, AltitudeM: e.Altitude, AccuracyM: e.Accuracy}, true
}

// --- GeoJSON FeatureCollection (Google Takeout location history) ---

type geoJSONFile struct {
	Features []struct {
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			Timespan struct {
				Begin string `json:"begin"`
				End   string `json:"end"`
			} `json:"timespan"`
		} `json:"properties"`
	} `json:"features"`
}

// parseGeoJSON reads Point and LineString features with a properties.timespan. A Point is put
// at the begin time; a LineString's vertices are spread evenly from begin to end.
func parseGeoJSON(r io.Reader) ([]Point, error) {
	var doc geoJSONFile
	if err := decodeLenient(json.NewDecoder(r), &doc); err != nil {
		return nil, err
	}
	var points []Point
	for _, f := range doc.Features {
		begin, err := parseISO8601(f.Properties.Timespan.Begin)
		if err != nil {
			continue
		}
		end, err := parseISO8601(f.Properties.Timespan.End)
		if err != nil {
			end = begin
		}
		switch f.Geometry.Type {
		case "Point":
			if p, ok := geoJSONPosition(f.Geometry.Coordinates, begin); ok {
				points = append(points, p)
			}
		case "LineString":
			var vertices []json.RawMessage
			if json.Unmarshal(f.Geometry.Coordinates, &vertices) != nil {
				continue
			}
			for i, v := range vertices {
				if p, ok := geoJSONPosition(v, spread(begin, end, i, len(vertices))); ok {
					points = append(points, p)
				}
			}
		}
	}
	return points, nil
}

// geoJSONPosition reads [lng, lat, alt?].
func geoJSONPosition(raw json.RawMessage, at time.Time) (Point, bool) {
	var c []float64
	if json.Unmarshal(raw, &c) != nil || len(c) < 2 {
		return Point{}, false
	}
	p := Point{Time: at, Lat: c[1], Lng: c[0]}
	if len(c) > 2 {
		p.AltitudeM = &c[2]
	}
	return p, true
}

// spread is vertex i of n spaced evenly from begin to end.
func spread(begin, end time.Time, i, n int) time.Time {
	if n < 2 {
		return begin
	}
	return begin.Add(time.Duration(float64(end.Sub(begin)) * float64(i) / float64(n-1)))
}
