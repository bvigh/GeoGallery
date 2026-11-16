package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// placement is an item's location as the matcher leaves it, comparable as a whole.
type placement struct {
	source               string
	lat, lng, confidence float64
}

func (f *timeFixture) placement(t *testing.T, id string) placement {
	t.Helper()
	s := f.state(t, id)
	require.NotNil(t, s.gpsSource, "item %s has a location", id)
	require.NotNil(t, s.lat)
	require.NotNil(t, s.confidence)
	return placement{source: *s.gpsSource, lat: *s.lat, lng: *s.lng, confidence: *s.confidence}
}

// A location set by hand writes a manual track point at the item's time and runs the owner's matcher
// again over their interpolated items, so those whose bracket the new point enters are placed again
// from it. Items with an EXIF location are left alone, and so is everything of another user's. The
// match window is 1 h (newOwnershipRouter).
//
//	11:40     gpx point P1 (47.0, 19.0)
//	11:50     near1, interpolated; bob's interpolated item and a point of bob's at the same instant
//	12:00     the item set by hand: the new manual point M (47.2, 19.2)
//	12:15     a video located from its own tag, which makes no track point
//	12:30     near2, interpolated
//	13:00:01  beyond, interpolated, a second past M's window
//	13:45     gpx point P2 (47.0, 20.0)
//
// Before, near1 and near2 rest on P1 alone and beyond on P2 alone. M becomes near1's nearest point
// after it (placing it halfway to P1) and near2's nearest point before it (placing it on M). Nothing
// lies between beyond and M, so only the window keeps M out of beyond's bracket: were it not kept,
// beyond would move to between M and P2. Were bob's point read for alice, near1 would sit on it.
func TestUpdateLocation_PlacesNearbyInterpolatedItemsAgain(t *testing.T) {
	f := newTimeFixture(t)
	ctx := context.Background()
	insertTrack(t, f.pool, f.alice, "gpx:day.gpx", "2024-05-05T11:40:00Z", 47.0, 19.0)
	insertTrack(t, f.pool, f.alice, "gpx:day.gpx", "2024-05-05T13:45:00Z", 47.0, 20.0)
	insertTrack(t, f.pool, f.bob, "gpx:bob.gpx", "2024-05-05T11:50:00Z", 40.0, 10.0)

	item := f.item(t, timeItem{owner: f.alice, name: "SET.JPG", takenAt: "2024-05-05T12:00:00Z"})
	near1 := f.item(t, timeItem{owner: f.alice, name: "N1.JPG", takenAt: "2024-05-05T11:50:00Z",
		gpsSource: "interpolated", lat: 47.0, lng: 19.0})
	near2 := f.item(t, timeItem{owner: f.alice, name: "N2.JPG", takenAt: "2024-05-05T12:30:00Z",
		gpsSource: "interpolated", lat: 47.0, lng: 19.0})
	beyond := f.item(t, timeItem{owner: f.alice, name: "B.JPG", takenAt: "2024-05-05T13:00:01Z",
		gpsSource: "interpolated", lat: 47.0, lng: 20.0})
	tagged := f.item(t, timeItem{owner: f.alice, name: "TAG.MP4", mediaType: "video", takenAt: "2024-05-05T12:15:00Z",
		gpsSource: "exif", lat: budapestLat, lng: budapestLng})
	f.item(t, timeItem{owner: f.bob, name: "BOB.JPG", takenAt: "2024-05-05T11:50:00Z",
		gpsSource: "interpolated", lat: 40.0, lng: 10.0})

	// Settle every estimate as the matcher computes it, so a value that changes below was changed by
	// the new point and not by a stale fixture.
	require.NoError(t, RunGPSMatcher(ctx, f.pool, f.alice, time.Hour, true))
	require.NoError(t, RunGPSMatcher(ctx, f.pool, f.bob, time.Hour, true))
	for _, id := range []string{near1, near2} {
		p := f.placement(t, id)
		assert.Equal(t, "interpolated", p.source)
		assert.InDelta(t, 47.0, p.lat, 1e-9, "on P1 before the change")
		assert.InDelta(t, 19.0, p.lng, 1e-9)
	}
	beyondBefore := f.placement(t, beyond)
	assert.InDelta(t, 47.0, beyondBefore.lat, 1e-9, "on P2")
	assert.InDelta(t, 20.0, beyondBefore.lng, 1e-9)
	taggedBefore := f.placement(t, tagged)
	bobBefore := ownerSnapshot(t, f.pool, f.bob)

	rec := do(t, f.router, f.alice, http.MethodPatch, "/api/media/"+item+"/location", `{"lat":47.2,"lng":19.2}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assert.Equal(t, placement{source: "manual", lat: 47.2, lng: 19.2, confidence: 1}, f.placement(t, item))
	assert.Equal(t, []string{"2024-05-05T12:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "manual"),
		"a manual point at the item's time")

	p := f.placement(t, near1)
	assert.Equal(t, "interpolated", p.source)
	assert.InDelta(t, 47.1, p.lat, 1e-9, "halfway between P1 and M")
	assert.InDelta(t, 19.1, p.lng, 1e-9)

	p = f.placement(t, near2)
	assert.Equal(t, "interpolated", p.source)
	assert.InDelta(t, 47.2, p.lat, 1e-9, "on M, now its nearest point before it")
	assert.InDelta(t, 19.2, p.lng, 1e-9)
	assert.InDelta(t, 0.5, p.confidence, 1e-9, "half an hour from M in a 1 h window")

	assert.Equal(t, beyondBefore, f.placement(t, beyond), "a second past M's window: still on P2 alone")
	assert.Equal(t, taggedBefore, f.placement(t, tagged), "an EXIF location is never re-placed")
	assert.Equal(t, bobBefore, ownerSnapshot(t, f.pool, f.bob), "nothing of bob's moved, and bob got no point")
}
