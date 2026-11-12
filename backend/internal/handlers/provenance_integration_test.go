package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// All of it is synthetic: a date nobody photographed and a point in open water.
const (
	provAt             = "2001-02-03T12:00:00Z"
	provLat, provLng   = 10.0, 20.0
	provLat2, provLng2 = 10.01, 20.01
)

var provTime = mustParseTime(provAt)

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// provOffset is a timestamp offset from the items' capture time.
func provOffset(d time.Duration) string {
	return provTime.Add(d).Format(time.RFC3339Nano)
}

type provenanceFixture struct {
	pool   *pgxpool.Pool
	router http.Handler
}

func newProvenanceFixture(t *testing.T) *provenanceFixture {
	t.Helper()
	pool := testdb.New(t)
	return &provenanceFixture{pool: pool, router: newOwnershipRouter(pool, &fakeObjectStore{})}
}

var provItemSeq atomic.Int64

type provItem struct {
	filename   string
	takenAt    string // empty is NULL
	lat, lng   *float64
	gpsSource  *string
	confidence *float64
	evidence   string
}

func (f *provenanceFixture) item(t *testing.T, owner string, m provItem) string {
	t.Helper()
	if m.filename == "" {
		m.filename = fmt.Sprintf("item-%d.jpg", provItemSeq.Add(1))
	}
	var takenAt, evidence *string
	if m.takenAt != "" {
		takenAt = &m.takenAt
	}
	if m.evidence != "" {
		evidence = &m.evidence
	}
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, taken_at, location, gps_source, gps_confidence, capture_evidence)
		VALUES ($1, $2, 'photo', $9, $10, $3::timestamptz,
		        CASE WHEN $4::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($5, $4), 4326) END, $6, $7, $8::jsonb)
		RETURNING id::text`,
		owner, m.filename, takenAt, m.lat, m.lng, m.gpsSource, m.confidence, evidence,
		"media/"+owner+"/"+m.filename, "hash-"+owner+"-"+m.filename).Scan(&id))
	return id
}

func (f *provenanceFixture) get(t *testing.T, user, id string) ProvenanceResponse {
	t.Helper()
	rec := do(t, f.router, user, http.MethodGet, "/api/media/"+id+"/provenance", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp ProvenanceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp
}

func insertTrackDetail(t *testing.T, pool *pgxpool.Pool, ownerID, source, at string, lat, lng float64, accuracy, altitude *float64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO gps_tracks (owner_id, source, recorded_at, location, accuracy_m, altitude_m)
		VALUES ($1, $2, $3::timestamptz, ST_SetSRID(ST_MakePoint($4, $5), 4326), $6, $7)`,
		ownerID, source, at, lng, lat, accuracy, altitude)
	require.NoError(t, err)
}

func runMatcherFor(t *testing.T, pool *pgxpool.Pool, owner string) {
	t.Helper()
	require.NoError(t, RunGPSMatcher(context.Background(), pool, owner, time.Hour, true))
}

func TestProvenance_PlacementStates(t *testing.T) {
	f := newProvenanceFixture(t)
	ctx := context.Background()
	estimate := ptr("interpolated")

	inWindow := func(t *testing.T, owner string) {
		insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-10*time.Minute), provLat, provLng)
		insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(10*time.Minute), provLat2, provLng2)
	}
	outsideWindow := func(t *testing.T, owner string) {
		insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-2*time.Hour), provLat, provLng)
		insertTrack(t, f.pool, owner, "nmea:b.nmea", provOffset(3*time.Hour), provLat2, provLng2)
	}
	matched := func(t *testing.T, owner string, tweak string) string {
		inWindow(t, owner)
		id := f.item(t, owner, provItem{takenAt: provAt})
		runMatcherFor(t, f.pool, owner)
		if tweak != "" {
			_, err := f.pool.Exec(ctx, `UPDATE media SET `+tweak+` WHERE id = $1`, id)
			require.NoError(t, err)
		}
		return id
	}

	tests := []struct {
		name        string
		setup       func(t *testing.T, owner string) string
		wantState   string
		wantNow     bool
		wantOutside bool
	}{
		{"manual", func(t *testing.T, owner string) string {
			inWindow(t, owner)
			return f.item(t, owner, provItem{takenAt: provAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("manual")})
		}, placementManual, false, false},
		{"own position", func(t *testing.T, owner string) string {
			inWindow(t, owner)
			return f.item(t, owner, provItem{takenAt: provAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("exif")})
		}, placementOwnPosition, false, false},
		{"no time", func(t *testing.T, owner string) string {
			inWindow(t, owner)
			return f.item(t, owner, provItem{})
		}, placementNoTime, false, false},
		{"pending", func(t *testing.T, owner string) string {
			inWindow(t, owner)
			return f.item(t, owner, provItem{takenAt: provAt})
		}, placementPending, true, false},
		{"pending, gps_source none", func(t *testing.T, owner string) string {
			inWindow(t, owner)
			return f.item(t, owner, provItem{takenAt: provAt, gpsSource: ptr("none")})
		}, placementPending, true, false},
		{"no bracket", func(t *testing.T, owner string) string {
			outsideWindow(t, owner)
			return f.item(t, owner, provItem{takenAt: provAt})
		}, placementNoBracket, false, true},
		{"no bracket and no tracks at all", func(t *testing.T, owner string) string {
			return f.item(t, owner, provItem{takenAt: provAt})
		}, placementNoBracket, false, true},
		{"no support", func(t *testing.T, owner string) string {
			outsideWindow(t, owner)
			return f.item(t, owner, provItem{takenAt: provAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: estimate, confidence: ptr(0.5)})
		}, placementNoSupport, false, true},
		{"current", func(t *testing.T, owner string) string {
			return matched(t, owner, "")
		}, placementCurrent, true, false},
		{"stale: the position moved", func(t *testing.T, owner string) string {
			return matched(t, owner, `location = ST_SetSRID(ST_MakePoint(21, 11), 4326)`)
		}, placementStale, true, false},
		{"stale: the confidence changed", func(t *testing.T, owner string) string {
			return matched(t, owner, `gps_confidence = gps_confidence - 0.01`)
		}, placementStale, true, false},
		{"stale: no stored confidence", func(t *testing.T, owner string) string {
			return matched(t, owner, `gps_confidence = NULL`)
		}, placementStale, true, false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner := createUser(t, f.pool, fmt.Sprintf("state-%d", i))
			id := tt.setup(t, owner)

			got := f.get(t, owner, id)

			assert.Equal(t, tt.wantState, got.Placement.State)
			assert.EqualValues(t, 3600, got.Placement.WindowS)
			assert.Equal(t, tt.wantNow, got.Placement.Now != nil, "now")
			assert.Equal(t, tt.wantOutside, got.Placement.NearestOutside != nil, "nearest_outside")
			assert.NotNil(t, got.Placement.Others)
			assert.NotNil(t, got.Placement.Warnings)
		})
	}

	t.Run("no track points at all leaves both nearest points null", func(t *testing.T) {
		owner := createUser(t, f.pool, "empty")
		got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt}))

		require.NotNil(t, got.Placement.NearestOutside)
		assert.Nil(t, got.Placement.NearestOutside.Before)
		assert.Nil(t, got.Placement.NearestOutside.After)
	})

	t.Run("nearest points outside the window", func(t *testing.T) {
		owner := createUser(t, f.pool, "outside")
		outsideWindow(t, owner)
		// Inside no window of the item, but nearer than either: a closer pair must win.
		insertTrack(t, f.pool, owner, "kml:c.kml", provOffset(-90*time.Minute), provLat2, provLng2)

		got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt}))

		require.NotNil(t, got.Placement.NearestOutside)
		before, after := got.Placement.NearestOutside.Before, got.Placement.NearestOutside.After
		require.NotNil(t, before)
		require.NotNil(t, after)
		assert.Equal(t, "kml:c.kml", before.Source)
		assert.Equal(t, "kml", before.Format)
		assert.InDelta(t, 5400, before.GapS, 1e-6)
		assert.Equal(t, "nmea:b.nmea", after.Source)
		assert.InDelta(t, 10800, after.GapS, 1e-6)
	})

	t.Run("an item with no time has empty arrays and a match window", func(t *testing.T) {
		owner := createUser(t, f.pool, "no-time")
		inWindow(t, owner)

		rec := do(t, f.router, owner, http.MethodGet, "/api/media/"+f.item(t, owner, provItem{})+"/provenance", "")

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var raw struct {
			Placement struct {
				Others   json.RawMessage `json:"others"`
				Warnings json.RawMessage `json:"warnings"`
			} `json:"placement"`
			Context struct {
				Windows      json.RawMessage `json:"windows"`
				Sources      json.RawMessage `json:"sources"`
				Points       json.RawMessage `json:"points"`
				MatchWindowS int             `json:"match_window_s"`
			} `json:"context"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		assert.JSONEq(t, "[]", string(raw.Placement.Others))
		assert.JSONEq(t, "[]", string(raw.Placement.Warnings))
		assert.JSONEq(t, "[]", string(raw.Context.Windows))
		assert.JSONEq(t, "[]", string(raw.Context.Sources))
		assert.JSONEq(t, "[]", string(raw.Context.Points))
		assert.Equal(t, 3600, raw.Context.MatchWindowS)
	})
}

// The matcher's own query and arithmetic, so a matched item is reproduced exactly.
func TestProvenance_RecomputeEqualsTheMatcher(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-15*time.Minute), provLat, provLng)
	insertTrack(t, f.pool, owner, "nmea:b.nmea", provOffset(5*time.Minute), provLat2, provLng2)
	id := f.item(t, owner, provItem{takenAt: provAt})
	runMatcherFor(t, f.pool, owner)
	stored := snapshotMedia(t, f.pool, id)

	got := f.get(t, owner, id)

	require.Equal(t, placementCurrent, got.Placement.State)
	now := got.Placement.Now
	require.NotNil(t, stored.lat)
	assert.InDelta(t, *stored.lat, now.Lat, 1e-9)
	assert.InDelta(t, *stored.lng, now.Lng, 1e-9)
	assert.Equal(t, "two_sided", now.Arithmetic.Shape)
	assert.InDelta(t, 0.75, *now.Arithmetic.T, 1e-9)
	require.NotNil(t, now.DeltaM)
	assert.LessOrEqual(t, *now.DeltaM, 1.0)
	require.NotNil(t, now.Before)
	require.NotNil(t, now.After)
	assert.Equal(t, "gpx:a.gpx", now.Before.Source)
	assert.Equal(t, "gpx", now.Before.Format)
	assert.Equal(t, bracketKindLog, now.Before.Kind)
	assert.Nil(t, now.Before.Origin, "a log point has no origin")
	assert.InDelta(t, 900, now.Before.GapS, 1e-6)
	assert.InDelta(t, 300, now.After.GapS, 1e-6)
}

func TestProvenance_ItemFieldsAndEvidence(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	const evidence = `{"v":1,"by":"ingest","src":"exif_original","camera_zone":"UTC"}`
	id := f.item(t, owner, provItem{filename: "fields.jpg", takenAt: provAt, lat: ptr(provLat), lng: ptr(provLng),
		gpsSource: ptr("exif"), evidence: evidence})
	_, err := f.pool.Exec(context.Background(),
		`UPDATE media SET camera_model = 'Model X', width_px = 640, height_px = 480, duration_sec = 1.5 WHERE id = $1`, id)
	require.NoError(t, err)

	got := f.get(t, owner, id)

	assert.Equal(t, id, got.Item.ID)
	assert.Equal(t, "fields.jpg", got.Item.Filename)
	assert.Equal(t, "photo", got.Item.MediaType)
	require.NotNil(t, got.Item.TakenAt)
	assert.True(t, got.Item.TakenAt.Equal(provTime))
	assert.False(t, got.Item.CreatedAt.IsZero())
	assert.Equal(t, ptr(provLat), got.Item.Lat)
	assert.Equal(t, ptr(provLng), got.Item.Lng)
	assert.Equal(t, ptr("exif"), got.Item.GPSSource)
	assert.Nil(t, got.Item.GPSConfidence)
	assert.Equal(t, ptr("Model X"), got.Item.CameraModel)
	assert.Equal(t, ptr(640), got.Item.WidthPx)
	assert.Equal(t, ptr(480), got.Item.HeightPx)
	assert.Equal(t, ptr(1.5), got.Item.DurationSec)

	assert.Equal(t, "ingest", got.Time.Recorded)
	assert.JSONEq(t, evidence, string(got.Time.Evidence))
	assert.Equal(t, "server zone (unset)", got.Time.ServerCameraZone.Name, "the test router has a zero zone")

	t.Run("an item without evidence is not_recorded", func(t *testing.T) {
		rec := do(t, f.router, owner, http.MethodGet, "/api/media/"+f.item(t, owner, provItem{takenAt: provAt})+"/provenance", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var raw struct {
			Time struct {
				Recorded string          `json:"recorded"`
				Evidence json.RawMessage `json:"evidence"`
			} `json:"time"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		assert.Equal(t, "not_recorded", raw.Time.Recorded)
		assert.JSONEq(t, "null", string(raw.Time.Evidence))
	})
}

// A year RFC 3339 cannot express must not cost the owner the whole view: the row can exist, because
// PostgreSQL accepts it and ingest stores what a crafted file says.
func TestProvenance_StoredYearJSONCannotCarry(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	id := f.item(t, owner, provItem{takenAt: "10000-01-01T00:00:00Z"})

	got := f.get(t, owner, id)

	assert.Nil(t, got.Item.TakenAt)
	assert.Equal(t, "no_time", got.Placement.State)
	assert.Empty(t, got.Context.Points)
}

// A time JSON can carry can still sit so near the limits of RFC 3339 that the window around it does
// not: that window, and every point inside it, would lose the whole response.
func TestProvenance_MatchWindowJSONCannotCarry(t *testing.T) {
	f := newProvenanceFixture(t)
	tests := []struct {
		name, takenAt string
	}{
		{"the window ends in year 10000", "9999-12-31T23:59:00Z"},
		{"the window starts in year -1", "0001-01-01 00:10:00+00 BC"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner := createUser(t, f.pool, fmt.Sprintf("limit-%d", i))
			got := f.get(t, owner, f.item(t, owner, provItem{takenAt: tt.takenAt}))

			assert.Nil(t, got.Item.TakenAt)
			assert.Equal(t, "no_time", got.Placement.State)
			assert.Empty(t, got.Context.Windows)
			assert.Empty(t, got.Context.Points)
		})
	}
}

// The nearest point on a side has no time limit, so a row JSON cannot carry can be it. Every row
// further out on that side is beyond the limit too, so the side has no point to show.
func TestProvenance_NearestPointJSONCannotCarry(t *testing.T) {
	f := newProvenanceFixture(t)
	const pastTheEnd, beforeTheStart = "9999-12-31T23:59:59-14:00", "0001-01-01 00:00:00+01 BC"
	inRangeBefore, inRangeAfter := provOffset(-2*time.Hour), provOffset(2*time.Hour)

	tests := []struct {
		name, before, after string
		wantBefore          bool
		wantAfter           bool
	}{
		{"later than year 9999", inRangeBefore, pastTheEnd, true, false},
		{"earlier than year 0", beforeTheStart, inRangeAfter, false, true},
		{"both sides", beforeTheStart, pastTheEnd, false, false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner := createUser(t, f.pool, fmt.Sprintf("nearest-limit-%d", i))
			insertTrack(t, f.pool, owner, "gpx:before.gpx", tt.before, provLat, provLng)
			insertTrack(t, f.pool, owner, "gpx:after.gpx", tt.after, provLat2, provLng2)

			got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt}))

			assert.Equal(t, "no_bracket", got.Placement.State)
			require.NotNil(t, got.Placement.NearestOutside)
			assert.Equal(t, tt.wantBefore, got.Placement.NearestOutside.Before != nil, "before")
			assert.Equal(t, tt.wantAfter, got.Placement.NearestOutside.After != nil, "after")
		})
	}
}

func TestProvenance_OthersOneRowPerFormat(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	accuracy := 7.5

	insertTrack(t, f.pool, owner, "gpx:far.gpx", provOffset(-40*time.Minute), provLat, provLng)
	insertTrackDetail(t, f.pool, owner, "gpx:near.gpx", provOffset(-10*time.Minute), provLat, provLng, &accuracy, nil)
	// Two unknown prefixes at the same distance: two rows, each as written, ordered by prefix.
	insertTrack(t, f.pool, owner, "strava:ride", provOffset(-5*time.Minute), provLat, provLng)
	insertTrack(t, f.pool, owner, "komoot:tour", provOffset(5*time.Minute), provLat2, provLng2)
	insertTrack(t, f.pool, owner, "exif", provOffset(20*time.Minute), provLat, provLng)
	insertTrack(t, f.pool, owner, "google_timeline:r.json", provOffset(70*time.Minute), provLat, provLng) // outside the window

	want := []struct {
		format, source string
		gapS           float64
		used           bool
	}{
		{"komoot", "komoot:tour", 300, true},
		{"strava", "strava:ride", 300, true},
		{"gpx", "gpx:near.gpx", 600, false},
		{"exif", "exif", 1200, false},
	}
	check := func(t *testing.T, got []provenanceOtherFormat) {
		require.Len(t, got, len(want))
		for i, w := range want {
			assert.Equal(t, w.format, got[i].Format, "row %d", i)
			assert.Equal(t, w.source, got[i].Source, "row %d", i)
			assert.InDelta(t, w.gapS, got[i].GapS, 1e-6, "row %d", i)
			assert.Equal(t, w.used, got[i].Used, "row %d", i)
			require.NotNil(t, got[i].DistanceM, "row %d", i)
		}
		assert.Equal(t, &accuracy, got[2].AccuracyM)
		assert.Nil(t, got[0].AccuracyM)
	}

	t.Run("distance from now when there is no stored position", func(t *testing.T) {
		got := f.get(t, owner, f.item(t, owner, provItem{filename: "pending.jpg", takenAt: provAt}))

		require.Equal(t, placementPending, got.Placement.State)
		check(t, got.Placement.Others)
		now := got.Placement.Now
		wantM := geo.HaversineKm(now.Lat, now.Lng, got.Placement.Others[0].Lat, got.Placement.Others[0].Lng) * 1000
		assert.InDelta(t, wantM, *got.Placement.Others[0].DistanceM, 1e-6)
	})

	t.Run("distance from the stored position when there is one", func(t *testing.T) {
		const storedLat, storedLng = 11.0, 21.0
		got := f.get(t, owner, f.item(t, owner, provItem{filename: "stale.jpg", takenAt: provAt,
			lat: ptr(storedLat), lng: ptr(storedLng), gpsSource: ptr("interpolated"), confidence: ptr(0.5)}))

		require.Equal(t, placementStale, got.Placement.State)
		check(t, got.Placement.Others)
		wantM := geo.HaversineKm(storedLat, storedLng, got.Placement.Others[0].Lat, got.Placement.Others[0].Lng) * 1000
		assert.InDelta(t, wantM, *got.Placement.Others[0].DistanceM, 1e-6)
	})

	t.Run("an own position has rows and none is used", func(t *testing.T) {
		got := f.get(t, owner, f.item(t, owner, provItem{filename: "own.jpg", takenAt: provAt,
			lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("exif")}))

		require.Len(t, got.Placement.Others, len(want))
		for _, row := range got.Placement.Others {
			assert.False(t, row.Used, row.Source)
		}
	})
}

func TestProvenance_IndependentExcludesDerivedPoints(t *testing.T) {
	f := newProvenanceFixture(t)

	t.Run("a bracket of logs behind a nearer photo and pin", func(t *testing.T) {
		owner := createUser(t, f.pool, "both")
		insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-30*time.Minute), provLat, provLng)
		insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(30*time.Minute), provLat2, provLng2)
		insertTrack(t, f.pool, owner, "exif", provOffset(-time.Minute), 12, 22)
		insertTrack(t, f.pool, owner, "manual", provOffset(time.Minute), 13, 23)

		got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt}))

		require.NotNil(t, got.Placement.Now)
		assert.Equal(t, "exif", got.Placement.Now.Before.Source)
		assert.Equal(t, bracketKindExif, got.Placement.Now.Before.Kind)
		assert.Equal(t, "manual", got.Placement.Now.After.Source)
		assert.Equal(t, bracketKindManual, got.Placement.Now.After.Kind)

		independent := got.Placement.Independent
		require.NotNil(t, independent)
		assert.Equal(t, "gpx:a.gpx", independent.Before.Source)
		assert.Equal(t, "gpx:a.gpx", independent.After.Source)
		assert.Nil(t, independent.Before.Origin)
		assert.InDelta(t, (provLat+provLat2)/2, independent.Lat, 1e-9)
		require.NotNil(t, independent.MovedM, "measured from now, as there is no stored position")
		assert.InDelta(t, geo.HaversineKm(got.Placement.Now.Lat, got.Placement.Now.Lng, independent.Lat, independent.Lng)*1000,
			*independent.MovedM, 1e-6)
	})

	t.Run("only derived points leaves it null", func(t *testing.T) {
		owner := createUser(t, f.pool, "derived")
		insertTrack(t, f.pool, owner, "exif", provOffset(-time.Minute), 12, 22)

		got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt}))

		assert.NotNil(t, got.Placement.Now)
		assert.Nil(t, got.Placement.Independent)
	})

	t.Run("an own position measures the logs from its stored position", func(t *testing.T) {
		owner := createUser(t, f.pool, "own")
		insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-time.Minute), provLat2, provLng2)

		got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("exif")}))

		assert.Nil(t, got.Placement.Now)
		require.NotNil(t, got.Placement.Independent)
		require.NotNil(t, got.Placement.Independent.MovedM)
		assert.InDelta(t, geo.HaversineKm(provLat, provLng, provLat2, provLng2)*1000, *got.Placement.Independent.MovedM, 1e-6)
	})
}

func TestProvenance_Origin(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	stranger := createUser(t, f.pool, "stranger")

	exifAt := provOffset(-10 * time.Minute)
	manualAt := provOffset(10 * time.Minute)
	insertTrack(t, f.pool, owner, "exif", exifAt, provLat, provLng)
	insertTrack(t, f.pool, owner, "manual", manualAt, provLat2, provLng2)
	photo := f.item(t, owner, provItem{filename: "photo.jpg", takenAt: exifAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("exif")})
	// Another user's item at that second, and an own item that is not a source of positions.
	f.item(t, stranger, provItem{filename: "foreign.jpg", takenAt: manualAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("exif")})
	f.item(t, owner, provItem{filename: "estimate.jpg", takenAt: manualAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("interpolated")})

	t.Run("linked and orphan", func(t *testing.T) {
		got := f.get(t, owner, f.item(t, owner, provItem{filename: "subject.jpg", takenAt: provAt}))

		before, after := got.Placement.Now.Before, got.Placement.Now.After
		require.NotNil(t, before.Origin)
		assert.Equal(t, originLinked, before.Origin.State)
		assert.Equal(t, []provenanceOriginItem{{ID: photo, Filename: "photo.jpg"}}, before.Origin.Media)

		require.NotNil(t, after.Origin)
		assert.Equal(t, originOrphan, after.Origin.State, "foreign rows and estimates are no origin")
		assert.NotNil(t, after.Origin.Media)
		assert.Empty(t, after.Origin.Media)
		assert.Contains(t, got.Placement.Warnings, provenanceWarning{Code: warningPointOrphan, Level: warningLevelWarn})
	})

	t.Run("burst, capped at six items", func(t *testing.T) {
		burstAt := provOffset(24 * time.Hour)
		insertTrack(t, f.pool, owner, "exif", burstAt, provLat, provLng)
		for i := range 8 {
			f.item(t, owner, provItem{filename: fmt.Sprintf("burst-%d.jpg", i), takenAt: burstAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("exif")})
		}

		got := f.get(t, owner, f.item(t, owner, provItem{filename: "burst-subject.jpg", takenAt: provOffset(24*time.Hour + time.Minute)}))

		origin := got.Placement.Now.Before.Origin
		require.NotNil(t, origin)
		assert.Equal(t, originBurst, origin.State)
		require.Len(t, origin.Media, 6)
		assert.Equal(t, "burst-0.jpg", origin.Media[0].Filename)
		assert.Empty(t, got.Placement.Warnings)
	})

	t.Run("two items are already a burst", func(t *testing.T) {
		pairAt := provOffset(48 * time.Hour)
		insertTrack(t, f.pool, owner, "manual", pairAt, provLat, provLng)
		for i := range 2 {
			f.item(t, owner, provItem{filename: fmt.Sprintf("pair-%d.jpg", i), takenAt: pairAt, lat: ptr(provLat), lng: ptr(provLng), gpsSource: ptr("manual")})
		}

		got := f.get(t, owner, f.item(t, owner, provItem{filename: "pair-subject.jpg", takenAt: provOffset(48*time.Hour + time.Minute)}))

		assert.Equal(t, originBurst, got.Placement.Now.Before.Origin.State)
		assert.Len(t, got.Placement.Now.Before.Origin.Media, 2)
	})
}

func TestProvenance_ArithmeticOnTheWire(t *testing.T) {
	f := newProvenanceFixture(t)

	tests := []struct {
		name      string
		tracks    []time.Duration
		wantShape string
		wantNull  []string
		wantSet   []string
	}{
		{"one_sided", []time.Duration{-10 * time.Minute}, "one_sided",
			[]string{"pair_gap_s", "pair_km", "t", "distance_factor", "implied_kmh"}, []string{"time_factor", "confidence"}},
		{"exact", []time.Duration{0}, "exact",
			[]string{"t", "implied_kmh"}, []string{"pair_gap_s", "pair_km", "time_factor", "distance_factor", "confidence"}},
		{"two_sided", []time.Duration{-10 * time.Minute, 10 * time.Minute}, "two_sided",
			nil, []string{"pair_gap_s", "pair_km", "t", "time_factor", "distance_factor", "confidence", "implied_kmh"}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner := createUser(t, f.pool, fmt.Sprintf("wire-%d", i))
			for _, offset := range tt.tracks {
				insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(offset), provLat, provLng)
			}

			rec := do(t, f.router, owner, http.MethodGet, "/api/media/"+f.item(t, owner, provItem{takenAt: provAt})+"/provenance", "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var raw struct {
				Placement struct {
					Now struct {
						Arithmetic map[string]any `json:"arithmetic"`
					} `json:"now"`
				} `json:"placement"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
			arithmetic := raw.Placement.Now.Arithmetic
			assert.Equal(t, tt.wantShape, arithmetic["shape"])
			for _, key := range tt.wantNull {
				value, present := arithmetic[key]
				assert.True(t, present, key)
				assert.Nil(t, value, key)
			}
			for _, key := range tt.wantSet {
				assert.NotNil(t, arithmetic[key], key)
			}
		})
	}
}

func TestProvenance_TiedPointsPickOneRowOnBothSides(t *testing.T) {
	f := newProvenanceFixture(t)
	ctx := context.Background()

	t.Run("a tie at the exact instant is the same row twice", func(t *testing.T) {
		owner := createUser(t, f.pool, "exact")
		insertTrack(t, f.pool, owner, "gpx:b.gpx", provAt, provLat2, provLng2)
		insertTrack(t, f.pool, owner, "gpx:a.gpx", provAt, provLat, provLng)

		got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt}))

		now := got.Placement.Now
		assert.Equal(t, "exact", now.Arithmetic.Shape)
		assert.Equal(t, "gpx:a.gpx", now.Before.Source)
		assert.Equal(t, now.Before, now.After)
		assert.Equal(t, provLat, now.Lat)
		assert.Equal(t, 1.0, now.Confidence)

		before, after, err := bracketingPoints(ctx, f.pool, owner, provTime, time.Hour, bracketOptions{})
		require.NoError(t, err)
		assert.Equal(t, before, after, "both queries return one row")
	})

	t.Run("a tie on each side picks the first source", func(t *testing.T) {
		owner := createUser(t, f.pool, "sides")
		for _, offset := range []time.Duration{-time.Minute, time.Minute} {
			insertTrack(t, f.pool, owner, "nmea:z.nmea", provOffset(offset), 12, 22)
			insertTrack(t, f.pool, owner, "gpx:m.gpx", provOffset(offset), 13, 23)
			insertTrack(t, f.pool, owner, "esoko:a.log", provOffset(offset), provLat, provLng)
		}

		before, after, err := bracketingPoints(ctx, f.pool, owner, provTime, time.Hour, bracketOptions{})

		require.NoError(t, err)
		require.NotNil(t, before)
		require.NotNil(t, after)
		assert.Equal(t, "esoko:a.log", before.Source)
		assert.Equal(t, "esoko:a.log", after.Source)
	})
}

func TestBracketingPoints_ExclusionsAgainstRealRows(t *testing.T) {
	f := newProvenanceFixture(t)
	ctx := context.Background()
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "exif", provOffset(-time.Minute), 1, 1)
	insertTrack(t, f.pool, owner, "exif:extra", provOffset(-2*time.Minute), 2, 2)
	insertTrack(t, f.pool, owner, "exifish:name", provOffset(-3*time.Minute), 3, 3)
	insertTrack(t, f.pool, owner, "manual", provOffset(time.Minute), 4, 4)
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(5*time.Minute), 5, 5)

	tests := []struct {
		name                  string
		opts                  bracketOptions
		wantBefore, wantAfter string
	}{
		{"no options sees every point (a nil list is normalised)", bracketOptions{}, "exif", "manual"},
		{"excludes only the whole prefix", bracketOptions{excludeFormats: []string{"exif", "manual"}}, "exifish:name", "gpx:a.gpx"},
		{"an empty list sees every point", bracketOptions{excludeFormats: []string{}}, "exif", "manual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, after, err := bracketingPoints(ctx, f.pool, owner, provTime, time.Hour, tt.opts)

			require.NoError(t, err)
			require.NotNil(t, before)
			require.NotNil(t, after)
			assert.Equal(t, tt.wantBefore, before.Source)
			assert.Equal(t, tt.wantAfter, after.Source)
		})
	}

	t.Run("accuracy and altitude come back", func(t *testing.T) {
		other := createUser(t, f.pool, "details")
		accuracy, altitude := 3.5, 250.0
		insertTrackDetail(t, f.pool, other, "gpx:a.gpx", provOffset(-time.Minute), 1, 2, &accuracy, &altitude)

		before, after, err := bracketingPoints(ctx, f.pool, other, provTime, time.Hour, bracketOptions{})

		require.NoError(t, err)
		assert.Nil(t, after)
		require.NotNil(t, before)
		assert.Equal(t, "gpx:a.gpx", before.Source)
		assert.Equal(t, 1.0, before.Lat)
		assert.Equal(t, 2.0, before.Lng)
		assert.Equal(t, &accuracy, before.AccuracyM)
		assert.Equal(t, &altitude, before.AltitudeM)
	})
}

// The mock tests ignore their arguments, so only a real query shows that a nil exclusion list still
// matches rows: x <> ALL(NULL) would leave every item unplaced.
func TestRunGPSMatcher_PlacesItemsWithNoExclusions(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-10*time.Minute), provLat, provLng)
	insertTrack(t, f.pool, owner, "exif", provOffset(10*time.Minute), provLat2, provLng2)
	id := f.item(t, owner, provItem{takenAt: provAt})

	runMatcherFor(t, f.pool, owner)

	got := snapshotMedia(t, f.pool, id)
	require.NotNil(t, got.lat)
	assert.InDelta(t, (provLat+provLat2)/2, *got.lat, 1e-9, "the exif point counts for the matcher")
	require.NotNil(t, got.gpsSource)
	assert.Equal(t, "interpolated", *got.gpsSource)
}

// insertTrackSeries writes one point per step from start, count times, under one source label.
func insertTrackSeries(t *testing.T, pool *pgxpool.Pool, ownerID, source string, start time.Time, step time.Duration, count int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
		SELECT $1, $2, $3::timestamptz + (n * $4::float8) * interval '1 second', ST_SetSRID(ST_MakePoint(20, 10), 4326)
		FROM generate_series(0, $5::int - 1) AS n`, ownerID, source, start, step.Seconds(), count)
	require.NoError(t, err)
}

func TestLoadTrack(t *testing.T) {
	f := newProvenanceFixture(t)
	ctx := context.Background()
	window := []timeWindow{{from: provTime.Add(-time.Hour), to: provTime.Add(time.Hour)}}

	sourceCounts := func(resp TrackResponse) map[string]int {
		counts := map[string]int{}
		for _, p := range resp.Points {
			counts[resp.Sources[p.S].Label]++
		}
		return counts
	}

	t.Run("within the budget returns every row", func(t *testing.T) {
		owner := createUser(t, f.pool, "under")
		insertTrackSeries(t, f.pool, owner, "gpx:a.gpx", provTime.Add(-time.Hour), time.Minute, 10)
		insertTrackSeries(t, f.pool, owner, "exif", provTime, time.Minute, 5)

		resp, err := loadTrack(ctx, f.pool, owner, window, 10)

		require.NoError(t, err)
		assert.Zero(t, resp.BucketS)
		assert.Equal(t, 15, resp.TotalPoints)
		assert.Equal(t, 15, resp.ReturnedPoints)
		assert.Equal(t, map[string]int{"gpx:a.gpx": 10, "exif": 5}, sourceCounts(resp))
	})

	t.Run("over the budget keeps one real row per format and bucket", func(t *testing.T) {
		const maxPoints = 40
		owner := createUser(t, f.pool, "over")
		start := provTime.Add(-time.Hour)
		insertTrackSeries(t, f.pool, owner, "gpx:a.gpx", start, 10*time.Second, 361) // reaches exactly the window's end
		insertTrackSeries(t, f.pool, owner, "gpx:b.gpx", start.Add(5*time.Second), 10*time.Second, 360)
		insertTrackSeries(t, f.pool, owner, "nmea:c.nmea", start.Add(3*time.Second), 10*time.Second, 360)
		insertTrackSeries(t, f.pool, owner, "exif", start.Add(time.Second), time.Minute, 70)
		insertTrackSeries(t, f.pool, owner, "manual", start.Add(2*time.Second), time.Minute, 30)
		// A second owner's rows at the same moments, earlier in each bucket than the owner's own NMEA
		// rows and under their own label and position, and a row outside the window.
		const strangerLabel, strangerLat = "nmea:foreign.nmea", 11.0
		stranger := createUser(t, f.pool, "over-stranger")
		insertTrackSeries(t, f.pool, stranger, strangerLabel, start, 10*time.Second, 361)
		_, err := f.pool.Exec(ctx, `UPDATE gps_tracks SET location = ST_SetSRID(ST_MakePoint(21, $2), 4326) WHERE owner_id = $1`, stranger, strangerLat)
		require.NoError(t, err)
		insertTrackSeries(t, f.pool, owner, "gpx:a.gpx", start.Add(3*time.Hour), time.Second, 100)

		resp, err := loadTrack(ctx, f.pool, owner, window, maxPoints)

		require.NoError(t, err)
		const logFormats, derived = 2, 100
		assert.Equal(t, 361+360+360+derived, resp.TotalPoints, "log and derived rows, only in the window")
		assert.Equal(t, 360, resp.BucketS, "ceil(7200 * 2 / 40)")
		assert.Equal(t, len(resp.Points), resp.ReturnedPoints)
		assert.LessOrEqual(t, resp.ReturnedPoints, maxPoints+logFormats+derived)

		counts := sourceCounts(resp)
		assert.Equal(t, 70, counts["exif"], "derived rows are never thinned")
		assert.Equal(t, 30, counts["manual"], "derived rows are never thinned")
		logReturned := resp.ReturnedPoints - derived
		assert.Greater(t, logReturned, 0)
		assert.LessOrEqual(t, logReturned, maxPoints+logFormats)
		assert.NotContains(t, counts, strangerLabel, "another owner's track never appears")

		for i, p := range resp.Points {
			if i > 0 {
				assert.False(t, p.T.Before(resp.Points[i-1].T), "points are in time order")
			}
			assert.NotEqual(t, strangerLat, p.Lat, "another owner's position never appears")
			var rows int
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT count(*) FROM gps_tracks WHERE owner_id = $1 AND source = $2 AND recorded_at = $3`,
				owner, resp.Sources[p.S].Label, p.T).Scan(&rows))
			assert.Equal(t, 1, rows, "each point is a real row")
		}
	})

	t.Run("no rows is empty arrays", func(t *testing.T) {
		resp, err := loadTrack(ctx, f.pool, createUser(t, f.pool, "none"), window, 10)

		require.NoError(t, err)
		assert.Empty(t, resp.Points)
		assert.NotNil(t, resp.Points)
		assert.NotNil(t, resp.Sources)
		require.Len(t, resp.Windows, 1)
		assert.Zero(t, resp.BucketS)
	})

	t.Run("sources are labelled by format", func(t *testing.T) {
		owner := createUser(t, f.pool, "labels")
		for _, source := range []string{"kml:%weird_name.kml", "google_timeline:a.json", "custom", "manual"} {
			insertTrack(t, f.pool, owner, source, provAt, provLat, provLng)
		}

		resp, err := loadTrack(ctx, f.pool, owner, window, 10)

		require.NoError(t, err)
		assert.ElementsMatch(t, []TrackSource{
			{Label: "kml:%weird_name.kml", Format: "kml", Kind: trackKindTimeMayBeEstimated},
			{Label: "google_timeline:a.json", Format: "google_timeline", Kind: trackKindSemantic},
			{Label: "custom", Format: "other", Kind: trackKindOther},
			{Label: "manual", Format: "manual", Kind: trackKindDerived},
		}, resp.Sources)
	})

	t.Run("accuracy and altitude are carried", func(t *testing.T) {
		owner := createUser(t, f.pool, "detail")
		accuracy, altitude := 8.5, 300.0
		insertTrackDetail(t, f.pool, owner, "gpx:a.gpx", provAt, provLat, provLng, &accuracy, &altitude)

		resp, err := loadTrack(ctx, f.pool, owner, window, 10)

		require.NoError(t, err)
		require.Len(t, resp.Points, 1)
		assert.Equal(t, provLat, resp.Points[0].Lat)
		assert.Equal(t, provLng, resp.Points[0].Lng)
		assert.Equal(t, &accuracy, resp.Points[0].Acc)
		assert.Equal(t, &altitude, resp.Points[0].Alt)
	})
}

func TestProvenance_ContextBudget(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	start := provTime.Add(-time.Hour)
	insertTrackSeries(t, f.pool, owner, "gpx:a.gpx", start, 2*time.Second, 3000)
	insertTrackSeries(t, f.pool, owner, "nmea:b.nmea", start.Add(time.Second), 2*time.Second, 3000)
	insertTrackSeries(t, f.pool, owner, "exif", start.Add(500*time.Millisecond), 2*time.Minute, 40)

	got := f.get(t, owner, f.item(t, owner, provItem{takenAt: provAt}))

	track := got.Context
	assert.EqualValues(t, 3600, track.MatchWindowS)
	require.Len(t, track.Windows, 1)
	assert.True(t, track.Windows[0].From.Equal(start))
	assert.True(t, track.Windows[0].To.Equal(provTime.Add(time.Hour)))
	assert.Equal(t, 6040, track.TotalPoints)
	assert.Equal(t, 8, track.BucketS, "ceil(7200 * 2 / 2000)")
	assert.LessOrEqual(t, track.ReturnedPoints, provenanceContextMaxPoints+2+40)

	exif := 0
	for _, p := range track.Points {
		if track.Sources[p.S].Format == formatExif {
			exif++
		}
	}
	assert.Equal(t, 40, exif, "derived rows are never thinned")
}

func TestProvenance_ContextBudgetIsTwoThousandLogPoints(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	start := provTime.Add(-time.Hour)
	insertTrackSeries(t, f.pool, owner, "gpx:a.gpx", start, time.Second, 2000)
	// Derived points come on top of the budget, so they never push a context over it.
	insertTrackSeries(t, f.pool, owner, "exif", start.Add(500*time.Millisecond), time.Minute, 5)
	id := f.item(t, owner, provItem{takenAt: provAt})

	got := f.get(t, owner, id)
	assert.Zero(t, got.Context.BucketS, "2,000 log points fit")
	assert.Equal(t, 2005, got.Context.ReturnedPoints)

	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(30*time.Minute), provLat, provLng)

	got = f.get(t, owner, id)
	assert.Equal(t, 4, got.Context.BucketS, "2,001 do not: ceil(7200 * 1 / 2000)")
	assert.Equal(t, 2006, got.Context.TotalPoints)
}

// pgx scans a timestamptz into the process's zone, so nothing may reach the wire without .UTC().
func TestProvenance_NonUTCProcessStillSendsUTC(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-10*time.Minute), provLat, provLng)
	insertTrack(t, f.pool, owner, "exif", provOffset(10*time.Minute), provLat2, provLng2)
	insertTrack(t, f.pool, owner, "nmea:b.nmea", provOffset(-3*time.Hour), provLat, provLng)
	insertTrack(t, f.pool, owner, "nmea:b.nmea", provOffset(3*time.Hour), provLat2, provLng2)
	pending := f.item(t, owner, provItem{takenAt: provAt})

	far := provOffset(30 * 24 * time.Hour)
	unplaced := f.item(t, owner, provItem{takenAt: far})
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(30*24*time.Hour-2*time.Hour), provLat, provLng)
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(30*24*time.Hour+2*time.Hour), provLat, provLng)

	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	time.Local = time.FixedZone("TEST+9", 9*3600)

	body := func(t *testing.T, id string) *httptest.ResponseRecorder {
		rec := do(t, f.router, owner, http.MethodGet, "/api/media/"+id+"/provenance", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}

	assertUTC := func(t *testing.T, name, value string) {
		t.Helper()
		assert.True(t, strings.HasSuffix(value, "Z"), "%s must be UTC, got %q", name, value)
		_, err := time.Parse(time.RFC3339Nano, value)
		assert.NoError(t, err, name)
	}

	t.Run("placed item", func(t *testing.T) {
		var raw struct {
			Item struct {
				TakenAt   string `json:"taken_at"`
				CreatedAt string `json:"created_at"`
			} `json:"item"`
			Placement struct {
				Now struct {
					Before struct {
						T string `json:"t"`
					} `json:"before"`
					After struct {
						T string `json:"t"`
					} `json:"after"`
				} `json:"now"`
				Independent struct {
					Before struct {
						T string `json:"t"`
					} `json:"before"`
				} `json:"independent"`
				Others []struct {
					T string `json:"t"`
				} `json:"others"`
			} `json:"placement"`
			Context struct {
				Windows []struct {
					From string `json:"from"`
					To   string `json:"to"`
				} `json:"windows"`
				Points []struct {
					T string `json:"t"`
				} `json:"points"`
			} `json:"context"`
		}
		require.NoError(t, json.Unmarshal(body(t, pending).Body.Bytes(), &raw))

		assert.Equal(t, provAt, raw.Item.TakenAt)
		assertUTC(t, "created_at", raw.Item.CreatedAt)
		assertUTC(t, "now.before.t", raw.Placement.Now.Before.T)
		assertUTC(t, "now.after.t", raw.Placement.Now.After.T)
		assertUTC(t, "independent.before.t", raw.Placement.Independent.Before.T)
		require.NotEmpty(t, raw.Placement.Others)
		for _, row := range raw.Placement.Others {
			assertUTC(t, "others.t", row.T)
		}
		require.Len(t, raw.Context.Windows, 1)
		assertUTC(t, "windows.from", raw.Context.Windows[0].From)
		assertUTC(t, "windows.to", raw.Context.Windows[0].To)
		require.NotEmpty(t, raw.Context.Points)
		for _, p := range raw.Context.Points {
			assertUTC(t, "points.t", p.T)
		}
	})

	t.Run("nearest points outside the window", func(t *testing.T) {
		var raw struct {
			Item struct {
				TakenAt string `json:"taken_at"`
			} `json:"item"`
			Placement struct {
				NearestOutside struct {
					Before struct {
						T string `json:"t"`
					} `json:"before"`
					After struct {
						T string `json:"t"`
					} `json:"after"`
				} `json:"nearest_outside"`
			} `json:"placement"`
		}
		require.NoError(t, json.Unmarshal(body(t, unplaced).Body.Bytes(), &raw))

		assert.Equal(t, far, raw.Item.TakenAt)
		assertUTC(t, "nearest_outside.before.t", raw.Placement.NearestOutside.Before.T)
		assertUTC(t, "nearest_outside.after.t", raw.Placement.NearestOutside.After.T)
	})
}

func TestProvenance_NotFound(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	stranger := createUser(t, f.pool, "stranger")
	insertTrack(t, f.pool, owner, "gpx:secret.gpx", provOffset(-time.Minute), provLat, provLng)
	id := f.item(t, owner, provItem{filename: "mine.jpg", takenAt: provAt})

	tests := []struct {
		name, user, id string
		want           int
	}{
		{"the owner", owner, id, http.StatusOK},
		{"a foreign item", stranger, id, http.StatusNotFound},
		{"a missing item", owner, "11111111-1111-4111-8111-111111111111", http.StatusNotFound},
		{"a malformed id", owner, "not-a-uuid", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.router, tt.user, http.MethodGet, "/api/media/"+tt.id+"/provenance", "")

			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.want == http.StatusNotFound {
				assert.NotContains(t, rec.Body.String(), "gpx:secret.gpx")
				assert.NotContains(t, rec.Body.String(), "mine.jpg")
			}
		})
	}
}

// Every query filters on the owner: another user's points and items are never seen, however close.
func TestProvenance_IgnoresOtherUsersData(t *testing.T) {
	f := newProvenanceFixture(t)
	owner := createUser(t, f.pool, "owner")
	stranger := createUser(t, f.pool, "stranger")
	insertTrack(t, f.pool, owner, "gpx:mine.gpx", provOffset(-30*time.Minute), provLat, provLng)
	insertTrack(t, f.pool, owner, "gpx:mine.gpx", provOffset(30*time.Minute), provLat2, provLng2)
	// Closer on every side, in a format the owner does not have.
	insertTrack(t, f.pool, stranger, "nmea:foreign.nmea", provOffset(-time.Second), 40, 40)
	insertTrack(t, f.pool, stranger, "exif", provOffset(time.Second), 41, 41)
	f.item(t, stranger, provItem{filename: "foreign.jpg", takenAt: provOffset(time.Second), lat: ptr(41.0), lng: ptr(41.0), gpsSource: ptr("exif")})

	rec := do(t, f.router, owner, http.MethodGet, "/api/media/"+f.item(t, owner, provItem{takenAt: provAt})+"/provenance", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "foreign")
	var got ProvenanceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "gpx:mine.gpx", got.Placement.Now.Before.Source)
	assert.Equal(t, "gpx:mine.gpx", got.Placement.Now.After.Source)
	require.Len(t, got.Placement.Others, 1)
	assert.Equal(t, 2, got.Context.TotalPoints)

	t.Run("nearest points outside the window", func(t *testing.T) {
		other := createUser(t, f.pool, "lonely")
		insertTrack(t, f.pool, stranger, "nmea:foreign.nmea", provOffset(-2*time.Hour), 40, 40)

		rec := do(t, f.router, other, http.MethodGet, "/api/media/"+f.item(t, other, provItem{takenAt: provAt})+"/provenance", "")

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), "foreign")
	})
}

// Invariant 8: evidence stays out of every payload that can reach another user.
func TestCaptureEvidence_NeverInSharedPayloads(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	const marker = "EVIDENCE-MARKER"
	tag, err := f.pool.Exec(ctx, `
		UPDATE media SET capture_evidence = jsonb_build_object('v', 1, 'by', 'ingest', 'make', $1::text)`, marker)
	require.NoError(t, err)
	require.EqualValues(t, 3, tag.RowsAffected())

	guess := `{"media_id":"` + f.mediaA + `","guess_lat":47.5,"guess_lng":19.0,"guess_date":"2024-05-02","max_dist_km":100}`
	requests := []struct {
		name, method, path, body string
	}{
		{"media list", http.MethodGet, "/api/media", ""},
		{"media list, shared only", http.MethodGet, "/api/media?owner=shared", ""},
		{"game round", http.MethodGet, "/api/game/round", ""},
		{"game guess", http.MethodPost, "/api/game/guess", guess},
	}
	for _, user := range []struct{ name, id string }{{"owner", f.userA}, {"album member", f.userB}} {
		for _, tt := range requests {
			t.Run(user.name+": "+tt.name, func(t *testing.T) {
				rec := do(t, f.router, user.id, tt.method, tt.path, tt.body)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.NotContains(t, rec.Body.String(), "capture_evidence")
				assert.NotContains(t, rec.Body.String(), marker)
			})
		}
	}

	t.Run("positive control: the owner's provenance has it", func(t *testing.T) {
		rec := do(t, f.router, f.userA, http.MethodGet, "/api/media/"+f.mediaA+"/provenance", "")

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), marker)
	})
}

// fileFixture is a real database behind the file re-read, with a fake bucket a test can fill.
type fileFixture struct {
	*provenanceFixture
	store *fakeObjectStore
	owner string
}

func newFileFixture(t *testing.T) *fileFixture {
	t.Helper()
	pool := testdb.New(t)
	store := &fakeObjectStore{objects: map[string][]byte{}}
	f := &fileFixture{provenanceFixture: &provenanceFixture{pool: pool, router: newOwnershipRouter(pool, store)}, store: store}
	f.owner = createUser(t, pool, "owner")
	return f
}

// photo inserts an own item whose stored object holds these tags.
func (f *fileFixture) photo(t *testing.T, filename, takenAt string, photo exiftest.Photo) string {
	t.Helper()
	f.store.objects["media/"+f.owner+"/"+filename] = exiftest.JPEG(photo)
	return f.item(t, f.owner, provItem{filename: filename, takenAt: takenAt})
}

func (f *fileFixture) reread(t *testing.T, id string) (*httptest.ResponseRecorder, FileReread) {
	t.Helper()
	rec := do(t, f.router, f.owner, http.MethodGet, "/api/media/"+id+"/provenance/file", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got FileReread
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
	return rec, got
}

// Invariant 5: the re-read reports a different time and changes nothing, in any column.
func TestProvenanceFile_NeverMovesTakenAt(t *testing.T) {
	f := newFileFixture(t)
	id := f.photo(t, "reread.jpg", provAt, exiftest.Photo{DateTimeOriginal: "2001:02:03 13:00:00"})
	row := func() string {
		var snapshot string
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT to_jsonb(m)::text || xmin::text FROM media m WHERE id = $1`, id).Scan(&snapshot))
		return snapshot
	}
	before := row()

	_, got := f.reread(t, id)

	require.NotNil(t, got.RuleToday)
	assert.Equal(t, provTime.Add(time.Hour).UTC(), got.RuleToday.TakenAt, "today's rules read another time")
	assert.Equal(t, rereadWholeOffset, got.Class)
	require.NotNil(t, got.DeltaS)
	assert.Equal(t, 3600.0, *got.DeltaS)
	assert.Equal(t, before, row(), "the row is untouched")
	assert.Equal(t, 0, countTracks(t, f), "and no track point appeared")
}

func countTracks(t *testing.T, f *fileFixture) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM gps_tracks`).Scan(&n))
	return n
}

func TestProvenanceFile_StoredRow(t *testing.T) {
	f := newFileFixture(t)

	t.Run("a NULL taken_at", func(t *testing.T) {
		_, got := f.reread(t, f.photo(t, "no-time.jpg", "", exiftest.Photo{DateTimeOriginal: "2001:02:03 12:00:00"}))
		assert.Nil(t, got.StoredTakenAt)
		assert.Equal(t, rereadStoredHasNoTime, got.Class)
	})
	t.Run("the upload time is the row's created_at", func(t *testing.T) {
		id := f.photo(t, "created.jpg", provAt, exiftest.Photo{DateTimeOriginal: "2001:02:03 12:00:00"})
		var created time.Time
		require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT created_at FROM media WHERE id = $1`, id).Scan(&created))

		_, got := f.reread(t, id)

		assert.True(t, created.Equal(got.UploadedAt))
		assert.Equal(t, rereadSame, got.Class)
	})

	// The evidence goes through a real jsonb column here; the unit tests hand it over as bytes.
	lavf := ingest.VideoProbeResult{CodecName: "h264", Encoder: "Lavf60.16.100"}
	video := func(t *testing.T, evidence string) string {
		t.Helper()
		id := f.item(t, f.owner, provItem{takenAt: provAt, evidence: evidence})
		_, err := f.pool.Exec(context.Background(), `UPDATE media SET media_type = 'video' WHERE id = $1`, id)
		require.NoError(t, err)
		return id
	}
	tests := []struct {
		name, evidence, want string
	}{
		{"evidence says transcoded", `{"v":1,"by":"ingest","transcoded":true}`, rereadUnverifiableTranscoded},
		{"evidence says not, and only that counts", `{"v":1,"by":"ingest","transcoded":false}`, rereadNoFileTime},
		{"no evidence, and it looks re-encoded", "", rereadUnverifiableTranscoded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.store.probe = lavf
			_, got := f.reread(t, video(t, tt.evidence))
			assert.Equal(t, tt.want, got.Class)
			assert.Equal(t, ptr("h264"), got.File.Video.Codec)
		})
	}
}

func TestProvenanceFile_NotFound(t *testing.T) {
	f := newFileFixture(t)
	stranger := createUser(t, f.pool, "stranger")
	id := f.photo(t, "mine.jpg", provAt, exiftest.Photo{Make: "SecretMake", DateTimeOriginal: "2001:02:03 12:00:00"})

	tests := []struct {
		name, user, id string
	}{
		{"a foreign item", stranger, id},
		{"a missing item", f.owner, "11111111-1111-4111-8111-111111111111"},
		{"a malformed id", f.owner, "not-a-uuid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.router, tt.user, http.MethodGet, "/api/media/"+tt.id+"/provenance/file", "")

			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "SecretMake")
			assert.Empty(t, f.store.heads, "the object is never read")
		})
	}
	t.Run("the owner", func(t *testing.T) {
		_, got := f.reread(t, id)
		assert.Equal(t, ptr("SecretMake"), got.File.Make)
	})
}

func TestProvenanceFile_NonUTCProcessStillSendsUTC(t *testing.T) {
	f := newFileFixture(t)
	id := f.photo(t, "zone.jpg", provAt, exiftest.Photo{DateTimeOriginal: "2001:02:03 12:00:00"})
	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	time.Local = time.FixedZone("TEST+9", 9*3600)

	rec, got := f.reread(t, id)

	assert.NotContains(t, rec.Body.String(), "+09:00")
	assert.Contains(t, rec.Body.String(), `"stored_taken_at":"2001-02-03T12:00:00Z"`)
	assert.Equal(t, time.UTC, got.UploadedAt.Location())
}
