package handlers

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// matcherDB is a subset of pgxpool.Pool used by RunGPSMatcher.
// *pgxpool.Pool satisfies this interface without any wrapping.
type matcherDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RunGPSMatcher assigns interpolated GPS positions to ownerID's media rows using bracketing
// points from ownerID's GPS tracks only. When reprocess is true, rows with
// gps_source='interpolated' are included so improved track data can produce better estimates.
//
// GPS stays per owner: this query and bracketingPoints must keep their literal owner_id filters
// and must never use readableMediaCond. Media shared with ownerID through an album are not
// ownerID's rows, so ownerID's matcher may neither write their location nor read it as a
// track point, and another member's tracks never place ownerID's media.
func RunGPSMatcher(ctx context.Context, db matcherDB, ownerID string, maxWindow time.Duration, reprocess bool) error {
	var query string
	if reprocess {
		query = `
			SELECT id, taken_at
			FROM media
			WHERE owner_id = $1
			  AND (gps_source IS NULL OR gps_source = 'interpolated') AND taken_at IS NOT NULL
		`
	} else {
		query = `
			SELECT id, taken_at
			FROM media
			WHERE owner_id = $1
			  AND location IS NULL AND gps_source IS NULL AND taken_at IS NOT NULL
		`
	}
	rows, err := db.Query(ctx, query, ownerID)
	if err != nil {
		return err
	}
	items, err := collectMatcherItems(rows)
	if err != nil {
		return err
	}
	return matchItems(ctx, db, ownerID, maxWindow, items)
}

// RunGPSMatcherAround is RunGPSMatcher with reprocess for the rows near some instants only: it
// places ownerID's dated rows without an EXIF or manual location whose taken_at lies within
// maxWindow of an instant in around. A track point at time p only ever enters the bracket of a row
// within maxWindow of p, so after points moved from or to some instants (a capture time changed by
// hand) these are the only rows whose estimate can change, and the rest of a large library is not
// read again. Nothing runs without an instant.
//
// GPS stays per owner, as in RunGPSMatcher: the literal owner_id filter is deliberate and must never
// become readableMediaCond.
func RunGPSMatcherAround(ctx context.Context, db matcherDB, ownerID string, maxWindow time.Duration, around []time.Time) error {
	ranges := mergeTimeRanges(around, maxWindow)
	if len(ranges) == 0 {
		return nil
	}
	los := make([]time.Time, len(ranges))
	his := make([]time.Time, len(ranges))
	for i, r := range ranges {
		los[i], his[i] = r.lo, r.hi
	}
	// The ranges are disjoint, so the join lists a row once, and each range is one scan of
	// media_owner_taken_at_idx however large the library is.
	rows, err := db.Query(ctx, `
		SELECT m.id, m.taken_at
		FROM unnest($2::timestamptz[], $3::timestamptz[]) AS r(lo, hi)
		JOIN media m ON m.owner_id = $1 AND m.taken_at BETWEEN r.lo AND r.hi
		WHERE m.gps_source IS NULL OR m.gps_source = 'interpolated'
	`, ownerID, los, his)
	if err != nil {
		return err
	}
	items, err := collectMatcherItems(rows)
	if err != nil {
		return err
	}
	return matchItems(ctx, db, ownerID, maxWindow, items)
}

// matcherItem is a row the matcher listed, with taken_at as it was listed.
type matcherItem struct {
	id      string
	takenAt time.Time
}

// collectMatcherItems reads the matcher's listing in full, so no rows are held open while each item
// queries its bracket.
func collectMatcherItems(rows pgx.Rows) ([]matcherItem, error) {
	defer rows.Close()
	var items []matcherItem
	for rows.Next() {
		var p matcherItem
		if err := rows.Scan(&p.id, &p.takenAt); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// matchItems interpolates a position for each listed item from ownerID's track points and writes
// it, the per-item loop RunGPSMatcher and RunGPSMatcherAround share.
func matchItems(ctx context.Context, db matcherDB, ownerID string, maxWindow time.Duration, items []matcherItem) error {
	for _, item := range items {
		before, after, err := bracketingPoints(ctx, db, ownerID, item.takenAt, maxWindow, bracketOptions{})
		if err != nil {
			return err
		}

		result := geo.Interpolate(before, after, item.takenAt, maxWindow)
		if result.Source == "none" {
			continue
		}

		// Written only while the row is as listed. `server retime` or a capture time changed by hand
		// may have moved taken_at since, and a position computed at the old time would stay: a later
		// run keeps a location it cannot recompute. A location set by hand meanwhile must not be
		// overwritten either.
		_, err = db.Exec(ctx, `
			UPDATE media
			SET location       = ST_SetSRID(ST_MakePoint($1, $2), 4326),
			    gps_confidence = $3,
			    gps_source     = 'interpolated'
			WHERE id = $4 AND owner_id = $5 AND taken_at = $6
			  AND (gps_source IS NULL OR gps_source = 'interpolated')
		`, result.Lng, result.Lat, result.Confidence, item.id, ownerID, item.takenAt)
		if err != nil {
			return err
		}
	}
	return nil
}

// timeRange is the closed interval [lo, hi].
type timeRange struct {
	lo, hi time.Time
}

// mergeTimeRanges returns the instants' windows, [t - window, t + window] each, merged where they
// overlap or touch and ordered by start. Merged, they never select a row twice, and a run of nearby
// instants (a burst shifted together) becomes one range.
func mergeTimeRanges(instants []time.Time, window time.Duration) []timeRange {
	if len(instants) == 0 {
		return nil
	}
	sorted := slices.SortedFunc(slices.Values(instants), func(a, b time.Time) int { return a.Compare(b) })
	ranges := []timeRange{{sorted[0].Add(-window), sorted[0].Add(window)}}
	for _, t := range sorted[1:] {
		lo, hi := t.Add(-window), t.Add(window)
		last := &ranges[len(ranges)-1]
		if !lo.After(last.hi) {
			if hi.After(last.hi) {
				last.hi = hi
			}
			continue
		}
		ranges = append(ranges, timeRange{lo, hi})
	}
	return ranges
}

// bracketOptions narrows the track points bracketingPoints may pick. The zero value is what the
// matcher itself uses: every point counts.
type bracketOptions struct {
	excludeFormats []string // compared with split_part(source, ':', 1)
}

// excluded is the exclusion list as the bracket queries take it. x <> ALL(NULL) is NULL and would
// match nothing, so a nil list must never reach a query.
func (o bracketOptions) excluded() []string {
	if o.excludeFormats == nil {
		return []string{}
	}
	return o.excludeFormats
}

// The nearest point at or before $1 (and at or after it), no further away than $2. Sorting ties by
// source makes the choice deterministic, and because UNIQUE (owner_id, source, recorded_at) makes it
// a total order, a point at the exact instant is the same row on both sides. It sorts on source
// rather than id because a re-import replaces ids.
const (
	bracketBeforeSQL = `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at, source, accuracy_m, altitude_m
		FROM gps_tracks
		WHERE owner_id = $3 AND recorded_at <= $1 AND recorded_at >= $2
		  AND split_part(source, ':', 1) <> ALL($4::text[])
		ORDER BY recorded_at DESC, source ASC
		LIMIT 1
	`
	bracketAfterSQL = `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at, source, accuracy_m, altitude_m
		FROM gps_tracks
		WHERE owner_id = $3 AND recorded_at >= $1 AND recorded_at <= $2
		  AND split_part(source, ':', 1) <> ALL($4::text[])
		ORDER BY recorded_at ASC, source ASC
		LIMIT 1
	`
)

// bracketingPoints returns ownerID's nearest track points on each side of at, within maxWindow.
// It is the only bracket query, which bracketingPointsMany sends in batches: the matcher, the
// provenance route and the checks must not drift apart.
func bracketingPoints(ctx context.Context, db matcherDB, ownerID string, at time.Time,
	maxWindow time.Duration, opts bracketOptions) (before, after *geo.Point, err error) {
	exclude := opts.excluded()
	before, err = scanBracketPoint(db.QueryRow(ctx, bracketBeforeSQL, at, at.Add(-maxWindow), ownerID, exclude))
	if err != nil {
		return nil, nil, err
	}
	after, err = scanBracketPoint(db.QueryRow(ctx, bracketAfterSQL, at, at.Add(maxWindow), ownerID, exclude))
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

// batchDB sends a pgx.Batch. *pgxpool.Pool satisfies it.
type batchDB interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// bracketLookup is one instant for bracketingPointsMany to bracket, with its own options.
type bracketLookup struct {
	at   time.Time
	opts bracketOptions
}

// bracketPair is the nearest point on each side of one lookup's instant. Either can be nil.
type bracketPair struct {
	before, after *geo.Point
}

// bracketBatchLookups is how many lookups go into one pgx.Batch, at two queries each.
const bracketBatchLookups = 500

// bracketingPointsMany is bracketingPoints for many instants of one owner in few round trips: it
// queues bracketBeforeSQL and bracketAfterSQL, with the arguments bracketingPoints sends, for every
// lookup in a pgx.Batch. The SQL text is shared, so this is not a second bracket query, and each
// result equals what bracketingPoints returns for that lookup. Results are in the order of lookups.
func bracketingPointsMany(ctx context.Context, db batchDB, ownerID string, lookups []bracketLookup,
	maxWindow time.Duration) ([]bracketPair, error) {
	pairs := make([]bracketPair, 0, len(lookups))
	for chunk := range slices.Chunk(lookups, bracketBatchLookups) {
		batch := &pgx.Batch{}
		for _, l := range chunk {
			exclude := l.opts.excluded()
			batch.Queue(bracketBeforeSQL, l.at, l.at.Add(-maxWindow), ownerID, exclude)
			batch.Queue(bracketAfterSQL, l.at, l.at.Add(maxWindow), ownerID, exclude)
		}
		results := db.SendBatch(ctx, batch)
		for range chunk {
			pair, err := readBracketPair(results)
			if err != nil {
				results.Close() // frees the connection; the read error is the one to report
				return nil, err
			}
			pairs = append(pairs, pair)
		}
		if err := results.Close(); err != nil {
			return nil, err
		}
	}
	return pairs, nil
}

// readBracketPair reads the next lookup's two rows from a batch bracketingPointsMany queued.
func readBracketPair(results pgx.BatchResults) (bracketPair, error) {
	before, err := scanBracketPoint(results.QueryRow())
	if err != nil {
		return bracketPair{}, err
	}
	after, err := scanBracketPoint(results.QueryRow())
	if err != nil {
		return bracketPair{}, err
	}
	return bracketPair{before: before, after: after}, nil
}

// scanBracketPoint reads one row of a bracket query. No row is a nil point, not an error.
func scanBracketPoint(row pgx.Row) (*geo.Point, error) {
	var p geo.Point
	err := row.Scan(&p.Lat, &p.Lng, &p.At, &p.Source, &p.AccuracyM, &p.AltitudeM)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
