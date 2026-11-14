package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// retimeStorage is what RunRetime needs from object storage: one ranged GET per item, so no
// presigned URL is ever made, let alone logged.
type retimeStorage interface {
	ReadHead(ctx context.Context, key string, n int64) ([]byte, error)
}

// RetimeDeps holds what RunRetime needs; the operator `server retime` command builds it.
type RetimeDeps struct {
	pool         *pgxpool.Pool
	storage      retimeStorage
	cameraZone   ingest.CameraZone // as ingest reads files; RunRetime adds the owner's clock rules
	matchWindow  time.Duration
	tripGapHours int
}

func NewRetimeDeps(pool *pgxpool.Pool, s *storage.Client, matchWindow time.Duration, gapHours int, cameraZone ingest.CameraZone) RetimeDeps {
	return RetimeDeps{pool: pool, storage: s, cameraZone: cameraZone, matchWindow: matchWindow, tripGapHours: gapHours}
}

// RetimeChange is one media item whose taken_at RunRetime moves (in a dry run: would move).
type RetimeChange struct {
	MediaID  string
	Filename string
	Old, New time.Time
	// Zone is where New's offset came from: ingest.ZoneGPS, ZoneClockRule, ZoneOffset or ZoneCamera.
	Zone string
	// ClockRule is the owner's rule that gave the offset, set exactly when Zone is ZoneClockRule.
	ClockRule *ingest.ClockRule
	GPSSource string // the item's gps_source before the change; "" without a location
}

// RetimeResult counts what RunRetime found among the owner's media with a taken_at.
type RetimeResult struct {
	Checked   int
	Retimed   int // taken_at moved (in a dry run: would move)
	Unchanged int // already what ingest computes today, within retimeTolerance
	NotExif   int // no EXIF capture time to read: taken_at came from the video container or the file time
	Mismatch  int // an EXIF time the stored taken_at is not, at any offset: left alone, logged
	Failed    int // object unreadable or the update failed: logged
}

// retimeTolerance is how far a stored taken_at may sit from what ingest computes today and still
// count as the same reading. A Panasonic MP4's container time sits a second off the wall clock in its
// EXIF, so a video stored from its container is that second off the EXIF time read at some offset,
// and one stored from its EXIF can be that second off too.
const retimeTolerance = 5 * time.Second

// maxUTCOffset is the largest distance of a UTC offset in real use from UTC (UTC+14:00).
const maxUTCOffset = 14 * time.Hour

// RunRetime re-reads the EXIF capture time of ownerID's media and moves taken_at to what ingest
// computes today: ingest.ExtractExif, or ingest.VideoExif for a video, with the camera zone, the
// camera rules and the owner's own clock rules (camera_clock_rules, loaded once). That repairs rows
// stored before a rule existed: a camera whose clock showed a fixed wrong offset for a period
// (a Panasonic left on summer time all year), a Panasonic video dated from its container time, and
// the rows stored before CAMERA_TIMEZONE or before ingest read OffsetTimeOriginal.
//
// Only rows dated from EXIF change. A photo is read from the same mediaHeadSize bytes ingest read; a
// video only when a camera rule dates some videos from their EXIF (otherwise nothing is read for it),
// and never with ffprobe. A row is left alone when that head has no EXIF capture time, when the
// stored taken_at is already the new time (within retimeTolerance), or when the stored taken_at is
// not the EXIF wall clock read at any UTC offset (within retimeTolerance): then it did not come from
// this EXIF, and it is logged as a mismatch.
//
// With apply, each change is one transaction that also:
//   - clears an interpolated location, which was matched at the old time;
//   - moves the track point ingest or a manual location put at the old time: the 'exif' point of
//     a photo with an EXIF position, and the 'manual' point of an item placed by hand. See
//     moveTrackPoint;
//   - when a point moved, clears the interpolated locations within the match window of its old
//     time, since they may rest on it. Items that are not retimed (videos) are among them;
//   - records the new derivation in capture_evidence (retimedEvidence), with by "retime" and the
//     old time in retimed_from, and fills a missing camera_model from the EXIF.
//
// Afterwards the GPS matcher and trip detection run for ownerID, as after a scan. They run on every
// apply, even with nothing to change, so that a re-run completes an interrupted one. Without apply
// nothing is written. report is called for each change, in taken_at order. On cancellation the loop
// stops after the current item, the matcher and trip detection are skipped, and ctx.Err() is
// returned.
//
// GPS stays per owner: every query here filters on ownerID literally, like RunGPSMatcher, and must
// never use readableMediaCond: media shared with ownerID through an album are not ownerID's rows
// (TestSharing_RetimeIgnoresSharedMedia). The clock rules are ownerID's own.
func RunRetime(ctx context.Context, deps RetimeDeps, ownerID string, apply bool, report func(RetimeChange)) (RetimeResult, error) {
	slog.Info("retime: started", "owner_id", ownerID, "apply", apply)
	var res RetimeResult

	rules, err := loadClockRules(ctx, deps.pool, ownerID)
	if err != nil {
		return res, err
	}
	zone := deps.cameraZone
	zone.Clock = rules
	slog.Info("retime: clock rules", "owner_id", ownerID, "rules", len(rules))

	items, err := retimeCandidates(ctx, deps.pool, ownerID)
	if err != nil {
		return res, fmt.Errorf("list media: %w", err)
	}

	for _, m := range items {
		if err := ctx.Err(); err != nil {
			break
		}
		res.Checked++
		exifData, found, err := rereadExifTime(ctx, deps.storage, m, zone)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.Error("retime: read object failed", "id", m.id, "filename", m.filename, "err", err)
			res.Failed++
			continue
		}
		if !found {
			res.NotExif++
			continue
		}
		switch {
		case exifData.TakenAt.Sub(m.takenAt).Abs() <= retimeTolerance:
			res.Unchanged++
			continue
		case !sameWallClock(m.takenAt, *exifData.TakenAt):
			slog.Warn("retime: stored taken_at is not the EXIF time at any offset, left alone",
				"id", m.id, "filename", m.filename, "taken_at", m.takenAt.UTC(),
				"exif_time", wallClock(*exifData.TakenAt).Format(time.DateTime))
			res.Mismatch++
			continue
		}

		change := RetimeChange{
			MediaID: m.id, Filename: m.filename, Old: m.takenAt, New: *exifData.TakenAt,
			Zone: exifData.TakenAtZone, ClockRule: exifData.ClockRule, GPSSource: m.gpsSource,
		}
		if apply {
			if err := applyRetime(ctx, deps.pool, ownerID, change, exifData, zone, deps.matchWindow); err != nil {
				if ctx.Err() != nil {
					break
				}
				slog.Error("retime: update failed", "id", m.id, "filename", m.filename, "err", err)
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
	id, filename, storageKey, mediaType, gpsSource string
	takenAt                                        time.Time
}

// retimeCandidates lists ownerID's media with a taken_at (a row without one has no EXIF time), read
// up front so no connection is held while objects are fetched.
func retimeCandidates(ctx context.Context, pool *pgxpool.Pool, ownerID string) ([]retimeCandidate, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, filename, storage_key, media_type, taken_at, coalesce(gps_source, '')
		FROM media
		WHERE owner_id = $1 AND taken_at IS NOT NULL
		ORDER BY taken_at, id`, ownerID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (retimeCandidate, error) {
		var c retimeCandidate
		err := row.Scan(&c.id, &c.filename, &c.storageKey, &c.mediaType, &c.takenAt, &c.gpsSource)
		return c, err
	})
}

// rereadExifTime reads the EXIF capture time of one stored item as ingest reads it today, with
// zone. found is false when there is none to read, or when its year is one JSON cannot carry, which
// ingest would not store either (jsonCanCarry). A photo's object is the original (photos are never
// transcoded), so its head is what ingest read. A video is read only when a camera rule dates some
// videos from their EXIF, as ingest does; otherwise it was dated from its container or its file time
// and nothing is fetched. The decode takes one of the exifDecodeSlots, like ingest's.
func rereadExifTime(ctx context.Context, store retimeStorage, m retimeCandidate, zone ingest.CameraZone) (e ingest.ExifData, found bool, err error) {
	video := isVideoMedia(m.mediaType)
	if video && !zone.Rules.DatesVideosFromExif() {
		return ingest.ExifData{}, false, nil
	}
	head, err := store.ReadHead(ctx, m.storageKey, mediaHeadSize)
	if err != nil {
		return ingest.ExifData{}, false, err
	}
	if video {
		v, err := videoExifInSlot(ctx, head, zone)
		if err != nil {
			return ingest.ExifData{}, false, err
		}
		return v.exif, v.ok && usableTime(v.exif.TakenAt), nil
	}
	e, err = extractExifInSlot(ctx, head, zone)
	if err != nil {
		return ingest.ExifData{}, false, err
	}
	return e, usableTime(e.TakenAt), nil
}

// usableTime reports whether t is a capture time ingest would store.
func usableTime(t *time.Time) bool {
	return t != nil && jsonCanCarry(*t)
}

// wallClock returns t's clock reading in its own location, as a time in UTC.
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// sameWallClock reports whether stored is now's wall clock read at some UTC offset (a whole quarter
// hour, at most 14 hours), within retimeTolerance. That is what every earlier ingest stored for an
// EXIF time, whatever zone or rule it used, and what it stored for a Panasonic video's container
// time, which is the camera's clock marked UTC and a second off its EXIF. A file time or a value
// set by hand almost never is.
func sameWallClock(stored, now time.Time) bool {
	d := wallClock(now).Sub(stored)
	if d.Abs() > maxUTCOffset+retimeTolerance {
		return false
	}
	const quarter = 15 * time.Minute
	off := (d % quarter).Abs()
	return min(off, quarter-off) <= retimeTolerance
}

func applyRetime(ctx context.Context, pool *pgxpool.Pool, ownerID string, c RetimeChange, exifData ingest.ExifData, zone ingest.CameraZone, matchWindow time.Duration) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var gpsSource string
	var lat, lng *float64
	var oldEvidence []byte
	err = tx.QueryRow(ctx, `
		SELECT coalesce(gps_source, ''), ST_Y(location::geometry), ST_X(location::geometry), capture_evidence
		FROM media
		WHERE id = $1 AND owner_id = $2 AND taken_at = $3
		FOR UPDATE`, c.MediaID, ownerID, c.Old).Scan(&gpsSource, &lat, &lng, &oldEvidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("the row changed since it was read; re-run to pick it up")
	}
	if err != nil {
		return err
	}
	evidence, err := retimedEvidence(oldEvidence, exifData, zone, c.Old)
	if err != nil {
		return fmt.Errorf("capture evidence: %w", err)
	}

	// SET expressions see the row as it was, so every CASE tests the old gps_source.
	if _, err := tx.Exec(ctx, `
		UPDATE media
		SET taken_at         = $3,
		    location         = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE location END,
		    gps_confidence   = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE gps_confidence END,
		    gps_source       = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE gps_source END,
		    capture_evidence = $4,
		    camera_model     = coalesce(camera_model, $5)
		WHERE id = $1 AND owner_id = $2`,
		c.MediaID, ownerID, c.New, evidence, nilIfEmpty(exifData.CameraModel)); err != nil {
		return fmt.Errorf("update media: %w", err)
	}

	// Ingest stored the EXIF position as an 'exif' point at taken_at (insertExifGPSTrack). Its owner
	// may since have placed the photo by hand, which leaves gps_source 'manual'. A video read through
	// VideoExif has no position, as ingest makes no point for it.
	movedExif, movedManual := false, false
	if exifData.Lat != nil && exifData.Lng != nil {
		p := ownTrackPoint{"exif", *exifData.Lat, *exifData.Lng}
		if movedExif, err = moveTrackPoint(ctx, tx, ownerID, p, c.Old, c.New); err != nil {
			return err
		}
	}
	// A manual location put a 'manual' point at taken_at (MediaHandler.UpdateLocation).
	if gpsSource == "manual" && lat != nil && lng != nil {
		p := ownTrackPoint{"manual", *lat, *lng}
		if movedManual, err = moveTrackPoint(ctx, tx, ownerID, p, c.Old, c.New); err != nil {
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

// captureByRetime is captureEvidence.By once server retime has moved taken_at.
const captureByRetime = "retime"

// retimeReplacedKeys are the capture_evidence keys retimedEvidence takes from the re-read: what the
// EXIF says and how today's rules read it. Every other key (v, and the container and file inputs:
// creation_time, file_mtime, duration_s, encoder, codec, transcoded) stays as ingest recorded it,
// because the re-read has only the head of the stored object and cannot know them.
var retimeReplacedKeys = map[string]bool{
	"by": true, "retimed_from": true,
	"src": true, "zone": true, "wall": true, "gps_utc": true, "wall_minus_gps_s": true,
	"offset_tag": true, "offset_ignored": true, "offset_used_min": true,
	"camera_zone": true, "camera_zone_utc_fallback": true,
	"make": true, "model": true, "exif_partial": true, "clock_rule": true,
}

// retimedEvidence returns the capture_evidence of a row retimed from e: old, when it is a JSON
// object, with the retimeReplacedKeys taken from e as read with zone, by "retime" and retimed_from
// the old taken_at. Without an old object (rows ingested before migration 10 have NULL) it is a whole
// evidence object whose container and file inputs are null, since nothing recorded them, and whose
// transcoded is false: a retimed row was dated from the EXIF in its stored object, which a re-encode
// would have dropped.
func retimedEvidence(old []byte, e ingest.ExifData, zone ingest.CameraZone, oldTakenAt time.Time) ([]byte, error) {
	fresh := newCaptureEvidence(e, ingest.VideoProbeResult{}, nil, zone)
	fresh.By = captureByRetime
	fresh.RetimedFrom = rfc3339UTC(&oldTakenAt)
	b, err := json.Marshal(fresh)
	if err != nil {
		return nil, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(b, &merged); err != nil {
		return nil, err
	}
	var prev map[string]json.RawMessage
	if len(old) > 0 && json.Unmarshal(old, &prev) == nil {
		for k, v := range prev {
			if !retimeReplacedKeys[k] {
				merged[k] = v
			}
		}
	}
	return json.Marshal(merged)
}

// ownTrackPoint is the item's own position, which its track point at taken_at records.
type ownTrackPoint struct {
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
// The old point stays while another of ownerID's media at from may own it (the caller has already
// moved its own item): see trackPointOwnersSQL. Any other item that merely shares the second does
// not keep a stale point alive. A point that stays is given the position of an item that keeps it,
// as it may have held the moved item's.
func moveTrackPoint(ctx context.Context, tx pgx.Tx, ownerID string, p ownTrackPoint, from, to time.Time) (bool, error) {
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
		  AND NOT EXISTS (`+trackPointOwnersSQL+`)`,
		ownerID, p.source, from); err != nil {
		return false, fmt.Errorf("delete old %s track point: %w", p.source, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE gps_tracks t
		SET location = m.location
		FROM (`+trackPointOwnersSQL+`
		      ORDER BY id LIMIT 1) m
		WHERE t.owner_id = $1 AND t.source = $2 AND t.recorded_at = $3`,
		ownerID, p.source, from); err != nil {
		return false, fmt.Errorf("hand over old %s track point: %w", p.source, err)
	}
	return true, nil
}

// trackPointOwnersSQL selects the positions of ownerID's ($1) media at the second $3 that may own
// its $2 track point: the items that would have made it.
//   - A 'manual' point: any item placed by hand there (MediaHandler.UpdateLocation), a video too.
//   - An 'exif' point: a photo located from its EXIF there (insertExifGPSTrack). A video located from
//     its container's location tag is gps_source 'exif' as well, but ingest makes no point from a
//     tag, so a video must neither keep nor fill one. An item placed by hand has its own 'manual'
//     point; whatever EXIF position it has is the one its owner overrode.
const trackPointOwnersSQL = `
		SELECT location FROM media
		WHERE owner_id = $1 AND taken_at = $3 AND gps_source = $2 AND location IS NOT NULL
		  AND ($2 <> 'exif' OR media_type IN ('photo', 'photo_360'))`
