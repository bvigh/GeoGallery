package handlers

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/gpstrack"
	"github.com/jackc/pgx/v5"
)

// The formats a derived track point can have: it was written from one of the owner's own items, so
// it is not independent evidence.
const (
	formatExif   = "exif"
	formatManual = "manual"
	formatOther  = "other"
)

// What a track format's rows are (TrackSource.Kind). It describes the format and is not a claim
// about any one row: stored rows carry no kind, and KML holds both real and spread times.
const (
	trackKindRecorded           = "recorded"
	trackKindSemantic           = "semantic"
	trackKindTimeMayBeEstimated = "time_may_be_estimated"
	trackKindDerived            = "derived"
	trackKindOther              = "other"
)

// trackKinds is keyed by every known format. A prefix that is not in it is reported as "other".
var trackKinds = map[string]string{
	gpstrack.FormatGPX:            trackKindRecorded,
	gpstrack.FormatTCX:            trackKindRecorded,
	gpstrack.FormatNMEA:           trackKindRecorded,
	gpstrack.FormatEsoko:          trackKindRecorded,
	gpstrack.FormatSamsungHealth:  trackKindRecorded,
	gpstrack.FormatGoogleTimeline: trackKindSemantic,
	gpstrack.FormatKML:            trackKindTimeMayBeEstimated,
	gpstrack.FormatKMZ:            trackKindTimeMayBeEstimated,
	gpstrack.FormatGeoJSON:        trackKindTimeMayBeEstimated,
	gpstrack.FormatGeoGallery:     trackKindOther,
	formatExif:                    trackKindDerived,
	formatManual:                  trackKindDerived,
}

// sourcePrefix is the part of a source label before the first colon, as split_part(source, ':', 1)
// computes it.
func sourcePrefix(source string) string {
	prefix, _, _ := strings.Cut(source, ":")
	return prefix
}

// trackFormat is the format a source label is reported as: its prefix, or "other" when that is not
// a known format.
func trackFormat(source string) string {
	prefix := sourcePrefix(source)
	if _, known := trackKinds[prefix]; known {
		return prefix
	}
	return formatOther
}

// trackKind is decided by the format alone.
func trackKind(source string) string {
	if kind, known := trackKinds[sourcePrefix(source)]; known {
		return kind
	}
	return trackKindOther
}

func isDerivedPrefix(prefix string) bool {
	return prefix == formatExif || prefix == formatManual
}

// timeWindow is one inclusive time range of a track.
type timeWindow struct {
	from, to time.Time
}

type TrackWindow struct {
	From time.Time `json:"from"` // UTC
	To   time.Time `json:"to"`   // UTC
}

type TrackSource struct {
	Label  string `json:"label"`
	Format string `json:"format"` // "other" when the prefix matches no known format
	Kind   string `json:"kind"`
}

type TrackPoint struct {
	T   time.Time `json:"t"` // UTC
	Lat float64   `json:"lat"`
	Lng float64   `json:"lng"`
	S   int       `json:"s"` // index into TrackResponse.Sources
	Acc *float64  `json:"acc"`
	Alt *float64  `json:"alt"`
}

// TrackResponse is a piece of the owner's track. Every point is a real row, so it keeps its true
// time, accuracy and source. MatchWindowS is for the caller to fill.
type TrackResponse struct {
	Windows        []TrackWindow `json:"windows"`
	MatchWindowS   int64         `json:"match_window_s"`
	TotalPoints    int           `json:"total_points"`
	ReturnedPoints int           `json:"returned_points"`
	BucketS        int           `json:"bucket_s"` // 0 when nothing was downsampled
	Sources        []TrackSource `json:"sources"`
	Points         []TrackPoint  `json:"points"`
}

// emptyTrackResponse has empty arrays rather than nulls, as the API promises.
func emptyTrackResponse() TrackResponse {
	return TrackResponse{Windows: []TrackWindow{}, Sources: []TrackSource{}, Points: []TrackPoint{}}
}

// A format is always computed after the time-range scan. Never filter one with LIKE or a prefix
// range on source: under an en_US.utf8 collation a range test returned no rows at all.
const (
	trackCountSQL = `
		SELECT split_part(source, ':', 1), count(*)
		FROM gps_tracks
		WHERE owner_id = $1 AND recorded_at BETWEEN $2 AND $3
		GROUP BY 1
	`
	trackLogPointsSQL = `
		SELECT source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry), accuracy_m, altitude_m
		FROM gps_tracks
		WHERE owner_id = $1 AND recorded_at BETWEEN $2 AND $3
		  AND split_part(source, ':', 1) NOT IN ('exif', 'manual')
	`
	// One real row per log format and bucket, the earliest in the bucket.
	trackBucketedLogPointsSQL = `
		SELECT DISTINCT ON (split_part(source, ':', 1), floor(extract(epoch FROM recorded_at - $2::timestamptz) / $4::int))
		       source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry), accuracy_m, altitude_m
		FROM gps_tracks
		WHERE owner_id = $1 AND recorded_at BETWEEN $2 AND $3
		  AND split_part(source, ':', 1) NOT IN ('exif', 'manual')
		ORDER BY split_part(source, ':', 1), floor(extract(epoch FROM recorded_at - $2::timestamptz) / $4::int), recorded_at, source
	`
	trackDerivedPointsSQL = `
		SELECT source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry), accuracy_m, altitude_m
		FROM gps_tracks
		WHERE owner_id = $1 AND recorded_at BETWEEN $2 AND $3
		  AND split_part(source, ':', 1) IN ('exif', 'manual')
	`
)

// loadTrack returns ownerID's track points in the windows. Log points are limited to about
// maxPoints by keeping one real row per format and time bucket. Derived points (photos and pins) are
// never thinned and come on top of the budget, because they are what a debugging view most needs
// to show. The range is inclusive, so a single window can return maxPoints + log formats + derived
// rows at most.
func loadTrack(ctx context.Context, db matcherDB, ownerID string, windows []timeWindow, maxPoints int) (TrackResponse, error) {
	logPoints, derivedPoints := 0, 0
	logFormats := map[string]struct{}{}
	for _, w := range windows {
		if err := countTrackWindow(ctx, db, ownerID, w, &logPoints, &derivedPoints, logFormats); err != nil {
			return TrackResponse{}, err
		}
	}
	bucketS := trackBucketSeconds(windows, len(logFormats), logPoints, maxPoints)

	var points []geo.Point
	for _, w := range windows {
		logRows, err := queryLogPoints(ctx, db, ownerID, w, bucketS)
		if err != nil {
			return TrackResponse{}, err
		}
		points = append(points, logRows...)
		derivedRows, err := queryTrackPoints(ctx, db, trackDerivedPointsSQL, ownerID, w.from, w.to)
		if err != nil {
			return TrackResponse{}, err
		}
		points = append(points, derivedRows...)
	}

	resp := newTrackResponse(windows, points)
	resp.TotalPoints = logPoints + derivedPoints
	resp.BucketS = bucketS
	return resp, nil
}

func countTrackWindow(ctx context.Context, db matcherDB, ownerID string, w timeWindow,
	logPoints, derivedPoints *int, logFormats map[string]struct{}) error {
	rows, err := db.Query(ctx, trackCountSQL, ownerID, w.from, w.to)
	if err != nil {
		return fmt.Errorf("count track points: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var prefix string
		var count int
		if err := rows.Scan(&prefix, &count); err != nil {
			return fmt.Errorf("scan track count: %w", err)
		}
		if isDerivedPrefix(prefix) {
			*derivedPoints += count
			continue
		}
		*logPoints += count
		logFormats[prefix] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read track counts: %w", err)
	}
	return nil
}

// trackBucketSeconds is 0 when the log points fit the budget. Otherwise the buckets are sized so
// that every log format, keeping one row per bucket, stays within it.
func trackBucketSeconds(windows []timeWindow, logFormats, logPoints, maxPoints int) int {
	if logPoints <= maxPoints {
		return 0
	}
	var span float64
	for _, w := range windows {
		span += w.to.Sub(w.from).Seconds()
	}
	// A zero-length window would otherwise divide by zero in the bucket query.
	return max(1, int(math.Ceil(span*float64(logFormats)/float64(maxPoints))))
}

func queryLogPoints(ctx context.Context, db matcherDB, ownerID string, w timeWindow, bucketS int) ([]geo.Point, error) {
	if bucketS == 0 {
		return queryTrackPoints(ctx, db, trackLogPointsSQL, ownerID, w.from, w.to)
	}
	return queryTrackPoints(ctx, db, trackBucketedLogPointsSQL, ownerID, w.from, w.to, bucketS)
}

func queryTrackPoints(ctx context.Context, db matcherDB, sql string, args ...any) ([]geo.Point, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query track points: %w", err)
	}
	defer rows.Close()
	points, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (geo.Point, error) {
		var p geo.Point
		err := row.Scan(&p.Source, &p.At, &p.Lat, &p.Lng, &p.AccuracyM, &p.AltitudeM)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read track points: %w", err)
	}
	return points, nil
}

// newTrackResponse orders the points by time and indexes their source labels. Points are compared
// as times, never as strings: "…:05Z" sorts after "…:05.5Z".
func newTrackResponse(windows []timeWindow, points []geo.Point) TrackResponse {
	slices.SortFunc(points, func(a, b geo.Point) int {
		return cmp.Or(a.At.Compare(b.At), strings.Compare(a.Source, b.Source))
	})

	indexes := map[string]int{}
	var labels []string
	for _, p := range points {
		if _, seen := indexes[p.Source]; !seen {
			indexes[p.Source] = 0
			labels = append(labels, p.Source)
		}
	}
	slices.Sort(labels)

	resp := emptyTrackResponse()
	for i, label := range labels {
		indexes[label] = i
		resp.Sources = append(resp.Sources, TrackSource{Label: label, Format: trackFormat(label), Kind: trackKind(label)})
	}
	for _, w := range windows {
		resp.Windows = append(resp.Windows, TrackWindow{From: w.from.UTC(), To: w.to.UTC()})
	}
	for _, p := range points {
		resp.Points = append(resp.Points, TrackPoint{
			T: p.At.UTC(), Lat: p.Lat, Lng: p.Lng, S: indexes[p.Source], Acc: p.AccuracyM, Alt: p.AltitudeM,
		})
	}
	resp.ReturnedPoints = len(resp.Points)
	return resp
}
