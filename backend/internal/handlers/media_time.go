package handlers

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// What the two capture time routes take.
const (
	maxTakenAtBodyBytes   = 16 << 10
	maxShiftTimeBodyBytes = 1 << 20
	maxShiftMediaIDs      = 5000
	// maxShiftSeconds caps one shift at 100 years of 365.25 days: beyond any camera clock left
	// wrong, and far from what a time.Duration can hold.
	maxShiftSeconds = 3_155_760_000
	// The years a wall clock set by hand may name, and a stored time may land in: jsonCanCarry's.
	minTakenAtYear = jsonFirstYear + 1
	maxTakenAtYear = jsonLastYear - 1
)

// takenAtWallLayout is how PATCH /api/media/{id}/taken-at takes the wall clock: seconds required,
// no fraction and no zone.
const takenAtWallLayout = "2006-01-02T15:04:05"

// timeEditFollowupTimeout bounds the matcher and trip detection after a capture time change. They
// run on a context the request's end cannot cancel, so a closed tab cannot stop them half-way.
const timeEditFollowupTimeout = 2 * time.Minute

// The kinds of change time_edit records.
const (
	timeEditSet   = "set"
	timeEditShift = "shift"
)

// timeEdit is media.time_edit: the owner overrode taken_at through PATCH /api/media/{id}/taken-at
// (kind "set") or POST /api/media/shift-time (kind "shift"). capture_evidence is left as it was: it
// says how the file's time was read, and this says that the owner changed it afterwards. Every key
// is always present, null where it does not apply. Times are RFC 3339 in UTC.
type timeEdit struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	At   string `json:"at"` // when the change was made, by the server's clock
	// From is taken_at before this change, null when the item had none.
	From *string `json:"from"`
	// Original is taken_at before the first change through these routes: carried over unchanged
	// from the time_edit already stored (null there too when the first change dated an undated
	// item), else From.
	Original *string `json:"original"`
	// DeltaS is the new taken_at minus From, in seconds; null when From is.
	DeltaS *float64 `json:"delta_s"`
	// Wall, Zone and OffsetMin are a set's own: the wall clock as entered, the IANA zone it named
	// (null for a fixed offset), and the UTC offset in minutes the wall clock was read at.
	Wall      *string `json:"wall"`
	Zone      *string `json:"zone"`
	OffsetMin *int    `json:"offset_min"`
}

const timeEditVersion = 1

// newTimeEdit records a change of kind from from (nil for an undated item) to to, made at now.
// prev is the time_edit stored on the row, nil when it was never edited; its original is kept.
func newTimeEdit(kind string, now time.Time, from *time.Time, to time.Time, prev []byte) timeEdit {
	e := timeEdit{
		V: timeEditVersion, Kind: kind, At: now.UTC().Format(time.RFC3339Nano), From: rfc3339UTC(from),
	}
	e.Original = e.From
	if prev != nil {
		var stored struct {
			Original *string `json:"original"`
		}
		if err := json.Unmarshal(prev, &stored); err != nil {
			// Only these routes write time_edit, so this is a row changed by hand in SQL. The time
			// before this change is the best original left.
			slog.Warn("media: stored time_edit unreadable, original taken from this change", "err", err)
		} else {
			e.Original = stored.Original
		}
	}
	if from != nil {
		// Not to.Sub(*from): a Duration saturates past 292 years, and a set may cross millennia.
		d := float64(to.Unix()-from.Unix()) + float64(to.Nanosecond()-from.Nanosecond())/1e9
		e.DeltaS = &d
	}
	return e
}

// takenAtInput is the body of PATCH /api/media/{id}/taken-at: a wall clock and exactly one of an
// IANA zone or a UTC offset in minutes to read it in.
type takenAtInput struct {
	Wall         *string `json:"wall"`
	Zone         *string `json:"zone"`
	UTCOffsetMin *int    `json:"utc_offset_min"`
}

// takenAtValue is a validated takenAtInput: the instant, and what time_edit records of it.
type takenAtValue struct {
	at        time.Time
	wall      string
	zone      *string // the IANA name; nil for a fixed offset
	offsetMin int     // the UTC offset the wall clock was read at
}

// validateTakenAt resolves in to the instant it names, or says why it cannot.
func validateTakenAt(in takenAtInput) (takenAtValue, error) {
	if in.Wall == nil {
		return takenAtValue{}, errors.New("wall is required")
	}
	if (in.Zone == nil) == (in.UTCOffsetMin == nil) {
		return takenAtValue{}, errors.New("give exactly one of zone and utc_offset_min")
	}
	wall, err := parseTakenAtWall(*in.Wall)
	if err != nil {
		return takenAtValue{}, err
	}
	v := takenAtValue{wall: *in.Wall}
	if in.Zone != nil {
		loc, err := ingest.LoadZone(*in.Zone)
		if err != nil {
			return takenAtValue{}, fmt.Errorf("zone: %w", err)
		}
		at, ok := wallInZone(wall, loc)
		if !ok {
			return takenAtValue{}, fmt.Errorf("wall: %s does not exist in %s: the clocks skipped it", *in.Wall, *in.Zone)
		}
		// A zone's local mean time can be off by seconds; time_edit keeps whole minutes.
		_, off := at.In(loc).Zone()
		v.at, v.zone, v.offsetMin = at, in.Zone, off/60
	} else {
		// The offsets a camera clock rule may name: those in real use, in quarter hours.
		off := *in.UTCOffsetMin
		if off < minClockRuleOffsetMin || off > maxClockRuleOffsetMin || off%clockRuleOffsetStepMin != 0 {
			return takenAtValue{}, fmt.Errorf("utc_offset_min must be a multiple of %d from %d to %d",
				clockRuleOffsetStepMin, minClockRuleOffsetMin, maxClockRuleOffsetMin)
		}
		v.at, v.offsetMin = wall.Add(-time.Duration(off)*time.Minute), off
	}
	if !jsonCanCarry(v.at) {
		return takenAtValue{}, fmt.Errorf("wall: %s is outside the years %d-%d in UTC", *in.Wall, minTakenAtYear, maxTakenAtYear)
	}
	return v, nil
}

// parseTakenAtWall reads a wall clock written exactly takenAtWallLayout, a real date in the years
// minTakenAtYear-maxTakenAtYear, as that reading in UTC. time.Parse also takes a fraction after the
// seconds, so the reading must format back to what was sent.
func parseTakenAtWall(raw string) (time.Time, error) {
	wall, err := time.Parse(takenAtWallLayout, raw)
	if err != nil || wall.Format(takenAtWallLayout) != raw || wall.Year() < minTakenAtYear || wall.Year() > maxTakenAtYear {
		return time.Time{}, fmt.Errorf("wall must be a real date and time written YYYY-MM-DDTHH:MM:SS in the years %d-%d",
			minTakenAtYear, maxTakenAtYear)
	}
	return wall, nil
}

// wallInZone returns the instant at which loc's clocks read wall (a reading given in UTC). A reading
// the zone skipped, when its clocks went forward, has none. One it showed twice, when they went
// back, is the earlier instant: time.Date leaves both choices open. The candidates are wall read at
// each offset loc uses within two days of it, and one is an answer when loc's clock at that instant
// reads wall. The process's own zone plays no part.
func wallInZone(wall time.Time, loc *time.Location) (time.Time, bool) {
	var found time.Time
	ok := false
	for _, off := range zoneOffsetsNear(wall, loc) {
		at := wall.Add(-time.Duration(off) * time.Second)
		if wallClock(at.In(loc)).Equal(wall) && (!ok || at.Before(found)) {
			found, ok = at, true
		}
	}
	return found, ok
}

// zoneOffsetsNear lists the distinct UTC offsets, in seconds, that loc uses from two days before t
// to two days after it, following its transitions.
func zoneOffsetsNear(t time.Time, loc *time.Location) []int {
	const span = 48 * time.Hour
	end := t.Add(span)
	var offsets []int
	for at := t.Add(-span); !at.After(end); {
		in := at.In(loc)
		if _, off := in.Zone(); !slices.Contains(offsets, off) {
			offsets = append(offsets, off)
		}
		_, next := in.ZoneBounds()
		if next.IsZero() || !next.After(at) { // the zone keeps this offset for good
			break
		}
		at = next
	}
	return offsets
}

// shiftTimeInput is the body of POST /api/media/shift-time. delta_seconds is kept raw so that only
// a JSON integer passes: a fraction, an exponent or a number in a string is refused, not rounded.
type shiftTimeInput struct {
	MediaIDs     []string        `json:"media_ids"`
	DeltaSeconds json.RawMessage `json:"delta_seconds"`
}

// shiftTimeValue is a validated shiftTimeInput.
type shiftTimeValue struct {
	ids   []string // canonical, without duplicates
	delta time.Duration
}

func validateShiftTime(in shiftTimeInput) (shiftTimeValue, error) {
	if n := len(in.MediaIDs); n < 1 || n > maxShiftMediaIDs {
		return shiftTimeValue{}, fmt.Errorf("media_ids must hold 1-%d IDs", maxShiftMediaIDs)
	}
	ids := make([]string, 0, len(in.MediaIDs))
	seen := make(map[string]bool, len(in.MediaIDs))
	for _, raw := range in.MediaIDs {
		id, ok := auth.CanonicalUUID(raw)
		if !ok {
			return shiftTimeValue{}, errors.New("invalid media_ids")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(in.DeltaSeconds) == 0 || string(in.DeltaSeconds) == "null" {
		return shiftTimeValue{}, errors.New("delta_seconds is required")
	}
	secs, err := strconv.ParseInt(string(in.DeltaSeconds), 10, 64)
	switch {
	case err != nil:
		return shiftTimeValue{}, errors.New("delta_seconds must be a whole number of seconds")
	case secs == 0:
		return shiftTimeValue{}, errors.New("delta_seconds must not be 0")
	case secs < -maxShiftSeconds || secs > maxShiftSeconds:
		return shiftTimeValue{}, fmt.Errorf("delta_seconds must be at most %d (100 years) either way", maxShiftSeconds)
	}
	return shiftTimeValue{ids: ids, delta: time.Duration(secs) * time.Second}, nil
}

// decodeStrictJSON decodes the body into v: one JSON object of at most limit bytes, with no unknown
// key and nothing after it. It writes 413 for a larger body and 400 with invalid for anything else
// it refuses, and reports whether v was decoded.
func decodeStrictJSON(w http.ResponseWriter, r *http.Request, limit int64, v any, invalid string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, next := dec.Token(); next != io.EOF {
			err = errors.Join(errors.New("data after the JSON object"), next)
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, invalid, http.StatusBadRequest)
		return false
	}
	return true
}

// decodeTakenAt reads and validates the body of PATCH /api/media/{id}/taken-at, or writes 400 with
// a short reason (413 for a body over 16 KiB).
func decodeTakenAt(w http.ResponseWriter, r *http.Request) (takenAtValue, bool) {
	var in takenAtInput
	if !decodeStrictJSON(w, r, maxTakenAtBodyBytes, &in,
		"invalid body: want a JSON object with wall and either zone or utc_offset_min") {
		return takenAtValue{}, false
	}
	v, err := validateTakenAt(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return takenAtValue{}, false
	}
	return v, true
}

// decodeShiftTime reads and validates the body of POST /api/media/shift-time, or writes 400 with a
// short reason (413 for a body over 1 MiB).
func decodeShiftTime(w http.ResponseWriter, r *http.Request) (shiftTimeValue, bool) {
	var in shiftTimeInput
	if !decodeStrictJSON(w, r, maxShiftTimeBodyBytes, &in,
		"invalid body: want a JSON object with media_ids and delta_seconds") {
		return shiftTimeValue{}, false
	}
	v, err := validateShiftTime(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return shiftTimeValue{}, false
	}
	return v, true
}

// timeEditRow is one of the owner's media as a capture time change reads it, under FOR UPDATE.
type timeEditRow struct {
	id        string
	takenAt   *time.Time
	mediaType string
	gpsSource string   // "" without one
	lat, lng  *float64 // the row's own location
	timeEdit  []byte   // the stored time_edit; nil when NULL
	tripID    *string
}

// timeEditRowColumns are what scanTimeEditRow reads, from media unaliased.
const timeEditRowColumns = `id::text, taken_at, media_type, coalesce(gps_source, ''),
	ST_Y(location::geometry), ST_X(location::geometry), time_edit, trip_id::text`

func scanTimeEditRow(row pgx.Row) (timeEditRow, error) {
	var m timeEditRow
	err := row.Scan(&m.id, &m.takenAt, &m.mediaType, &m.gpsSource, &m.lat, &m.lng, &m.timeEdit, &m.tripID)
	return m, err
}

func isPhotoMedia(mediaType string) bool {
	return mediaType == "photo" || mediaType == "photo_360"
}

// applyTimeChange moves one of ownerID's media, locked in tx, from row.takenAt to newTime and records
// edit in time_edit. In the same transaction it:
//   - clears an interpolated location, which was matched at the old time;
//   - moves the item's own track points with it: the 'exif' point ingest made for a photo located
//     from its EXIF, and the 'manual' point of an item placed by hand (moveTrackPoint). A photo
//     placed by hand takes along the 'exif' point ingest made before it was placed, with that
//     point's own position (moveUnownedTrackPoint), which would otherwise be left with no item;
//   - for an item that had no time, makes those points at newTime, as ingest or a manual location
//     would have;
//   - when a point moved, clears the interpolations within matchWindow of its old time, since they
//     may rest on it. timeEditFollowup places them again, or they stay empty when nothing brackets
//     them.
//
// capture_evidence stays as it was: it says how the file's time was read. Every statement filters on
// ownerID literally and never uses readableMediaCond: a shared item is not the caller's to move, and
// neither are another user's track points.
func applyTimeChange(ctx context.Context, tx pgx.Tx, ownerID string, row timeEditRow, newTime time.Time, edit timeEdit, matchWindow time.Duration) error {
	editJSON, err := json.Marshal(edit)
	if err != nil {
		return fmt.Errorf("time_edit: %w", err)
	}
	// SET expressions see the row as it was, so every CASE tests the old gps_source.
	if _, err := tx.Exec(ctx, `
		UPDATE media
		SET taken_at       = $3,
		    time_edit      = $4,
		    location       = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE location END,
		    gps_confidence = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE gps_confidence END,
		    gps_source     = CASE WHEN gps_source = 'interpolated' THEN NULL ELSE gps_source END
		WHERE id = $1 AND owner_id = $2`,
		row.id, ownerID, newTime, editJSON); err != nil {
		return fmt.Errorf("update media: %w", err)
	}

	photo := isPhotoMedia(row.mediaType)
	located := row.lat != nil && row.lng != nil
	// A video located from its container's tag is gps_source 'exif' too, but ingest makes no point
	// for it (trackPointOwnersSQL).
	exifPoint := photo && row.gpsSource == "exif" && located
	manualPoint := row.gpsSource == "manual" && located

	if row.takenAt == nil {
		return createOwnTrackPoints(ctx, tx, ownerID, row, newTime, exifPoint, manualPoint)
	}
	old := *row.takenAt
	moved := false
	if exifPoint {
		m, err := moveTrackPoint(ctx, tx, ownerID, ownTrackPoint{"exif", *row.lat, *row.lng}, old, newTime)
		if err != nil {
			return err
		}
		moved = moved || m
	}
	if manualPoint {
		m, err := moveTrackPoint(ctx, tx, ownerID, ownTrackPoint{"manual", *row.lat, *row.lng}, old, newTime)
		if err != nil {
			return err
		}
		moved = moved || m
	}
	if photo && row.gpsSource == "manual" {
		m, err := moveUnownedTrackPoint(ctx, tx, ownerID, old, newTime)
		if err != nil {
			return err
		}
		moved = moved || m
	}

	// The matcher keeps a location it cannot recompute, so an interpolation resting on a point
	// that left is cleared here rather than left to it.
	if moved {
		if _, err := tx.Exec(ctx, `
			UPDATE media
			SET location = NULL, gps_confidence = NULL, gps_source = NULL
			WHERE owner_id = $1 AND gps_source = 'interpolated' AND taken_at BETWEEN $2 AND $3`,
			ownerID, old.Add(-matchWindow), old.Add(matchWindow)); err != nil {
			return fmt.Errorf("clear interpolations near the old time: %w", err)
		}
	}
	return nil
}

// createOwnTrackPoints makes the points of an item that had no time and now has one at at: ingest
// makes an 'exif' point only for a dated photo (insertExifGPSTrack), and a manual location a
// 'manual' point only for a dated item (MediaHandler.UpdateLocation). A point already at that second
// is kept, as ingest's ON CONFLICT does.
func createOwnTrackPoints(ctx context.Context, tx pgx.Tx, ownerID string, row timeEditRow, at time.Time, exifPoint, manualPoint bool) error {
	for _, p := range []struct {
		source string
		want   bool
	}{{"exif", exifPoint}, {"manual", manualPoint}} {
		if !p.want {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
			VALUES ($1, $2, $3, ST_SetSRID(ST_MakePoint($4, $5), 4326))
			ON CONFLICT (owner_id, source, recorded_at) DO NOTHING`,
			ownerID, p.source, at, *row.lng, *row.lat); err != nil {
			return fmt.Errorf("insert %s track point: %w", p.source, err)
		}
	}
	return nil
}

// otherPhotoAtSQL finds another of ownerID's ($1) photos at the second $3 that may have made the
// 'exif' point there. Only a photo located from its EXIF makes one (insertExifGPSTrack), and such a
// photo stays located: from its EXIF, or as 'manual' once its owner places it by hand. An unlocated
// or interpolated photo never made it, so it does not keep the point (orphanPointsSQL agrees).
const otherPhotoAtSQL = `
		SELECT 1 FROM media
		WHERE owner_id = $1 AND taken_at = $3 AND media_type IN ('photo', 'photo_360')
		  AND gps_source IN ('exif', 'manual') AND location IS NOT NULL`

// moveUnownedTrackPoint moves ownerID's 'exif' point from one time to another with its own stored
// position, and reports whether it moved. It is for the point that the moved item's row no longer
// describes: the one ingest made for a photo its owner has since placed by hand, whose EXIF position
// is on no row. The point stays while another of ownerID's photos that may have made it sits at
// from (otherPhotoAtSQL; the caller has already moved its own item), and it yields to a point
// already at to, as moveTrackPoint does.
//
// One case is left: when the moved photo never had an EXIF position and a photo since deleted left
// its point at that second (a delete leaves the track points), the point moves with the photo.
// Telling the two apart takes the photo's EXIF position, which only a re-read of its file gives, and
// that is accepted.
func moveUnownedTrackPoint(ctx context.Context, tx pgx.Tx, ownerID string, from, to time.Time) (bool, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
		SELECT owner_id, source, $4::timestamptz, location FROM gps_tracks
		WHERE owner_id = $1 AND source = $2 AND recorded_at = $3
		  AND NOT EXISTS (`+otherPhotoAtSQL+`)
		ON CONFLICT (owner_id, source, recorded_at) DO NOTHING`,
		ownerID, "exif", from, to); err != nil {
		return false, fmt.Errorf("insert moved exif track point: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM gps_tracks
		WHERE owner_id = $1 AND source = $2 AND recorded_at = $3
		  AND NOT EXISTS (`+otherPhotoAtSQL+`)`,
		ownerID, "exif", from)
	if err != nil {
		return false, fmt.Errorf("delete old exif track point: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// timeEditFollowup places the rows near the changed instants again (RunGPSMatcherAround: the old
// time a point left, the new time it arrived at) and rebuilds ownerID's trips, as after an upload.
// It reports whether both ran. The change is committed by then, so a failure is logged and told to
// the client, never a 500 that would invite a retry: a second shift moves the items again.
func (h *MediaHandler) timeEditFollowup(ctx context.Context, ownerID string, around []time.Time) bool {
	if err := h.runMatcherAround(ctx, h.matcherPool, ownerID, h.matchWindow, around); err != nil {
		slog.Error("media: GPS matcher failed after a capture time change", "owner", ownerID, "err", err)
		return false
	}
	if err := h.runTripDetect(ctx, h.db, ownerID, h.tripGapHours); err != nil {
		slog.Error("media: trip detect failed after a capture time change", "owner", ownerID, "err", err)
		return false
	}
	return true
}

// timeEditFollowupContext is ctx without its cancellation, bounded by timeEditFollowupTimeout.
func timeEditFollowupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeEditFollowupTimeout)
}

func writeTimeEditJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("media: encode capture time response failed", "err", err)
	}
}

// timeChangeAttempts is how often a capture time change runs its transaction, from Begin, while
// Postgres aborts it as a deadlock victim (SQLSTATE 40P01). Both routes take the owner's lock
// first (lockOwnerRow), so two time changes of one owner never deadlock; trip detection after a
// concurrent upload (auto_detect_trips) does not take it, and rewrites the owner's media in its own
// order. Nothing is committed before Commit, so a retry applies the change once.
const timeChangeAttempts = 3

// pgDeadlockDetected is the SQLSTATE of a transaction Postgres aborted to break a deadlock.
const pgDeadlockDetected = "40P01"

func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgDeadlockDetected
}

// retryOnDeadlock runs attempt, again while it fails as a deadlock victim, at most
// timeChangeAttempts times in all, and returns the last attempt's result.
func retryOnDeadlock[T any](what string, attempt func() (T, error)) (T, error) {
	for n := 1; ; n++ {
		out, err := attempt()
		if err == nil || n >= timeChangeAttempts || !isDeadlock(err) {
			return out, err
		}
		slog.Warn("media: "+what+" aborted as a deadlock victim, running it again", "attempt", n, "err", err)
	}
}

// timeTxError is a failed step of a capture time change's transaction: what to log, with its
// attributes, and the cause.
type timeTxError struct {
	msg   string
	attrs []any
	err   error
}

func (e *timeTxError) Error() string { return e.msg + ": " + e.err.Error() }
func (e *timeTxError) Unwrap() error { return e.err }

func newTimeTxError(msg string, err error, attrs ...any) error {
	return &timeTxError{msg: msg, attrs: attrs, err: err}
}

// logTimeTxError logs a failed capture time change at Error level before its 500.
func logTimeTxError(err error, attrs ...any) {
	msg := "media: capture time change failed"
	var e *timeTxError
	if errors.As(err, &e) {
		msg, err = e.msg, e.err
		attrs = append(attrs, e.attrs...)
	}
	slog.Error(msg, append(attrs, "err", err)...)
}

// setTakenAtResponse answers PATCH /api/media/{id}/taken-at. TripID is the item's trip after trip
// detection.
type setTakenAtResponse struct {
	TakenAt        string  `json:"taken_at"`
	TripID         *string `json:"trip_id"`
	FollowupFailed bool    `json:"followup_failed"`
}

// setTakenAtResult is what one run of a set's transaction found and did.
type setTakenAtResult struct {
	found   bool        // the item is the caller's own
	row     timeEditRow // the item as it was locked, before the change
	changed bool        // the change was committed; false when the stored time already says it
}

// SetTakenAt sets the capture time of one of the caller's own items by hand: PATCH
// /api/media/{id}/taken-at with {"wall", "zone"} or {"wall", "utc_offset_min"}. A malformed ID is
// 404, then the body is checked (400, 413), then a foreign, shared or missing ID is 404 (a literal
// owner_id: an album member never moves the owner's item). A time in the stored time's second writes
// and runs nothing: the route takes whole seconds, and the dialog shows a stored fraction (a file
// date in milliseconds) without it. Otherwise the item moves (applyTimeChange), and after the commit
// the matcher and trip detection run (timeEditFollowup).
func (h *MediaHandler) SetTakenAt(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	v, ok := decodeTakenAt(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	res, err := retryOnDeadlock("set taken_at", func() (setTakenAtResult, error) {
		return h.setTakenAtTx(ctx, ownerID, id, v)
	})
	if err != nil {
		logTimeTxError(err, "id", id)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if !res.found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	row := res.row
	if !res.changed {
		writeTimeEditJSON(w, setTakenAtResponse{TakenAt: row.takenAt.UTC().Format(time.RFC3339Nano), TripID: row.tripID})
		return
	}
	slog.Info("media: capture time set", "id", id, "owner", ownerID, "taken_at", v.at.UTC(), "was_dated", row.takenAt != nil)

	resp := setTakenAtResponse{TakenAt: v.at.UTC().Format(time.RFC3339Nano)}
	around := []time.Time{v.at}
	if row.takenAt != nil {
		around = append(around, *row.takenAt)
	}
	fctx, cancel := timeEditFollowupContext(ctx)
	defer cancel()
	ok = h.timeEditFollowup(fctx, ownerID, around)
	err = h.db.QueryRow(fctx, `SELECT trip_id::text FROM media WHERE id = $1 AND owner_id = $2`, id, ownerID).
		Scan(&resp.TripID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { // no row: deleted meanwhile, in no trip
		slog.Error("media: trip lookup failed after a capture time change", "id", id, "err", err)
		ok = false
	}
	resp.FollowupFailed = !ok
	writeTimeEditJSON(w, resp)
}

// setTakenAtTx runs a set's transaction once: under the owner's lock, it locks the item and moves it
// to v unless it is already there.
func (h *MediaHandler) setTakenAtTx(ctx context.Context, ownerID, id string, v takenAtValue) (setTakenAtResult, error) {
	var res setTakenAtResult
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return res, newTimeTxError("media: set taken_at begin tx failed", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockOwnerRow(ctx, tx, ownerID); err != nil {
		return res, newTimeTxError("media: set taken_at owner lock failed", err)
	}
	row, err := scanTimeEditRow(tx.QueryRow(ctx, `
		SELECT `+timeEditRowColumns+`
		FROM media WHERE id = $1 AND owner_id = $2
		FOR UPDATE`, id, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, nil
	}
	if err != nil {
		return res, newTimeTxError("media: set taken_at lookup failed", err)
	}
	res.found, res.row = true, row
	if row.takenAt != nil && row.takenAt.Truncate(time.Second).Equal(v.at) {
		return res, nil
	}

	edit := newTimeEdit(timeEditSet, time.Now(), row.takenAt, v.at, row.timeEdit)
	edit.Wall, edit.Zone, edit.OffsetMin = &v.wall, v.zone, &v.offsetMin
	if err := applyTimeChange(ctx, tx, ownerID, row, v.at, edit, h.matchWindow); err != nil {
		return res, newTimeTxError("media: set taken_at failed", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, newTimeTxError("media: set taken_at commit failed", err)
	}
	res.changed = true
	return res, nil
}

// shiftTimesResponse answers POST /api/media/shift-time. Trips are the trips the shifted items sit
// in after trip detection, most items first, then by id; items in no trip are not listed.
type shiftTimesResponse struct {
	Shifted        int           `json:"shifted"`
	Undated        int           `json:"undated"`
	Trips          []shiftedTrip `json:"trips"`
	FollowupFailed bool          `json:"followup_failed"`
}

type shiftedTrip struct {
	ID    string `json:"id"`
	Items int    `json:"items"`
}

// shiftTimesResult is what one run of a shift's transaction did.
type shiftTimesResult struct {
	refused int    // 404 or 400 when the request is refused with nothing written, else 0
	reason  string // the refusal's plain-text reason
	undated int
	shifted []string    // the IDs moved
	around  []time.Time // each moved item's old and new time
}

// ShiftTimes moves the capture times of some of the caller's own items by the same amount: POST
// /api/media/shift-time with {"media_ids", "delta_seconds"}. The body is checked first (400, 413).
// Then every item is locked in one transaction, in id order, under the owner's lock. An ID that is
// not the caller's own item (foreign, shared through an album, or gone) makes the whole request 404
// with nothing written, and so does nothing else: items without a time are skipped and counted, and
// a shift that would carry any item out of the years JSON can carry is 400.
//
// The items move one by one (applyTimeChange), in the order that vacates each destination before it
// is needed: latest first for a shift later, earliest first for one earlier. Two items an hour apart
// shifted by an hour then never meet at one second, and each track point keeps its own item's
// position. After the commit the matcher and trip detection run (timeEditFollowup).
func (h *MediaHandler) ShiftTimes(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	v, ok := decodeShiftTime(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	res, err := retryOnDeadlock("shift times", func() (shiftTimesResult, error) {
		return h.shiftTimesTx(ctx, ownerID, v)
	})
	if err != nil {
		logTimeTxError(err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if res.refused != 0 {
		http.Error(w, res.reason, res.refused)
		return
	}
	resp := shiftTimesResponse{Shifted: len(res.shifted), Undated: res.undated, Trips: []shiftedTrip{}}
	slog.Info("media: capture times shifted", "owner", ownerID, "shifted", resp.Shifted, "undated", resp.Undated,
		"delta", v.delta)
	if len(res.shifted) == 0 {
		writeTimeEditJSON(w, resp)
		return
	}

	fctx, cancel := timeEditFollowupContext(ctx)
	defer cancel()
	ok = h.timeEditFollowup(fctx, ownerID, res.around)
	trips, err := shiftedTrips(fctx, h.db, ownerID, res.shifted)
	if err != nil {
		slog.Error("media: trip lookup failed after a capture time shift", "err", err)
		ok = false
	} else {
		resp.Trips = trips
	}
	resp.FollowupFailed = !ok
	writeTimeEditJSON(w, resp)
}

// shiftTimesTx runs a shift's transaction once: under the owner's lock, it locks the items and
// moves the dated ones by v.delta, or refuses the request with nothing written.
func (h *MediaHandler) shiftTimesTx(ctx context.Context, ownerID string, v shiftTimeValue) (shiftTimesResult, error) {
	var res shiftTimesResult
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return res, newTimeTxError("media: shift times begin tx failed", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockOwnerRow(ctx, tx, ownerID); err != nil {
		return res, newTimeTxError("media: shift times owner lock failed", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT `+timeEditRowColumns+`
		FROM media WHERE owner_id = $1 AND id = ANY($2)
		ORDER BY id
		FOR UPDATE`, ownerID, v.ids)
	if err != nil {
		return res, newTimeTxError("media: shift times lookup failed", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (timeEditRow, error) {
		return scanTimeEditRow(row)
	})
	if err != nil {
		return res, newTimeTxError("media: shift times rows failed", err)
	}
	if len(items) < len(v.ids) {
		return shiftTimesResult{refused: http.StatusNotFound, reason: "not found"}, nil
	}

	var dated []timeEditRow
	for _, m := range items {
		if m.takenAt == nil {
			res.undated++
			continue
		}
		if !jsonCanCarry(m.takenAt.Add(v.delta)) {
			return shiftTimesResult{refused: http.StatusBadRequest, reason: fmt.Sprintf(
				"the shift would move a capture time out of the years %d-%d", minTakenAtYear, maxTakenAtYear)}, nil
		}
		dated = append(dated, m)
	}
	slices.SortFunc(dated, func(a, b timeEditRow) int {
		byTime := a.takenAt.Compare(*b.takenAt)
		if v.delta > 0 {
			byTime = -byTime
		}
		return cmp.Or(byTime, cmp.Compare(a.id, b.id))
	})

	now := time.Now()
	res.around = make([]time.Time, 0, 2*len(dated))
	res.shifted = make([]string, 0, len(dated))
	for _, m := range dated {
		newTime := m.takenAt.Add(v.delta)
		edit := newTimeEdit(timeEditShift, now, m.takenAt, newTime, m.timeEdit)
		if err := applyTimeChange(ctx, tx, ownerID, m, newTime, edit, h.matchWindow); err != nil {
			return res, newTimeTxError("media: shift times failed", err, "id", m.id)
		}
		res.around = append(res.around, *m.takenAt, newTime)
		res.shifted = append(res.shifted, m.id)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, newTimeTxError("media: shift times commit failed", err)
	}
	return res, nil
}

// shiftedTrips counts the items of ids in each of ownerID's trips, most items first, then by id.
func shiftedTrips(ctx context.Context, db mediaDB, ownerID string, ids []string) ([]shiftedTrip, error) {
	rows, err := db.Query(ctx, `
		SELECT trip_id::text, count(*)::int
		FROM media
		WHERE owner_id = $1 AND id = ANY($2) AND trip_id IS NOT NULL
		GROUP BY trip_id
		ORDER BY count(*) DESC, trip_id`, ownerID, ids)
	if err != nil {
		return nil, err
	}
	trips, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (shiftedTrip, error) {
		var t shiftedTrip
		err := row.Scan(&t.ID, &t.Items)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	if trips == nil {
		trips = []shiftedTrip{}
	}
	return trips, nil
}
