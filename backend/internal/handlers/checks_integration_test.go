package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newChecksRouter serves both checks routes, authenticating each request as the user in the
// X-Test-User header, as newOwnershipRouter does.
func newChecksRouter(h *ChecksHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.User{ID: r.Header.Get(testUserHeader), Role: "user"}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u, auth.MethodSession)))
		})
	})
	r.Get("/api/checks", h.Get)
	r.Get("/api/checks/agreement", h.Agreement)
	return r
}

type checksFixture struct {
	pool   *pgxpool.Pool
	router http.Handler
}

func newChecksFixture(t *testing.T) *checksFixture {
	t.Helper()
	pool := testdb.New(t)
	return &checksFixture{pool: pool, router: newChecksRouter(&ChecksHandler{db: pool, matchWindow: time.Hour})}
}

// checksItem is one media row. Empty strings and nil pointers are NULL, except id (generated),
// mediaType (photo) and createdAt (now).
type checksItem struct {
	id, filename, mediaType string
	takenAt, createdAt      string
	lat, lng                *float64
	gpsSource, camera       *string
	confidence              *float64
}

var checksItemSeq atomic.Int64

func (f *checksFixture) item(t *testing.T, owner string, m checksItem) string {
	t.Helper()
	if m.filename == "" {
		m.filename = fmt.Sprintf("checks-%d.jpg", checksItemSeq.Add(1))
	}
	if m.mediaType == "" {
		m.mediaType = "photo"
	}
	null := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	var id string
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		INSERT INTO media (id, owner_id, filename, media_type, storage_key, file_hash, taken_at, created_at,
		                   location, gps_source, gps_confidence, camera_model)
		VALUES (coalesce($13::uuid, gen_random_uuid()), $1, $2, $3, $4, $5, $6::timestamptz, coalesce($7::timestamptz, now()),
		        CASE WHEN $8::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($9, $8), 4326) END, $10, $11, $12)
		RETURNING id::text`,
		owner, m.filename, m.mediaType, "media/"+owner+"/"+m.filename, "hash-"+owner+"-"+m.filename,
		null(m.takenAt), null(m.createdAt), m.lat, m.lng, m.gpsSource, m.confidence, m.camera, null(m.id)).Scan(&id))
	return id
}

func (f *checksFixture) checks(t *testing.T, user string) ChecksResponse {
	t.Helper()
	rec := do(t, f.router, user, http.MethodGet, "/api/checks", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp ChecksResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp
}

func (f *checksFixture) agreement(t *testing.T, user string) AgreementResponse {
	t.Helper()
	return getAgreement(t, f.router, user, "")
}

// getAgreement asks router for GET /api/checks/agreement with query ("" or "?from=…") and decodes
// its 200.
func getAgreement(t *testing.T, router http.Handler, user, query string) AgreementResponse {
	t.Helper()
	rec := do(t, router, user, http.MethodGet, "/api/checks/agreement"+query, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp AgreementResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return resp
}

func findCheck(t *testing.T, resp ChecksResponse, code string) checksCheck {
	t.Helper()
	for _, c := range resp.Checks {
		if c.Code == code {
			return c
		}
	}
	t.Fatalf("the response has no check %s", code)
	return checksCheck{}
}

func agreementFor(t *testing.T, resp AgreementResponse, source string) agreementFile {
	t.Helper()
	for _, f := range resp.Files {
		if f.Source == source {
			return f
		}
	}
	t.Fatalf("the agreement has no file %s", source)
	return agreementFile{}
}

// insertSeriesAt writes count points under one label, step apart from start, all at one position.
func insertSeriesAt(t *testing.T, pool *pgxpool.Pool, ownerID, source string, start time.Time, step time.Duration, count int, lat, lng float64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
		SELECT $1, $2, $3::timestamptz + (n * $4::float8) * interval '1 second', ST_SetSRID(ST_MakePoint($6, $5), 4326)
		FROM generate_series(0, $7::int - 1) AS n`, ownerID, source, start, step.Seconds(), lat, lng, count)
	require.NoError(t, err)
}

// day is the nth synthetic day after provAt, so that each case has a match window to itself.
func checksDay(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestChecks_EmptyLibrary(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")

	rec := do(t, f.router, owner, http.MethodGet, "/api/checks", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var raw struct {
		WindowS   int             `json:"window_s"`
		Files     json.RawMessage `json:"files"`
		Residuals json.RawMessage `json:"residuals"`
		Checks    []struct {
			Code     string          `json:"code"`
			Severity string          `json:"severity"`
			Total    int             `json:"total"`
			Groups   json.RawMessage `json:"groups"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Equal(t, 3600, raw.WindowS)
	assert.JSONEq(t, "[]", string(raw.Files))
	assert.JSONEq(t, "[]", string(raw.Residuals))

	var order []string
	for _, c := range raw.Checks {
		order = append(order, c.Code+":"+c.Severity)
		assert.Zero(t, c.Total, c.Code)
		assert.JSONEq(t, "[]", string(c.Groups), c.Code)
	}
	assert.Equal(t, []string{
		"pair_impossible:high", "future_track_points:high", "taken_after_upload:high", "stale_estimates:high",
		"pair_far:low", "orphan_points:low", "time_may_be_estimated_used:low",
	}, order, "all seven, always, high severity first")

	agreement := do(t, f.router, owner, http.MethodGet, "/api/checks/agreement", "")
	require.Equal(t, http.StatusOK, agreement.Code, agreement.Body.String())
	assert.JSONEq(t, "[]", rawField(t, agreement.Body.Bytes(), "files"))
	assert.JSONEq(t, "false", rawField(t, agreement.Body.Bytes(), "truncated"))
	assert.JSONEq(t, "0", rawField(t, agreement.Body.Bytes(), "total_files"))
	assert.JSONEq(t, "null", rawField(t, agreement.Body.Bytes(), "next_from"))
}

// rawField returns one top-level field of a JSON object as raw JSON.
func rawField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &fields))
	return string(fields[key])
}

func TestChecks_PairChecks(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")

	// 55 km in a minute across two formats.
	insertTrack(t, f.pool, owner, "gpx:ride.gpx", provOffset(-30*time.Second), 10, 20)
	insertTrack(t, f.pool, owner, "nmea:car.nmea", provOffset(30*time.Second), 10.5, 20)
	impossible := f.item(t, owner, checksItem{takenAt: provAt})
	// 11 km in 40 minutes across two formats.
	insertTrack(t, f.pool, owner, "gpx:ride.gpx", provOffset(checksDay(1)-20*time.Minute), 10, 20)
	insertTrack(t, f.pool, owner, "esoko:van.log", provOffset(checksDay(1)+20*time.Minute), 10.1, 20)
	far := f.item(t, owner, checksItem{takenAt: provOffset(checksDay(1))})
	// One format is never a pair warning, however far.
	insertTrack(t, f.pool, owner, "gpx:ride.gpx", provOffset(checksDay(2)-30*time.Second), 10, 20)
	insertTrack(t, f.pool, owner, "gpx:other.gpx", provOffset(checksDay(2)+30*time.Second), 10.5, 20)
	f.item(t, owner, checksItem{takenAt: provOffset(checksDay(2))})
	// 44 km in a minute, from the same gpx file to another nmea file.
	insertTrack(t, f.pool, owner, "gpx:ride.gpx", provOffset(checksDay(3)-30*time.Second), 10, 20)
	insertTrack(t, f.pool, owner, "nmea:bus.nmea", provOffset(checksDay(3)+30*time.Second), 10.4, 20)
	again := f.item(t, owner, checksItem{takenAt: provOffset(checksDay(3))})
	runMatcherFor(t, f.pool, owner)

	got := f.checks(t, owner)

	impossibleKm := geo.HaversineKm(10, 20, 10.5, 20)
	pi := findCheck(t, got, "pair_impossible")
	assert.Equal(t, 2, pi.Total)
	require.Len(t, pi.Groups, 1)
	assert.Equal(t, "gpx+nmea", pi.Groups[0].Key)
	assert.Equal(t, "gpx+nmea", pi.Groups[0].Label)
	assert.Equal(t, 2, pi.Groups[0].Count)
	assert.Equal(t, []string{impossible, again}, pi.Groups[0].MediaIDs)
	ev := pi.Groups[0].Evidence
	assert.InDelta(t, impossibleKm, ev["max_km"], 1e-9)
	assert.InDelta(t, impossibleKm*60, ev["max_kmh"], 1e-6)
	assert.Equal(t, "gpx:ride.gpx", ev["top_source"], "under both, against each nmea file under one")
	assert.EqualValues(t, 2, ev["top_source_estimates"])

	pf := findCheck(t, got, "pair_far")
	assert.Equal(t, "low", pf.Severity)
	assert.Equal(t, 1, pf.Total)
	require.Len(t, pf.Groups, 1)
	assert.Equal(t, "esoko+gpx", pf.Groups[0].Key)
	assert.Equal(t, []string{far}, pf.Groups[0].MediaIDs)
	assert.InDelta(t, geo.HaversineKm(10, 20, 10.1, 20), pf.Groups[0].Evidence["max_km"], 1e-9)
	assert.NotContains(t, pf.Groups[0].Evidence, "max_kmh")
	// Both files are under its one estimate: a tie names neither, on the wire as null.
	assert.Contains(t, pf.Groups[0].Evidence, "top_source")
	assert.Nil(t, pf.Groups[0].Evidence["top_source"])
	assert.Contains(t, pf.Groups[0].Evidence, "top_source_estimates")
	assert.Nil(t, pf.Groups[0].Evidence["top_source_estimates"])
}

func TestChecks_FutureTrackPoints(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	now := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	insertTrack(t, f.pool, owner, "nmea:week.nmea", at(-time.Hour), 10, 20)
	insertTrack(t, f.pool, owner, "nmea:week.nmea", at(48*time.Hour), 10, 20)
	insertTrack(t, f.pool, owner, "nmea:week.nmea", at(72*time.Hour), 10, 20)
	insertTrack(t, f.pool, owner, "nmea:week.nmea", "10000-01-01T00:00:00Z", 10, 20)
	insertTrack(t, f.pool, owner, "gpx:today.gpx", at(23*time.Hour), 10, 20) // within the day's margin

	got := findCheck(t, f.checks(t, owner), "future_track_points")

	assert.Equal(t, "high", got.Severity)
	assert.Equal(t, 3, got.Total, "points, not files")
	require.Len(t, got.Groups, 1)
	g := got.Groups[0]
	assert.Equal(t, "nmea:week.nmea", g.Key)
	assert.Equal(t, 3, g.Count)
	assert.Equal(t, []string{}, g.MediaIDs)
	assert.Equal(t, now.Add(48*time.Hour).Format(time.RFC3339), g.Evidence["first"])
	assert.Nil(t, g.Evidence["last"], "a year JSON cannot carry")
	assert.Contains(t, g.Evidence, "last")
}

func TestChecks_TakenAfterUpload(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	const uploaded = "2020-01-01T00:00:00Z"

	located := f.item(t, owner, checksItem{takenAt: "2021-01-01T00:00:00Z", createdAt: uploaded, camera: ptr("Model X"),
		lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("exif")})
	earlier := f.item(t, owner, checksItem{takenAt: "2020-06-01T00:00:00Z", createdAt: uploaded, camera: ptr("Model X")})
	unknown := f.item(t, owner, checksItem{takenAt: "2030-01-01T00:00:00Z", createdAt: uploaded})
	f.item(t, owner, checksItem{takenAt: "2019-01-01T00:00:00Z", createdAt: uploaded, camera: ptr("Model X")})
	f.item(t, owner, checksItem{createdAt: uploaded})

	got := findCheck(t, f.checks(t, owner), "taken_after_upload")

	assert.Equal(t, 3, got.Total)
	require.Len(t, got.Groups, 2)
	model, none := got.Groups[0], got.Groups[1]
	assert.Equal(t, "Model X", model.Key)
	assert.Equal(t, 2, model.Count)
	assert.Equal(t, []string{earlier, located}, model.MediaIDs, "by taken_at")
	assert.EqualValues(t, 366*86400, model.Evidence["max_ahead_s"])
	assert.EqualValues(t, 1, model.Evidence["located"])
	assert.Equal(t, "", none.Key, "no camera model")
	assert.Equal(t, []string{unknown}, none.MediaIDs)
	assert.EqualValues(t, 3653*86400, none.Evidence["max_ahead_s"])
	assert.EqualValues(t, 0, none.Evidence["located"])
}

func TestChecks_StaleEstimates(t *testing.T) {
	f := newChecksFixture(t)
	ctx := context.Background()
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(-10*time.Minute), 10, 20)
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(10*time.Minute), 10.01, 20.01)
	f.item(t, owner, checksItem{takenAt: provAt})
	stale := f.item(t, owner, checksItem{takenAt: provAt})
	runMatcherFor(t, f.pool, owner)
	_, err := f.pool.Exec(ctx, `UPDATE media SET location = ST_SetSRID(ST_MakePoint(21, 11), 4326) WHERE id = $1`, stale)
	require.NoError(t, err)
	// An estimate whose points are gone: the matcher can neither place nor clear it.
	unsupported := f.item(t, owner, checksItem{takenAt: provOffset(checksDay(10)), lat: ptr(10.0), lng: ptr(20.0),
		gpsSource: ptr("interpolated"), confidence: ptr(0.5)})
	// An estimate with no location is pending, which is no finding.
	f.item(t, owner, checksItem{takenAt: provAt, gpsSource: ptr("interpolated")})

	got := findCheck(t, f.checks(t, owner), "stale_estimates")

	assert.Equal(t, 2, got.Total)
	require.Len(t, got.Groups, 2)
	assert.Equal(t, "no_support", got.Groups[0].Key, "a tie on count is ordered by key")
	assert.Equal(t, []string{unsupported}, got.Groups[0].MediaIDs)
	assert.Empty(t, got.Groups[0].Evidence)
	assert.Equal(t, "stale", got.Groups[1].Key)
	assert.Equal(t, []string{stale}, got.Groups[1].MediaIDs)
	assert.InDelta(t, geo.HaversineKm(11, 21, 10.005, 20.005)*1000, got.Groups[1].Evidence["max_delta_m"], 0.01)
}

func TestChecks_OrphanPoints(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	stranger := createUser(t, f.pool, "stranger")

	linkedAt, orphanAt := provOffset(-10*time.Minute), provOffset(10*time.Minute)
	insertTrack(t, f.pool, owner, "exif", linkedAt, 10, 20)
	f.item(t, owner, checksItem{takenAt: linkedAt, lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("exif")})
	insertTrack(t, f.pool, owner, "exif", orphanAt, 10.01, 20.01)
	insertTrack(t, f.pool, owner, "manual", provOffset(checksDay(3)), 10, 20)
	// Neither another user's photo nor an own item that is not a source of positions explains it.
	f.item(t, stranger, checksItem{takenAt: orphanAt, lat: ptr(10.01), lng: ptr(20.01), gpsSource: ptr("exif")})
	atOrphan := f.item(t, owner, checksItem{takenAt: orphanAt})
	between := f.item(t, owner, checksItem{takenAt: provAt})
	runMatcherFor(t, f.pool, owner)

	got := findCheck(t, f.checks(t, owner), "orphan_points")

	assert.Equal(t, "low", got.Severity)
	assert.Equal(t, 2, got.Total, "points")
	require.Len(t, got.Groups, 2)
	exif, manual := got.Groups[0], got.Groups[1]
	assert.Equal(t, "exif", exif.Key)
	assert.Equal(t, 1, exif.Count)
	assert.EqualValues(t, 2, exif.Evidence["estimates"])
	assert.Equal(t, []string{between, atOrphan}, exif.MediaIDs, "the estimates resting on it, by taken_at")
	assert.Equal(t, "manual", manual.Key)
	assert.Equal(t, 1, manual.Count)
	assert.EqualValues(t, 0, manual.Evidence["estimates"])
	assert.Equal(t, []string{}, manual.MediaIDs)
}

func TestChecks_TimeMayBeEstimatedUsed(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	track := func(source string, at time.Duration, lat, lng float64) {
		insertTrack(t, f.pool, owner, source, provOffset(at), lat, lng)
	}

	// kml before, a real point after: without kml it is placed on the gpx point alone.
	track("kml:spread.kml", -10*time.Minute, 10, 20)
	track("gpx:real.gpx", 30*time.Minute, 10, 20.03)
	kmlFallback := f.item(t, owner, checksItem{takenAt: provAt})
	// geojson alone: it uses the format but has nothing to fall back on.
	track("geojson:alone.json", checksDay(1)-10*time.Minute, 10, 20)
	f.item(t, owner, checksItem{takenAt: provOffset(checksDay(1))})
	// kmz and kml: the kml exclusion falls back on kmz, the wider kmz one on nothing.
	track("kmz:z.kmz", checksDay(2)-10*time.Minute, 10, 20)
	track("kml:spread.kml", checksDay(2)+10*time.Minute, 10, 20.001)
	kmzAndKML := f.item(t, owner, checksItem{takenAt: provOffset(checksDay(2))})
	// kmz before, a real point after.
	track("kmz:z.kmz", checksDay(3)-5*time.Minute, 10, 20)
	track("gpx:real.gpx", checksDay(3)+50*time.Minute, 10.02, 20)
	kmzFallback := f.item(t, owner, checksItem{takenAt: provOffset(checksDay(3))})
	// geojson before, a real point after, a short way off.
	track("geojson:alone.json", checksDay(4)-10*time.Minute, 10, 20)
	track("gpx:real.gpx", checksDay(4)+10*time.Minute, 10, 20.004)
	geojsonFallback := f.item(t, owner, checksItem{takenAt: provOffset(checksDay(4))})
	runMatcherFor(t, f.pool, owner)

	stored := func(id string) (float64, float64) {
		s := snapshotMedia(t, f.pool, id)
		require.NotNil(t, s.lat, id)
		return *s.lat, *s.lng
	}
	moveTo := func(id string, lat, lng float64) float64 {
		sLat, sLng := stored(id)
		return geo.HaversineKm(sLat, sLng, lat, lng) * 1000
	}

	got := findCheck(t, f.checks(t, owner), "time_may_be_estimated_used")

	assert.Equal(t, "low", got.Severity)
	assert.Equal(t, 4, got.Total, "kmzAndKML is in one group only")
	require.Len(t, got.Groups, 3)
	kml, geojson, kmz := got.Groups[0], got.Groups[1], got.Groups[2]

	assert.Equal(t, "kml", kml.Key)
	assert.Equal(t, 2, kml.Count)
	assert.Equal(t, []string{kmlFallback, kmzAndKML}, kml.MediaIDs)
	assert.EqualValues(t, 2, kml.Evidence["using"])
	assert.EqualValues(t, 1, kml.Evidence["over_100m"], "the kmz fallback moves 55 m")
	assert.EqualValues(t, 1, kml.Evidence["over_1km"])
	assert.InDelta(t, moveTo(kmlFallback, 10, 20.03), kml.Evidence["max_move_m"], 0.01)

	assert.Equal(t, "geojson", geojson.Key)
	assert.Equal(t, []string{geojsonFallback}, geojson.MediaIDs)
	assert.EqualValues(t, 2, geojson.Evidence["using"], "the lone geojson estimate uses it but is not counted")
	assert.EqualValues(t, 1, geojson.Evidence["over_100m"])
	assert.EqualValues(t, 0, geojson.Evidence["over_1km"])
	assert.InDelta(t, moveTo(geojsonFallback, 10, 20.004), geojson.Evidence["max_move_m"], 0.01)

	assert.Equal(t, "kmz", kmz.Key, "unproven: its exclusion leaves out kmz too")
	assert.Equal(t, []string{kmzFallback}, kmz.MediaIDs)
	assert.EqualValues(t, 2, kmz.Evidence["using"])
	assert.EqualValues(t, 1, kmz.Evidence["over_1km"])
	assert.InDelta(t, moveTo(kmzFallback, 10.02, 20), kmz.Evidence["max_move_m"], 0.01)
}

func TestChecks_Inventory(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "gpx:b.gpx", provOffset(-10*time.Minute), 10, 20)
	insertTrack(t, f.pool, owner, "gpx:b.gpx", provOffset(10*time.Minute), 10.01, 20.01)
	insertTrack(t, f.pool, owner, "kml:a.kml", provOffset(-50*time.Minute), 10, 20)
	insertTrack(t, f.pool, owner, "kml:a.kml", "10000-01-01T00:00:00Z", 10, 20)
	insertTrack(t, f.pool, owner, "exif", provOffset(-2*time.Hour), 10, 20)
	insertTrack(t, f.pool, owner, "manual", provOffset(-2*time.Hour+2*time.Minute), 10, 20)
	insertTrack(t, f.pool, owner, "strava:ride", provOffset(checksDay(5)), 10, 20)
	f.item(t, owner, checksItem{takenAt: provAt})                                 // both sides from gpx:b.gpx
	f.item(t, owner, checksItem{takenAt: provOffset(-2*time.Hour + time.Minute)}) // between exif and manual
	runMatcherFor(t, f.pool, owner)

	got := f.checks(t, owner).Files

	at := func(d time.Duration) *time.Time { return ptr(provTime.Add(d)) }
	assert.Equal(t, []checksFile{
		{Source: "exif", Format: "exif", Kind: "derived", Points: 1, First: at(-2 * time.Hour), Last: at(-2 * time.Hour), ItemsPlaced: 1},
		{Source: "gpx:b.gpx", Format: "gpx", Kind: "recorded", Points: 2, First: at(-10 * time.Minute), Last: at(10 * time.Minute), ItemsPlaced: 1},
		{Source: "kml:a.kml", Format: "kml", Kind: "time_may_be_estimated", Points: 2, First: at(-50 * time.Minute), Last: nil},
		{Source: "manual", Format: "manual", Kind: "derived", Points: 1, First: at(-2*time.Hour + 2*time.Minute), Last: at(-2*time.Hour + 2*time.Minute), ItemsPlaced: 1},
		{Source: "strava:ride", Format: "strava", Kind: "other", Points: 1, First: at(checksDay(5)), Last: at(checksDay(5))},
	}, got)
}

func TestChecks_Residuals(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	ownPosition := func(mediaType string, at time.Duration) {
		f.item(t, owner, checksItem{mediaType: mediaType, takenAt: provOffset(at), lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("exif")})
	}
	track := func(source string, at time.Duration, lat, lng float64) {
		insertTrack(t, f.pool, owner, source, provOffset(at), lat, lng)
	}

	ownPosition("photo", 0)
	track("gpx:a.gpx", 5*time.Second, 10, 20.001) // 5 s is inside
	track("nmea:b.nmea", -3*time.Second, 10.001, 20)
	track("exif", 0, 10.5, 20)             // the photo's own point
	track("manual", time.Second, 10.5, 20) // a pin
	ownPosition("photo_360", checksDay(1)) // 360° photos count too
	track("gpx:a.gpx", checksDay(1)-2*time.Second, 10, 20.002)
	track("gpx:c.gpx", checksDay(1)+2*time.Second, 10, 20.009) // as near, but later
	track("nmea:b.nmea", checksDay(1)+time.Second, 10.0002, 20)
	ownPosition("photo", checksDay(2))
	track("gpx:a.gpx", checksDay(2)+6*time.Second, 10.5, 20) // just outside
	ownPosition("video", checksDay(3))
	track("gpx:a.gpx", checksDay(3), 10.5, 20)
	for i, source := range []string{"interpolated", "manual"} {
		f.item(t, owner, checksItem{takenAt: provOffset(checksDay(4 + i)), lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr(source), confidence: ptr(1.0)})
		track("gpx:a.gpx", checksDay(4+i), 10.5, 20)
	}

	got := f.checks(t, owner).Residuals

	nmea1, nmea2 := geo.HaversineKm(10, 20, 10.001, 20)*1000, geo.HaversineKm(10, 20, 10.0002, 20)*1000
	gpx1, gpx2 := geo.HaversineKm(10, 20, 10, 20.001)*1000, geo.HaversineKm(10, 20, 10, 20.002)*1000
	require.Len(t, got, 2, "no exif or manual row")
	assert.Equal(t, "nmea", got[0].Format, "lowest median first")
	assert.Equal(t, 2, got[0].N)
	assert.InDelta(t, (nmea1+nmea2)/2, got[0].P50M, 1e-6)
	assert.InDelta(t, nmea2+0.9*(nmea1-nmea2), got[0].P90M, 1e-6)
	assert.Equal(t, "gpx", got[1].Format)
	assert.Equal(t, 2, got[1].N, "photos with a point of the format within 5 s")
	assert.InDelta(t, (gpx1+gpx2)/2, got[1].P50M, 1e-6)
	assert.InDelta(t, gpx1+0.9*(gpx2-gpx1), got[1].P90M, 1e-6)
}

func TestChecks_Agreement(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	start := provTime

	// Two formats a minute apart, 111 m apart: each samples the other.
	insertSeriesAt(t, f.pool, owner, "gpx:a.gpx", start, time.Minute, 31, 10, 20)
	insertSeriesAt(t, f.pool, owner, "nmea:b.nmea", start.Add(10*time.Second), time.Minute, 31, 10.001, 20)
	// A second gpx file and the owner's photos and pins at the same instants: none of them count.
	next := start.Add(checksDay(1))
	insertSeriesAt(t, f.pool, owner, "gpx:twin.gpx", next, time.Minute, 11, 10, 20)
	insertSeriesAt(t, f.pool, owner, "gpx:triplet.gpx", next, time.Minute, 11, 10.5, 20)
	insertSeriesAt(t, f.pool, owner, "exif", next, time.Minute, 11, 10.5, 20)
	insertSeriesAt(t, f.pool, owner, "manual", next.Add(time.Second), time.Minute, 11, 10.5, 20)
	// Three samples, all matched: too few for a median.
	insertSeriesAt(t, f.pool, owner, "esoko:short.log", start.Add(checksDay(2)), time.Minute, 3, 10, 20)
	insertSeriesAt(t, f.pool, owner, "nmea:c.nmea", start.Add(checksDay(2)+5*time.Second), time.Minute, 3, 10, 20)
	// Two minutes at 1 Hz: twenty targets, four samples 30 s apart.
	insertSeriesAt(t, f.pool, owner, "tcx:dense.tcx", start.Add(checksDay(3)), time.Second, 121, 10, 20)
	// Gaps of 10 s six times out of ten.
	for _, s := range []int{0, 10, 20, 30, 40, 50, 60, 75, 95, 120, 150} {
		insertTrack(t, f.pool, owner, "kml:irregular.kml", start.Add(checksDay(4)+time.Duration(s)*time.Second).Format(time.RFC3339), 10, 20)
	}

	got := f.agreement(t, owner)

	assert.False(t, got.Truncated)
	assert.Nil(t, got.NextFrom)
	var sources []string
	for _, file := range got.Files {
		sources = append(sources, file.Source)
	}
	assert.Equal(t, []string{"esoko:short.log", "gpx:a.gpx", "gpx:triplet.gpx", "gpx:twin.gpx", "kml:irregular.kml",
		"nmea:b.nmea", "nmea:c.nmea", "tcx:dense.tcx"}, sources, "log files only, in label order")
	assert.Equal(t, len(sources), got.TotalFiles)

	apart := geo.HaversineKm(10, 20, 10.001, 20) * 1000
	for _, source := range []string{"gpx:a.gpx", "nmea:b.nmea"} {
		file := agreementFor(t, got, source)
		assert.Equal(t, 20, file.Samples, source)
		assert.Equal(t, 20, file.Matched, source)
		require.NotNil(t, file.MedianM, source)
		assert.InDelta(t, apart, *file.MedianM, 1e-6, source)
		assert.Equal(t, ptr(1.0), file.ModalGapShare, "%s: a regular file", source)
	}
	for _, source := range []string{"gpx:twin.gpx", "gpx:triplet.gpx"} {
		file := agreementFor(t, got, source)
		assert.Positive(t, file.Samples, source)
		assert.Zero(t, file.Matched, "%s: only another format counts, and exif and manual never do", source)
		assert.Nil(t, file.MedianM, source)
	}
	short := agreementFor(t, got, "esoko:short.log")
	assert.Equal(t, 3, short.Samples)
	assert.Equal(t, 3, short.Matched)
	assert.Nil(t, short.MedianM, "fewer than five matches")
	assert.Nil(t, short.ModalGapShare, "two gaps")
	dense := agreementFor(t, got, "tcx:dense.tcx")
	assert.Equal(t, 4, dense.Samples, "samples at least 30 s apart")
	assert.Equal(t, ptr(1.0), dense.ModalGapShare)
	irregular := agreementFor(t, got, "kml:irregular.kml")
	assert.Equal(t, 5, irregular.Samples)
	assert.Equal(t, ptr(0.6), irregular.ModalGapShare)

	// One file per chunk, and the budget spent before the first: each request compares one file.
	oneAtATime := newChecksRouter(&ChecksHandler{db: f.pool, matchWindow: time.Hour, agreementBudget: time.Nanosecond, agreementChunkSize: 1})

	t.Run("the work budget stops it between chunks", func(t *testing.T) {
		resp := getAgreement(t, oneAtATime, owner, "")

		assert.True(t, resp.Truncated)
		assert.Equal(t, []agreementFile{short}, resp.Files, "the first chunk always runs")
		assert.Equal(t, len(sources), resp.TotalFiles)
		assert.Equal(t, ptr(1), resp.NextFrom)
	})

	t.Run("following next_from compares every file once", func(t *testing.T) {
		var files []agreementFile
		for from := 0; ; {
			resp := getAgreement(t, oneAtATime, owner, fmt.Sprintf("?from=%d", from))

			assert.Equal(t, len(sources), resp.TotalFiles)
			require.Len(t, resp.Files, 1, "from %d: every request makes progress", from)
			files = append(files, resp.Files...)
			if resp.NextFrom == nil {
				assert.False(t, resp.Truncated)
				break
			}
			assert.True(t, resp.Truncated)
			require.Equal(t, from+1, *resp.NextFrom)
			from = *resp.NextFrom
		}
		assert.Equal(t, got.Files, files, "exactly the result of one request that had the time")
	})

	t.Run("the points cap ends a chunk before the files cap does", func(t *testing.T) {
		h := &ChecksHandler{db: f.pool, matchWindow: time.Hour, agreementBudget: time.Nanosecond, agreementChunkPointCap: 40}

		resp := getAgreement(t, newChecksRouter(h), owner, "")

		// 3 + 31 points; gpx:triplet.gpx's 11 would make 45.
		assert.Equal(t, got.Files[:2], resp.Files)
		assert.Equal(t, ptr(2), resp.NextFrom)
	})

	t.Run("from at or past the last file", func(t *testing.T) {
		for _, from := range []int{len(sources), len(sources) + 5, agreementMaxFrom} {
			rec := do(t, f.router, owner, http.MethodGet, fmt.Sprintf("/api/checks/agreement?from=%d", from), "")

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			body := rec.Body.Bytes()
			assert.JSONEq(t, "[]", rawField(t, body, "files"), "from %d", from)
			assert.JSONEq(t, "false", rawField(t, body, "truncated"), "from %d", from)
			assert.JSONEq(t, "null", rawField(t, body, "next_from"), "from %d", from)
			assert.JSONEq(t, fmt.Sprint(len(sources)), rawField(t, body, "total_files"), "from %d", from)
		}
	})
}

// The agreement compares a sample with the nearest point of another format within 5 minutes on
// either side, 5 minutes included.
func TestChecks_AgreementWindow(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	// Ten minutes from the other format's nearest point: never compared.
	insertSeriesAt(t, f.pool, owner, "gpx:far.gpx", provTime, 20*time.Minute, 10, 10, 20)
	insertSeriesAt(t, f.pool, owner, "nmea:far.nmea", provTime.Add(10*time.Minute), 20*time.Minute, 10, 10, 20)
	// Five minutes exactly: compared.
	next := provTime.Add(checksDay(1))
	insertSeriesAt(t, f.pool, owner, "esoko:edge.log", next, 20*time.Minute, 10, 10, 20)
	insertSeriesAt(t, f.pool, owner, "tcx:edge.tcx", next.Add(5*time.Minute), 20*time.Minute, 10, 10, 20)

	got := f.agreement(t, owner)

	for _, source := range []string{"gpx:far.gpx", "nmea:far.nmea"} {
		file := agreementFor(t, got, source)
		assert.Equal(t, 10, file.Samples, source)
		assert.Zero(t, file.Matched, "%s: the other format is 10 minutes away", source)
	}
	for _, source := range []string{"esoko:edge.log", "tcx:edge.tcx"} {
		file := agreementFor(t, got, source)
		assert.Equal(t, 10, file.Samples, source)
		assert.Equal(t, 10, file.Matched, "%s: 5 minutes is inside", source)
		require.NotNil(t, file.MedianM, source)
		assert.Zero(t, *file.MedianM, source)
	}
}

// agreementHook is the pool, with hook run as each chunk's first statement, the samples query,
// starts: n counts them from 1. An error from hook fails that statement.
type agreementHook struct {
	*pgxpool.Pool
	hook    func(n int) error
	samples int
}

func (d *agreementHook) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if sql == agreementSamplesSQL {
		d.samples++
		if err := d.hook(d.samples); err != nil {
			return nil, err
		}
	}
	return d.Pool.Query(ctx, sql, args...)
}

// A chunk that the request's end cuts short is dropped and the finished ones are answered, but a
// chunk that fails for another reason fails the request.
func TestChecks_AgreementCutShortMidRun(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	insertSeriesAt(t, f.pool, owner, "gpx:a.gpx", provTime, time.Minute, 31, 10, 20)
	insertSeriesAt(t, f.pool, owner, "nmea:b.nmea", provTime.Add(10*time.Second), time.Minute, 31, 10.001, 20)
	full := f.agreement(t, owner)
	require.Len(t, full.Files, 2)

	serve := func(ctx context.Context, hook func(n int) error) *httptest.ResponseRecorder {
		h := &ChecksHandler{db: &agreementHook{Pool: f.pool, hook: hook}, matchWindow: time.Hour, agreementChunkSize: 1}
		req := httptest.NewRequest(http.MethodGet, "/api/checks/agreement", nil).WithContext(ctx)
		req.Header.Set(testUserHeader, owner)
		rec := httptest.NewRecorder()
		newChecksRouter(h).ServeHTTP(rec, req)
		return rec
	}

	t.Run("the request ends during the second chunk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Closing the view ends the request this way; the deadline ends it the same way.
		rec := serve(ctx, func(n int) error {
			if n == 2 {
				cancel()
			}
			return nil
		})

		require.Equal(t, http.StatusOK, rec.Code, "a partial answer is still an answer: %s", rec.Body.String())
		var got AgreementResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.True(t, got.Truncated)
		assert.Equal(t, []agreementFile{full.Files[0]}, got.Files, "the finished chunk, as a full run has it")
		assert.Equal(t, 2, got.TotalFiles)
		assert.Equal(t, ptr(1), got.NextFrom, "the dropped chunk is where the next request starts")
	})

	t.Run("the database fails during the second chunk", func(t *testing.T) {
		rec := serve(context.Background(), func(n int) error {
			if n == 2 {
				return errors.New("could not read block 4711 of relation base/16384/2619")
			}
			return nil
		})

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "4711", "the error stays in the log")
	})
}

// pgx scans a timestamptz into the process's zone, so nothing may reach the wire without .UTC().
func TestChecks_NonUTCProcessStillSendsUTC(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provAt, 10, 20)
	insertTrack(t, f.pool, owner, "nmea:week.nmea", time.Now().Add(72*time.Hour).Format(time.RFC3339), 10, 20)

	saved := time.Local
	t.Cleanup(func() { time.Local = saved })
	time.Local = time.FixedZone("TEST+9", 9*3600)

	assertUTC := func(t *testing.T, name string, value any) {
		t.Helper()
		s, ok := value.(string)
		require.True(t, ok, "%s is %v", name, value)
		assert.True(t, strings.HasSuffix(s, "Z"), "%s must be UTC, got %q", name, s)
	}

	rec := do(t, f.router, owner, http.MethodGet, "/api/checks", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var raw struct {
		GeneratedAt string `json:"generated_at"`
		Files       []struct {
			First string `json:"first"`
			Last  string `json:"last"`
		} `json:"files"`
		Checks []struct {
			Code   string `json:"code"`
			Groups []struct {
				Evidence map[string]any `json:"evidence"`
			} `json:"groups"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assertUTC(t, "generated_at", raw.GeneratedAt)
	require.Len(t, raw.Files, 2)
	for _, file := range raw.Files {
		assertUTC(t, "first", file.First)
		assertUTC(t, "last", file.Last)
	}
	assert.Equal(t, provAt, raw.Files[0].First)
	var future int
	for _, c := range raw.Checks {
		if c.Code == checksFutureTrackPoints {
			require.Len(t, c.Groups, 1)
			assertUTC(t, "evidence.first", c.Groups[0].Evidence["first"])
			assertUTC(t, "evidence.last", c.Groups[0].Evidence["last"])
			future++
		}
	}
	assert.Equal(t, 1, future)

	agreement := do(t, f.router, owner, http.MethodGet, "/api/checks/agreement", "")
	require.Equal(t, http.StatusOK, agreement.Code, agreement.Body.String())
	var generated struct {
		GeneratedAt string `json:"generated_at"`
	}
	require.NoError(t, json.Unmarshal(agreement.Body.Bytes(), &generated))
	assertUTC(t, "agreement generated_at", generated.GeneratedAt)
}

func TestChecks_CancelledRequest(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provAt, 10, 20)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(cancelled)
		req.Header.Set(testUserHeader, owner)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}

	t.Run("checks answer 504", func(t *testing.T) {
		rec := get("/api/checks")

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"error":"checks_timeout"}`, rec.Body.String())
	})

	t.Run("the agreement answers what it has", func(t *testing.T) {
		rec := get("/api/checks/agreement?from=3")

		require.Equal(t, http.StatusOK, rec.Code, "never a 504: a partial answer is valid")
		var resp AgreementResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.True(t, resp.Truncated)
		assert.Empty(t, resp.Files)
		assert.Zero(t, resp.TotalFiles, "no inventory was read")
		assert.Equal(t, ptr(3), resp.NextFrom, "nothing was compared, so a retry starts where this one did")
	})
}

// Every query filters on a literal owner_id: another user's tracks and findings, and media another
// user shares with the owner through an album, never enter either response. The queries that look
// tracks up by label or by time near a photo are pinned by
// TestChecks_IgnoresAnotherUsersSameLabelAndNearbyPoints, and the orphan query's own items by
// TestChecks_SharedPhotoNeverExplainsAnOrphan.
func TestChecks_IgnoresOtherUsersAndSharedMedia(t *testing.T) {
	f := newChecksFixture(t)
	ctx := context.Background()
	owner := createUser(t, f.pool, "owner")
	stranger := createUser(t, f.pool, "stranger")
	now := time.Now().UTC().Truncate(time.Second)

	// The owner: one quiet file with a current estimate on it, and a photo's own point, so that the
	// orphan query runs for the label exif, which the stranger's orphan has too.
	insertSeriesAt(t, f.pool, owner, "gpx:mine.gpx", provTime.Add(-30*time.Minute), time.Minute, 61, 10, 20)
	f.item(t, owner, checksItem{takenAt: provAt})
	insertTrack(t, f.pool, owner, "exif", provOffset(-checksDay(1)), 10, 20)
	f.item(t, owner, checksItem{takenAt: provOffset(-checksDay(1)), lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("exif")})
	runMatcherFor(t, f.pool, owner)

	// The stranger: points of another format beside every point of the owner's, and a finding for
	// every check, with the items shared with the owner.
	const marker = "stranger-only"
	label := func(format string) string { return format + ":" + marker }
	insertSeriesAt(t, f.pool, stranger, label("nmea"), provTime.Add(-30*time.Minute), time.Minute, 61, 10.5, 20)
	insertTrack(t, f.pool, stranger, label("gpx"), provOffset(checksDay(1)-30*time.Second), 10, 20)
	insertTrack(t, f.pool, stranger, label("nmea"), provOffset(checksDay(1)+30*time.Second), 10.5, 20)
	insertTrack(t, f.pool, stranger, label("kml"), provOffset(checksDay(2)-10*time.Minute), 10, 20)
	insertTrack(t, f.pool, stranger, label("gpx"), provOffset(checksDay(2)+30*time.Minute), 10, 20.03)
	insertTrack(t, f.pool, stranger, "exif", provOffset(checksDay(3)), 10, 20)
	insertTrack(t, f.pool, stranger, label("gpx"), now.Add(72*time.Hour).Format(time.RFC3339), 10, 20)
	shared := []string{
		// An estimate the owner's own points would place too, and an own position a second from one
		// of them: either would show in the owner's numbers if it leaked.
		f.item(t, stranger, checksItem{takenAt: provOffset(10 * time.Second)}),
		f.item(t, stranger, checksItem{takenAt: provOffset(time.Second), lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("exif"), filename: marker + ".jpg"}),
		f.item(t, stranger, checksItem{takenAt: provOffset(checksDay(1))}),
		f.item(t, stranger, checksItem{takenAt: provOffset(checksDay(2))}),
		f.item(t, stranger, checksItem{takenAt: "2030-01-01T00:00:00Z", createdAt: "2020-01-01T00:00:00Z", camera: ptr(marker)}),
		f.item(t, stranger, checksItem{takenAt: provOffset(checksDay(9)), lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("interpolated"), confidence: ptr(0.5)}),
	}
	runMatcherFor(t, f.pool, stranger)
	album := insertAlbum(t, f.pool, stranger, "Shared with the owner")
	insertAlbumMember(t, f.pool, album, owner)
	for _, id := range shared {
		insertAlbumMedia(t, f.pool, album, id)
	}

	t.Run("checks", func(t *testing.T) {
		rec := do(t, f.router, owner, http.MethodGet, "/api/checks", "")

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := rec.Body.String()
		assert.NotContains(t, body, marker)
		for _, id := range shared {
			assert.NotContains(t, body, id)
		}
		var got ChecksResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Files, 2)
		assert.Equal(t, "exif", got.Files[0].Source)
		assert.Equal(t, 1, got.Files[0].Points)
		assert.Equal(t, "gpx:mine.gpx", got.Files[1].Source)
		assert.Equal(t, 1, got.Files[1].ItemsPlaced)
		assert.Empty(t, got.Residuals)
		for _, c := range got.Checks {
			assert.Zero(t, c.Total, c.Code)
		}
	})

	t.Run("agreement", func(t *testing.T) {
		got := f.agreement(t, owner)

		require.Len(t, got.Files, 1)
		assert.Equal(t, "gpx:mine.gpx", got.Files[0].Source)
		assert.Positive(t, got.Files[0].Samples)
		assert.Zero(t, got.Files[0].Matched, "another user's points are never a match")
	})

	t.Run("the stranger's own response has them", func(t *testing.T) {
		got := f.checks(t, stranger)
		for _, code := range []string{checksPairImpossible, checksFutureTrackPoints, checksTakenAfterUpload,
			checksStaleEstimates, checksOrphanPoints, checksTimeMayBeEstimatedUsed} {
			assert.Positive(t, findCheck(t, got, code).Total, "so the owner's zero can fail: %s", code)
		}
		assert.NotEmpty(t, got.Residuals)
		var n int
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM album_media WHERE album_id = $1`, album).Scan(&n))
		assert.Equal(t, len(shared), n, "the owner can read all of them")
	})
}

// Two users can hold the same label, both having imported a "Records.json", say, and record at
// the same moments. The owner's samples, gaps and residuals still come from the owner's points.
func TestChecks_IgnoresAnotherUsersSameLabelAndNearbyPoints(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	stranger := createUser(t, f.pool, "stranger")
	start, photoAt := provTime, provTime.Add(5*time.Minute)

	// The owner: a log every 30 s for 20 minutes, a second format 111 m away every minute, and a
	// photo with its own position on a point of the first.
	insertSeriesAt(t, f.pool, owner, "gpx:Track.gpx", start, 30*time.Second, 41, 10, 20)
	insertSeriesAt(t, f.pool, owner, "nmea:own.nmea", start.Add(10*time.Second), time.Minute, 21, 10.001, 20)
	f.item(t, owner, checksItem{takenAt: photoAt.Format(time.RFC3339), lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("exif")})

	// The stranger: the same label, between the owner's points and far away, and a log point of
	// another format a second after the owner's photo.
	insertSeriesAt(t, f.pool, stranger, "gpx:Track.gpx", start.Add(7*time.Second), 13*time.Second, 90, 50, 60)
	insertTrack(t, f.pool, stranger, "esoko:van.log", photoAt.Add(time.Second).Format(time.RFC3339), 50, 60)

	t.Run("residuals", func(t *testing.T) {
		got := f.checks(t, owner).Residuals

		assert.Equal(t, []checksResidual{{Format: "gpx", N: 1}}, got, "the owner's own point, at the photo's position")
	})

	t.Run("agreement", func(t *testing.T) {
		got := agreementFor(t, f.agreement(t, owner), "gpx:Track.gpx")

		assert.Equal(t, 20, got.Samples)
		assert.Equal(t, 20, got.Matched)
		require.NotNil(t, got.MedianM)
		assert.InDelta(t, geo.HaversineKm(10, 20, 10.001, 20)*1000, *got.MedianM, 1e-6, "sampled on the owner's points")
		assert.Equal(t, ptr(1.0), got.ModalGapShare, "the owner's gaps only")
	})

	t.Run("the stranger's file has the owner's label", func(t *testing.T) {
		got := agreementFor(t, f.agreement(t, stranger), "gpx:Track.gpx")

		assert.Positive(t, got.Samples, "so the owner's numbers can fail")
	})
}

// A photo another user shares with the owner, taken in the second of the owner's orphan point, is
// not the owner's item, so the point stays an orphan.
func TestChecks_SharedPhotoNeverExplainsAnOrphan(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	stranger := createUser(t, f.pool, "stranger")
	orphanAt := provOffset(10 * time.Minute)
	insertTrack(t, f.pool, owner, "exif", orphanAt, 10.01, 20.01)
	shared := f.item(t, stranger, checksItem{takenAt: orphanAt, lat: ptr(10.01), lng: ptr(20.01), gpsSource: ptr("exif")})
	album := insertAlbum(t, f.pool, stranger, "Shared with the owner")
	insertAlbumMember(t, f.pool, album, owner)
	insertAlbumMedia(t, f.pool, album, shared)

	got := findCheck(t, f.checks(t, owner), "orphan_points")

	assert.Equal(t, 1, got.Total)
	require.Len(t, got.Groups, 1)
	assert.Equal(t, "exif", got.Groups[0].Key)
}

// Items at one taken_at are listed by id, and of two points of one format at the same distance in
// time from a photo the lower label measures it. Each pair is inserted the other way round, so
// that neither order can come from the table's physical order.
func TestChecks_TieBreaks(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	unsupported := []string{
		"ffffffff-0000-4000-8000-000000000003",
		"88888888-0000-4000-8000-000000000002",
		"11111111-0000-4000-8000-000000000001",
	}
	for _, id := range unsupported {
		// No point near them: the matcher could no longer place them.
		f.item(t, owner, checksItem{id: id, takenAt: provOffset(checksDay(10)), lat: ptr(10.0), lng: ptr(20.0),
			gpsSource: ptr("interpolated"), confidence: ptr(0.5)})
	}
	ahead := []string{
		"eeeeeeee-0000-4000-8000-000000000003",
		"77777777-0000-4000-8000-000000000002",
		"22222222-0000-4000-8000-000000000001",
	}
	for _, id := range ahead {
		f.item(t, owner, checksItem{id: id, takenAt: "2021-01-01T00:00:00Z", createdAt: "2020-01-01T00:00:00Z", camera: ptr("Model T")})
	}
	f.item(t, owner, checksItem{takenAt: provAt, lat: ptr(10.0), lng: ptr(20.0), gpsSource: ptr("exif")})
	insertTrack(t, f.pool, owner, "gpx:b.gpx", provOffset(time.Second), 10, 20.01)
	insertTrack(t, f.pool, owner, "gpx:a.gpx", provOffset(time.Second), 10, 20.001)

	got := f.checks(t, owner)

	stale := findCheck(t, got, "stale_estimates")
	require.Len(t, stale.Groups, 1)
	assert.Equal(t, "no_support", stale.Groups[0].Key)
	assert.Equal(t, slices.Sorted(slices.Values(unsupported)), stale.Groups[0].MediaIDs)
	after := findCheck(t, got, "taken_after_upload")
	require.Len(t, after.Groups, 1)
	assert.Equal(t, slices.Sorted(slices.Values(ahead)), after.Groups[0].MediaIDs)
	require.Len(t, got.Residuals, 1)
	assert.InDelta(t, geo.HaversineKm(10, 20, 10, 20.001)*1000, got.Residuals[0].P50M, 1e-6, "gpx:a.gpx's point")
}

// An estimate whose match window JSON cannot carry is left out, as the evidence view reads its
// time as none. Ingest never stores such a time; SQL can.
func TestChecks_LeavesOutAnEstimateWhoseWindowJSONCannotCarry(t *testing.T) {
	f := newChecksFixture(t)
	owner := createUser(t, f.pool, "owner")
	estimate := func(takenAt string) string {
		return f.item(t, owner, checksItem{takenAt: takenAt, lat: ptr(10.0), lng: ptr(20.0),
			gpsSource: ptr("interpolated"), confidence: ptr(0.5)})
	}
	unsupported := estimate(provAt)
	estimate("9999-12-31T23:30:00Z") // its window ends in year 10000

	got := findCheck(t, f.checks(t, owner), "stale_estimates")

	assert.Equal(t, 1, got.Total)
	require.Len(t, got.Groups, 1)
	assert.Equal(t, []string{unsupported}, got.Groups[0].MediaIDs)
}
