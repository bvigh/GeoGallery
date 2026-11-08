package handlers

import (
	"context"
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
		before, after, err := bracketingPoints(ctx, db, ownerID, item.takenAt, maxWindow)
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

func bracketingPoints(ctx context.Context, db matcherDB, ownerID string, at time.Time, maxWindow time.Duration) (*geo.Point, *geo.Point, error) {
	var before, after *geo.Point

	row := db.QueryRow(ctx, `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at
		FROM gps_tracks
		WHERE owner_id = $3 AND recorded_at <= $1 AND recorded_at >= $2
		ORDER BY recorded_at DESC
		LIMIT 1
	`, at, at.Add(-maxWindow), ownerID)
	var lat, lng float64
	var recordedAt time.Time
	if err := row.Scan(&lat, &lng, &recordedAt); err == nil {
		before = &geo.Point{Lat: lat, Lng: lng, At: recordedAt}
	} else if err != pgx.ErrNoRows {
		return nil, nil, err
	}

	row = db.QueryRow(ctx, `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at
		FROM gps_tracks
		WHERE owner_id = $3 AND recorded_at >= $1 AND recorded_at <= $2
		ORDER BY recorded_at ASC
		LIMIT 1
	`, at, at.Add(maxWindow), ownerID)
	if err := row.Scan(&lat, &lng, &recordedAt); err == nil {
		after = &geo.Point{Lat: lat, Lng: lng, At: recordedAt}
	} else if err != pgx.ErrNoRows {
		return nil, nil, err
	}

	return before, after, nil
}
