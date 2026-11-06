package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"codeberg.org/bvigh/geogallery/internal/ingest"
	"codeberg.org/bvigh/geogallery/internal/ingest/exiftest"
	"codeberg.org/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHeads serves object heads from memory. onRead runs before each read.
type fakeHeads struct {
	objects map[string][]byte
	fail    map[string]bool
	onRead  func(key string)
}

func (f *fakeHeads) ReadHead(_ context.Context, key string, n int64) ([]byte, error) {
	if f.onRead != nil {
		f.onRead(key)
	}
	if f.fail[key] {
		return nil, errors.New("storage unavailable")
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("no such key %s", key)
	}
	return b[:min(int64(len(b)), n)], nil
}

type retimeRow struct {
	owner, name, mediaType string
	object                 []byte
	takenAt                string // RFC3339
	gpsSource              string // "", "exif", "manual", "interpolated"
	lat, lng               float64
}

func insertRetimeRow(t *testing.T, pool *pgxpool.Pool, store *fakeHeads, r retimeRow) string {
	t.Helper()
	key := "media/" + r.owner + "/" + r.name
	store.objects[key] = r.object
	if r.mediaType == "" {
		r.mediaType = "photo"
	}
	var source *string
	if r.gpsSource != "" {
		source = &r.gpsSource
	}
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, taken_at, gps_source, location)
		VALUES ($1, $2, $3, $4, $5, $6::timestamptz, $7,
		        CASE WHEN $7::text IS NOT NULL THEN ST_SetSRID(ST_MakePoint($9, $8), 4326) END)
		RETURNING id::text`,
		r.owner, r.name, r.mediaType, key, "hash-"+key, r.takenAt, source, r.lat, r.lng).Scan(&id))
	return id
}

type retimedMedia struct {
	takenAt   time.Time
	gpsSource *string
	lat, lng  *float64
	tripID    *string
}

func readRetimedMedia(t *testing.T, pool *pgxpool.Pool, id string) retimedMedia {
	t.Helper()
	var m retimedMedia
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT taken_at, gps_source, ST_Y(location::geometry), ST_X(location::geometry), trip_id::text
		FROM media WHERE id = $1`, id).Scan(&m.takenAt, &m.gpsSource, &m.lat, &m.lng, &m.tripID))
	return m
}

func trackTimes(t *testing.T, pool *pgxpool.Pool, ownerID, source string) []string {
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

// librarySnapshot renders every media and track row, to show a run wrote nothing. Trip detection
// re-creates auto trips under new IDs, so a row shows only whether it has a trip.
func librarySnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id::text || ' ' || coalesce(taken_at::text, '-') || ' ' || coalesce(gps_source, '-') || ' ' ||
		        coalesce(ST_AsText(location::geometry), '-') || ' trip=' || (trip_id IS NOT NULL)::text FROM media ORDER BY id`,
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

func utc(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
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
	store                                          *fakeHeads
}

func seedRetimeLibrary(t *testing.T, pool *pgxpool.Pool) retimeLibrary {
	t.Helper()
	l := retimeLibrary{store: &fakeHeads{objects: map[string][]byte{}, fail: map[string]bool{}}}
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

func retimeDeps(t *testing.T, pool *pgxpool.Pool, store *fakeHeads) RetimeDeps {
	t.Helper()
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	return RetimeDeps{pool: pool, storage: store, cameraZone: warsaw, matchWindow: time.Hour, tripGapHours: 24}
}

func TestRunRetime_Integration_Apply(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	ctx := context.Background()

	var changes []RetimeChange
	res, err := RunRetime(ctx, retimeDeps(t, pool, l.store), l.alice, true, func(c RetimeChange) { changes = append(changes, c) })
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 5, Unchanged: 1, NotExif: 2, Mismatch: 1}, res)

	type reported struct{ id, old, new, zone, gpsSource string }
	var got []reported
	for _, c := range changes {
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
	assert.True(t, utc(t, "2018-10-23T09:50:53Z").Equal(m.takenAt))
	require.NotNil(t, m.gpsSource)
	assert.Equal(t, "interpolated", *m.gpsSource)
	require.NotNil(t, m.lat)
	assert.InDelta(t, 48.06, *m.lat, 0.01)
	assert.InDelta(t, 15.06, *m.lng, 0.01)

	// A manual location stays, and its track point moves with the photo.
	m = readRetimedMedia(t, pool, l.manual)
	assert.True(t, utc(t, "2018-10-23T10:00:00Z").Equal(m.takenAt))
	require.NotNil(t, m.gpsSource)
	assert.Equal(t, "manual", *m.gpsSource)
	assert.InDelta(t, 47.5, *m.lat, 1e-9)
	assert.InDelta(t, 19.0, *m.lng, 1e-9)
	assert.Equal(t, []string{"2018-10-23T10:00:00Z"}, trackTimes(t, pool, l.alice, "manual"))

	// EXIF positions stay, and their track points move; the GPS clock placed the Tokyo photo.
	m = readRetimedMedia(t, pool, l.exifGPS)
	assert.True(t, utc(t, "2018-10-23T10:10:00Z").Equal(m.takenAt))
	assert.Equal(t, "exif", *m.gpsSource)
	assert.InDelta(t, 47.6, *m.lat, 1e-6)
	assert.True(t, utc(t, "2023-05-01T09:00:00Z").Equal(readRetimedMedia(t, pool, l.abroad).takenAt))
	assert.Equal(t, []string{"2018-10-23T10:10:00Z", "2023-05-01T09:00:00Z"}, trackTimes(t, pool, l.alice, "exif"))

	// The old interpolation is cleared when nothing brackets the true time.
	m = readRetimedMedia(t, pool, l.noTrack)
	assert.True(t, utc(t, "2018-10-25T13:00:00Z").Equal(m.takenAt))
	assert.Nil(t, m.gpsSource)
	assert.Nil(t, m.lat)

	// Rows not dated from this EXIF keep their time.
	for id, want := range map[string]string{
		l.correct: "2018-10-23T11:00:00Z", l.video: "2018-10-23T10:30:00Z",
		l.fileTime: "2018-10-23T10:31:17.123Z", l.mismatch: "2018-10-23T14:07:00Z",
	} {
		assert.True(t, utc(t, want).Equal(readRetimedMedia(t, pool, id).takenAt), want)
	}

	// Trip detection ran for alice only; bob's rows and points are untouched.
	assert.NotNil(t, readRetimedMedia(t, pool, l.interpolated).tripID)
	b := readRetimedMedia(t, pool, l.bobPhoto)
	assert.True(t, utc(t, "2018-10-23T11:50:53Z").Equal(b.takenAt))
	assert.InDelta(t, 50, *b.lat, 1e-9)
	assert.Nil(t, b.tripID)
	assert.Equal(t, []string{"2018-10-23T12:00:00Z"}, trackTimes(t, pool, l.bob, "manual"))
	assert.Equal(t, []string{"2018-10-23T12:10:00Z"}, trackTimes(t, pool, l.bob, "exif"))

	// A second run finds nothing to change.
	before := librarySnapshot(t, pool)
	changes = nil
	res, err = RunRetime(ctx, retimeDeps(t, pool, l.store), l.alice, true, func(c RetimeChange) { changes = append(changes, c) })
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Unchanged: 6, NotExif: 2, Mismatch: 1}, res)
	assert.Empty(t, changes)
	assert.Equal(t, before, librarySnapshot(t, pool))
}

// TestRunRetime_Integration_OffsetTag repairs what ingest stored before it read OffsetTimeOriginal:
// a phone's photo abroad moves, with its track point. The Panasonic that writes the same offset and
// a phone at home stay, and so does a row whose EXIF ingest could not read and that holds a file
// time. A Sony whose Exif IFD goexif rejects moves when its stored time is its IFD0 clock in a zone.
func TestRunRetime_Integration_OffsetTag(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	alice := createUser(t, pool, "alice")
	store := &fakeHeads{objects: map[string][]byte{}, fail: map[string]bool{}}

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

	assert.True(t, utc(t, "2025-07-14T14:30:00Z").Equal(readRetimedMedia(t, pool, london).takenAt))
	assert.Equal(t, []string{"2025-07-14T14:30:00Z"}, trackTimes(t, pool, alice, "exif"), "the track point moves with the photo")
	assert.True(t, utc(t, "2009-04-15T08:20:30Z").Equal(readRetimedMedia(t, pool, sonyInZone).takenAt))

	assert.True(t, utc(t, "2018-07-23T09:50:53Z").Equal(readRetimedMedia(t, pool, panasonic).takenAt))
	assert.True(t, utc(t, "2026-01-01T11:00:00Z").Equal(readRetimedMedia(t, pool, home).takenAt))
	assert.True(t, utc(t, "2010-02-03T14:31:18Z").Equal(readRetimedMedia(t, pool, sonyFileTime).takenAt))
}

func TestRunRetime_Integration_DryRunWritesNothing(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	before := librarySnapshot(t, pool)

	var ids []string
	res, err := RunRetime(context.Background(), retimeDeps(t, pool, l.store), l.alice, false, func(c RetimeChange) { ids = append(ids, c.MediaID) })
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 5, Unchanged: 1, NotExif: 2, Mismatch: 1}, res)
	assert.Equal(t, []string{l.interpolated, l.manual, l.exifGPS, l.noTrack, l.abroad}, ids)
	assert.Equal(t, before, librarySnapshot(t, pool))
}

func TestRunRetime_Integration_ReadFailureSkipsTheItem(t *testing.T) {
	pool := testdb.New(t)
	l := seedRetimeLibrary(t, pool)
	l.store.fail["media/"+l.alice+"/P1011821.JPG"] = true

	res, err := RunRetime(context.Background(), retimeDeps(t, pool, l.store), l.alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 4, Unchanged: 1, NotExif: 2, Mismatch: 1, Failed: 1}, res)
	assert.True(t, utc(t, "2018-10-23T12:00:00Z").Equal(readRetimedMedia(t, pool, l.manual).takenAt))
	assert.Equal(t, []string{"2018-10-23T12:00:00Z"}, trackTimes(t, pool, l.alice, "manual"))
	assert.True(t, utc(t, "2018-10-23T09:50:53Z").Equal(readRetimedMedia(t, pool, l.interpolated).takenAt))
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
	assert.True(t, utc(t, "2018-10-23T09:50:53Z").Equal(m.takenAt))
	assert.Nil(t, m.lat, "cleared, and the matcher did not run")
	assert.Nil(t, m.tripID, "trip detection did not run")
	assert.True(t, utc(t, "2018-10-23T12:00:00Z").Equal(readRetimedMedia(t, pool, l.manual).takenAt))

	l.store.onRead = nil
	res, err = RunRetime(context.Background(), retimeDeps(t, pool, l.store), l.alice, true, func(RetimeChange) {})
	require.NoError(t, err)
	assert.Equal(t, RetimeResult{Checked: 9, Retimed: 4, Unchanged: 2, NotExif: 2, Mismatch: 1}, res)
	m = readRetimedMedia(t, pool, l.interpolated)
	require.NotNil(t, m.lat, "the re-run's matcher places the item retimed before the interrupt")
	assert.InDelta(t, 48.06, *m.lat, 0.01)
	assert.NotNil(t, m.tripID)
}

// An old track point stays while another item is still at the old time: it may be that item's.
func TestRunRetime_Integration_SharedOldTime(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeHeads{objects: map[string][]byte{}}
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
	}, trackTimes(t, pool, alice, "manual"))
}

func trackLat(t *testing.T, pool *pgxpool.Pool, ownerID, source, at string) float64 {
	t.Helper()
	var lat float64
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT ST_Y(location::geometry) FROM gps_tracks
		WHERE owner_id = $1 AND source = $2 AND recorded_at = $3::timestamptz`, ownerID, source, at).Scan(&lat))
	return lat
}

// Burst photos with one EXIF time share one 'exif' point (ingest's ON CONFLICT keeps the first
// position). It moves once, with one of their own positions.
func TestRunRetime_Integration_BurstSharesOnePoint(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeHeads{objects: map[string][]byte{}}
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
	assert.Equal(t, []string{"2026-09-10T17:17:36Z"}, trackTimes(t, pool, alice, "exif"))
	lat := trackLat(t, pool, alice, "exif", "2026-09-10T17:17:36Z")
	assert.True(t, math.Abs(lat-48.89518) < 1e-6 || math.Abs(lat-48.89503) < 1e-6, "one of the photos' positions, got %v", lat)
}

// A retimed item can sit at the same second as an item that does not own its old point. The old
// point must then go, or it would stay an hour or two from where it was recorded.
func TestRunRetime_Integration_UnrelatedItemAtOldTime(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeHeads{objects: map[string][]byte{}}
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
	}, trackTimes(t, pool, alice, "exif"))
	assert.InDelta(t, 47.1, trackLat(t, pool, alice, "exif", "2021-06-03T11:08:56Z"), 1e-9)
	assert.InDelta(t, 47.5, trackLat(t, pool, alice, "exif", "2022-08-06T12:33:49Z"), 1e-9)
	assert.InDelta(t, 47.6, trackLat(t, pool, alice, "exif", "2022-08-06T14:33:49Z"), 1e-9)
	assert.Equal(t, []string{"2022-08-04T11:00:00Z"}, trackTimes(t, pool, alice, "manual"),
		"an EXIF-located photo at the old second does not keep a manual point")
}

// A moved point takes its weight away from interpolations near the old time, including items that
// are never retimed, such as videos whose time was always right.
func TestRunRetime_Integration_NeighboursOfAMovedPointAreMatchedAgain(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeHeads{objects: map[string][]byte{}}
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
