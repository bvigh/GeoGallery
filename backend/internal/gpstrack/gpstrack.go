// Package gpstrack reads GPS track files into points. It is a port of gps_tracks.py from Loom
// (codeberg.org/bvigh/Loom): the parser is chosen by file extension, and a point a parser cannot
// read is skipped, not reported. Speed is not kept (gps_tracks has no column for it).
package gpstrack

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Point is one track point. Times are UTC.
type Point struct {
	Time      time.Time
	Lat       float64
	Lng       float64
	AltitudeM *float64
	AccuracyM *float64
	// Activity and Source are set by FormatGeoGallery only. An empty Source means the caller
	// labels the point with the file's format and name.
	Activity *string
	Source   string
}

// Format names; the upload uses them as the source prefix ("gpx:<file name>"), as Loom does.
const (
	FormatGPX            = "gpx"
	FormatTCX            = "tcx"
	FormatKML            = "kml"
	FormatKMZ            = "kmz"
	FormatGeoJSON        = "geojson"
	FormatNMEA           = "nmea"
	FormatEsoko          = "esoko"
	FormatGoogleTimeline = "google_timeline"
	FormatSamsungHealth  = "samsung_health"
	// FormatGeoGallery is the body of POST /api/gps-tracks/import saved as a file.
	FormatGeoGallery = "geogallery"
)

// ErrUnsupported is returned for a file name with no known extension.
var ErrUnsupported = errors.New("unsupported file type (supported: .gpx, .tcx, .kml, .kmz, .geojson, .json, .nmea, .log, each optionally .gz)")

// maxExpandedBytes bounds what a .gz file or a KMZ entry may expand to. A var so tests can lower it.
var maxExpandedBytes int64 = 1 << 30

// parsers by extension. A .json file's format depends on its content.
var parsers = map[string]func(io.Reader) (string, []Point, error){
	".gpx":     fixed(FormatGPX, parseGPX),
	".tcx":     fixed(FormatTCX, parseTCX),
	".kml":     fixed(FormatKML, parseKML),
	".kmz":     fixed(FormatKMZ, parseKMZ),
	".geojson": fixed(FormatGeoJSON, parseGeoJSON),
	".nmea":    fixed(FormatNMEA, parseNMEA),
	".log":     fixed(FormatEsoko, parseEsokoLog),
	".json":    parseJSON,
}

func fixed(format string, parse func(io.Reader) ([]Point, error)) func(io.Reader) (string, []Point, error) {
	return func(r io.Reader) (string, []Point, error) {
		points, err := parse(r)
		return format, points, err
	}
}

// Parse reads the track file name from r and returns its format and points. Every format may be
// gzipped (name ending in .gz). A .json file is a Google Timeline export when it holds an object,
// and Samsung Health or GeoGallery's own format when it holds an array.
func Parse(name string, r io.Reader) (string, []Point, error) {
	base, gzipped := strings.CutSuffix(strings.ToLower(name), ".gz")
	parse, ok := parsers[path.Ext(base)]
	if !ok {
		return "", nil, ErrUnsupported
	}
	if gzipped {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return "", nil, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		r = limitExpanded(gz)
	}

	format, points, err := parse(r)
	if err != nil {
		if format == "" {
			return "", nil, err
		}
		return format, nil, fmt.Errorf("%s: %w", format, err)
	}

	// PostGIS refuses a coordinate out of range, which would fail the whole insert batch.
	valid := points[:0]
	for _, p := range points {
		if p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180 && !p.Time.IsZero() {
			valid = append(valid, p)
		}
	}
	return format, valid, nil
}

// parseJSON tells the JSON formats apart by their first character, as Loom does.
func parseJSON(r io.Reader) (string, []Point, error) {
	br := bufio.NewReader(r)
	if bom, err := br.Peek(3); err == nil && string(bom) == "\xef\xbb\xbf" {
		br.Discard(3)
	}
	for {
		c, err := br.Peek(1)
		if err == io.EOF {
			return "", nil, errors.New("json: empty file")
		}
		if err != nil {
			return "", nil, fmt.Errorf("json: %w", err)
		}
		switch c[0] {
		case ' ', '\t', '\r', '\n':
			br.Discard(1)
			continue
		case '[':
			return parseJSONArray(br)
		}
		points, err := parseGoogleTimeline(br)
		return FormatGoogleTimeline, points, err
	}
}

var errTooLarge = errors.New("expands to more than 1 GiB")

// limitExpanded reads r up to maxExpandedBytes and fails beyond it, so a small compressed file
// cannot expand without bound.
func limitExpanded(r io.Reader) io.Reader {
	return &expandLimit{r: r, left: maxExpandedBytes}
}

type expandLimit struct {
	r    io.Reader
	left int64
}

func (l *expandLimit) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// One more byte tells a file of exactly the limit from a larger one.
		var b [1]byte
		n, err := l.r.Read(b[:])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

var isoOffsetNoColon = regexp.MustCompile(`([+-])(\d{2})(\d{2})$`)

// parseISO8601 reads 2024-05-30T10:48:32Z, 2015-09-02T08:00:00.000+02:00 and +0200 offsets; a
// time without an offset is UTC.
func parseISO8601(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	s = isoOffsetNoColon.ReplaceAllString(s, "$1$2:$3")
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse("2006-01-02T15:04:05.999999999", s)
}

// unixTime converts unix seconds (with a fraction) to a time, rounded to the microsecond that
// Postgres keeps.
func unixTime(sec float64) (time.Time, bool) {
	// Up to the end of year 9999; beyond is no GPS time and overflows time.Unix arithmetic.
	if math.IsNaN(sec) || sec < 0 || sec > 253402300799 {
		return time.Time{}, false
	}
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(math.Round(frac*1e6))*1e3).UTC(), true
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// optFloat reads an optional value such as an altitude: unreadable or not finite is missing.
func optFloat(s string) *float64 {
	v, err := parseFloat(s)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
