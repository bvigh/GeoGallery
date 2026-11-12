package handlers

import (
	"context"
	"errors"
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
	defer rows.Close()

	type pending struct {
		id      string
		takenAt time.Time
	}
	var items []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.takenAt); err != nil {
			return err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range items {
		before, after, err := bracketingPoints(ctx, db, ownerID, item.takenAt, maxWindow, bracketOptions{})
		if err != nil {
			return err
		}

		result := geo.Interpolate(before, after, item.takenAt, maxWindow)
		if result.Source == "none" {
			continue
		}

		_, err = db.Exec(ctx, `
			UPDATE media
			SET location       = ST_SetSRID(ST_MakePoint($1, $2), 4326),
			    gps_confidence = $3,
			    gps_source     = 'interpolated'
			WHERE id = $4
		`, result.Lng, result.Lat, result.Confidence, item.id)
		if err != nil {
			return err
		}
	}
	return nil
}

// bracketOptions narrows the track points bracketingPoints may pick. The zero value is what the
// matcher itself uses: every point counts.
type bracketOptions struct {
	excludeFormats []string // compared with split_part(source, ':', 1)
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
// It is the only bracket query: the matcher and the provenance route must not drift apart.
func bracketingPoints(ctx context.Context, db matcherDB, ownerID string, at time.Time,
	maxWindow time.Duration, opts bracketOptions) (before, after *geo.Point, err error) {
	// x <> ALL(NULL) is NULL and would match nothing, so a nil list must never reach the query.
	exclude := opts.excludeFormats
	if exclude == nil {
		exclude = []string{}
	}

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
