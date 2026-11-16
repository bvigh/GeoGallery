package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timeFixture is alice's and bob's libraries behind the real handlers, for the capture time routes.
type timeFixture struct {
	pool       *pgxpool.Pool
	router     http.Handler
	alice, bob string
}

func newTimeFixture(t *testing.T) *timeFixture {
	t.Helper()
	pool := testdb.New(t)
	f := &timeFixture{pool: pool, router: newOwnershipRouter(pool, &fakeObjectStore{})}
	f.alice = createUser(t, pool, "alice")
	f.bob = createUser(t, pool, "bob")
	return f
}

type timeItem struct {
	id          string // "" lets the database choose
	owner, name string
	mediaType   string // "" is a photo
	takenAt     string // RFC 3339; "" stores NULL
	gpsSource   string // "" stores no location; any other source stores lat, lng
	lat, lng    float64
	cameraModel string // "" stores NULL
	evidence    string // capture_evidence; "" stores NULL
}

func (f *timeFixture) item(t *testing.T, m timeItem) string {
	t.Helper()
	if m.mediaType == "" {
		m.mediaType = "photo"
	}
	key := "media/" + m.owner + "/" + m.name
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		INSERT INTO media (id, owner_id, filename, media_type, storage_key, file_hash, taken_at, gps_source, location,
		                   gps_confidence, camera_model, capture_evidence)
		VALUES (coalesce(NULLIF($12, '')::uuid, gen_random_uuid()), $1, $2, $3, $4, $5, NULLIF($6, '')::timestamptz,
		        NULLIF($7, ''), CASE WHEN $7 <> '' THEN ST_SetSRID(ST_MakePoint($9, $8), 4326) END,
		        CASE WHEN $7 <> '' THEN 0.5 END, NULLIF($10, ''), NULLIF($11, '')::jsonb)
		RETURNING id::text`,
		m.owner, m.name, m.mediaType, key, "hash-"+key, m.takenAt, m.gpsSource, m.lat, m.lng,
		m.cameraModel, m.evidence, m.id).Scan(&id))
	return id
}

type timeItemState struct {
	takenAt    *time.Time
	gpsSource  *string
	lat, lng   *float64
	confidence *float64
	tripID     *string
	timeEdit   []byte
	evidence   *string
}

func (f *timeFixture) state(t *testing.T, id string) timeItemState {
	t.Helper()
	var s timeItemState
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		SELECT taken_at, gps_source, ST_Y(location::geometry), ST_X(location::geometry), gps_confidence,
		       trip_id::text, time_edit, capture_evidence::text
		FROM media WHERE id = $1`, id).Scan(&s.takenAt, &s.gpsSource, &s.lat, &s.lng, &s.confidence,
		&s.tripID, &s.timeEdit, &s.evidence))
	return s
}

// takenAt returns an item's taken_at in RFC 3339 UTC, "" when it has none.
func (f *timeFixture) takenAt(t *testing.T, id string) string {
	t.Helper()
	at := f.state(t, id).takenAt
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// edit decodes an item's time_edit without its "at", which it checks lies between from and to.
func (f *timeFixture) edit(t *testing.T, id string, from, to time.Time) map[string]any {
	t.Helper()
	raw := f.state(t, id).timeEdit
	require.NotNil(t, raw, "time_edit is set")
	var e map[string]any
	require.NoError(t, json.Unmarshal(raw, &e))
	at := retimeUTC(t, e["at"].(string))
	assert.False(t, at.Before(from.Truncate(time.Second)), "at %s is when the change was made", at)
	assert.False(t, at.After(to))
	delete(e, "at")
	return e
}

func (f *timeFixture) set(t *testing.T, user, id, body string) setTakenAtResponse {
	t.Helper()
	rec := do(t, f.router, user, http.MethodPatch, "/api/media/"+id+"/taken-at", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp setTakenAtResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func (f *timeFixture) shiftRequest(t *testing.T, user string, delta int64, ids ...string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(map[string]any{"media_ids": ids, "delta_seconds": delta})
	require.NoError(t, err)
	return do(t, f.router, user, http.MethodPost, "/api/media/shift-time", string(b))
}

func (f *timeFixture) shift(t *testing.T, user string, delta int64, ids ...string) shiftTimesResponse {
	t.Helper()
	rec := f.shiftRequest(t, user, delta, ids...)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp shiftTimesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

// orphans lists the owner's derived points that no own item explains, as the Checks page does.
func (f *timeFixture) orphans(t *testing.T, owner string) []orphanPoint {
	t.Helper()
	points, err := loadOrphanPoints(context.Background(), f.pool, owner, []string{"exif", "manual"})
	require.NoError(t, err)
	return points
}

// ownerSnapshot renders one owner's media, track points and trips, to show nothing of theirs moved.
func ownerSnapshot(t *testing.T, pool *pgxpool.Pool, owner string) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id::text || ' ' || coalesce(taken_at::text, '-') || ' ' || coalesce(gps_source, '-') || ' ' ||
		        coalesce(ST_AsText(location::geometry), '-') || ' ' || coalesce(gps_confidence::text, '-') ||
		        ' trip=' || coalesce(trip_id::text, '-') || ' edit=' || coalesce(time_edit::text, '-')
		 FROM media WHERE owner_id = $1 ORDER BY id`,
		`SELECT source || ' ' || recorded_at::text || ' ' || ST_AsText(location::geometry)
		 FROM gps_tracks WHERE owner_id = $1 ORDER BY source, recorded_at`,
		`SELECT id::text || ' ' || name || ' ' || coalesce(start_date::text, '-') || ' ' || coalesce(end_date::text, '-')
		 FROM trips WHERE owner_id = $1 ORDER BY id`,
	} {
		rows, err := pool.Query(context.Background(), q, owner)
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

func TestTimeEdit_SetMovesAnExifPhotoAndItsPoint(t *testing.T) {
	f := newTimeFixture(t)
	const evidence = `{"v": 1, "by": "ingest", "src": "exif_original"}`
	p := f.item(t, timeItem{owner: f.alice, name: "P1.JPG", takenAt: "2024-05-01T10:00:00Z", gpsSource: "exif",
		lat: budapestLat, lng: budapestLng, cameraModel: "DC-TZ200", evidence: evidence})
	insertTrack(t, f.pool, f.alice, "exif", "2024-05-01T10:00:00Z", budapestLat, budapestLng)

	before := time.Now()
	resp := f.set(t, f.alice, p, `{"wall":"2024-05-01T14:00:00","zone":"Europe/Warsaw"}`)

	s := f.state(t, p)
	assert.Equal(t, "2024-05-01T12:00:00Z", resp.TakenAt)
	assert.Equal(t, "2024-05-01T12:00:00Z", f.takenAt(t, p))
	require.NotNil(t, s.tripID, "trip detection ran")
	assert.Equal(t, s.tripID, resp.TripID)
	assert.False(t, resp.FollowupFailed)
	require.NotNil(t, s.gpsSource)
	assert.Equal(t, "exif", *s.gpsSource, "its own location stays")
	assert.InDelta(t, budapestLat, *s.lat, 1e-9)
	require.NotNil(t, s.evidence)
	assert.JSONEq(t, evidence, *s.evidence, "capture_evidence still says how the file's time was read")

	assert.Equal(t, []string{"2024-05-01T12:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"))
	assert.InDelta(t, budapestLat, retimeTrackLat(t, f.pool, f.alice, "exif", "2024-05-01T12:00:00Z"), 1e-9)
	assert.Empty(t, f.orphans(t, f.alice))
	assert.Equal(t, map[string]any{
		"v": 1.0, "kind": "set", "from": "2024-05-01T10:00:00Z", "original": "2024-05-01T10:00:00Z",
		"delta_s": 7200.0, "wall": "2024-05-01T14:00:00", "zone": "Europe/Warsaw", "offset_min": 120.0,
	}, f.edit(t, p, before, time.Now()))

	t.Run("the list carries camera_model and never time_edit", func(t *testing.T) {
		rec := do(t, f.router, f.alice, http.MethodGet, "/api/media", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var items []map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
		require.Len(t, items, 1)
		assert.Equal(t, "DC-TZ200", items[0]["camera_model"])
		assert.True(t, retimeUTC(t, "2024-05-01T12:00:00Z").Equal(retimeUTC(t, items[0]["taken_at"].(string))))
		assert.NotContains(t, items[0], "time_edit")
		assert.NotContains(t, items[0], "capture_evidence")
		assert.NotContains(t, rec.Body.String(), "Europe/Warsaw")
	})
}

func TestTimeEdit_SetMovesAManualVideosPoint(t *testing.T) {
	f := newTimeFixture(t)
	v := f.item(t, timeItem{owner: f.alice, name: "clip.mp4", mediaType: "video", takenAt: "2024-05-02T08:00:00Z",
		gpsSource: "manual", lat: viennaLat, lng: viennaLng})
	insertTrack(t, f.pool, f.alice, "manual", "2024-05-02T08:00:00Z", viennaLat, viennaLng)

	before := time.Now()
	f.set(t, f.alice, v, `{"wall":"2024-05-02T09:30:00","utc_offset_min":0}`)

	assert.Equal(t, "2024-05-02T09:30:00Z", f.takenAt(t, v))
	assert.Equal(t, []string{"2024-05-02T09:30:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "manual"))
	assert.InDelta(t, viennaLat, retimeTrackLat(t, f.pool, f.alice, "manual", "2024-05-02T09:30:00Z"), 1e-9)
	assert.Equal(t, []string{}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "a video has no EXIF point")
	assert.Empty(t, f.orphans(t, f.alice))
	assert.Equal(t, map[string]any{
		"v": 1.0, "kind": "set", "from": "2024-05-02T08:00:00Z", "original": "2024-05-02T08:00:00Z",
		"delta_s": 5400.0, "wall": "2024-05-02T09:30:00", "zone": nil, "offset_min": 0.0,
	}, f.edit(t, v, before, time.Now()))
}

// A photo placed by hand still has the 'exif' point ingest made from its EXIF position, which is on
// no row. It moves too, with its own position, or the Checks page would report it as an orphan.
func TestTimeEdit_SetMovesAManualPhotosPointsAndItsExifPoint(t *testing.T) {
	f := newTimeFixture(t)
	m := f.item(t, timeItem{owner: f.alice, name: "P2.JPG", takenAt: "2024-05-03T10:00:00Z", gpsSource: "manual",
		lat: viennaLat, lng: viennaLng})
	insertTrack(t, f.pool, f.alice, "manual", "2024-05-03T10:00:00Z", viennaLat, viennaLng)
	insertTrack(t, f.pool, f.alice, "exif", "2024-05-03T10:00:00Z", budapestLat, budapestLng)

	f.set(t, f.alice, m, `{"wall":"2024-05-03T11:00:00","utc_offset_min":0}`)

	assert.Equal(t, []string{"2024-05-03T11:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "manual"))
	assert.InDelta(t, viennaLat, retimeTrackLat(t, f.pool, f.alice, "manual", "2024-05-03T11:00:00Z"), 1e-9)
	assert.Equal(t, []string{"2024-05-03T11:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"))
	assert.InDelta(t, budapestLat, retimeTrackLat(t, f.pool, f.alice, "exif", "2024-05-03T11:00:00Z"), 1e-9,
		"the EXIF point keeps its own position")
	assert.Empty(t, f.orphans(t, f.alice))
}

// A burst: another photo located from its EXIF at the old second keeps the 'exif' point there.
func TestTimeEdit_BurstSiblingKeepsTheExifPoint(t *testing.T) {
	const old, moved = "2024-05-04T10:00:00Z", "2024-05-04T11:00:00Z"
	const siblingLat = budapestLat + 0.01

	t.Run("an EXIF photo", func(t *testing.T) {
		f := newTimeFixture(t)
		a := f.item(t, timeItem{owner: f.alice, name: "A.JPG", takenAt: old, gpsSource: "exif", lat: budapestLat, lng: budapestLng})
		f.item(t, timeItem{owner: f.alice, name: "B.JPG", takenAt: old, gpsSource: "exif", lat: siblingLat, lng: budapestLng})
		insertTrack(t, f.pool, f.alice, "exif", old, budapestLat, budapestLng)

		f.set(t, f.alice, a, `{"wall":"2024-05-04T11:00:00","utc_offset_min":0}`)

		assert.Equal(t, []string{old, moved}, retimeTrackTimes(t, f.pool, f.alice, "exif"))
		assert.InDelta(t, siblingLat, retimeTrackLat(t, f.pool, f.alice, "exif", old), 1e-9, "handed to the sibling")
		assert.InDelta(t, budapestLat, retimeTrackLat(t, f.pool, f.alice, "exif", moved), 1e-9)
		assert.Empty(t, f.orphans(t, f.alice))
	})

	t.Run("a photo placed by hand", func(t *testing.T) {
		f := newTimeFixture(t)
		m := f.item(t, timeItem{owner: f.alice, name: "M.JPG", takenAt: old, gpsSource: "manual", lat: viennaLat, lng: viennaLng})
		f.item(t, timeItem{owner: f.alice, name: "S.JPG", takenAt: old, gpsSource: "exif", lat: siblingLat, lng: budapestLng})
		insertTrack(t, f.pool, f.alice, "manual", old, viennaLat, viennaLng)
		insertTrack(t, f.pool, f.alice, "exif", old, siblingLat, budapestLng)

		f.set(t, f.alice, m, `{"wall":"2024-05-04T11:00:00","utc_offset_min":0}`)

		assert.Equal(t, []string{moved}, retimeTrackTimes(t, f.pool, f.alice, "manual"))
		assert.Equal(t, []string{old}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "the sibling's point stays")
		assert.InDelta(t, siblingLat, retimeTrackLat(t, f.pool, f.alice, "exif", old), 1e-9)
		assert.Empty(t, f.orphans(t, f.alice))
	})
}

// A photo placed by hand takes along the 'exif' point at its old second only while no other located
// photo (from its EXIF or by hand) sits there: any such photo may be the one whose ingest made it,
// one placed by hand too. P never had an EXIF position; the point at its second is Q's.
func TestTimeEdit_ExifPointStaysWithAnotherPhotoAtItsSecond(t *testing.T) {
	const old, moved = "2024-05-17T10:00:00Z", "2024-05-17T11:00:00Z"
	const qExifLat, qExifLng = 21.0, 21.0 // Q's EXIF position, on no row once Q is placed by hand
	byHand := func(name, mediaType string) timeItem {
		return timeItem{name: name, mediaType: mediaType, gpsSource: "manual", lat: 20, lng: 20}
	}
	tests := []struct {
		name       string
		sibling    timeItem
		wantExifAt string   // where the 'exif' point ends, with Q's EXIF position
		wantManual []string // the sibling keeps a 'manual' point at the old second; P's moves
	}{
		{"a photo placed by hand", byHand("Q.JPG", "photo"), old, []string{old, moved}},
		{"a 360° photo placed by hand", byHand("Q360.JPG", "photo_360"), old, []string{old, moved}},
		{"a photo located from its EXIF", timeItem{name: "Q.JPG", gpsSource: "exif", lat: qExifLat, lng: qExifLng},
			old, []string{moved}},
		{"a video placed by hand makes no EXIF point", byHand("Q.MP4", "video"), moved, []string{old, moved}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTimeFixture(t)
			p := f.item(t, timeItem{owner: f.alice, name: "P.JPG", takenAt: old, gpsSource: "manual", lat: 10, lng: 10})
			sibling := tt.sibling
			sibling.owner, sibling.takenAt = f.alice, old
			f.item(t, sibling)
			// One 'manual' point per second: the sibling's when it was placed by hand, else P's.
			manualLat := 10.0
			if sibling.gpsSource == "manual" {
				manualLat = 20
			}
			insertTrack(t, f.pool, f.alice, "manual", old, manualLat, manualLat)
			insertTrack(t, f.pool, f.alice, "exif", old, qExifLat, qExifLng)

			f.set(t, f.alice, p, `{"wall":"2024-05-17T11:00:00","utc_offset_min":0}`)

			assert.Equal(t, moved, f.takenAt(t, p))
			assert.Equal(t, []string{tt.wantExifAt}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "one point, never copied")
			assert.InDelta(t, qExifLat, retimeTrackLat(t, f.pool, f.alice, "exif", tt.wantExifAt), 1e-9,
				"the point keeps its own position")
			assert.Equal(t, tt.wantManual, retimeTrackTimes(t, f.pool, f.alice, "manual"))
			assert.InDelta(t, 10, retimeTrackLat(t, f.pool, f.alice, "manual", moved), 1e-9, "P's own point")
			if len(tt.wantManual) == 2 {
				assert.InDelta(t, 20, retimeTrackLat(t, f.pool, f.alice, "manual", old), 1e-9, "the sibling's")
			}
			assert.Empty(t, f.orphans(t, f.alice))
		})
	}
}

// Only a photo located from its EXIF makes an 'exif' point, so an unlocated or interpolated photo at
// P's second never made the one there and does not keep it: the point P's ingest made from its EXIF
// position goes with P, placed by hand since. Left behind, it would be an orphan. A set of P and a
// shift of both end alike, whichever of the two has the lower id (a shift moves one second's items
// in id order).
func TestTimeEdit_ExifPointLeavesASiblingThatCannotHaveMadeIt(t *testing.T) {
	const old, moved = "2024-05-19T10:00:00Z", "2024-05-19T11:00:00Z"
	const pExifLat, pExifLng = 11.0, 11.0 // P's EXIF position, on no row since P was placed by hand
	const lowID, highID = "00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b"
	siblings := []struct {
		name string
		q    timeItem
	}{
		{"an unlocated photo", timeItem{name: "Q.JPG"}},
		{"an interpolated photo", timeItem{name: "Q.JPG", gpsSource: "interpolated", lat: 10.5, lng: 10.5}},
	}
	moves := []struct {
		name     string
		pID, qID string
		do       func(t *testing.T, f *timeFixture, p, q string)
		qAt      string // where Q ends
	}{
		{"set P", "", "", func(t *testing.T, f *timeFixture, p, _ string) {
			f.set(t, f.alice, p, `{"wall":"2024-05-19T11:00:00","utc_offset_min":0}`)
		}, old},
		{"shift both, P's id lower", lowID, highID, func(t *testing.T, f *timeFixture, p, q string) {
			f.shift(t, f.alice, 3600, p, q)
		}, moved},
		{"shift both, Q's id lower", highID, lowID, func(t *testing.T, f *timeFixture, p, q string) {
			f.shift(t, f.alice, 3600, p, q)
		}, moved},
	}
	for _, sibling := range siblings {
		for _, move := range moves {
			t.Run(sibling.name+"/"+move.name, func(t *testing.T) {
				f := newTimeFixture(t)
				p := f.item(t, timeItem{id: move.pID, owner: f.alice, name: "P.JPG", takenAt: old, gpsSource: "manual",
					lat: 10, lng: 10})
				q := sibling.q
				q.id, q.owner, q.takenAt = move.qID, f.alice, old
				qID := f.item(t, q)
				insertTrack(t, f.pool, f.alice, "manual", old, 10, 10)
				insertTrack(t, f.pool, f.alice, "exif", old, pExifLat, pExifLng)

				move.do(t, f, p, qID)

				assert.Equal(t, moved, f.takenAt(t, p))
				assert.Equal(t, move.qAt, f.takenAt(t, qID))
				assert.Equal(t, []string{moved}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "it went with P")
				assert.InDelta(t, pExifLat, retimeTrackLat(t, f.pool, f.alice, "exif", moved), 1e-9,
					"the point keeps its own position")
				assert.Equal(t, []string{moved}, retimeTrackTimes(t, f.pool, f.alice, "manual"))
				assert.InDelta(t, 10, retimeTrackLat(t, f.pool, f.alice, "manual", moved), 1e-9, "P's own point")
				assert.Empty(t, f.orphans(t, f.alice))
			})
		}
	}
}

// The 'exif' point a photo placed by hand takes along yields to one already at its new second: E's
// point stays E's, and M's old point does not stay behind.
func TestTimeEdit_CarriedExifPointYieldsToOneAtTheNewTime(t *testing.T) {
	const t0, t1 = "2024-05-18T10:00:00Z", "2024-05-18T12:00:00Z"
	f := newTimeFixture(t)
	m := f.item(t, timeItem{owner: f.alice, name: "M.JPG", takenAt: t0, gpsSource: "manual", lat: 10, lng: 10})
	insertTrack(t, f.pool, f.alice, "manual", t0, 10, 10)
	insertTrack(t, f.pool, f.alice, "exif", t0, 11, 11)
	e := f.item(t, timeItem{owner: f.alice, name: "E.JPG", takenAt: t1, gpsSource: "exif", lat: viennaLat, lng: viennaLng})
	insertTrack(t, f.pool, f.alice, "exif", t1, viennaLat, viennaLng)

	f.set(t, f.alice, m, `{"wall":"2024-05-18T12:00:00","utc_offset_min":0}`)

	assert.Equal(t, t1, f.takenAt(t, m))
	assert.Equal(t, []string{t1}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "none left at the old second")
	assert.InDelta(t, viennaLat, retimeTrackLat(t, f.pool, f.alice, "exif", t1), 1e-9, "E's point is kept")
	s := f.state(t, e)
	require.NotNil(t, s.gpsSource)
	assert.Equal(t, "exif", *s.gpsSource)
	assert.InDelta(t, viennaLat, *s.lat, 1e-9, "E's own position is kept")
	assert.Equal(t, []string{t1}, retimeTrackTimes(t, f.pool, f.alice, "manual"))
	assert.InDelta(t, 10, retimeTrackLat(t, f.pool, f.alice, "manual", t1), 1e-9)
	assert.Empty(t, f.orphans(t, f.alice))
}

func TestTimeEdit_InterpolatedItemIsPlacedAgainAtTheNewTime(t *testing.T) {
	f := newTimeFixture(t)
	insertTrack(t, f.pool, f.alice, "gpx:day.gpx", "2024-05-05T10:00:00Z", 47.0, 19.0)
	insertTrack(t, f.pool, f.alice, "gpx:day.gpx", "2024-05-05T10:20:00Z", 47.2, 19.0)
	insertTrack(t, f.pool, f.alice, "gpx:day.gpx", "2024-05-05T15:00:00Z", 48.0, 16.0)
	insertTrack(t, f.pool, f.alice, "gpx:day.gpx", "2024-05-05T15:20:00Z", 48.2, 16.0)
	placed := f.item(t, timeItem{owner: f.alice, name: "I1.JPG", takenAt: "2024-05-05T10:10:00Z",
		gpsSource: "interpolated", lat: 47.1, lng: 19.0})
	lost := f.item(t, timeItem{owner: f.alice, name: "I2.JPG", takenAt: "2024-05-05T10:15:00Z",
		gpsSource: "interpolated", lat: 47.15, lng: 19.0})

	f.set(t, f.alice, placed, `{"wall":"2024-05-05T15:10:00","utc_offset_min":0}`)
	f.set(t, f.alice, lost, `{"wall":"2024-06-01T00:00:00","utc_offset_min":0}`)

	s := f.state(t, placed)
	require.NotNil(t, s.gpsSource)
	assert.Equal(t, "interpolated", *s.gpsSource)
	assert.InDelta(t, 48.1, *s.lat, 1e-3, "placed between the points around the new time")
	assert.InDelta(t, 16.0, *s.lng, 1e-3)

	s = f.state(t, lost)
	assert.Nil(t, s.gpsSource, "nothing brackets the new time: no location from the old one")
	assert.Nil(t, s.lat)
	assert.Nil(t, s.confidence)
	assert.Equal(t, []string{}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "an interpolation makes no point")
}

// Interpolations near the old time may rest on the point that left with the item. They are cleared,
// then placed again from what is left, or left empty. The new time is days away, far beyond twice
// the match window, so only a re-match around the old time places the neighbour that has another
// bracket: through a set and through a shift alike.
func TestTimeEdit_NeighboursOfAMovedPointArePlacedAgain(t *testing.T) {
	for _, move := range []struct {
		name string
		do   func(t *testing.T, f *timeFixture, p string)
	}{
		{"set", func(t *testing.T, f *timeFixture, p string) {
			f.set(t, f.alice, p, `{"wall":"2024-05-10T10:00:00","utc_offset_min":0}`)
		}},
		{"shift", func(t *testing.T, f *timeFixture, p string) { f.shift(t, f.alice, 4*86400, p) }},
	} {
		t.Run(move.name, func(t *testing.T) {
			f := newTimeFixture(t)
			p := f.item(t, timeItem{owner: f.alice, name: "P.JPG", takenAt: "2024-05-06T10:00:00Z", gpsSource: "exif",
				lat: budapestLat, lng: budapestLng})
			insertTrack(t, f.pool, f.alice, "exif", "2024-05-06T10:00:00Z", budapestLat, budapestLng)
			insertTrack(t, f.pool, f.alice, "gpx:walk.gpx", "2024-05-06T10:40:00Z", 48.0, 16.0)
			onlyOnIt := f.item(t, timeItem{owner: f.alice, name: "a.mp4", mediaType: "video", takenAt: "2024-05-06T09:30:00Z",
				gpsSource: "interpolated", lat: budapestLat, lng: budapestLng})
			between := f.item(t, timeItem{owner: f.alice, name: "b.mp4", mediaType: "video", takenAt: "2024-05-06T10:20:00Z",
				gpsSource: "interpolated", lat: 47.7, lng: 17.5})
			farAway := f.item(t, timeItem{owner: f.alice, name: "c.mp4", mediaType: "video", takenAt: "2024-05-06T13:00:00Z",
				gpsSource: "interpolated", lat: 10, lng: 10})

			move.do(t, f, p)

			assert.Equal(t, "2024-05-10T10:00:00Z", f.takenAt(t, p))
			assert.Equal(t, []string{"2024-05-10T10:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"))
			s := f.state(t, onlyOnIt)
			assert.Nil(t, s.gpsSource, "its only point left")
			assert.Nil(t, s.lat)
			s = f.state(t, between)
			require.NotNil(t, s.gpsSource, "placed again around the old time")
			assert.Equal(t, "interpolated", *s.gpsSource)
			assert.InDelta(t, 48.0, *s.lat, 1e-9, "from the point that is left")
			assert.InDelta(t, 16.0, *s.lng, 1e-9)
			s = f.state(t, farAway)
			require.NotNil(t, s.lat)
			assert.InDelta(t, 10, *s.lat, 1e-9, "outside the match window of the old time: untouched")
		})
	}
}

func TestTimeEdit_UndatedItemGetsATime(t *testing.T) {
	f := newTimeFixture(t)
	anchor := f.item(t, timeItem{owner: f.alice, name: "anchor.jpg", takenAt: "2024-05-07T09:00:00Z"})
	photo := f.item(t, timeItem{owner: f.alice, name: "scan.jpg", gpsSource: "exif", lat: budapestLat, lng: budapestLng})
	video := f.item(t, timeItem{owner: f.alice, name: "pinned.mp4", mediaType: "video", gpsSource: "manual",
		lat: viennaLat, lng: viennaLng})
	tagged := f.item(t, timeItem{owner: f.alice, name: "tagged.mp4", mediaType: "video", gpsSource: "exif",
		lat: 46, lng: 18})
	require.NoError(t, RunTripDetect(context.Background(), f.pool, f.alice, 24))

	before := time.Now()
	resp := f.set(t, f.alice, photo, `{"wall":"2024-05-07T12:00:00","zone":"Europe/Budapest"}`)
	assert.Equal(t, "2024-05-07T10:00:00Z", resp.TakenAt)
	s := f.state(t, photo)
	require.NotNil(t, s.tripID)
	assert.Equal(t, s.tripID, resp.TripID)
	assert.Equal(t, f.state(t, anchor).tripID, s.tripID, "it joins the trip of the items around it")
	assert.Equal(t, []string{"2024-05-07T10:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "ingest's point, made now")
	assert.InDelta(t, budapestLat, retimeTrackLat(t, f.pool, f.alice, "exif", "2024-05-07T10:00:00Z"), 1e-9)
	assert.Equal(t, map[string]any{
		"v": 1.0, "kind": "set", "from": nil, "original": nil, "delta_s": nil,
		"wall": "2024-05-07T12:00:00", "zone": "Europe/Budapest", "offset_min": 120.0,
	}, f.edit(t, photo, before, time.Now()))

	f.set(t, f.alice, video, `{"wall":"2024-05-07T11:00:00","utc_offset_min":0}`)
	assert.Equal(t, []string{"2024-05-07T11:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "manual"), "the manual point, made now")
	assert.InDelta(t, viennaLat, retimeTrackLat(t, f.pool, f.alice, "manual", "2024-05-07T11:00:00Z"), 1e-9)

	f.set(t, f.alice, tagged, `{"wall":"2024-05-07T11:30:00","utc_offset_min":0}`)
	assert.Equal(t, []string{"2024-05-07T10:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"),
		"a video's location tag makes no point")
	assert.Empty(t, f.orphans(t, f.alice))

	t.Run("a second change keeps the original null", func(t *testing.T) {
		f.set(t, f.alice, photo, `{"wall":"2024-05-07T10:30:00","utc_offset_min":0}`)
		e := f.edit(t, photo, before, time.Now())
		assert.Equal(t, "2024-05-07T10:00:00Z", e["from"])
		assert.Nil(t, e["original"])
		assert.Equal(t, 1800.0, e["delta_s"])
		assert.Equal(t, []string{"2024-05-07T10:30:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"))
	})
}

// Saving the stored time unchanged writes nothing. The route takes whole seconds, and the dialog
// shows a stored fraction (an upload's file date is in milliseconds) without it, so a time in the
// stored time's second counts as the stored time.
func TestTimeEdit_SameInstantWritesNothing(t *testing.T) {
	for _, stored := range []string{"2024-05-08T10:00:00Z", "2024-05-08T10:00:00.437Z"} {
		t.Run(stored, func(t *testing.T) {
			f := newTimeFixture(t)
			p := f.item(t, timeItem{owner: f.alice, name: "P.JPG", takenAt: stored, gpsSource: "exif",
				lat: budapestLat, lng: budapestLng})
			insertTrack(t, f.pool, f.alice, "exif", stored, budapestLat, budapestLng)
			require.NoError(t, RunTripDetect(context.Background(), f.pool, f.alice, 24))
			trip := f.state(t, p).tripID
			require.NotNil(t, trip)
			before := retimeSnapshot(t, f.pool)

			resp := f.set(t, f.alice, p, `{"wall":"2024-05-08T12:00:00","zone":"Europe/Warsaw"}`)

			assert.Equal(t, setTakenAtResponse{TakenAt: stored, TripID: trip}, resp, "the stored values")
			assert.Equal(t, before, retimeSnapshot(t, f.pool))
			assert.Equal(t, trip, f.state(t, p).tripID, "trip detection did not run: the trip keeps its id")
			assert.Nil(t, f.state(t, p).timeEdit)
		})
	}
}

func TestTimeEdit_LaterChangesKeepTheOriginal(t *testing.T) {
	f := newTimeFixture(t)
	p := f.item(t, timeItem{owner: f.alice, name: "P.JPG", takenAt: "2024-05-09T10:00:00Z"})
	before := time.Now()

	f.set(t, f.alice, p, `{"wall":"2024-05-09T11:00:00","utc_offset_min":0}`)
	f.set(t, f.alice, p, `{"wall":"2024-05-09T14:30:00","utc_offset_min":120}`)
	assert.Equal(t, map[string]any{
		"v": 1.0, "kind": "set", "from": "2024-05-09T11:00:00Z", "original": "2024-05-09T10:00:00Z",
		"delta_s": 5400.0, "wall": "2024-05-09T14:30:00", "zone": nil, "offset_min": 120.0,
	}, f.edit(t, p, before, time.Now()))

	resp := f.shift(t, f.alice, 60, p)
	assert.Equal(t, 1, resp.Shifted)
	assert.Equal(t, map[string]any{
		"v": 1.0, "kind": "shift", "from": "2024-05-09T12:30:00Z", "original": "2024-05-09T10:00:00Z",
		"delta_s": 60.0, "wall": nil, "zone": nil, "offset_min": nil,
	}, f.edit(t, p, before, time.Now()))
}

func TestTimeEdit_TripsAreDetectedAgain(t *testing.T) {
	f := newTimeFixture(t)
	a1 := f.item(t, timeItem{owner: f.alice, name: "a1.jpg", takenAt: "2024-05-01T10:00:00Z"})
	a2 := f.item(t, timeItem{owner: f.alice, name: "a2.jpg", takenAt: "2024-05-01T12:00:00Z"})
	a3 := f.item(t, timeItem{owner: f.alice, name: "a3.jpg", takenAt: "2024-05-02T09:00:00Z"})
	holiday := insertNamedTrip(t, f.pool, f.alice, "Holiday", ptr("2024-07-10"), ptr("2024-07-20"))
	require.NoError(t, RunTripDetect(context.Background(), f.pool, f.alice, 24))
	require.Equal(t, f.state(t, a1).tripID, f.state(t, a3).tripID)

	t.Run("days away: another trip", func(t *testing.T) {
		resp := f.set(t, f.alice, a3, `{"wall":"2024-05-20T09:00:00","utc_offset_min":0}`)
		moved := f.state(t, a3).tripID
		require.NotNil(t, moved)
		assert.Equal(t, moved, resp.TripID, "the response names the trip it is in now")
		assert.NotEqual(t, f.state(t, a1).tripID, moved)
		assert.Equal(t, f.state(t, a1).tripID, f.state(t, a2).tripID)
	})
	t.Run("onto a named trip's days: that trip", func(t *testing.T) {
		resp := f.set(t, f.alice, a2, `{"wall":"2024-07-15T12:00:00","utc_offset_min":0}`)
		require.NotNil(t, resp.TripID)
		assert.Equal(t, holiday, *resp.TripID)
		assert.Equal(t, resp.TripID, f.state(t, a2).tripID)
		assert.Equal(t, "Holiday", tripName(t, f.pool, holiday))
	})
}

// Two items an hour apart shifted by an hour: each point ends at its own item's new time with its
// own item's position, and none stays behind. The items move so that a destination is free first:
// a photo placed by hand carries an 'exif' point that no row describes, which would otherwise yield
// to the other item's point still at its destination and be lost.
func TestTimeEdit_ShiftChainCollision(t *testing.T) {
	const t0, t1 = "2024-05-10T10:00:00Z", "2024-05-10T11:00:00Z"
	tests := []struct {
		name      string
		delta     int64
		wantTimes []string // A's, then B's
	}{
		{"later", 3600, []string{"2024-05-10T11:00:00Z", "2024-05-10T12:00:00Z"}},
		{"earlier", -3600, []string{"2024-05-10T09:00:00Z", "2024-05-10T10:00:00Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name+", photos located from their EXIF", func(t *testing.T) {
			f := newTimeFixture(t)
			a := f.item(t, timeItem{owner: f.alice, name: "A.JPG", takenAt: t0, gpsSource: "exif", lat: budapestLat, lng: budapestLng})
			b := f.item(t, timeItem{owner: f.alice, name: "B.JPG", takenAt: t1, gpsSource: "exif", lat: viennaLat, lng: viennaLng})
			insertTrack(t, f.pool, f.alice, "exif", t0, budapestLat, budapestLng)
			insertTrack(t, f.pool, f.alice, "exif", t1, viennaLat, viennaLng)

			resp := f.shift(t, f.alice, tt.delta, a, b)

			assert.Equal(t, 2, resp.Shifted)
			assert.Zero(t, resp.Undated)
			assert.Equal(t, tt.wantTimes, []string{f.takenAt(t, a), f.takenAt(t, b)})
			assert.Equal(t, tt.wantTimes, retimeTrackTimes(t, f.pool, f.alice, "exif"))
			assert.InDelta(t, budapestLat, retimeTrackLat(t, f.pool, f.alice, "exif", tt.wantTimes[0]), 1e-9, "A's point")
			assert.InDelta(t, viennaLat, retimeTrackLat(t, f.pool, f.alice, "exif", tt.wantTimes[1]), 1e-9, "B's point")
			assert.Empty(t, f.orphans(t, f.alice))
		})
		t.Run(tt.name+", photos placed by hand", func(t *testing.T) {
			f := newTimeFixture(t)
			a := f.item(t, timeItem{owner: f.alice, name: "A.JPG", takenAt: t0, gpsSource: "manual", lat: 10, lng: 10})
			b := f.item(t, timeItem{owner: f.alice, name: "B.JPG", takenAt: t1, gpsSource: "manual", lat: 20, lng: 20})
			insertTrack(t, f.pool, f.alice, "manual", t0, 10, 10)
			insertTrack(t, f.pool, f.alice, "exif", t0, 11, 11)
			insertTrack(t, f.pool, f.alice, "manual", t1, 20, 20)
			insertTrack(t, f.pool, f.alice, "exif", t1, 21, 21)

			f.shift(t, f.alice, tt.delta, b, a)

			assert.Equal(t, tt.wantTimes, []string{f.takenAt(t, a), f.takenAt(t, b)})
			for source, lats := range map[string][2]float64{"manual": {10, 20}, "exif": {11, 21}} {
				assert.Equal(t, tt.wantTimes, retimeTrackTimes(t, f.pool, f.alice, source), source)
				assert.InDelta(t, lats[0], retimeTrackLat(t, f.pool, f.alice, source, tt.wantTimes[0]), 1e-9, "A's %s point", source)
				assert.InDelta(t, lats[1], retimeTrackLat(t, f.pool, f.alice, source, tt.wantTimes[1]), 1e-9, "B's %s point", source)
			}
			assert.Empty(t, f.orphans(t, f.alice))
		})
	}
}

func TestTimeEdit_ShiftABurst(t *testing.T) {
	f := newTimeFixture(t)
	const at = "2024-05-11T10:00:00Z"
	lats := map[string]float64{}
	var ids []string
	for i := range 3 {
		lat := budapestLat + float64(i)*0.01
		id := f.item(t, timeItem{owner: f.alice, name: fmt.Sprintf("B%d.JPG", i), takenAt: at, gpsSource: "exif",
			lat: lat, lng: budapestLng})
		lats[id] = lat
		ids = append(ids, id)
	}
	insertTrack(t, f.pool, f.alice, "exif", at, budapestLat, budapestLng)

	resp := f.shift(t, f.alice, 1800, ids...)

	assert.Equal(t, 3, resp.Shifted)
	for _, id := range ids {
		assert.Equal(t, "2024-05-11T10:30:00Z", f.takenAt(t, id))
	}
	assert.Equal(t, []string{"2024-05-11T10:30:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"), "none left behind")
	first := slices.Min(ids) // at one second, the lowest id moves first and its position is kept
	assert.InDelta(t, lats[first], retimeTrackLat(t, f.pool, f.alice, "exif", "2024-05-11T10:30:00Z"), 1e-9)
	assert.Empty(t, f.orphans(t, f.alice))
}

func TestTimeEdit_ShiftSkipsUndatedItems(t *testing.T) {
	f := newTimeFixture(t)
	dated := f.item(t, timeItem{owner: f.alice, name: "d.jpg", takenAt: "2024-05-12T10:00:00Z"})
	undated := f.item(t, timeItem{owner: f.alice, name: "u.jpg"})

	resp := f.shift(t, f.alice, 120, dated, undated)

	assert.Equal(t, 1, resp.Shifted)
	assert.Equal(t, 1, resp.Undated)
	assert.Equal(t, "2024-05-12T10:02:00Z", f.takenAt(t, dated))
	assert.Empty(t, f.takenAt(t, undated))
	assert.Nil(t, f.state(t, undated).timeEdit)
	require.Len(t, resp.Trips, 1)
	assert.Equal(t, shiftedTrip{ID: *f.state(t, dated).tripID, Items: 1}, resp.Trips[0])

	t.Run("only undated items: nothing to do", func(t *testing.T) {
		before := retimeSnapshot(t, f.pool)
		resp := f.shift(t, f.alice, 120, undated)
		assert.Equal(t, shiftTimesResponse{Undated: 1, Trips: []shiftedTrip{}}, resp)
		assert.Equal(t, before, retimeSnapshot(t, f.pool))
	})
}

func TestTimeEdit_ShiftRefusedWritesNothing(t *testing.T) {
	f := newTimeFixture(t)
	own := f.item(t, timeItem{owner: f.alice, name: "own.jpg", takenAt: "2024-05-13T10:00:00Z", gpsSource: "exif",
		lat: budapestLat, lng: budapestLng})
	insertTrack(t, f.pool, f.alice, "exif", "2024-05-13T10:00:00Z", budapestLat, budapestLng)
	lastDay := f.item(t, timeItem{owner: f.alice, name: "last.jpg", takenAt: "9998-12-31T12:00:00Z"})
	firstDay := f.item(t, timeItem{owner: f.alice, name: "first.jpg", takenAt: "0001-01-01T12:00:00Z"})
	bobs := f.item(t, timeItem{owner: f.bob, name: "bob.jpg", takenAt: "2024-05-13T10:00:00Z"})
	bobShared := f.item(t, timeItem{owner: f.bob, name: "shared.jpg", takenAt: "2024-05-13T10:00:00Z"})
	album := insertAlbum(t, f.pool, f.bob, "Bob's")
	insertAlbumMember(t, f.pool, album, f.alice)
	insertAlbumMedia(t, f.pool, album, bobShared)
	const missing = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	before := retimeSnapshot(t, f.pool)

	tests := []struct {
		name       string
		delta      int64
		ids        []string
		wantStatus int
	}{
		{"past year 9998", 86400, []string{own, lastDay}, http.StatusBadRequest},
		{"before year 1", -86400, []string{firstDay, own}, http.StatusBadRequest},
		{"another user's item", 60, []string{own, bobs}, http.StatusNotFound},
		{"an item shared through an album", 60, []string{own, bobShared}, http.StatusNotFound},
		{"a missing item", 60, []string{own, missing}, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.shiftRequest(t, f.alice, tt.delta, tt.ids...)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, before, retimeSnapshot(t, f.pool), "nothing written")
			assert.Nil(t, f.state(t, own).timeEdit)
		})
	}
}

func TestTimeEdit_ShiftCollapsesDuplicates(t *testing.T) {
	f := newTimeFixture(t)
	p := f.item(t, timeItem{owner: f.alice, name: "p.jpg", takenAt: "2024-05-14T10:00:00Z"})

	resp := f.shift(t, f.alice, 60, p, strings.ToUpper(p), p)

	assert.Equal(t, 1, resp.Shifted)
	assert.Equal(t, "2024-05-14T10:01:00Z", f.takenAt(t, p), "moved once")
}

func TestTimeEdit_ShiftTripsSummary(t *testing.T) {
	f := newTimeFixture(t)
	var ids []string
	add := func(name, takenAt string) string {
		id := f.item(t, timeItem{owner: f.alice, name: name, takenAt: takenAt})
		ids = append(ids, id)
		return id
	}
	x := add("x1.jpg", "2024-08-01T10:00:00Z")
	add("x2.jpg", "2024-08-01T11:00:00Z")
	add("x3.jpg", "2024-08-01T12:00:00Z")
	y := add("y1.jpg", "2024-09-01T10:00:00Z")
	z := add("z1.jpg", "2024-10-01T10:00:00Z")
	add("z2.jpg", "2024-10-01T11:00:00Z")
	w := add("w1.jpg", "2024-11-01T10:00:00Z")
	add("w2.jpg", "2024-11-01T11:00:00Z")
	ids = append(ids, f.item(t, timeItem{owner: f.alice, name: "undated.jpg"}))

	resp := f.shift(t, f.alice, 60, ids...)

	assert.Equal(t, 8, resp.Shifted)
	assert.Equal(t, 1, resp.Undated)
	tripOf := func(id string) string { return *f.state(t, id).tripID }
	tied := []string{tripOf(z), tripOf(w)}
	slices.Sort(tied)
	assert.Equal(t, []shiftedTrip{
		{ID: tripOf(x), Items: 3}, {ID: tied[0], Items: 2}, {ID: tied[1], Items: 2}, {ID: tripOf(y), Items: 1},
	}, resp.Trips, "most items first, then by id; the trips after detection")
}

// Another user's items and track points at the very instants alice changes stay as they were.
func TestTimeEdit_NeverTouchesAnotherUsersData(t *testing.T) {
	f := newTimeFixture(t)
	const at, pinnedAt = "2024-05-15T10:00:00Z", "2024-05-15T14:00:00Z"
	a := f.item(t, timeItem{owner: f.alice, name: "a.jpg", takenAt: at, gpsSource: "exif", lat: budapestLat, lng: budapestLng})
	insertTrack(t, f.pool, f.alice, "exif", at, budapestLat, budapestLng)
	m := f.item(t, timeItem{owner: f.alice, name: "m.jpg", takenAt: pinnedAt, gpsSource: "manual", lat: viennaLat, lng: viennaLng})
	insertTrack(t, f.pool, f.alice, "manual", pinnedAt, viennaLat, viennaLng)
	insertTrack(t, f.pool, f.alice, "exif", pinnedAt, budapestLat, budapestLng)

	f.item(t, timeItem{owner: f.bob, name: "b.jpg", takenAt: at, gpsSource: "exif", lat: viennaLat, lng: viennaLng})
	insertTrack(t, f.pool, f.bob, "exif", at, viennaLat, viennaLng)
	f.item(t, timeItem{owner: f.bob, name: "bm.jpg", takenAt: pinnedAt, gpsSource: "manual", lat: 1, lng: 1})
	insertTrack(t, f.pool, f.bob, "manual", pinnedAt, 1, 1)
	insertTrack(t, f.pool, f.bob, "exif", pinnedAt, 2, 2)
	f.item(t, timeItem{owner: f.bob, name: "bi.mp4", mediaType: "video", takenAt: "2024-05-15T10:10:00Z",
		gpsSource: "interpolated", lat: 3, lng: 3})
	f.item(t, timeItem{owner: f.bob, name: "bu.jpg", takenAt: "2024-05-15T11:05:00Z"})
	insertTrack(t, f.pool, f.bob, "gpx:bob.gpx", "2024-05-15T11:00:00Z", 4, 4)
	require.NoError(t, RunTripDetect(context.Background(), f.pool, f.bob, 24))
	bobBefore := ownerSnapshot(t, f.pool, f.bob)

	f.set(t, f.alice, a, `{"wall":"2024-05-15T11:00:00","utc_offset_min":0}`)
	f.shift(t, f.alice, -3600, m)

	assert.Equal(t, "2024-05-15T11:00:00Z", f.takenAt(t, a))
	assert.Equal(t, "2024-05-15T13:00:00Z", f.takenAt(t, m))
	assert.Equal(t, []string{"2024-05-15T11:00:00Z", "2024-05-15T13:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"))
	assert.Equal(t, bobBefore, ownerSnapshot(t, f.pool, f.bob), "bob's media, points and trips are untouched")
}

// clearBarrierPool begins transactions that wait at arrive before they first clear the
// interpolations near a moved point's old time.
type clearBarrierPool struct {
	*pgxpool.Pool
	arrive func()
}

func (p *clearBarrierPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &clearBarrierTx{Tx: tx, arrive: p.arrive}, nil
}

type clearBarrierTx struct {
	pgx.Tx
	arrive func()
	once   sync.Once
}

func (tx *clearBarrierTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "gps_source = 'interpolated' AND taken_at BETWEEN") {
		tx.once.Do(tx.arrive)
	}
	return tx.Tx.Exec(ctx, sql, args...)
}

// meetingPoint returns a function that returns once n calls have reached it, or after wait.
func meetingPoint(n int, wait time.Duration) func() {
	var mu sync.Mutex
	arrived := 0
	all := make(chan struct{})
	return func() {
		mu.Lock()
		arrived++
		if arrived == n {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
		case <-time.After(wait):
		}
	}
}

// Two shifts of one owner at once, each holding an item the other's clearing of interpolations
// needs: A's photo moves a point from 10:00 on 1 June, whose window holds B's interpolated item, and
// B's photo moves one from 10:00 on 3 June, whose window holds A's. Both lock their items, then meet
// before they clear, which without the owner's lock deadlocks, and Postgres aborts one of them. Both
// answer 200, and each change is applied exactly once.
func TestTimeEdit_ConcurrentShiftsBothApply(t *testing.T) {
	f := newTimeFixture(t)
	const ta, tb = "2024-06-01T10:00:00Z", "2024-06-03T10:00:00Z"
	a1 := f.item(t, timeItem{owner: f.alice, name: "a1.jpg", takenAt: ta, gpsSource: "exif", lat: budapestLat, lng: budapestLng})
	insertTrack(t, f.pool, f.alice, "exif", ta, budapestLat, budapestLng)
	a2 := f.item(t, timeItem{owner: f.alice, name: "a2.mp4", mediaType: "video", takenAt: "2024-06-03T10:10:00Z",
		gpsSource: "interpolated", lat: 1, lng: 1})
	b1 := f.item(t, timeItem{owner: f.alice, name: "b1.jpg", takenAt: tb, gpsSource: "exif", lat: viennaLat, lng: viennaLng})
	insertTrack(t, f.pool, f.alice, "exif", tb, viennaLat, viennaLng)
	b2 := f.item(t, timeItem{owner: f.alice, name: "b2.mp4", mediaType: "video", takenAt: "2024-06-01T10:10:00Z",
		gpsSource: "interpolated", lat: 2, lng: 2})

	h := &MediaHandler{
		db: f.pool, pool: &clearBarrierPool{Pool: f.pool, arrive: meetingPoint(2, time.Second)},
		matcherPool: f.pool, matchWindow: time.Hour, tripGapHours: 24,
		runTripDetect: RunTripDetect, runMatcherAround: RunGPSMatcherAround,
	}
	shift := func(id1, id2 string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"media_ids":[%q,%q],"delta_seconds":3600}`, id1, id2)
		r := httptest.NewRequest(http.MethodPost, "/api/media/shift-time", strings.NewReader(body))
		r = r.WithContext(auth.WithUser(r.Context(), auth.User{ID: f.alice, Role: "user"}, auth.MethodSession))
		w := httptest.NewRecorder()
		h.ShiftTimes(w, r)
		return w
	}

	var wg sync.WaitGroup
	var recA, recB *httptest.ResponseRecorder
	wg.Add(2)
	go func() { defer wg.Done(); recA = shift(a1, a2) }()
	go func() { defer wg.Done(); recB = shift(b1, b2) }()
	wg.Wait()

	for name, rec := range map[string]*httptest.ResponseRecorder{"A": recA, "B": recB} {
		require.Equal(t, http.StatusOK, rec.Code, "shift %s: %s", name, rec.Body.String())
		var resp shiftTimesResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, 2, resp.Shifted, "shift %s", name)
	}
	for id, want := range map[string]string{
		a1: "2024-06-01T11:00:00Z", a2: "2024-06-03T11:10:00Z", b1: "2024-06-03T11:00:00Z", b2: "2024-06-01T11:10:00Z",
	} {
		assert.Equal(t, want, f.takenAt(t, id), "moved by an hour, once")
		e := f.edit(t, id, time.Time{}, time.Now())
		assert.Equal(t, 3600.0, e["delta_s"])
		assert.Equal(t, e["from"], e["original"], "a first change: no earlier attempt was kept")
	}
	assert.Equal(t, []string{"2024-06-01T11:00:00Z", "2024-06-03T11:00:00Z"}, retimeTrackTimes(t, f.pool, f.alice, "exif"))
	// Each interpolation was cleared and placed again from the other shift's moved point.
	for id, lat := range map[string]float64{a2: viennaLat, b2: budapestLat} {
		s := f.state(t, id)
		require.NotNil(t, s.gpsSource)
		assert.Equal(t, "interpolated", *s.gpsSource)
		assert.InDelta(t, lat, *s.lat, 1e-9)
	}
	assert.Empty(t, f.orphans(t, f.alice))
}

// beforeWriteDB runs before once, just before the first write through it.
type beforeWriteDB struct {
	*pgxpool.Pool
	once   sync.Once
	before func()
}

func (d *beforeWriteDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.once.Do(d.before)
	return d.Pool.Exec(ctx, sql, args...)
}

func TestRunGPSMatcherAround_Integration(t *testing.T) {
	pool := testdb.New(t)
	f := &timeFixture{pool: pool}
	alice, bob := createUser(t, pool, "alice"), createUser(t, pool, "bob")
	const near = "2024-05-16T10:00:00Z"
	insertTrack(t, pool, alice, "gpx:a.gpx", "2024-05-16T09:50:00Z", 47.0, 19.0)
	insertTrack(t, pool, alice, "gpx:a.gpx", "2024-05-16T10:10:00Z", 47.2, 19.0)
	insertTrack(t, pool, alice, "gpx:a.gpx", "2024-05-18T09:50:00Z", 48.0, 16.0)
	insertTrack(t, pool, alice, "gpx:a.gpx", "2024-05-18T10:10:00Z", 48.2, 16.0)
	insertTrack(t, pool, bob, "gpx:b.gpx", "2024-05-16T09:55:00Z", 40.0, 10.0)

	unplaced := f.item(t, timeItem{owner: alice, name: "unplaced.jpg", takenAt: near})
	stale := f.item(t, timeItem{owner: alice, name: "stale.jpg", takenAt: "2024-05-16T10:05:00Z",
		gpsSource: "interpolated", lat: 10, lng: 10})
	pinned := f.item(t, timeItem{owner: alice, name: "pinned.jpg", takenAt: "2024-05-16T10:02:00Z",
		gpsSource: "manual", lat: 1, lng: 1})
	located := f.item(t, timeItem{owner: alice, name: "located.jpg", takenAt: "2024-05-16T10:03:00Z",
		gpsSource: "exif", lat: 2, lng: 2})
	edge := f.item(t, timeItem{owner: alice, name: "edge.jpg", takenAt: "2024-05-16T11:00:00Z"})
	beyond := f.item(t, timeItem{owner: alice, name: "beyond.jpg", takenAt: "2024-05-16T11:00:01Z"})
	otherDay := f.item(t, timeItem{owner: alice, name: "other-day.jpg", takenAt: "2024-05-18T10:00:00Z"})
	bobs := f.item(t, timeItem{owner: bob, name: "bob.jpg", takenAt: near})

	require.NoError(t, RunGPSMatcherAround(context.Background(), pool, alice, time.Hour, []time.Time{retimeUTC(t, near)}))

	lat := func(id string) *float64 { return f.state(t, id).lat }
	require.NotNil(t, lat(unplaced))
	assert.InDelta(t, 47.1, *lat(unplaced), 1e-3)
	require.NotNil(t, lat(stale))
	assert.InDelta(t, 47.15, *lat(stale), 1e-3, "an interpolation within the window is placed again")
	assert.InDelta(t, 1, *lat(pinned), 1e-9, "a location set by hand stays")
	assert.InDelta(t, 2, *lat(located), 1e-9, "an EXIF location stays")
	require.NotNil(t, lat(edge), "the window's edge is in")
	assert.InDelta(t, 47.2, *lat(edge), 1e-9)
	assert.Nil(t, lat(beyond), "a second past the window: not listed, though a point would place it")
	assert.Nil(t, lat(otherDay), "far from every instant")
	assert.Nil(t, lat(bobs), "never another user's row, whatever their points")

	t.Run("a full run places what the scoped one left", func(t *testing.T) {
		require.NoError(t, RunGPSMatcher(context.Background(), pool, alice, time.Hour, true))
		assert.NotNil(t, lat(beyond))
		assert.NotNil(t, lat(otherDay))
	})

	t.Run("the write is bound to the row as listed", func(t *testing.T) {
		moving := f.item(t, timeItem{owner: alice, name: "moving.jpg", takenAt: "2024-05-16T09:58:00Z"})
		pinning := f.item(t, timeItem{owner: alice, name: "pinning.jpg", takenAt: "2024-05-16T09:59:00Z"})
		db := &beforeWriteDB{Pool: pool, before: func() {
			// A capture time change and a location set by hand land between the listing and the writes.
			_, err := pool.Exec(context.Background(),
				`UPDATE media SET taken_at = taken_at + interval '1 day' WHERE id = $1`, moving)
			require.NoError(t, err)
			_, err = pool.Exec(context.Background(), `
				UPDATE media SET location = ST_SetSRID(ST_MakePoint(5, 5), 4326), gps_source = 'manual'
				WHERE id = $1`, pinning)
			require.NoError(t, err)
		}}
		require.NoError(t, RunGPSMatcherAround(context.Background(), db, alice, time.Hour, []time.Time{retimeUTC(t, near)}))
		assert.Nil(t, lat(moving), "no position from the time it was listed at")
		require.NotNil(t, lat(pinning))
		assert.InDelta(t, 5, *lat(pinning), 1e-9, "the location set by hand stays")
	})
}
