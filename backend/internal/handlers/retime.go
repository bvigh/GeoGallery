package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"codeberg.org/bvigh/geogallery/internal/ingest"
	"codeberg.org/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type headReader interface {
	ReadHead(ctx context.Context, key string, n int64) ([]byte, error)
}

// RetimeDeps holds what RunRetime needs; the operator `server retime` command builds it.
type RetimeDeps struct {
	pool         *pgxpool.Pool
	storage      headReader
	cameraZone   *time.Location
	matchWindow  time.Duration
	tripGapHours int
}

func NewRetimeDeps(pool *pgxpool.Pool, s *storage.Client, cameraZone *time.Location, matchWindow time.Duration, gapHours int) RetimeDeps {
	return RetimeDeps{pool: pool, storage: s, cameraZone: cameraZone, matchWindow: matchWindow, tripGapHours: gapHours}
}

// RetimeChange is one media item whose taken_at RunRetime moves (in a dry run: would move).
type RetimeChange struct {
	MediaID   string
	Filename  string
	Old, New  time.Time
	Zone      string // where New's offset came from: ingest.ZoneGPS or ingest.ZoneCamera
	GPSSource string // the item's gps_source before the change; "" without a location
}

// RetimeResult counts what RunRetime found among the owner's media with a taken_at.
type RetimeResult struct {
	Checked   int
	Retimed   int // taken_at moved (in a dry run: would move)
	Unchanged int // already what ingest computes today
	NotExif   int // no EXIF capture time: taken_at came from the video container or the file time
	Mismatch  int // an EXIF time the stored taken_at is not, in any zone: left alone, logged
	Failed    int // object unreadable or the update failed: logged
}

// RunRetime re-reads the EXIF capture time of ownerID's media and moves taken_at to what ingest
// computes today: ingest.ExtractExif with the camera zone. Before CAMERA_TIMEZONE, ingest read the
// EXIF wall clock in the server's own zone, which is UTC in Docker, so those rows sit an hour or
// two off and their interpolated locations were matched at the wrong time.
//
// Only rows dated from EXIF change. RunRetime reads the same mediaHeadSize bytes of each object that
// ingest read, and leaves a row alone when that head has no EXIF capture time, or when the stored
// taken_at is not the EXIF wall clock in any UTC offset (then it did not come from this EXIF).
//
// With apply, each change is one transaction that also:
//   - clears an interpolated location, which was matched at the old time;
//   - moves the track point ingest or a manual location put at the old time: the 'exif' point of
//     a photo with an EXIF position, and the 'manual' point of an item placed by hand. See
//     moveTrackPoint;
//   - when a point moved, clears the interpolated locations within the match window of its old
//     time, since they may rest on it. Items that are not retimed (videos) are among them.
//
// Afterwards the GPS matcher and trip detection run for ownerID, as after a scan. They run on every
// apply, even with nothing to change, so that a re-run completes an interrupted one. Without apply
// nothing is written. report is called for each change, in taken_at order. On cancellation the loop
// stops after the current item, the matcher and trip detection are skipped, and ctx.Err() is
// returned.
//
// GPS stays per owner: every query here filters on ownerID literally, like RunGPSMatcher.
func RunRetime(ctx context.Context, deps RetimeDeps, ownerID string, apply bool, report func(RetimeChange)) (RetimeResult, error) {
	slog.Info("retime: started", "owner_id", ownerID, "apply", apply)
	var res RetimeResult

	items, err := retimeCandidates(ctx, deps.pool, ownerID)
	if err != nil {
		return res, fmt.Errorf("list media: %w", err)
	}

	for _, m := range items {
		if err := ctx.Err(); err != nil {
			break
		}
		res.Checked++
		head, err := deps.storage.ReadHead(ctx, m.storageKey, mediaHeadSize)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.Error("retime: read object failed", "id", m.id, "key", m.storageKey, "err", err)
			res.Failed++
			continue
		}
		exifData := ingest.ExtractExif(bytes.NewReader(head), deps.cameraZone)
		switch {
		case exifData.TakenAt == nil:
			res.NotExif++
			continue
		case exifData.TakenAt.Equal(m.takenAt):
			res.Unchanged++
			continue
		case !sameWallClock(m.takenAt, *exifData.TakenAt):
			slog.Warn("retime: stored taken_at is not the EXIF time in any zone, left alone",
				"id", m.id, "filename", m.filename, "taken_at", m.takenAt.UTC(),
				"exif_time", wallClock(*exifData.TakenAt).Format(time.DateTime))
			res.Mismatch++
			continue
		}

		change := RetimeChange{
			MediaID: m.id, Filename: m.filename, Old: m.takenAt, New: *exifData.TakenAt,
			Zone: exifData.TakenAtZone, GPSSource: m.gpsSource,
		}
		if apply {
			if err := applyRetime(ctx, deps.pool, ownerID, change, exifData, deps.matchWindow); err != nil {
				if ctx.Err() != nil {
					break
				}
				slog.Error("retime: update failed", "id", m.id, "err", err)
				res.Failed++
				continue
			}
		}
		res.Retimed++
		report(change)
	}
	if err := ctx.Err(); err != nil {
		slog.Warn("retime: interrupted", "checked", res.Checked, "retimed", res.Retimed)
		return res, err
	}
	if !apply {
		slog.Info("retime: dry run complete", "checked", res.Checked, "would_retime", res.Retimed)
		return res, nil
	}

	if err := RunGPSMatcher(ctx, deps.pool, ownerID, deps.matchWindow, true); err != nil {
		slog.Error("retime: GPS matcher failed", "err", err)
		return res, fmt.Errorf("gps matcher: %w", err)
	}
	if err := RunTripDetect(ctx, deps.pool, ownerID, deps.tripGapHours); err != nil {
		slog.Error("retime: trip detect failed", "err", err)
		return res, fmt.Errorf("trip detect: %w", err)
	}
	slog.Info("retime: complete", "checked", res.Checked, "retimed", res.Retimed, "failed", res.Failed)
	return res, nil
}

type retimeCandidate struct {
	id, filename, storageKey, gpsSource string
	takenAt                             time.Time
}

// retimeCandidates lists ownerID's media with a taken_at (a row without one has no EXIF time), read
// up front so no connection is held while objects are fetched.
func retimeCandidates(ctx context.Context, pool *pgxpool.Pool, ownerID string) ([]retimeCandidate, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, filename, storage_key, taken_at, coalesce(gps_source, '')
		FROM media
		WHERE owner_id = $1 AND taken_at IS NOT NULL
		ORDER BY taken_at, id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []retimeCandidate
	for rows.Next() {
		var c retimeCandidate
		if err := rows.Scan(&c.id, &c.filename, &c.storageKey, &c.takenAt, &c.gpsSource); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

// wallClock returns t's clock reading in its own location, as a time in UTC.
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// sameWallClock reports whether stored is now's wall clock read in some UTC offset (a whole
// quarter hour, at most 14 hours), which is what every earlier ingest stored for an EXIF time,
// whatever the server's zone was. A file time or a hand-edited value almost never is.
func sameWallClock(stored, now time.Time) bool {
	d := wallClock(now).Sub(stored)
	return d%(15*time.Minute) == 0 && d.Abs() <= 14*time.Hour
}

func applyRetime(ctx context.Context, pool *pgxpool.Pool, ownerID string, c RetimeChange, exifData ingest.ExifData, matchWindow time.Duration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var gpsSource string
	var lat, lng *float64
	err = tx.QueryRow(ctx, `
		SELECT coalesce(gps_source, ''), ST_Y(location::geometry), ST_X(location::geometry) FROM media
		WHERE id = $1 AND owner_id = $2 AND taken_at = $3
		FOR UPDATE`, c.MediaID, ownerID, c.Old).Scan(&gpsSource, &lat, &lng)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("the row changed since it was read; re-run to pick it up")
	}
	if err != nil {
		return err
	}

	// SET expressions see the row as it was, so every CASE tests the old gps_source.
	if _, err := tx.Exec(ctx, `
		UPDATE media
		SET taken_at       = $3,
		    location       = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE location END,
		    gps_confidence = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE gps_confidence END,
		    gps_source     = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE gps_source END
		WHERE id = $1 AND owner_id = $2`, c.MediaID, ownerID, c.New); err != nil {
		return fmt.Errorf("update media: %w", err)
	}

	// Ingest stored the EXIF position as an 'exif' point at taken_at (insertExifGPSTrack). Its owner
	// may since have placed the photo by hand, which leaves gps_source 'manual'.
	movedExif, movedManual := false, false
	if exifData.Lat != nil {
		p := trackPoint{"exif", *exifData.Lat, *exifData.Lng}
		if movedExif, err = moveTrackPoint(ctx, tx, ownerID, p, c.Old, c.New, []string{"exif", "manual"}); err != nil {
			return err
		}
	}
	// A manual location put a 'manual' point at taken_at (MediaHandler.UpdateLocation).
	if gpsSource == "manual" && lat != nil && lng != nil {
		p := trackPoint{"manual", *lat, *lng}
		if movedManual, err = moveTrackPoint(ctx, tx, ownerID, p, c.Old, c.New, []string{"manual"}); err != nil {
			return err
		}
	}

	// Interpolations near the old time may rest on the moved point, and the matcher keeps a location
	// it cannot recompute. Cleared here, in the same transaction, they are matched again at the end
	// (by a re-run's matcher, if this run is interrupted), or stay empty when nothing brackets them.
	if movedExif || movedManual {
		if _, err := tx.Exec(ctx, `
			UPDATE media
			SET location = NULL, gps_confidence = NULL, gps_source = NULL
			WHERE owner_id = $1 AND gps_source = 'interpolated' AND taken_at BETWEEN $2 AND $3`,
			ownerID, c.Old.Add(-matchWindow), c.Old.Add(matchWindow)); err != nil {
			return fmt.Errorf("clear interpolations near the old time: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// trackPoint is the item's own position, which its track point at taken_at records.
type trackPoint struct {
	source   string
	lat, lng float64
}

// moveTrackPoint moves the item's point p from one time to another, for one of ownerID's media, and
// reports whether there was a point to move. Nothing is created when there is no source point at
// from (say, an item ingested before EXIF points existed). The new point holds the item's own
// position rather than a copy of the old one: one point per second may hold another item's
// position, and copying that would put it at the wrong time. It yields to a point already at to,
// as ingest's ON CONFLICT does.
//
// The old point stays while another item that may own it is still at from: one of ownerID's media
// whose gps_source is in owners (the caller has already moved its own item). An item without such
// a location never owns the point, so one that merely shares the second does not keep a stale point
// alive. A point that stays is given the position of a remaining item with gps_source p.source, as
// it may have held the moved item's.
func moveTrackPoint(ctx context.Context, tx pgx.Tx, ownerID string, p trackPoint, from, to time.Time, owners []string) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM gps_tracks WHERE owner_id = $1 AND source = $2 AND recorded_at = $3)`,
		ownerID, p.source, from).Scan(&exists); err != nil {
		return false, fmt.Errorf("find old %s track point: %w", p.source, err)
	}
	if !exists {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
		VALUES ($1, $2, $3, ST_SetSRID(ST_MakePoint($4, $5), 4326))
		ON CONFLICT (owner_id, source, recorded_at) DO NOTHING`,
		ownerID, p.source, to, p.lng, p.lat); err != nil {
		return false, fmt.Errorf("insert %s track point: %w", p.source, err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM gps_tracks
		WHERE owner_id = $1 AND source = $2 AND recorded_at = $3
		  AND NOT EXISTS (
		      SELECT 1 FROM media
		      WHERE owner_id = $1 AND taken_at = $3 AND gps_source = ANY($4::text[]))`,
		ownerID, p.source, from, owners); err != nil {
		return false, fmt.Errorf("delete old %s track point: %w", p.source, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE gps_tracks t
		SET location = m.location
		FROM (SELECT location FROM media
		      WHERE owner_id = $1 AND taken_at = $3 AND gps_source = $2 AND location IS NOT NULL
		      ORDER BY id LIMIT 1) m
		WHERE t.owner_id = $1 AND t.source = $2 AND t.recorded_at = $3`,
		ownerID, p.source, from); err != nil {
		return false, fmt.Errorf("hand over old %s track point: %w", p.source, err)
	}
	return true, nil
}
