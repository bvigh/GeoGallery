package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retimeHeads serves object heads from memory and records the keys read. onRead runs before each
// read.
type retimeHeads struct {
	mu      sync.Mutex
	objects map[string][]byte
	fail    map[string]bool
	reads   []string
	onRead  func(key string)
}

func newRetimeHeads() *retimeHeads {
	return &retimeHeads{objects: map[string][]byte{}, fail: map[string]bool{}}
}

func (f *retimeHeads) ReadHead(_ context.Context, key string, n int64) ([]byte, error) {
	if f.onRead != nil {
		f.onRead(key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, key)
	if f.fail[key] {
		return nil, errors.New("storage unavailable")
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("no such key %s", key)
	}
	return b[:min(int64(len(b)), n)], nil
}

func (f *retimeHeads) readKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.reads...)
}

type retimeRow struct {
	id                     string // "" lets the database choose
	owner, name, mediaType string
	object                 []byte
	takenAt                string // RFC3339
	gpsSource              string // "", "exif", "manual", "interpolated"
	lat, lng               float64
	cameraModel            string // "" stores NULL
	evidence               []byte // nil stores NULL
}

func retimeKey(owner, name string) string { return "media/" + owner + "/" + name }

func insertRetimeRow(t *testing.T, pool *pgxpool.Pool, store *retimeHeads, r retimeRow) string {
	t.Helper()
	key := retimeKey(r.owner, r.name)
	store.objects[key] = r.object
	if r.mediaType == "" {
		r.mediaType = "photo"
	}
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO media (id, owner_id, filename, media_type, storage_key, file_hash, taken_at, gps_source, location,
		                   camera_model, capture_evidence)
		VALUES (coalesce($12::uuid, gen_random_uuid()), $1, $2, $3, $4, $5, $6::timestamptz, $7,
		        CASE WHEN $7::text IS NOT NULL THEN ST_SetSRID(ST_MakePoint($9, $8), 4326) END, $10, $11)
		RETURNING id::text`,
		r.owner, r.name, r.mediaType, key, "hash-"+key, r.takenAt, nilIfEmpty(r.gpsSource), r.lat, r.lng,
		nilIfEmpty(r.cameraModel), r.evidence, nilIfEmpty(r.id)).Scan(&id))
	return id
}

type retimedMedia struct {
	takenAt     time.Time
	gpsSource   *string
	lat, lng    *float64
	tripID      *string
	cameraModel *string
	evidence    []byte
}

func readRetimedMedia(t *testing.T, pool *pgxpool.Pool, id string) retimedMedia {
	t.Helper()
	var m retimedMedia
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT taken_at, gps_source, ST_Y(location::geometry), ST_X(location::geometry), trip_id::text,
		       camera_model, capture_evidence
		FROM media WHERE id = $1`, id).Scan(&m.takenAt, &m.gpsSource, &m.lat, &m.lng, &m.tripID, &m.cameraModel, &m.evidence))
	return m
}

// retimedEvidenceOf decodes a row's capture_evidence; nil when it is NULL.
func retimedEvidenceOf(t *testing.T, pool *pgxpool.Pool, id string) map[string]any {
	t.Helper()
	raw := readRetimedMedia(t, pool, id).evidence
	if raw == nil {
		return nil
	}
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	return got
}

func retimeTrackTimes(t *testing.T, pool *pgxpool.Pool, ownerID, source string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT recorded_at FROM gps_tracks WHERE owner_id = $1 AND source = $2 ORDER BY recorded_at`, ownerID, source)
	require.NoError(t, err)
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var at time.Time
		require.NoError(t, rows.Scan(&at))
		out = append(out, at.UTC().Format(time.RFC3339))
	}
	require.NoError(t, rows.Err())
	return out
}

func retimeTrackLat(t *testing.T, pool *pgxpool.Pool, ownerID, source, at string) float64 {
	t.Helper()
	var lat float64
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT ST_Y(location::geometry) FROM gps_tracks
		WHERE owner_id = $1 AND source = $2 AND recorded_at = $3::timestamptz`, ownerID, source, at).Scan(&lat))
	return lat
}

// retimeSnapshot renders every media and track row, to show a run wrote nothing. Trip detection
// re-creates auto trips under new IDs, so a row shows only whether it has a trip.
func retimeSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id::text || ' ' || coalesce(taken_at::text, '-') || ' ' || coalesce(gps_source, '-') || ' ' ||
		        coalesce(ST_AsText(location::geometry), '-') || ' ' || coalesce(gps_confidence::text, '-') ||
		        ' trip=' || (trip_id IS NOT NULL)::text || ' model=' || coalesce(camera_model, '-') ||
		        ' evidence=' || coalesce(capture_evidence::text, '-') || ' edit=' || coalesce(time_edit::text, '-')
		 FROM media ORDER BY id`,
		`SELECT owner_id::text || ' ' || source || ' ' || recorded_at::text || ' ' || ST_AsText(location::geometry)
		 FROM gps_tracks ORDER BY owner_id, source, recorded_at`,
		`SELECT count(*)::text FROM trips`,
	} {
		rows, err := pool.Query(context.Background(), q)
		require.NoError(t, err)
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			b.WriteString(line + "\n")
		}
		require.NoError(t, rows.Err())
		rows.Close()
	}
	return b.String()
}

func retimeUTC(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return at
}

// retimeLibrary is alice's library as a Docker server without a zone stored it: every EXIF time
// read as UTC, two hours late for Central European summer time.
type retimeLibrary struct {
	alice, bob                                     string
	interpolated, manual, exifGPS, abroad, noTrack string // retimed
	correct, video, fileTime, mismatch             string // left alone
	bobPhoto                                       string
	store                                          *retimeHeads
}

func seedRetimeLibrary(t *testing.T, pool *pgxpool.Pool) retimeLibrary {
	t.Helper()
	l := retimeLibrary{store: newRetimeHeads()}
	l.alice = createUser(t, pool, "alice")
	l.bob = createUser(t, pool, "bob")
	photo := func(dto string) []byte { return exiftest.JPEG(exiftest.Photo{DateTimeOriginal: dto}) }

	// Alice's GPS log: a walk around 09:50Z (the true time) and another place around 11:50Z (the
	// time the UTC reading put her photo at).
	insertTrack(t, pool, l.alice, "loom", "2018-10-23T09:45:00Z", 48.00, 15.00)
	insertTrack(t, pool, l.alice, "loom", "2018-10-23T09:55:00Z", 48.10, 15.10)
	insertTrack(t, pool, l.alice, "loom", "2018-10-23T11:45:00Z", 50.00, 20.00)
	insertTrack(t, pool, l.alice, "loom", "2018-10-23T11:55:00Z", 50.00, 20.00)
	insertTrack(t, pool, l.alice, "loom", "2018-10-25T15:05:00Z", 50.00, 20.00)

	l.interpolated = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "P1011841.JPG",
		object: photo("2018:10:23 11:50:53"), takenAt: "2018-10-23T11:50:53Z", gpsSource: "interpolated", lat: 50, lng: 20})

	l.manual = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "P1011821.JPG",
		object: photo("2018:10:23 12:00:00"), takenAt: "2018-10-23T12:00:00Z", gpsSource: "manual", lat: 47.5, lng: 19.0})
	insertTrack(t, pool, l.alice, "manual", "2018-10-23T12:00:00Z", 47.5, 19.0)

	l.exifGPS = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "IMG_1.jpg",
		object:  exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2018:10:23 12:10:00", GPS: &exiftest.GPS{Lat: 47.6, Lng: 19.1}}),
		takenAt: "2018-10-23T12:10:00Z", gpsSource: "exif", lat: 47.6, lng: 19.1})
	insertTrack(t, pool, l.alice, "exif", "2018-10-23T12:10:00Z", 47.6, 19.1)

	l.abroad = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "tokyo.jpg",
		object: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2023:05:01 18:00:00",
			GPS: &exiftest.GPS{Lat: 35.68, Lng: 139.76, Time: time.Date(2023, 5, 1, 9, 0, 3, 0, time.UTC)}}),
		takenAt: "2023-05-01T18:00:00Z", gpsSource: "exif", lat: 35.68, lng: 139.76})
	insertTrack(t, pool, l.alice, "exif", "2023-05-01T18:00:00Z", 35.68, 139.76)

	// Matched near the wrong time; nothing is logged within the match window of the true one.
	l.noTrack = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "P1022050.JPG",
		object: photo("2018:10:25 15:00:00"), takenAt: "2018-10-25T15:00:00Z", gpsSource: "interpolated", lat: 50, lng: 20})

	l.correct = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "uploaded-after-the-fix.jpg",
		object: photo("2018:10:23 13:00:00"), takenAt: "2018-10-23T11:00:00Z"})
	l.video = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "clip.mp4", mediaType: "video",
		object: []byte("\x00\x00\x00\x18ftypmp42 not exif"), takenAt: "2018-10-23T10:30:00Z", gpsSource: "interpolated", lat: 48.05, lng: 15.05})
	l.fileTime = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "scan.jpg",
		object: exiftest.JPEG(exiftest.Photo{}), takenAt: "2018-10-23T10:31:17.123Z"})
	l.mismatch = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "edited.jpg",
		object: photo("2018:10:23 14:00:00"), takenAt: "2018-10-23T14:07:00Z"})

	// Bob: the same photo at the same wrong time, his own manual and EXIF points at alice's old
	// times, and a GPS log closer to 09:50Z than alice's that must never place her photo.
	l.bobPhoto = insertRetimeRow(t, pool, l.store, retimeRow{owner: l.bob, name: "P1011841.JPG",
		object: photo("2018:10:23 11:50:53"), takenAt: "2018-10-23T11:50:53Z", gpsSource: "interpolated", lat: 50, lng: 20})
	insertTrack(t, pool, l.bob, "manual", "2018-10-23T12:00:00Z", 10, 10)
	insertTrack(t, pool, l.bob, "exif", "2018-10-23T12:10:00Z", 10, 10)
	insertTrack(t, pool, l.bob, "loom", "2018-10-23T09:46:00Z", 10, 10)
	insertTrack(t, pool, l.bob, "loom", "2018-10-23T09:54:00Z", 10, 10)
	return l
}

// retimeDeps reads in Warsaw with the shipped Panasonic rule minus its video part, so no video is
// read: what a server without video_time_from_exif does.
func retimeDeps(t *testing.T, pool *pgxpool.Pool, store *retimeHeads) RetimeDeps {
	t.Helper()
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicOffsetRule}
	return RetimeDeps{pool: pool, storage: store, cameraZone: zone, matchWindow: time.Hour, tripGapHours: 24}
}

// retimeVideoDeps reads with the shipped Panasonic rule, which dates its videos from their EXIF.
func retimeVideoDeps(t *testing.T, pool *pgxpool.Pool, store *retimeHeads) RetimeDeps {
	t.Helper()
	deps := retimeDeps(t, pool, store)
	deps.cameraZone.Rules = panasonicVideoRules
	return deps
}

func collectRetime(changes *[]RetimeChange) func(RetimeChange) {
	return func(c RetimeChange) { *changes = append(*changes, c) }
}

func TestRunRetime_Integration_Apply(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	ctx := context.Background()

	var changes []RetimeChange
	res, err := RunRetime(ctx, retimeDeps(t, pool, l.store), l.alice, true, collectRetime(&changes))
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 5, Unchanged: 1, NotExif: 2, Mismatch: 1}, res)
	assert.NotContains(t, l.store.readKeys(), retimeKey(l.alice, "clip.mp4"), "no rule dates videos from EXIF: the video is not read")
	assert.NotContains(t, l.store.readKeys(), retimeKey(l.bob, "P1011841.JPG"), "another owner's object is never read")

	type reported struct{ id, old, new, zone, gpsSource string }
	var got []reported
	for _, c := range changes {
		assert.Nil(t, c.ClockRule)
		got = append(got, reported{c.MediaID, c.Old.UTC().Format(time.RFC3339), c.New.UTC().Format(time.RFC3339), c.Zone, c.GPSSource})
	}
	assert.Equal(t, []reported{
		{l.interpolated, "2018-10-23T11:50:53Z", "2018-10-23T09:50:53Z", ingest.ZoneCamera, "interpolated"},
		{l.manual, "2018-10-23T12:00:00Z", "2018-10-23T10:00:00Z", ingest.ZoneCamera, "manual"},
		{l.exifGPS, "2018-10-23T12:10:00Z", "2018-10-23T10:10:00Z", ingest.ZoneCamera, "exif"},
		{l.noTrack, "2018-10-25T15:00:00Z", "2018-10-25T13:00:00Z", ingest.ZoneCamera, "interpolated"},
		{l.abroad, "2023-05-01T18:00:00Z", "2023-05-01T09:00:00Z", ingest.ZoneGPS, "exif"},
	}, got, "in stored taken_at order")

	// Re-matched at the true time from alice's own log, not bob's closer one.
	m := readRetimedMedia(t, pool, l.interpolated)
	assert.True(t, retimeUTC(t, "2018-10-23T09:50:53Z").Equal(m.takenAt))
	require.NotNil(t, m.gpsSource)
	assert.Equal(t, "interpolated", *m.gpsSource)
	require.NotNil(t, m.lat)
	assert.InDelta(t, 48.06, *m.lat, 0.01)
	assert.InDelta(t, 15.06, *m.lng, 0.01)

	// A manual location stays, and its track point moves with the photo.
	m = readRetimedMedia(t, pool, l.manual)
	assert.True(t, retimeUTC(t, "2018-10-23T10:00:00Z").Equal(m.takenAt))
	require.NotNil(t, m.gpsSource)
	assert.Equal(t, "manual", *m.gpsSource)
	assert.InDelta(t, 47.5, *m.lat, 1e-9)
	assert.InDelta(t, 19.0, *m.lng, 1e-9)
	assert.Equal(t, []string{"2018-10-23T10:00:00Z"}, retimeTrackTimes(t, pool, l.alice, "manual"))

	// EXIF positions stay, and their track points move; the GPS clock placed the Tokyo photo.
	m = readRetimedMedia(t, pool, l.exifGPS)
	assert.True(t, retimeUTC(t, "2018-10-23T10:10:00Z").Equal(m.takenAt))
	assert.Equal(t, "exif", *m.gpsSource)
	assert.InDelta(t, 47.6, *m.lat, 1e-6)
	assert.True(t, retimeUTC(t, "2023-05-01T09:00:00Z").Equal(readRetimedMedia(t, pool, l.abroad).takenAt))
	assert.Equal(t, []string{"2018-10-23T10:10:00Z", "2023-05-01T09:00:00Z"}, retimeTrackTimes(t, pool, l.alice, "exif"))

	// The old interpolation is cleared when nothing brackets the true time.
	m = readRetimedMedia(t, pool, l.noTrack)
	assert.True(t, retimeUTC(t, "2018-10-25T13:00:00Z").Equal(m.takenAt))
	assert.Nil(t, m.gpsSource)
	assert.Nil(t, m.lat)

	// Every retimed row records how, with the time it moved from.
	ev := retimedEvidenceOf(t, pool, l.abroad)
	require.NotNil(t, ev)
	assert.Equal(t, "retime", ev["by"])
	assert.Equal(t, "2023-05-01T18:00:00Z", ev["retimed_from"])
	assert.Equal(t, "gps", ev["zone"])
	assert.Equal(t, "2023-05-01T09:00:03Z", ev["gps_utc"])

	// Rows not dated from this EXIF keep their time, and their (absent) evidence.
	for id, want := range map[string]string{
		l.correct: "2018-10-23T11:00:00Z", l.video: "2018-10-23T10:30:00Z",
		l.fileTime: "2018-10-23T10:31:17.123Z", l.mismatch: "2018-10-23T14:07:00Z",
	} {
		assert.True(t, retimeUTC(t, want).Equal(readRetimedMedia(t, pool, id).takenAt), want)
		assert.Nil(t, readRetimedMedia(t, pool, id).evidence, want)
	}

	// Trip detection ran for alice only; bob's rows and points are untouched.
	assert.NotNil(t, readRetimedMedia(t, pool, l.interpolated).tripID)
	b := readRetimedMedia(t, pool, l.bobPhoto)
	assert.True(t, retimeUTC(t, "2018-10-23T11:50:53Z").Equal(b.takenAt))
	assert.InDelta(t, 50, *b.lat, 1e-9)
	assert.Nil(t, b.tripID)
	assert.Nil(t, b.evidence)
	assert.Equal(t, []string{"2018-10-23T12:00:00Z"}, retimeTrackTimes(t, pool, l.bob, "manual"))
	assert.Equal(t, []string{"2018-10-23T12:10:00Z"}, retimeTrackTimes(t, pool, l.bob, "exif"))

	// A second run finds nothing to change.
	before := retimeSnapshot(t, pool)
	changes = nil
	res, err = RunRetime(ctx, retimeDeps(t, pool, l.store), l.alice, true, collectRetime(&changes))
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Unchanged: 6, NotExif: 2, Mismatch: 1}, res)
	assert.Empty(t, changes)
	assert.Equal(t, before, retimeSnapshot(t, pool))
}

// TestRunRetime_Integration_OffsetTag repairs what ingest stored before it read OffsetTimeOriginal:
// a phone's photo abroad moves, with its track point. The Panasonic that writes the same offset and
// a phone at home stay, and so does a row whose EXIF ingest could not read and that holds a file
// time. A Sony with a broken Exif IFD moves when its stored time is its IFD0 clock at an offset.
func TestRunRetime_Integration_OffsetTag(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	alice := createUser(t, pool, "alice")
	store := newRetimeHeads()

	london := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "abroad.jpg",
		object: exiftest.JPEG(exiftest.Photo{Make: "samsung", DateTimeOriginal: "2025:07:14 15:30:00", OffsetTimeOriginal: "+01:00",
			GPS: &exiftest.GPS{Lat: 51.5074, Lng: -0.1278}}),
		takenAt: "2025-07-14T13:30:00Z", gpsSource: "exif", lat: 51.5074, lng: -0.1278})
	insertTrack(t, pool, alice, "exif", "2025-07-14T13:30:00Z", 51.5074, -0.1278)

	panasonic := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1011841.JPG",
		object:  exiftest.JPEG(exiftest.Photo{Make: "Panasonic", DateTimeOriginal: "2018:07:23 11:50:53", OffsetTimeOriginal: "+01:00"}),
		takenAt: "2018-07-23T09:50:53Z"})
	home := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "20260101_120000.jpg",
		object:  exiftest.JPEG(exiftest.Photo{Make: "samsung", DateTimeOriginal: "2026:01:01 12:00:00", OffsetTimeOriginal: "+01:00"}),
		takenAt: "2026-01-01T11:00:00Z"})
	sonyInZone := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "sony-in-zone.jpg",
		object:  exiftest.JPEG(exiftest.Photo{Make: "SONY", DateTime: "2009:04:15 10:20:30", BrokenExifIFD: true}),
		takenAt: "2009-04-15T07:20:30Z"})
	sonyFileTime := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "download.jpeg",
		object:  exiftest.JPEG(exiftest.Photo{Make: "SONY", DateTime: "2009:04:16 11:00:00", BrokenExifIFD: true}),
		takenAt: "2010-02-03T14:31:18Z"})

	zones := map[string]string{}
	res, err := RunRetime(ctx, retimeDeps(t, pool, store), alice, true, func(c RetimeChange) { zones[c.MediaID] = c.Zone })
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 5, Retimed: 2, Unchanged: 2, Mismatch: 1}, res)
	assert.Equal(t, map[string]string{london: ingest.ZoneOffset, sonyInZone: ingest.ZoneCamera}, zones)

	assert.True(t, retimeUTC(t, "2025-07-14T14:30:00Z").Equal(readRetimedMedia(t, pool, london).takenAt))
	assert.Equal(t, []string{"2025-07-14T14:30:00Z"}, retimeTrackTimes(t, pool, alice, "exif"), "the track point moves with the photo")
	assert.True(t, retimeUTC(t, "2009-04-15T08:20:30Z").Equal(readRetimedMedia(t, pool, sonyInZone).takenAt))
	assert.Equal(t, "exif_ifd0", retimedEvidenceOf(t, pool, sonyInZone)["src"])

	assert.True(t, retimeUTC(t, "2018-07-23T09:50:53Z").Equal(readRetimedMedia(t, pool, panasonic).takenAt))
	assert.True(t, retimeUTC(t, "2026-01-01T11:00:00Z").Equal(readRetimedMedia(t, pool, home).takenAt))
	assert.True(t, retimeUTC(t, "2010-02-03T14:31:18Z").Equal(readRetimedMedia(t, pool, sonyFileTime).takenAt))
}

// The owner's clock rule for a DC-TZ200 left on summer time (UTC+2 from 2018-07-01): its winter
// photos, stored by the camera zone at Warsaw's +01:00, move back an hour, and record the rule.
// Summer photos were right by luck and stay. A rule describes its owner's camera only.
func TestRunRetime_Integration_ClockRule(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")
	store := newRetimeHeads()
	deps := retimeDeps(t, pool, store)
	ruleID := insertClockRule(t, pool, alice, "Panasonic", "DC-TZ200", 120, ptr("2018-07-01"), nil)
	// Bob's rule for a model alice also owns, and a different offset for the one alice has a rule for.
	insertClockRule(t, pool, bob, "Panasonic", "DC-TZ100", 180, nil, nil)
	insertClockRule(t, pool, bob, "Panasonic", "DC-TZ200", -300, ptr("2030-01-01"), nil)

	tz := func(model, dto string) []byte {
		return exiftest.JPEG(exiftest.Photo{Make: "Panasonic", Model: model, DateTimeOriginal: dto, OffsetTimeOriginal: "+01:00"})
	}
	// What ingest recorded before the rule: the camera zone, with the file time that lost.
	mtime := time.Date(2019, 3, 14, 7, 0, 0, 0, time.UTC)
	ingested := func(object []byte) []byte {
		return newCaptureEvidence(captureReading{exif: ingest.ExtractExif(bytes.NewReader(object), deps.cameraZone), mtime: &mtime}, deps.cameraZone).encode()
	}

	winterObject := tz("DC-TZ200", "2019:03:13 19:28:15")
	winter := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1033627.JPG", object: winterObject,
		takenAt: "2019-03-13T18:28:15Z", cameraModel: "DC-TZ200", evidence: ingested(winterObject)})
	// Ingested before migration 10: no evidence and no camera model.
	oldWinter := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1044001.JPG", object: tz("DC-TZ200", "2018:12:01 10:00:00"),
		takenAt: "2018-12-01T09:00:00Z"})
	summerObject := tz("DC-TZ200", "2019:07:13 19:28:15")
	summer := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1077001.JPG", object: summerObject,
		takenAt: "2019-07-13T17:28:15Z", cameraModel: "DC-TZ200", evidence: ingested(summerObject)})
	beforeRule := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1000001.JPG", object: tz("DC-TZ200", "2018:06:30 12:00:00"),
		takenAt: "2018-06-30T10:00:00Z"})
	otherModel := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P2000001.JPG", object: tz("DC-TZ100", "2019:03:13 12:00:00"),
		takenAt: "2019-03-13T11:00:00Z"})
	bobWinter := insertRetimeRow(t, pool, store, retimeRow{owner: bob, name: "P1033627.JPG", object: winterObject,
		takenAt: "2019-03-13T18:28:15Z", evidence: ingested(winterObject)})

	t.Run("a dry run writes nothing", func(t *testing.T) {
		before := retimeSnapshot(t, pool)
		var changes []RetimeChange
		res, err := RunRetime(ctx, deps, alice, false, collectRetime(&changes))
		require.NoError(t, err)
		assert.Equal(t, RetimeResult{Checked: 5, Retimed: 2, Unchanged: 3}, res)
		require.Len(t, changes, 2)
		assert.Equal(t, before, retimeSnapshot(t, pool))
	})

	var changes []RetimeChange
	res, err := RunRetime(ctx, deps, alice, true, collectRetime(&changes))
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 5, Retimed: 2, Unchanged: 3}, res)
	assert.NotContains(t, store.readKeys(), retimeKey(bob, "P1033627.JPG"))

	require.Len(t, changes, 2)
	for i, want := range []struct{ id, old, new string }{
		{oldWinter, "2018-12-01T09:00:00Z", "2018-12-01T08:00:00Z"},
		{winter, "2019-03-13T18:28:15Z", "2019-03-13T17:28:15Z"},
	} {
		c := changes[i]
		assert.Equal(t, want.id, c.MediaID)
		assert.Equal(t, want.old, c.Old.UTC().Format(time.RFC3339))
		assert.Equal(t, want.new, c.New.UTC().Format(time.RFC3339))
		assert.Equal(t, -time.Hour, c.New.Sub(c.Old))
		assert.Equal(t, ingest.ZoneClockRule, c.Zone)
		require.NotNil(t, c.ClockRule)
		assert.Equal(t, ingest.ClockRule{ID: ruleID, Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120, StartsOn: ruleDay(2018, 7, 1)}, *c.ClockRule)
	}

	snapshot := map[string]any{"id": ruleID, "make": "Panasonic", "model": "DC-TZ200", "offset_min": 120.0, "starts_on": "2018-07-01", "ends_on": nil}
	reread := func(wall, retimedFrom string) map[string]any {
		return map[string]any{
			"by": "retime", "retimed_from": retimedFrom, "src": "exif_original", "zone": "clock_rule", "wall": wall,
			"offset_tag": "+01:00", "offset_ignored": true, "offset_used_min": 120.0, "clock_rule": snapshot,
			"make": "Panasonic", "model": "DC-TZ200",
		}
	}

	m := readRetimedMedia(t, pool, winter)
	assert.True(t, retimeUTC(t, "2019-03-13T17:28:15Z").Equal(m.takenAt))
	want := reread("2019:03:13 19:28:15", "2019-03-13T18:28:15Z")
	want["file_mtime"] = "2019-03-14T07:00:00Z"
	assert.Equal(t, evidenceWith(want), retimedEvidenceOf(t, pool, winter), "the file time ingest recorded is kept")

	m = readRetimedMedia(t, pool, oldWinter)
	assert.True(t, retimeUTC(t, "2018-12-01T08:00:00Z").Equal(m.takenAt))
	require.NotNil(t, m.cameraModel)
	assert.Equal(t, "DC-TZ200", *m.cameraModel, "a missing camera model is filled from the EXIF")
	assert.Equal(t, evidenceWith(reread("2018:12:01 10:00:00", "2018-12-01T09:00:00Z")), retimedEvidenceOf(t, pool, oldWinter),
		"a whole evidence object, with the container and file inputs null")

	// Unchanged rows keep their time and their evidence.
	for id, at := range map[string]string{summer: "2019-07-13T17:28:15Z", beforeRule: "2018-06-30T10:00:00Z", otherModel: "2019-03-13T11:00:00Z"} {
		assert.True(t, retimeUTC(t, at).Equal(readRetimedMedia(t, pool, id).takenAt), at)
	}
	assert.Equal(t, "ingest", retimedEvidenceOf(t, pool, summer)["by"])
	assert.Nil(t, readRetimedMedia(t, pool, otherModel).evidence, "bob's DC-TZ100 rule never reads alice's DC-TZ100")

	// Bob's copy of the winter photo is untouched by alice's run, and his own rules do not move it.
	b := readRetimedMedia(t, pool, bobWinter)
	assert.True(t, retimeUTC(t, "2019-03-13T18:28:15Z").Equal(b.takenAt))
	assert.Equal(t, "ingest", retimedEvidenceOf(t, pool, bobWinter)["by"])
	res, err = RunRetime(ctx, deps, bob, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 1, Unchanged: 1}, res, "alice's rule never applies to bob's camera")
	assert.True(t, retimeUTC(t, "2019-03-13T18:28:15Z").Equal(readRetimedMedia(t, pool, bobWinter).takenAt))

	t.Run("a second apply changes nothing", func(t *testing.T) {
		before := retimeSnapshot(t, pool)
		var again []RetimeChange
		res, err := RunRetime(ctx, deps, alice, true, collectRetime(&again))
		require.NoError(t, err)
		assert.Equal(t, RetimeResult{Checked: 5, Unchanged: 5}, res)
		assert.Empty(t, again)
		assert.Equal(t, before, retimeSnapshot(t, pool))
	})
}

// Panasonic videos stored before the camera rule dated them from their EXIF hold their container
// time: the camera's wall clock marked UTC, a second off the wall clock in the PANA box's EXIF.
// The rule moves them, through the owner's clock rule where one applies.
func TestRunRetime_Integration_PanasonicVideo(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	alice := createUser(t, pool, "alice")
	store := newRetimeHeads()
	ruleID := insertClockRule(t, pool, alice, "Panasonic", "DC-TZ200", 120, ptr("2018-07-01"), nil)

	video := func(dto string) []byte {
		return exiftest.PanasonicMP4(exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: dto, OffsetTimeOriginal: "+01:00"})
	}
	// What ingest recorded from the container: no EXIF, the container time and the codec.
	containerEvidence := func(created string) []byte {
		at := retimeUTC(t, created)
		duration := 3.0
		return newCaptureEvidence(captureReading{exif: ingest.ExifData{}, probe: ingest.VideoProbeResult{CreatedAt: &at, CodecName: "h264", DurationS: &duration}}, ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true}).encode()
	}

	beforeRule := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1000001.MP4", mediaType: "video",
		object: video("2001:02:03 04:05:06"), takenAt: "2001-02-03T04:05:07Z", evidence: containerEvidence("2001-02-03T04:05:07Z")})
	byRule := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1000002.MP4", mediaType: "video_360",
		object: video("2019:01:15 12:00:00"), takenAt: "2019-01-15T12:00:01Z"})
	fromExif := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1000003.MP4", mediaType: "video",
		object: video("2001:03:03 10:00:00"), takenAt: "2001-03-03T09:00:01Z", cameraModel: "DC-TZ200"})
	mismatch := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1000004.MP4", mediaType: "video",
		object: video("2001:04:04 10:00:00"), takenAt: "2001-04-04T10:07:00Z"})
	noBox := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "phone.mp4", mediaType: "video",
		object: []byte("\x00\x00\x00\x18ftypmp42 no PANA box"), takenAt: "2001-05-05T10:00:00Z"})

	t.Run("without the camera rule no video is read, the clock rule notwithstanding", func(t *testing.T) {
		before := retimeSnapshot(t, pool)
		res, err := RunRetime(ctx, retimeDeps(t, pool, store), alice, false, func(RetimeChange) {})
		require.NoError(t, err)
		assert.Equal(t, RetimeResult{Checked: 5, NotExif: 5}, res)
		assert.Empty(t, store.readKeys())
		assert.Equal(t, before, retimeSnapshot(t, pool))
	})

	logs := captureLogs(t)
	var changes []RetimeChange
	res, err := RunRetime(ctx, retimeVideoDeps(t, pool, store), alice, true, collectRetime(&changes))
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 5, Retimed: 2, Unchanged: 1, Mismatch: 1, NotExif: 1}, res)
	assert.Contains(t, logs.String(), "id="+mismatch)
	assert.Contains(t, logs.String(), "filename=P1000004.MP4")

	require.Len(t, changes, 2)
	assert.Equal(t, beforeRule, changes[0].MediaID)
	assert.Equal(t, "2001-02-03T03:05:06Z", changes[0].New.UTC().Format(time.RFC3339), "Warsaw's winter +01:00")
	assert.Equal(t, ingest.ZoneCamera, changes[0].Zone)
	assert.Nil(t, changes[0].ClockRule)
	assert.Equal(t, byRule, changes[1].MediaID)
	assert.Equal(t, "2019-01-15T10:00:00Z", changes[1].New.UTC().Format(time.RFC3339), "the owner's +02:00")
	assert.Equal(t, ingest.ZoneClockRule, changes[1].Zone)
	require.NotNil(t, changes[1].ClockRule)
	assert.Equal(t, ruleID, changes[1].ClockRule.ID)

	m := readRetimedMedia(t, pool, beforeRule)
	assert.True(t, retimeUTC(t, "2001-02-03T03:05:06Z").Equal(m.takenAt))
	require.NotNil(t, m.cameraModel)
	assert.Equal(t, "DC-TZ200", *m.cameraModel)
	assert.Equal(t, evidenceWith(map[string]any{
		"by": "retime", "retimed_from": "2001-02-03T04:05:07Z", "src": "exif_original", "zone": "camera",
		"wall": "2001:02:03 04:05:06", "offset_tag": "+01:00", "offset_ignored": true, "offset_used_min": 60.0,
		"make": "Panasonic", "model": "DC-TZ200",
		"creation_time": "2001-02-03T04:05:07Z", "duration_s": 3.0, "codec": "h264",
	}), retimedEvidenceOf(t, pool, beforeRule), "the container inputs stay, as the ones that lost")

	m = readRetimedMedia(t, pool, byRule)
	assert.True(t, retimeUTC(t, "2019-01-15T10:00:00Z").Equal(m.takenAt))
	ev := retimedEvidenceOf(t, pool, byRule)
	assert.Equal(t, "clock_rule", ev["zone"])
	assert.Equal(t, ruleID, ev["clock_rule"].(map[string]any)["id"])
	assert.Equal(t, "2019-01-15T12:00:01Z", ev["retimed_from"])
	assert.Nil(t, ev["creation_time"], "nothing recorded it")

	for id, at := range map[string]string{fromExif: "2001-03-03T09:00:01Z", mismatch: "2001-04-04T10:07:00Z", noBox: "2001-05-05T10:00:00Z"} {
		m := readRetimedMedia(t, pool, id)
		assert.True(t, retimeUTC(t, at).Equal(m.takenAt), at)
		assert.Nil(t, m.evidence, at)
	}
	assert.Nil(t, readRetimedMedia(t, pool, mismatch).cameraModel, "a mismatch is left alone entirely")

	before := retimeSnapshot(t, pool)
	res, err = RunRetime(ctx, retimeVideoDeps(t, pool, store), alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 5, Unchanged: 3, Mismatch: 1, NotExif: 1}, res)
	assert.Equal(t, before, retimeSnapshot(t, pool))
}

func TestRunRetime_Integration_DryRunWritesNothing(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	before := retimeSnapshot(t, pool)

	var ids []string
	res, err := RunRetime(context.Background(), retimeDeps(t, pool, l.store), l.alice, false, func(c RetimeChange) { ids = append(ids, c.MediaID) })
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 5, Unchanged: 1, NotExif: 2, Mismatch: 1}, res)
	assert.Equal(t, []string{l.interpolated, l.manual, l.exifGPS, l.noTrack, l.abroad}, ids)
	assert.Equal(t, before, retimeSnapshot(t, pool))
}

func TestRunRetime_Integration_ReadFailureSkipsTheItem(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	l.store.fail[retimeKey(l.alice, "P1011821.JPG")] = true
	logs := captureLogs(t)

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, l.store), l.alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 4, Unchanged: 1, NotExif: 2, Mismatch: 1, Failed: 1}, res)
	assert.True(t, retimeUTC(t, "2018-10-23T12:00:00Z").Equal(readRetimedMedia(t, pool, l.manual).takenAt))
	assert.Equal(t, []string{"2018-10-23T12:00:00Z"}, retimeTrackTimes(t, pool, l.alice, "manual"))
	assert.True(t, retimeUTC(t, "2018-10-23T09:50:53Z").Equal(readRetimedMedia(t, pool, l.interpolated).takenAt))

	assert.Contains(t, logs.String(), "retime: read object failed")
	assert.Contains(t, logs.String(), "id="+l.manual)
	assert.Contains(t, logs.String(), "filename=P1011821.JPG")
	assert.NotContains(t, logs.String(), "http")
}

// A failed clock rule load stops the run before anything is read: reading without the owner's rules
// would compute times ingest would not.
func TestRunRetime_Integration_ClockRulesUnavailable(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	_, err := pool.Exec(context.Background(), `DROP TABLE camera_clock_rules`)
	require.NoError(t, err)
	before := retimeSnapshot(t, pool)

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, l.store), l.alice, true, func(RetimeChange) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load clock rules")
	assert.Equal(t, RetimeResult{}, res)
	assert.Empty(t, l.store.readKeys())
	assert.Equal(t, before, retimeSnapshot(t, pool))
}

func TestRunRetime_Integration_InterruptedRunIsFinishedByARerun(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.store.onRead = func(key string) {
		if strings.HasSuffix(key, "/P1011821.JPG") { // the item after P1011841.JPG
			cancel()
		}
	}

	res, err := RunRetime(ctx, retimeDeps(t, pool, l.store), l.alice, true, func(RetimeChange) {})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, res.Retimed)
	m := readRetimedMedia(t, pool, l.interpolated)
	assert.True(t, retimeUTC(t, "2018-10-23T09:50:53Z").Equal(m.takenAt))
	assert.Nil(t, m.lat, "cleared, and the matcher did not run")
	assert.Nil(t, m.tripID, "trip detection did not run")
	assert.True(t, retimeUTC(t, "2018-10-23T12:00:00Z").Equal(readRetimedMedia(t, pool, l.manual).takenAt))
	assert.Nil(t, readRetimedMedia(t, pool, l.manual).evidence)

	l.store.onRead = nil
	res, err = RunRetime(context.Background(), retimeDeps(t, pool, l.store), l.alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 4, Unchanged: 2, NotExif: 2, Mismatch: 1}, res)
	m = readRetimedMedia(t, pool, l.interpolated)
	require.NotNil(t, m.lat, "the re-run's matcher places the item retimed before the interrupt")
	assert.InDelta(t, 48.06, *m.lat, 0.01)
	assert.NotNil(t, m.tripID)
	assert.Equal(t, "retime", retimedEvidenceOf(t, pool, l.manual)["by"])
}

// An old track point stays while another item is still at the old time: it may be that item's.
func TestRunRetime_Integration_SharedOldTime(t *testing.T) {
	pool := testdb.New(t)
	store := newRetimeHeads()
	alice := createUser(t, pool, "alice")
	photo := func(dto string) []byte { return exiftest.JPEG(exiftest.Photo{DateTimeOriginal: dto}) }

	// A photo and a video (not EXIF-dated) at the same instant, both placed by hand.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "a.jpg", object: photo("2019:07:01 12:00:00"),
		takenAt: "2019-07-01T12:00:00Z", gpsSource: "manual", lat: 47, lng: 19})
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "a.mp4", mediaType: "video", object: []byte("not exif"),
		takenAt: "2019-07-01T12:00:00Z", gpsSource: "manual", lat: 47, lng: 19})
	insertTrack(t, pool, alice, "manual", "2019-07-01T12:00:00Z", 47, 19)

	// A burst: two photos with one EXIF time, sharing one manual point.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "b1.jpg", object: photo("2019:08:01 12:00:00"),
		takenAt: "2019-08-01T12:00:00Z", gpsSource: "manual", lat: 46, lng: 18})
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "b2.jpg", object: photo("2019:08:01 12:00:00"),
		takenAt: "2019-08-01T12:00:00Z", gpsSource: "manual", lat: 46, lng: 18})
	insertTrack(t, pool, alice, "manual", "2019-08-01T12:00:00Z", 46, 18)

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, store), alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Retimed)
	assert.Equal(t, []string{
		"2019-07-01T10:00:00Z", "2019-07-01T12:00:00Z", // the video still needs its point
		"2019-08-01T10:00:00Z", // the burst's point moved once both photos left the old time
	}, retimeTrackTimes(t, pool, alice, "manual"))
}

// Burst photos with one EXIF time share one 'exif' point (ingest's ON CONFLICT keeps the first
// position). It moves once, with one of their own positions.
func TestRunRetime_Integration_BurstSharesOnePoint(t *testing.T) {
	pool := testdb.New(t)
	store := newRetimeHeads()
	alice := createUser(t, pool, "alice")
	at := func(lat float64) []byte {
		return exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2026:09:10 19:17:36", GPS: &exiftest.GPS{Lat: lat, Lng: 16.588}})
	}
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "20260910_191736.jpg", object: at(48.89518),
		takenAt: "2026-09-10T19:17:36Z", gpsSource: "exif", lat: 48.89518, lng: 16.588})
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "20260910_191736(0).jpg", object: at(48.89503),
		takenAt: "2026-09-10T19:17:36Z", gpsSource: "exif", lat: 48.89503, lng: 16.588})
	insertTrack(t, pool, alice, "exif", "2026-09-10T19:17:36Z", 48.89503, 16.588)

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, store), alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Retimed)
	assert.Equal(t, []string{"2026-09-10T17:17:36Z"}, retimeTrackTimes(t, pool, alice, "exif"))
	lat := retimeTrackLat(t, pool, alice, "exif", "2026-09-10T17:17:36Z")
	assert.True(t, math.Abs(lat-48.89518) < 1e-6 || math.Abs(lat-48.89503) < 1e-6, "one of the photos' positions, got %v", lat)
}

// A retimed item can sit at the same second as an item that does not own its old point. The old
// point must then go, or it would stay an hour or two from where it was recorded.
func TestRunRetime_Integration_UnrelatedItemAtOldTime(t *testing.T) {
	pool := testdb.New(t)
	store := newRetimeHeads()
	alice := createUser(t, pool, "alice")
	gpsPhoto := func(dto string, lat float64) []byte {
		return exiftest.JPEG(exiftest.Photo{DateTimeOriginal: dto, GPS: &exiftest.GPS{Lat: lat, Lng: 19}})
	}

	// An EXIF-GPS photo read as UTC, two hours late, onto the second of a correctly dated photo
	// without a position of its own.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_burst_a.jpg", object: gpsPhoto("2021:06:03 13:08:56", 47.1),
		takenAt: "2021-06-03T13:08:56Z", gpsSource: "exif", lat: 47.1, lng: 19})
	insertTrack(t, pool, alice, "exif", "2021-06-03T13:08:56Z", 47.1, 19)
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_burst_b.jpg",
		object:  exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2021:06:03 15:08:56"}),
		takenAt: "2021-06-03T13:08:56Z", gpsSource: "interpolated", lat: 47.2, lng: 19})

	// A photo placed by hand, read as UTC onto the second of a correctly dated EXIF-GPS photo.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1.JPG", object: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2022:08:04 13:00:00"}),
		takenAt: "2022-08-04T13:00:00Z", gpsSource: "manual", lat: 46, lng: 18})
	insertTrack(t, pool, alice, "manual", "2022-08-04T13:00:00Z", 46, 18)
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_20220804_150000.jpg", object: gpsPhoto("2022:08:04 15:00:00", 47.3),
		takenAt: "2022-08-04T13:00:00Z", gpsSource: "exif", lat: 47.3, lng: 19})
	insertTrack(t, pool, alice, "exif", "2022-08-04T13:00:00Z", 47.3, 19)

	// An EXIF-GPS photo read as UTC onto the second of a correctly dated EXIF-GPS photo. Their one
	// point there holds the moved photo's position (it was ingested first). The moved photo takes its
	// own position to its true time, and the point that stays gets the other photo's.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_20220806_143349.jpg", object: gpsPhoto("2022:08:06 14:33:49", 47.5),
		takenAt: "2022-08-06T14:33:49Z", gpsSource: "exif", lat: 47.5, lng: 19})
	insertTrack(t, pool, alice, "exif", "2022-08-06T14:33:49Z", 47.5, 19)
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_20220806_163349.jpg", object: gpsPhoto("2022:08:06 16:33:49", 47.6),
		takenAt: "2022-08-06T14:33:49Z", gpsSource: "exif", lat: 47.6, lng: 19})

	// An EXIF-GPS photo that never got a point (ingested before EXIF points existed): none is made.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "old.jpg", object: gpsPhoto("2022:08:05 12:00:00", 47.4),
		takenAt: "2022-08-05T12:00:00Z", gpsSource: "exif", lat: 47.4, lng: 19})

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, store), alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 7, Retimed: 4, Unchanged: 3}, res)
	assert.Equal(t, []string{
		"2021-06-03T11:08:56Z", // moved; the photo without a position does not keep 13:08:56
		"2022-08-04T13:00:00Z", // the correctly dated photo's own point
		"2022-08-06T12:33:49Z", // the moved photo's own position, not the other photo's
		"2022-08-06T14:33:49Z", // stays: the other photo is still there
	}, retimeTrackTimes(t, pool, alice, "exif"))
	assert.InDelta(t, 47.1, retimeTrackLat(t, pool, alice, "exif", "2021-06-03T11:08:56Z"), 1e-9)
	assert.InDelta(t, 47.5, retimeTrackLat(t, pool, alice, "exif", "2022-08-06T12:33:49Z"), 1e-9)
	assert.InDelta(t, 47.6, retimeTrackLat(t, pool, alice, "exif", "2022-08-06T14:33:49Z"), 1e-9)
	assert.Equal(t, []string{"2022-08-04T11:00:00Z"}, retimeTrackTimes(t, pool, alice, "manual"),
		"an EXIF-located photo at the old second does not keep a manual point")
}

// Only an item that would have made an 'exif' point keeps it at the old second: a photo located
// from its EXIF. An item placed by hand has its own 'manual' point, and a video located from its
// container's tag never makes one, so neither keeps the moved photo's point there, an hour or two
// from where it was recorded, nor gives it a position.
func TestRunRetime_Integration_OnlyEXIFLocatedPhotosKeepAnExifPoint(t *testing.T) {
	pool := testdb.New(t)
	store := newRetimeHeads()
	alice := createUser(t, pool, "alice")
	gpsPhoto := func(dto string, lat float64) []byte {
		return exiftest.JPEG(exiftest.Photo{DateTimeOriginal: dto, GPS: &exiftest.GPS{Lat: lat, Lng: 19}})
	}
	photo := func(dto string) []byte { return exiftest.JPEG(exiftest.Photo{DateTimeOriginal: dto}) }

	// An EXIF-GPS photo read as UTC, two hours late, onto the second of a correctly dated photo
	// without an EXIF position that its owner placed by hand.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1.JPG", object: gpsPhoto("2021:06:03 13:08:56", 47.1),
		takenAt: "2021-06-03T13:08:56Z", gpsSource: "exif", lat: 47.1, lng: 19})
	insertTrack(t, pool, alice, "exif", "2021-06-03T13:08:56Z", 47.1, 19)
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_20210603_150856.jpg", object: photo("2021:06:03 15:08:56"),
		takenAt: "2021-06-03T13:08:56Z", gpsSource: "manual", lat: 46, lng: 18})
	insertTrack(t, pool, alice, "manual", "2021-06-03T13:08:56Z", 46, 18)

	// A burst from one camera, both read two hours late: one photo with an EXIF position, one
	// without that was placed by hand. Retime takes them in ID order; either order ends the same.
	for _, burst := range []struct {
		day, gpsID, manualID string
	}{
		{"2021-06-04", "00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b"},
		{"2021-06-05", "00000000-0000-4000-8000-00000000000d", "00000000-0000-4000-8000-00000000000c"},
	} {
		wall := strings.ReplaceAll(burst.day, "-", ":") + " 13:08:56"
		insertRetimeRow(t, pool, store, retimeRow{id: burst.gpsID, owner: alice, name: "P" + burst.day + "-gps.JPG",
			object: gpsPhoto(wall, 47.2), takenAt: burst.day + "T13:08:56Z", gpsSource: "exif", lat: 47.2, lng: 19})
		insertTrack(t, pool, alice, "exif", burst.day+"T13:08:56Z", 47.2, 19)
		insertRetimeRow(t, pool, store, retimeRow{id: burst.manualID, owner: alice, name: "P" + burst.day + "-hand.JPG",
			object: photo(wall), takenAt: burst.day + "T13:08:56Z", gpsSource: "manual", lat: 46, lng: 18})
		insertTrack(t, pool, alice, "manual", burst.day+"T13:08:56Z", 46, 18)
	}

	// An EXIF-GPS photo read as UTC onto the second of a video whose location tag placed it.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P2.JPG", object: gpsPhoto("2021:06:06 13:08:56", 47.3),
		takenAt: "2021-06-06T13:08:56Z", gpsSource: "exif", lat: 47.3, lng: 19})
	insertTrack(t, pool, alice, "exif", "2021-06-06T13:08:56Z", 47.3, 19)
	video := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "20210606_150856.mp4", mediaType: "video",
		object: []byte("\x00\x00\x00\x18ftypmp42"), takenAt: "2021-06-06T13:08:56Z", gpsSource: "exif", lat: 40, lng: 10})

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, store), alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 8, Retimed: 6, Unchanged: 1, NotExif: 1}, res)
	assert.Equal(t, []string{
		"2021-06-03T11:08:56Z", "2021-06-04T11:08:56Z", "2021-06-05T11:08:56Z", "2021-06-06T11:08:56Z",
	}, retimeTrackTimes(t, pool, alice, "exif"), "no 'exif' point is left at an old second")
	assert.InDelta(t, 47.1, retimeTrackLat(t, pool, alice, "exif", "2021-06-03T11:08:56Z"), 1e-9)
	assert.InDelta(t, 47.3, retimeTrackLat(t, pool, alice, "exif", "2021-06-06T11:08:56Z"), 1e-9)
	assert.Equal(t, []string{
		"2021-06-03T13:08:56Z", // the correctly dated photo's own point stays
		"2021-06-04T11:08:56Z", "2021-06-05T11:08:56Z",
	}, retimeTrackTimes(t, pool, alice, "manual"))

	m := readRetimedMedia(t, pool, video)
	require.NotNil(t, m.lat)
	assert.Equal(t, "exif", *m.gpsSource, "the video keeps its tag's location")
	assert.InDelta(t, 40, *m.lat, 1e-9)
}

// heldMatcherDB is the pool for a matcher run that pauses at its first write until released: the
// matcher has listed its items and worked out that item's position, but not written it.
type heldMatcherDB struct {
	*pgxpool.Pool
	once             sync.Once
	reached, release chan struct{}
}

func (h *heldMatcherDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	h.once.Do(func() {
		close(h.reached)
		<-h.release
	})
	return h.Pool.Exec(ctx, sql, args...)
}

// A matcher run of the owner's (an upload, a track import, Re-match) can overlap retime. It writes
// only to rows still as it listed them: never a position worked out at the old time onto a retimed
// row, which every later run would keep since nothing brackets the new time, nor over a location
// set by hand meanwhile.
func TestRunRetime_Integration_OverlappingMatcherWritesNothingStale(t *testing.T) {
	pool := testdb.New(t)
	store := newRetimeHeads()
	alice := createUser(t, pool, "alice")

	// Matched near the wrong time; nothing is logged within the match window of the true one.
	insertTrack(t, pool, alice, "loom", "2018-10-25T15:05:00Z", 50, 20)
	retimed := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1022050.JPG",
		object:  exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2018:10:25 15:00:00"}),
		takenAt: "2018-10-25T15:00:00Z", gpsSource: "interpolated", lat: 50, lng: 20})
	// Not placed yet when the matcher lists it, then placed by hand.
	insertTrack(t, pool, alice, "loom", "2018-10-26T10:05:00Z", 51, 21)
	placed := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "clip.mp4", mediaType: "video",
		object: []byte("\x00\x00\x00\x18ftypmp42"), takenAt: "2018-10-26T10:00:00Z"})

	held := &heldMatcherDB{Pool: pool, reached: make(chan struct{}), release: make(chan struct{})}
	matcherDone := make(chan error, 1)
	go func() { matcherDone <- RunGPSMatcher(context.Background(), held, alice, time.Hour, true) }()
	select {
	case <-held.reached:
	case err := <-matcherDone:
		t.Fatalf("the matcher finished without writing: %v", err)
	}

	_, err := pool.Exec(context.Background(), `
		UPDATE media SET location = ST_SetSRID(ST_MakePoint(18, 46), 4326), gps_source = 'manual' WHERE id = $1`, placed)
	require.NoError(t, err)
	res, err := RunRetime(context.Background(), retimeDeps(t, pool, store), alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Retimed)
	close(held.release)
	require.NoError(t, <-matcherDone)

	m := readRetimedMedia(t, pool, retimed)
	assert.True(t, retimeUTC(t, "2018-10-25T13:00:00Z").Equal(m.takenAt))
	assert.Nil(t, m.gpsSource, "no position from the old time")
	assert.Nil(t, m.lat)
	m = readRetimedMedia(t, pool, placed)
	require.NotNil(t, m.gpsSource)
	assert.Equal(t, "manual", *m.gpsSource)
	assert.InDelta(t, 46, *m.lat, 1e-9)

	require.NoError(t, RunGPSMatcher(context.Background(), pool, alice, time.Hour, true))
	assert.Nil(t, readRetimedMedia(t, pool, retimed).lat, "and none later")
}

// A moved point takes its weight away from interpolations near the old time, including items that
// are never retimed, such as videos whose time was always right.
func TestRunRetime_Integration_NeighboursOfAMovedPointAreMatchedAgain(t *testing.T) {
	pool := testdb.New(t)
	store := newRetimeHeads()
	alice := createUser(t, pool, "alice")
	notExif := []byte("\x00\x00\x00\x18ftypmp42")

	// Placed by hand while it sat two hours late; its point is the only one around.
	insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "P1022050.JPG",
		object:  exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2019:07:01 12:00:00"}),
		takenAt: "2019-07-01T12:00:00Z", gpsSource: "manual", lat: 47, lng: 19})
	insertTrack(t, pool, alice, "manual", "2019-07-01T12:00:00Z", 47, 19)

	resting := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "a.mp4", mediaType: "video", object: notExif,
		takenAt: "2019-07-01T12:20:00Z", gpsSource: "interpolated", lat: 47, lng: 19})
	gaining := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "b.mp4", mediaType: "video", object: notExif,
		takenAt: "2019-07-01T10:10:00Z"})
	farAway := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "c.mp4", mediaType: "video", object: notExif,
		takenAt: "2019-07-01T15:00:00Z", gpsSource: "interpolated", lat: 10, lng: 10})

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, store), alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 4, Retimed: 1, NotExif: 3}, res)

	m := readRetimedMedia(t, pool, resting)
	assert.Nil(t, m.gpsSource, "nothing brackets 12:20 any more")
	assert.Nil(t, m.lat)

	m = readRetimedMedia(t, pool, gaining)
	require.NotNil(t, m.gpsSource)
	assert.Equal(t, "interpolated", *m.gpsSource)
	assert.InDelta(t, 47, *m.lat, 1e-9)

	m = readRetimedMedia(t, pool, farAway)
	require.NotNil(t, m.lat, "outside the match window of the old time: left as it was")
	assert.InDelta(t, 10, *m.lat, 1e-9)
}

// The owner's camera zone setting is today's camera zone for their items: retime moves the rows read
// in the server's zone into it. A zone named for an upload or a scan stays with its files, and another
// owner's setting never applies.
func TestRunRetime_Integration_CameraZoneSetting(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")
	store := newRetimeHeads()
	deps := retimeDeps(t, pool, store)
	tokyo := deps.cameraZone.In(mustLoadLocation(t, "Asia/Tokyo"), ingest.CameraZoneFromBatch)

	photo := func(dto string) []byte { return exiftest.JPEG(exiftest.Photo{DateTimeOriginal: dto}) }
	// What ingest recorded: the zone in effect, and who named it.
	ingested := func(object []byte, zone ingest.CameraZone) []byte {
		return newCaptureEvidence(captureReading{exif: ingest.ExtractExif(bytes.NewReader(object), zone)}, zone).encode()
	}
	inServerZone := photo("2024:01:10 12:00:00")
	serverRow := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "a.jpg", object: inServerZone,
		takenAt: "2024-01-10T11:00:00Z", evidence: ingested(inServerZone, deps.cameraZone)})
	// Ingested before migration 10: no evidence, so read in today's zone too.
	oldRow := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "b.jpg", object: photo("2023:07:01 12:00:00"),
		takenAt: "2023-07-01T10:00:00Z"})
	inBatchZone := photo("2024:01:11 12:00:00")
	batchRow := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "c.jpg", object: inBatchZone,
		takenAt: "2024-01-11T03:00:00Z", evidence: ingested(inBatchZone, tokyo)})
	bobRow := insertRetimeRow(t, pool, store, retimeRow{owner: bob, name: "a.jpg", object: inServerZone,
		takenAt: "2024-01-10T11:00:00Z", evidence: ingested(inServerZone, deps.cameraZone)})

	_, err := pool.Exec(ctx, `UPDATE users SET camera_timezone = 'America/New_York' WHERE id = $1`, alice)
	require.NoError(t, err)

	var changes []RetimeChange
	res, err := RunRetime(ctx, deps, alice, true, collectRetime(&changes))
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 3, Retimed: 2, Unchanged: 1}, res)
	require.Len(t, changes, 2)
	assert.Equal(t, oldRow, changes[0].MediaID)
	assert.Equal(t, serverRow, changes[1].MediaID)

	assert.True(t, retimeUTC(t, "2024-01-10T17:00:00Z").Equal(readRetimedMedia(t, pool, serverRow).takenAt), "read in New York")
	assert.True(t, retimeUTC(t, "2023-07-01T16:00:00Z").Equal(readRetimedMedia(t, pool, oldRow).takenAt), "New York's summer time")
	ev := retimedEvidenceOf(t, pool, serverRow)
	assert.Equal(t, "retime", ev["by"])
	assert.Equal(t, "America/New_York", ev["camera_zone"])
	assert.Equal(t, "user", ev["camera_zone_from"])
	assert.Equal(t, -300.0, ev["offset_used_min"])

	assert.True(t, retimeUTC(t, "2024-01-11T03:00:00Z").Equal(readRetimedMedia(t, pool, batchRow).takenAt), "its upload's zone stays")
	assert.Equal(t, "ingest", retimedEvidenceOf(t, pool, batchRow)["by"])

	assert.NotContains(t, store.readKeys(), retimeKey(bob, "a.jpg"))
	res, err = RunRetime(ctx, deps, bob, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 1, Unchanged: 1}, res, "alice's zone never applies to bob's files")
	assert.True(t, retimeUTC(t, "2024-01-10T11:00:00Z").Equal(readRetimedMedia(t, pool, bobRow).takenAt))

	t.Run("a second apply changes nothing, the retimed row now being in the owner's zone", func(t *testing.T) {
		before := retimeSnapshot(t, pool)
		res, err := RunRetime(ctx, deps, alice, true, func(RetimeChange) {})
		require.NoError(t, err)
		assert.Equal(t, RetimeResult{Checked: 3, Unchanged: 3}, res)
		assert.Equal(t, before, retimeSnapshot(t, pool))
	})

	t.Run("a stored zone this server cannot load stops the run before any read", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE users SET camera_timezone = 'Gone/Zone' WHERE id = $1`, alice)
		require.NoError(t, err)
		reads := len(store.readKeys())
		_, err = RunRetime(ctx, deps, alice, true, func(RetimeChange) {})
		assert.ErrorContains(t, err, `unknown time zone "Gone/Zone"`)
		assert.Len(t, store.readKeys(), reads)
	})
}

// A capture time the owner set or shifted is theirs. Retime leaves the row alone before reading its
// object, even when the stored time is the EXIF wall clock at another offset, which it would move on
// an unedited row. Such a row is counted as edited, in a dry run too, and its neighbours still move.
func TestRunRetime_Integration_EditedRowsAreLeftAlone(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	ctx := context.Background()
	deps := retimeDeps(t, pool, l.store)
	router := newOwnershipRouter(pool, &fakeObjectStore{})

	// Stored as UTC like the rest of alice's library, then set by hand through the route to the EXIF
	// wall clock at +01:00 (14:00Z), where Warsaw's summer time would put it at 13:00Z. Its EXIF point
	// moves with it.
	setByHand := insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "set-by-hand.jpg",
		object:  exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2018:10:24 15:00:00", GPS: &exiftest.GPS{Lat: 47.7, Lng: 19.2}}),
		takenAt: "2018-10-24T15:00:00Z", gpsSource: "exif", lat: 47.7, lng: 19.2})
	insertTrack(t, pool, l.alice, "exif", "2018-10-24T15:00:00Z", 47.7, 19.2)
	rec := do(t, router, l.alice, http.MethodPatch, "/api/media/"+setByHand+"/taken-at",
		`{"wall":"2018-10-24T15:00:00","utc_offset_min":60}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, retimeUTC(t, "2018-10-24T14:00:00Z").Equal(readRetimedMedia(t, pool, setByHand).takenAt))

	// Shifted an hour earlier, which left it at the EXIF wall clock read as UTC.
	shifted := insertRetimeRow(t, pool, l.store, retimeRow{owner: l.alice, name: "shifted.jpg",
		object: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2018:10:26 16:00:00"}), takenAt: "2018-10-26T16:00:00Z"})
	const shiftEdit = `{"v":1,"kind":"shift","at":"2026-11-15T19:00:00Z","from":"2018-10-26T17:00:00Z",` +
		`"original":"2018-10-26T17:00:00Z","delta_s":-3600,"wall":null,"zone":null,"offset_min":null}`
	_, err := pool.Exec(ctx, `UPDATE media SET time_edit = $2::jsonb WHERE id = $1`, shifted, shiftEdit)
	require.NoError(t, err)

	editedKeys := []string{retimeKey(l.alice, "set-by-hand.jpg"), retimeKey(l.alice, "shifted.jpg")}
	want := RetimeResult{Checked: 11, Retimed: 5, Unchanged: 1, NotExif: 2, Edited: 2, Mismatch: 1}

	t.Run("a dry run counts them", func(t *testing.T) {
		before := retimeSnapshot(t, pool)
		var ids []string
		res, err := RunRetime(ctx, deps, l.alice, false, func(c RetimeChange) { ids = append(ids, c.MediaID) })
		require.NoError(t, err)
		assert.Equal(t, want, res)
		assert.Equal(t, []string{l.interpolated, l.manual, l.exifGPS, l.noTrack, l.abroad}, ids)
		for _, key := range editedKeys {
			assert.NotContains(t, l.store.readKeys(), key, "an edited row's object is never read")
		}
		assert.Equal(t, before, retimeSnapshot(t, pool))
	})

	editedBefore := map[string]retimedMedia{setByHand: readRetimedMedia(t, pool, setByHand), shifted: readRetimedMedia(t, pool, shifted)}
	var setEdit string
	require.NoError(t, pool.QueryRow(ctx, `SELECT time_edit::text FROM media WHERE id = $1`, setByHand).Scan(&setEdit))

	res, err := RunRetime(ctx, deps, l.alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, want, res)
	for _, key := range editedKeys {
		assert.NotContains(t, l.store.readKeys(), key, "an edited row's object is never read")
	}
	for id, was := range editedBefore {
		got := readRetimedMedia(t, pool, id)
		assert.True(t, was.takenAt.Equal(got.takenAt), "taken_at stays where its owner put it: %s", id)
		assert.Equal(t, was.evidence, got.evidence, "no retime evidence: %s", id)
	}
	var gotEdit string
	require.NoError(t, pool.QueryRow(ctx, `SELECT time_edit::text FROM media WHERE id = $1`, setByHand).Scan(&gotEdit))
	assert.JSONEq(t, setEdit, gotEdit)
	assert.Equal(t, []string{"2018-10-23T10:10:00Z", "2018-10-24T14:00:00Z", "2023-05-01T09:00:00Z"},
		retimeTrackTimes(t, pool, l.alice, "exif"), "the edited photo's point stays at its time, the unedited one's moves")
	assert.True(t, retimeUTC(t, "2018-10-23T09:50:53Z").Equal(readRetimedMedia(t, pool, l.interpolated).takenAt),
		"an unedited neighbour is still retimed")

	t.Run("positive control: without time_edit the same rows would move", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE media SET time_edit = NULL WHERE id = ANY($1)`, []string{setByHand, shifted})
		require.NoError(t, err)

		var changes []RetimeChange
		res, err := RunRetime(ctx, deps, l.alice, false, collectRetime(&changes))
		require.NoError(t, err)
		assert.Equal(t, RetimeResult{Checked: 11, Retimed: 2, Unchanged: 6, NotExif: 2, Mismatch: 1}, res)
		require.Len(t, changes, 2)
		assert.Equal(t, setByHand, changes[0].MediaID)
		assert.True(t, retimeUTC(t, "2018-10-24T13:00:00Z").Equal(changes[0].New))
		assert.Equal(t, shifted, changes[1].MediaID)
		assert.True(t, retimeUTC(t, "2018-10-26T14:00:00Z").Equal(changes[1].New))
	})
}

// An edit made between the listing and the update is the owner's too: applyRetime's lock finds the
// row changed and writes nothing, even when the edits left taken_at where it was listed. A re-run
// counts the row as edited and does not read it.
func TestRunRetime_Integration_EditAfterListingIsNotOverwritten(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	alice := createUser(t, pool, "alice")
	store := newRetimeHeads()
	deps := retimeDeps(t, pool, store)
	const name = "P1011841.JPG"
	id := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: name,
		object:  exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", GPS: &exiftest.GPS{Lat: 47.6, Lng: 19.1}}),
		takenAt: "2018-10-23T11:50:53Z", gpsSource: "exif", lat: 47.6, lng: 19.1})
	insertTrack(t, pool, alice, "exif", "2018-10-23T11:50:53Z", 47.6, 19.1)
	const edit = `{"v":1,"kind":"set","at":"2026-11-15T19:00:00Z","from":"2018-10-23T10:50:53Z",` +
		`"original":"2018-10-23T11:50:53Z","delta_s":3600,"wall":"2018-10-23T11:50:53","zone":null,"offset_min":0}`
	setEdit := func(t *testing.T, value *string) {
		t.Helper()
		_, err := pool.Exec(ctx, `UPDATE media SET time_edit = $2::jsonb WHERE id = $1`, id, value)
		require.NoError(t, err)
	}

	t.Run("applyRetime", func(t *testing.T) {
		items, err := retimeCandidates(ctx, pool, alice)
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.False(t, items[0].edited)
		exifData, found, err := rereadExifTime(ctx, store, items[0], deps.cameraZone)
		require.NoError(t, err)
		require.True(t, found)
		require.True(t, retimeUTC(t, "2018-10-23T09:50:53Z").Equal(*exifData.TakenAt), "an unedited row would move")

		setEdit(t, ptr(edit))
		before := retimeSnapshot(t, pool)
		change := RetimeChange{MediaID: id, Filename: name, Old: items[0].takenAt, New: *exifData.TakenAt,
			Zone: exifData.TakenAtZone, GPSSource: items[0].gpsSource}
		err = applyRetime(ctx, pool, alice, change, captureReading{exif: exifData}, deps.cameraZone, deps.matchWindow)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the row changed since it was read")
		assert.Equal(t, before, retimeSnapshot(t, pool), "nothing written")
	})

	t.Run("RunRetime fails the row, and a re-run counts it as edited", func(t *testing.T) {
		setEdit(t, nil)
		logs := captureLogs(t)
		// The owner's edit lands while retime reads the object, after the listing.
		store.onRead = func(string) { setEdit(t, ptr(edit)) }

		res, err := RunRetime(ctx, deps, alice, true, func(RetimeChange) { t.Error("nothing is retimed") })
		require.NoError(t, err)
		assert.Equal(t, RetimeResult{Checked: 1, Failed: 1}, res)
		assert.Contains(t, logs.String(), "retime: update failed")
		assert.Contains(t, logs.String(), "the row changed since it was read")
		m := readRetimedMedia(t, pool, id)
		assert.True(t, retimeUTC(t, "2018-10-23T11:50:53Z").Equal(m.takenAt))
		assert.Nil(t, m.evidence)
		assert.Equal(t, []string{"2018-10-23T11:50:53Z"}, retimeTrackTimes(t, pool, alice, "exif"))

		store.onRead = nil
		reads := len(store.readKeys())
		res, err = RunRetime(ctx, deps, alice, true, func(RetimeChange) { t.Error("nothing is retimed") })
		require.NoError(t, err)
		assert.Equal(t, RetimeResult{Checked: 1, Edited: 1}, res)
		assert.Len(t, store.readKeys(), reads, "the re-run reads nothing")
		assert.True(t, retimeUTC(t, "2018-10-23T11:50:53Z").Equal(readRetimedMedia(t, pool, id).takenAt))
	})
}

// TestRunRetime_Integration_FilenameTime repairs rows stored before ingest read file names: edited
// copies whose EXIF was stripped, dated by the day they were copied, take the date and time their
// name holds, in the camera zone, and join the named trip of those days. A row dated from its name
// follows a new camera zone. Only a time that came from the name or from the file time is replaced,
// and a video recorded nothing that says which, unless its evidence does.
func TestRunRetime_Integration_FilenameTime(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	store := newRetimeHeads()
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")
	stripped := exiftest.JPEG(exiftest.Photo{})
	const copied = "2022-12-13T17:05:23.86Z" // the day the copies were made
	ingestedWith := func(src, takenAt string, extra map[string]any) []byte {
		ev := map[string]any{"v": 1, "by": "ingest", "src": src, "camera_zone": "server zone (unset)", "camera_zone_from": "server",
			"file_mtime": takenAt, "creation_time": nil, "transcoded": false}
		for k, v := range extra {
			ev[k] = v
		}
		b, err := json.Marshal(ev)
		require.NoError(t, err)
		return b
	}
	trip := insertTrip(t, pool, alice, "Summer 2012", true, "2012-08-24", "2012-08-28")

	// Rows ingested before migration 10 recorded no evidence: a photo without an EXIF time could only
	// be dated by its file time.
	edited := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "2012-08-24_18-14-24_124.jpg", object: stripped, takenAt: copied})
	located := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "2012-08-25_10-43-33_312.jpg",
		object:  exiftest.JPEG(exiftest.Photo{GPS: &exiftest.GPS{Lat: 46.5, Lng: 8.0}}),
		takenAt: "2022-12-13T17:05:24.19Z", gpsSource: "exif", lat: 46.5, lng: 8.0})
	insertTrack(t, pool, alice, "exif", "2022-12-13T17:05:24.19Z", 46.5, 8.0)
	// Recorded as dated by the file time, which the stored time still is.
	recorded := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "20120826_162650.jpg", object: stripped,
		takenAt: "2022-12-13T17:05:25.02Z", evidence: ingestedWith("mtime", "2022-12-13T17:05:25.02Z", nil)})
	videoRecorded := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "VID_20120827_101246.mp4", mediaType: "video",
		object: []byte("not read"), takenAt: "2022-12-13T17:05:26.42Z", evidence: ingestedWith("mtime", "2022-12-13T17:05:26.42Z", nil)})
	// Dated from its name in UTC, before the owner's zone was known: it follows Warsaw today.
	namedInUTC := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_20120827_091022.jpg", object: stripped,
		takenAt: "2012-08-27T09:10:22Z",
		evidence: ingestedWith("filename", "2022-12-13T17:05:26.08Z", map[string]any{"zone": "camera", "offset_used_min": 0,
			"name_wall": "2012-08-27 09:10:22"})})

	// Left alone.
	movedSince := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "20120828_154313.jpg", object: stripped,
		takenAt: "2023-01-01T00:00:00Z", evidence: ingestedWith("mtime", "2022-12-13T17:05:27.44Z", nil)})
	namedMovedSince := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG_20120828_100000.jpg", object: stripped,
		takenAt:  "2012-08-28T10:07:00Z",
		evidence: ingestedWith("filename", "2022-12-13T17:05:28Z", map[string]any{"zone": "camera", "offset_used_min": 0})})
	videoUnrecorded := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "VID_20120825_120000.mp4", mediaType: "video",
		object: []byte("not read"), takenAt: "2012-08-25T10:00:02Z"})
	container := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "VID_20120825_130000.mp4", mediaType: "video",
		object: []byte("not read"), takenAt: "2012-08-25T11:00:02Z",
		evidence: ingestedWith("container", "2022-12-13T17:05:29Z", map[string]any{"creation_time": "2012-08-25T11:00:02Z"})})
	exifWins := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "2012-08-24_18-21-56_198.jpg",
		object:  exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "2012:08:24 18:21:54"}),
		takenAt: "2012-08-24T16:21:54Z"})
	dateOnly := insertRetimeRow(t, pool, store, retimeRow{owner: alice, name: "IMG-20120828-WA0001.jpg", object: stripped,
		takenAt: "2022-12-13T17:05:30Z"})
	bobsCopy := insertRetimeRow(t, pool, store, retimeRow{owner: bob, name: "2012-08-24_18-14-24_124.jpg", object: stripped, takenAt: copied})

	var changes []RetimeChange
	res, err := RunRetime(ctx, retimeDeps(t, pool, store), alice, true, collectRetime(&changes))
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 11, Retimed: 5, Unchanged: 1, NotExif: 3, Mismatch: 2}, res)

	type reported struct{ id, old, new, src, zone string }
	var got []reported
	for _, c := range changes {
		assert.True(t, c.FromName())
		got = append(got, reported{c.MediaID, c.Old.UTC().Format(time.RFC3339Nano), c.New.UTC().Format(time.RFC3339), c.Src, c.Zone})
	}
	assert.Equal(t, []reported{
		{namedInUTC, "2012-08-27T09:10:22Z", "2012-08-27T07:10:22Z", "filename", "camera"},
		{edited, copied, "2012-08-24T16:14:24Z", "filename", "camera"},
		{located, "2022-12-13T17:05:24.19Z", "2012-08-25T08:43:33Z", "filename", "camera"},
		{recorded, "2022-12-13T17:05:25.02Z", "2012-08-26T14:26:50Z", "filename", "camera"},
		{videoRecorded, "2022-12-13T17:05:26.42Z", "2012-08-27T08:12:46Z", "filename", "camera"},
	}, got, "in stored taken_at order")
	reads := store.readKeys()
	for _, name := range []string{"VID_20120827_101246.mp4", "VID_20120825_120000.mp4", "VID_20120825_130000.mp4"} {
		assert.NotContains(t, reads, retimeKey(alice, name), "a video is never read without a rule that dates it from its EXIF")
	}
	assert.NotContains(t, reads, retimeKey(bob, "2012-08-24_18-14-24_124.jpg"))

	// The copies join the named trip of their days.
	for _, id := range []string{edited, located, recorded, videoRecorded, namedInUTC} {
		m := readRetimedMedia(t, pool, id)
		require.NotNil(t, m.tripID, id)
		assert.Equal(t, trip, *m.tripID, id)
	}

	// The evidence says the time came from the name, read in today's zone, and what it replaced. A row
	// without evidence recorded no file time; a recorded one keeps it.
	ev := retimedEvidenceOf(t, pool, edited)
	assert.Equal(t, "retime", ev["by"])
	assert.Equal(t, copied, ev["retimed_from"])
	assert.Equal(t, "filename", ev["src"])
	assert.Equal(t, "camera", ev["zone"])
	assert.Equal(t, 120.0, ev["offset_used_min"])
	assert.Equal(t, "2012-08-24 18:14:24", ev["name_wall"])
	assert.Equal(t, "Europe/Warsaw", ev["camera_zone"])
	assert.Nil(t, ev["file_mtime"])
	assert.Nil(t, ev["wall"])
	ev = retimedEvidenceOf(t, pool, recorded)
	assert.Equal(t, "filename", ev["src"])
	assert.Equal(t, "2022-12-13T17:05:25.02Z", ev["file_mtime"])
	assert.Equal(t, "2012-08-26 16:26:50", ev["name_wall"])

	// A photo's EXIF position keeps its location, and its track point moves with it.
	m := readRetimedMedia(t, pool, located)
	require.NotNil(t, m.gpsSource)
	assert.Equal(t, "exif", *m.gpsSource)
	assert.InDelta(t, 46.5, *m.lat, 1e-9)
	assert.Equal(t, []string{"2012-08-25T08:43:33Z"}, retimeTrackTimes(t, pool, alice, "exif"))

	// Times that did not come from the name or the file time keep them.
	for id, want := range map[string]string{
		movedSince: "2023-01-01T00:00:00Z", namedMovedSince: "2012-08-28T10:07:00Z", videoUnrecorded: "2012-08-25T10:00:02Z",
		container: "2012-08-25T11:00:02Z", exifWins: "2012-08-24T16:21:54Z", dateOnly: "2022-12-13T17:05:30Z",
	} {
		assert.True(t, retimeUTC(t, want).Equal(readRetimedMedia(t, pool, id).takenAt), want)
	}
	b := readRetimedMedia(t, pool, bobsCopy)
	assert.True(t, retimeUTC(t, copied).Equal(b.takenAt), "another owner's copy stays")
	assert.Nil(t, b.evidence)

	// A second run finds nothing to change.
	before := retimeSnapshot(t, pool)
	changes = nil
	res, err = RunRetime(ctx, retimeDeps(t, pool, store), alice, true, collectRetime(&changes))
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 11, Unchanged: 6, NotExif: 3, Mismatch: 2}, res)
	assert.Empty(t, changes)
	assert.Equal(t, before, retimeSnapshot(t, pool))
}
