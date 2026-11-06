package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"codeberg.org/bvigh/geogallery/internal/ingest"
	"codeberg.org/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// videoLibrary holds one video per case, each with a location tag unless its name says otherwise.
type videoLibrary struct {
	alice, bob                         string
	interpolated, unlocated            string // change
	manual, exif, dense, noTag, broken string // left alone
	photo, bobVideo                    string // never considered
	probed                             map[string]bool
	deps                               VideoLocationDeps
}

func seedVideoLibrary(t *testing.T, pool *pgxpool.Pool) videoLibrary {
	t.Helper()
	l := videoLibrary{probed: map[string]bool{}}
	l.alice = createUser(t, pool, "alice")
	l.bob = createUser(t, pool, "bob")
	store := &fakeHeads{objects: map[string][]byte{}, fail: map[string]bool{}}
	at := "2025-07-14T14:30:12Z"
	video := func(owner, name, source string, lat, lng float64) string {
		return insertRetimeRow(t, pool, store, retimeRow{owner: owner, name: name, mediaType: "video", object: []byte("x"),
			takenAt: at, gpsSource: source, lat: lat, lng: lng})
	}
	l.interpolated = video(l.alice, "interpolated.mp4", "interpolated", 51.5074, -0.1301)
	l.unlocated = video(l.alice, "unlocated.mp4", "", 0, 0)
	l.manual = video(l.alice, "manual.mp4", "manual", 47.5, 19.0)
	l.exif = video(l.alice, "exif.mp4", "exif", 47.6, 19.1)
	l.dense = insertRetimeRow(t, pool, store, retimeRow{owner: l.alice, name: "dense.mp4", mediaType: "video", object: []byte("x"),
		takenAt: "2025-07-15T08:00:00Z", gpsSource: "interpolated", lat: 46.0, lng: 8.0})
	_, err := pool.Exec(context.Background(), `UPDATE media SET gps_confidence = 0.9997 WHERE id = $1`, l.dense)
	require.NoError(t, err)
	insertTrack(t, pool, l.alice, "gpx", "2025-07-15T07:59:59Z", 46.0, 8.0)
	insertTrack(t, pool, l.alice, "gpx", "2025-07-15T08:00:01Z", 46.0, 8.0)
	l.noTag = video(l.alice, "notag.mp4", "interpolated", 48.0, 15.0)
	l.broken = video(l.alice, "broken.mp4", "interpolated", 48.1, 15.1)
	l.photo = insertRetimeRow(t, pool, store, retimeRow{owner: l.alice, name: "photo.jpg", object: []byte("x"),
		takenAt: at, gpsSource: "interpolated", lat: 48.2, lng: 15.2})
	l.bobVideo = video(l.bob, "bob.mp4", "interpolated", 10, 10)
	insertTrack(t, pool, l.alice, "loom", "2025-07-14T14:20:00Z", 51.51, -0.13)

	lat, lng := 51.5074, -0.1278
	l.deps = VideoLocationDeps{pool: pool, probe: func(_ context.Context, key string) (ingest.VideoProbeResult, error) {
		l.probed[key] = true
		switch key {
		case "media/" + l.alice + "/notag.mp4":
			return ingest.VideoProbeResult{}, nil
		case "media/" + l.alice + "/broken.mp4":
			return ingest.VideoProbeResult{}, errors.New("ffprobe failed")
		}
		return ingest.VideoProbeResult{Lat: &lat, Lng: &lng}, nil
	}}
	return l
}

func TestRunVideoLocations_Integration_Apply(t *testing.T) {
	pool := testdb.New(t)
	l := seedVideoLibrary(t, pool)
	ctx := context.Background()
	tracksBefore := trackTimes(t, pool, l.alice, "loom")

	changes := map[string]VideoLocationChange{}
	res, err := RunVideoLocations(ctx, l.deps, l.alice, true, func(c VideoLocationChange) { changes[c.MediaID] = c })
	require.NoError(t, err)
	assert.Equal(t, VideoLocationResult{Checked: 5, Located: 2, DenseLog: 1, NoTag: 1, Failed: 1}, res)
	require.Len(t, changes, 2)

	// Interpolated and unlocated videos take the tag, with what the dry-run table needs to judge it.
	require.NotNil(t, changes[l.interpolated].MovedM)
	assert.InDelta(t, 160, *changes[l.interpolated].MovedM, 10)
	assert.Equal(t, "interpolated", changes[l.interpolated].OldSource)
	assert.Nil(t, changes[l.unlocated].MovedM)
	assert.Equal(t, "", changes[l.unlocated].OldSource)
	for _, id := range []string{l.interpolated, l.unlocated} {
		m := readRetimedMedia(t, pool, id)
		require.NotNil(t, m.gpsSource)
		assert.Equal(t, "exif", *m.gpsSource)
		require.NotNil(t, m.lat)
		assert.InDelta(t, 51.5074, *m.lat, 1e-9)
		assert.InDelta(t, -0.1278, *m.lng, 1e-9)
		var confidence *float64
		require.NoError(t, pool.QueryRow(ctx, `SELECT gps_confidence FROM media WHERE id = $1`, id).Scan(&confidence))
		assert.Nil(t, confidence)
	}

	// A manual or EXIF location is not even looked at, and neither is a photo or another owner's video.
	for _, name := range []string{"manual.mp4", "exif.mp4", "photo.jpg"} {
		assert.False(t, l.probed["media/"+l.alice+"/"+name], name)
	}
	assert.False(t, l.probed["media/"+l.bob+"/bob.mp4"])
	assert.InDelta(t, 47.5, *readRetimedMedia(t, pool, l.manual).lat, 1e-9)
	assert.InDelta(t, 47.6, *readRetimedMedia(t, pool, l.exif).lat, 1e-9)
	assert.InDelta(t, 48.2, *readRetimedMedia(t, pool, l.photo).lat, 1e-9)
	assert.InDelta(t, 10, *readRetimedMedia(t, pool, l.bobVideo).lat, 1e-9)

	// A position from a GPS log recording during the clip beats the tag, and the file is not probed.
	assert.False(t, l.probed["media/"+l.alice+"/dense.mp4"])
	m := readRetimedMedia(t, pool, l.dense)
	assert.Equal(t, "interpolated", *m.gpsSource)
	assert.InDelta(t, 46.0, *m.lat, 1e-9)

	// No tag and a failed probe keep what they had.
	assert.InDelta(t, 48.0, *readRetimedMedia(t, pool, l.noTag).lat, 1e-9)
	assert.InDelta(t, 48.1, *readRetimedMedia(t, pool, l.broken).lat, 1e-9)

	// A tag makes no track point, so nothing else's location depends on it.
	assert.Equal(t, tracksBefore, trackTimes(t, pool, l.alice, "loom"))
	assert.Empty(t, trackTimes(t, pool, l.alice, "exif"))

	// The matcher now leaves the placed videos alone, and a second run finds nothing new.
	require.NoError(t, RunGPSMatcher(ctx, pool, l.alice, time.Hour, true))
	assert.InDelta(t, 51.5074, *readRetimedMedia(t, pool, l.interpolated).lat, 1e-9)
	before := librarySnapshot(t, pool)
	changes = map[string]VideoLocationChange{}
	res, err = RunVideoLocations(ctx, l.deps, l.alice, true, func(c VideoLocationChange) { changes[c.MediaID] = c })
	require.NoError(t, err)
	assert.Equal(t, VideoLocationResult{Checked: 3, DenseLog: 1, NoTag: 1, Failed: 1}, res)
	assert.Empty(t, changes)
	assert.Equal(t, before, librarySnapshot(t, pool))
}

func TestRunVideoLocations_Integration_DryRunWritesNothing(t *testing.T) {
	pool := testdb.New(t)
	l := seedVideoLibrary(t, pool)
	before := librarySnapshot(t, pool)

	var changes []VideoLocationChange
	res, err := RunVideoLocations(context.Background(), l.deps, l.alice, false, func(c VideoLocationChange) { changes = append(changes, c) })
	require.NoError(t, err)
	assert.Equal(t, VideoLocationResult{Checked: 5, Located: 2, DenseLog: 1, NoTag: 1, Failed: 1}, res)
	assert.Len(t, changes, 2)
	assert.Equal(t, before, librarySnapshot(t, pool))
}

func TestRunVideoLocations_Integration_ManualLocationSetDuringTheRunWins(t *testing.T) {
	pool := testdb.New(t)
	l := seedVideoLibrary(t, pool)
	ctx := context.Background()
	inner := l.deps.probe
	l.deps.probe = func(c context.Context, key string) (ingest.VideoProbeResult, error) {
		if key == "media/"+l.alice+"/interpolated.mp4" {
			_, err := pool.Exec(ctx, `
				UPDATE media SET location = ST_SetSRID(ST_MakePoint(19, 47), 4326), gps_source = 'manual' WHERE id = $1`, l.interpolated)
			require.NoError(t, err)
		}
		return inner(c, key)
	}

	res, err := RunVideoLocations(ctx, l.deps, l.alice, true, func(VideoLocationChange) {})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Failed, "the broken video and the row that changed")
	m := readRetimedMedia(t, pool, l.interpolated)
	assert.Equal(t, "manual", *m.gpsSource)
	assert.InDelta(t, 47, *m.lat, 1e-9)
}

func TestRunVideoLocations_Integration_Interrupted(t *testing.T) {
	pool := testdb.New(t)
	l := seedVideoLibrary(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RunVideoLocations(ctx, l.deps, l.alice, true, func(VideoLocationChange) {})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, res.Located)
}
