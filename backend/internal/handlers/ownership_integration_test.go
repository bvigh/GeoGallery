package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"codeberg.org/bvigh/geogallery/internal/testdb"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeObjectStore stands in for S3 in integration tests: the DB is real, the bucket is not.
type fakeObjectStore struct {
	mu      sync.Mutex
	keys    []string
	deleted []string
}

func (s *fakeObjectStore) Upload(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	if _, err := io.Copy(io.Discard, body); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	return nil
}

func (s *fakeObjectStore) PresignGet(_ context.Context, key string) (string, error) {
	return "https://s3.test/" + key + "?X-Amz-Signature=sig", nil
}

func (s *fakeObjectStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, key)
	return nil
}

const testUserHeader = "X-Test-User"

// newOwnershipRouter wires the real handlers against pool, authenticating each request as the
// user ID in the X-Test-User header (the auth middleware itself is tested in package auth).
func newOwnershipRouter(pool *pgxpool.Pool, store *fakeObjectStore) http.Handler {
	media := &MediaHandler{
		db: pool, pool: pool, storage: store,
		registrar:   &MediaRegistrar{db: pool, storage: store},
		matcherPool: pool, matchWindow: time.Hour, runMatcher: RunGPSMatcher,
		tripGapHours: 24, runTripDetect: RunTripDetect,
	}
	trips := NewTripHandler(pool, 24)
	game := &GameHandler{db: pool, storage: store}
	tracks := &GPSTrackHandler{db: pool, matcherPool: pool, matchWindow: time.Hour, runMatcher: RunGPSMatcher}
	matcher := &GPSMatcherHandler{matcherPool: pool, matchWindow: time.Hour, runMatcher: RunGPSMatcher}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.User{ID: r.Header.Get(testUserHeader), Role: "user"}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u, auth.MethodSession)))
		})
	})
	r.Route("/api", func(r chi.Router) {
		r.Get("/media", media.List)
		r.Post("/media/upload", media.Upload)
		r.Get("/media/{id}/url", media.GetURL)
		r.Patch("/media/{id}/location", media.UpdateLocation)
		r.Patch("/media/{id}/view-settings", media.UpdateViewSettings)
		r.Get("/media/{id}/keyframes", media.GetKeyframes)
		r.Put("/media/{id}/keyframes", media.ReplaceKeyframes)
		r.Delete("/media/{id}", media.Delete)
		r.Get("/trips", trips.List)
		r.Patch("/trips/{id}", trips.Update)
		r.Delete("/trips/{id}", trips.Delete)
		r.Get("/game/round", game.NewRound)
		r.Post("/game/guess", game.SubmitGuess)
		r.Post("/gps-tracks/import", tracks.Import)
		r.Post("/gps-matcher/run", matcher.Run)
	})
	return r
}

func do(t *testing.T, h http.Handler, userID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set(testUserHeader, userID)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func createUser(t *testing.T, pool *pgxpool.Pool, subject string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email, display_name)
		VALUES ('https://idp.test', $1, $1 || '@example.test', $1) RETURNING id::text`, subject).Scan(&id))
	return id
}

func insertTrip(t *testing.T, pool *pgxpool.Pool, ownerID, name string, userEdited bool, start, end string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO trips (owner_id, name, user_edited, start_date, end_date)
		VALUES ($1, $2, $3, $4::date, $5::date) RETURNING id::text`,
		ownerID, name, userEdited, start, end).Scan(&id))
	return id
}

type mediaFixture struct {
	owner, filename, takenAt string
	lat, lng                 *float64
	tripID                   *string
}

func insertMedia(t *testing.T, pool *pgxpool.Pool, m mediaFixture) string {
	t.Helper()
	var source *string
	if m.lat != nil {
		s := "exif"
		source = &s
	}
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, taken_at, location, gps_source, trip_id)
		VALUES ($1, $2, 'photo', $8, $9, $3::timestamptz,
		        CASE WHEN $4::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($5, $4), 4326) END, $6, $7)
		RETURNING id::text`,
		m.owner, m.filename, m.takenAt, m.lat, m.lng, source, m.tripID,
		"media/"+m.owner+"/"+m.filename, "hash-"+m.owner+"-"+m.filename).Scan(&id))
	return id
}

func insertTrack(t *testing.T, pool *pgxpool.Pool, ownerID, source, at string, lat, lng float64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
		VALUES ($1, $2, $3::timestamptz, ST_SetSRID(ST_MakePoint($4, $5), 4326))`, ownerID, source, at, lng, lat)
	require.NoError(t, err)
}

const (
	budapestLat, budapestLng = 47.4979, 19.0402
	viennaLat, viennaLng     = 48.2082, 16.3738
)

// ownershipFixture: A and B each own media, a trip and GPS tracks in the same date range.
type ownershipFixture struct {
	pool                    *pgxpool.Pool
	store                   *fakeObjectStore
	router                  http.Handler
	userA, userB            string
	tripA, tripB            string // tripA is user-edited, tripB is auto-detected
	mediaA, mediaAUnlocated string
	mediaB                  string
}

func newOwnershipFixture(t *testing.T) *ownershipFixture {
	t.Helper()
	pool := testdb.New(t)
	f := &ownershipFixture{pool: pool, store: &fakeObjectStore{}}
	f.router = newOwnershipRouter(pool, f.store)
	f.userA = createUser(t, pool, "alice")
	f.userB = createUser(t, pool, "bob")

	f.tripA = insertTrip(t, pool, f.userA, "A trip", true, "2024-05-02", "2024-05-02")
	f.tripB = insertTrip(t, pool, f.userB, "Trip 2024-05-03", false, "2024-05-03", "2024-05-03")

	f.mediaA = insertMedia(t, pool, mediaFixture{owner: f.userA, filename: "a1.jpg", takenAt: "2024-05-02T12:00:00Z",
		lat: ptr(budapestLat), lng: ptr(budapestLng), tripID: &f.tripA})
	f.mediaAUnlocated = insertMedia(t, pool, mediaFixture{owner: f.userA, filename: "a2.jpg", takenAt: "2024-05-03T12:30:00Z"})
	f.mediaB = insertMedia(t, pool, mediaFixture{owner: f.userB, filename: "b1.jpg", takenAt: "2024-05-03T13:00:00Z",
		lat: ptr(viennaLat), lng: ptr(viennaLng), tripID: &f.tripB})

	_, err := pool.Exec(context.Background(), `
		INSERT INTO video_keyframes (media_id, time_seconds, yaw, pitch, fov)
		VALUES ($1, 0, 10, 0, 90), ($1, 5, 20, 0, 90)`, f.mediaA)
	require.NoError(t, err)

	// A's only track point is 10 minutes before a2; B's is 1 minute after it.
	insertTrack(t, pool, f.userA, "google_timeline", "2024-05-03T12:20:00Z", budapestLat, budapestLng)
	insertTrack(t, pool, f.userB, "google_timeline", "2024-05-03T12:31:00Z", viennaLat, viennaLng)
	return f
}

func decodeIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var items []struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items), rec.Body.String())
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

type mediaSnapshot struct {
	lat, lng  *float64
	gpsSource *string
	tripID    *string
	yaw       *float64
	keyframes int
}

func snapshotMedia(t *testing.T, pool *pgxpool.Pool, id string) mediaSnapshot {
	t.Helper()
	var s mediaSnapshot
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), gps_source, trip_id::text, initial_yaw,
		       (SELECT COUNT(*) FROM video_keyframes WHERE media_id = m.id)
		FROM media m WHERE id = $1`, id).Scan(&s.lat, &s.lng, &s.gpsSource, &s.tripID, &s.yaw, &s.keyframes))
	return s
}

func tripName(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var name string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT name FROM trips WHERE id = $1`, id).Scan(&name))
	return name
}

func TestOwnership_ForeignResourcesAreInvisible(t *testing.T) {
	f := newOwnershipFixture(t)
	before := snapshotMedia(t, f.pool, f.mediaA)

	guess := `{"media_id":"` + f.mediaA + `","guess_lat":47.5,"guess_lng":19.0,"guess_date":"2024-05-02","max_dist_km":100}`
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		wantStatus  int
		wantBody    string // exact JSON body for the foreign request, when set
		ownerStatus int    // status for A on the same request; 0 skips the positive control
	}{
		{"get url", http.MethodGet, "/api/media/" + f.mediaA + "/url", "", http.StatusNotFound, "", http.StatusOK},
		{"update location", http.MethodPatch, "/api/media/" + f.mediaA + "/location", `{"lat":1,"lng":2}`, http.StatusNotFound, "", 0},
		{"update view settings", http.MethodPatch, "/api/media/" + f.mediaA + "/view-settings", `{"initial_yaw":45}`, http.StatusNotFound, "", 0},
		{"get keyframes", http.MethodGet, "/api/media/" + f.mediaA + "/keyframes", "", http.StatusOK, "[]", http.StatusOK},
		{"replace keyframes", http.MethodPut, "/api/media/" + f.mediaA + "/keyframes", `[{"time":1,"yaw":0,"pitch":0,"fov":90}]`, http.StatusNotFound, "", 0},
		{"delete media", http.MethodDelete, "/api/media/" + f.mediaA, "", http.StatusNotFound, "", 0},
		{"rename trip", http.MethodPatch, "/api/trips/" + f.tripA, `{"name":"pwned"}`, http.StatusNotFound, "", 0},
		{"delete trip", http.MethodDelete, "/api/trips/" + f.tripA, "", http.StatusNotFound, "", 0},
		{"guess", http.MethodPost, "/api/game/guess", guess, http.StatusNotFound, "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.router, f.userB, tt.method, tt.path, tt.body)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantBody != "" {
				assert.JSONEq(t, tt.wantBody, rec.Body.String())
			}
			if tt.ownerStatus != 0 {
				own := do(t, f.router, f.userA, tt.method, tt.path, tt.body)
				assert.Equal(t, tt.ownerStatus, own.Code, "positive control as owner: %s", own.Body.String())
			}
		})
	}

	// The foreign keyframes request gets [] just like a missing ID, so prove the owner gets real rows.
	t.Run("owner gets keyframes", func(t *testing.T) {
		rec := do(t, f.router, f.userA, http.MethodGet, "/api/media/"+f.mediaA+"/keyframes", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `[{"time":0,"yaw":10,"pitch":0,"fov":90},{"time":5,"yaw":20,"pitch":0,"fov":90}]`, rec.Body.String())
	})

	t.Run("A's resources are unchanged", func(t *testing.T) {
		assert.Equal(t, before, snapshotMedia(t, f.pool, f.mediaA))
		assert.Equal(t, 2, before.keyframes)
		assert.Equal(t, "A trip", tripName(t, f.pool, f.tripA))
		assert.Empty(t, f.store.deleted)
	})

	t.Run("media list", func(t *testing.T) {
		rec := do(t, f.router, f.userB, http.MethodGet, "/api/media", "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []string{f.mediaB}, decodeIDs(t, rec))

		rec = do(t, f.router, f.userA, http.MethodGet, "/api/media", "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []string{f.mediaA, f.mediaAUnlocated}, decodeIDs(t, rec))
	})

	t.Run("trip list", func(t *testing.T) {
		rec := do(t, f.router, f.userB, http.MethodGet, "/api/trips", "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []string{f.tripB}, decodeIDs(t, rec))
	})

	t.Run("game round", func(t *testing.T) {
		for range 10 {
			rec := do(t, f.router, f.userB, http.MethodGet, "/api/game/round", "")
			require.Equal(t, http.StatusOK, rec.Code)
			var round roundMedia
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &round))
			assert.Equal(t, f.mediaB, round.ID)
			assert.InDelta(t, 1.0, round.MaxDistKm, 1e-9, "B has one geolocated point; A's Budapest point would add ~214 km")
		}

		rec := do(t, f.router, f.userA, http.MethodGet, "/api/game/round", "")
		require.Equal(t, http.StatusOK, rec.Code)
		var round roundMedia
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &round))
		assert.Equal(t, f.mediaA, round.ID)
		assert.InDelta(t, 1.0, round.MaxDistKm, 1e-9, "A has one geolocated point; B's Vienna point would add ~214 km")
	})
}

func TestOwnership_UserWithoutDataGetsEmptyLists(t *testing.T) {
	f := newOwnershipFixture(t)
	carol := createUser(t, f.pool, "carol")

	for _, path := range []string{"/api/trips", "/api/media"} {
		rec := do(t, f.router, carol, http.MethodGet, path, "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, "[]", rec.Body.String(), path)
	}
	rec := do(t, f.router, carol, http.MethodGet, "/api/game/round", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestOwnership_MatcherIgnoresOtherOwnersTracks(t *testing.T) {
	f := newOwnershipFixture(t)

	rec := do(t, f.router, f.userA, http.MethodPost, "/api/gps-matcher/run", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := snapshotMedia(t, f.pool, f.mediaAUnlocated)
	require.NotNil(t, got.lat, "A's track point is within the window")
	assert.InDelta(t, budapestLat, *got.lat, 1e-6, "interpolated from A's track only, never B's closer point")
	assert.InDelta(t, budapestLng, *got.lng, 1e-6)
	assert.Equal(t, "interpolated", *got.gpsSource)

	b := snapshotMedia(t, f.pool, f.mediaB)
	assert.InDelta(t, viennaLat, *b.lat, 1e-6)
	assert.Equal(t, "exif", *b.gpsSource)
}

func TestOwnership_TripDetectLeavesOtherOwnersTrips(t *testing.T) {
	f := newOwnershipFixture(t)
	bUnassigned := insertMedia(t, f.pool, mediaFixture{owner: f.userB, filename: "b2.jpg", takenAt: "2024-05-03T12:45:00Z"})

	require.NoError(t, RunTripDetect(context.Background(), f.pool, f.userA, 24))

	assert.Nil(t, snapshotMedia(t, f.pool, bUnassigned).tripID, "B's unassigned media is not grouped by A's detection")
	var foreignInATrips int
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM media m JOIN trips t ON t.id = m.trip_id
		WHERE t.owner_id = $1 AND m.owner_id <> $1`, f.userA).Scan(&foreignInATrips))
	assert.Zero(t, foreignInATrips, "no A trip contains B's media")

	assert.Equal(t, "Trip 2024-05-03", tripName(t, f.pool, f.tripB), "B's auto trip survives A's detection")
	assert.Equal(t, f.tripB, *snapshotMedia(t, f.pool, f.mediaB).tripID)

	var aAutoTrips int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM trips WHERE owner_id = $1 AND user_edited = FALSE`, f.userA).Scan(&aAutoTrips))
	assert.Equal(t, 1, aAutoTrips, "A's unassigned a2 gets its own auto trip")
	aUnlocated := snapshotMedia(t, f.pool, f.mediaAUnlocated)
	require.NotNil(t, aUnlocated.tripID)
	var tripOwner string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT owner_id::text FROM trips WHERE id = $1`, *aUnlocated.tripID).Scan(&tripOwner))
	assert.Equal(t, f.userA, tripOwner)
}

func uploadAs(t *testing.T, h http.Handler, userID, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", filename)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/media/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(testUserHeader, userID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOwnership_SameFileUploadedByTwoUsers(t *testing.T) {
	f := newOwnershipFixture(t)
	content := []byte("identical bytes uploaded by both users")

	require.Equal(t, http.StatusOK, uploadAs(t, f.router, f.userA, "shared.jpg", content).Code)
	require.Equal(t, http.StatusOK, uploadAs(t, f.router, f.userB, "shared.jpg", content).Code)
	rec := uploadAs(t, f.router, f.userA, "renamed.jpg", content)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"uploaded":null,"failed":null}`, rec.Body.String(), "A's re-upload is deduplicated")

	rows, err := f.pool.Query(context.Background(),
		`SELECT owner_id::text, storage_key FROM media WHERE filename = 'shared.jpg' ORDER BY owner_id`)
	require.NoError(t, err)
	owners := map[string]string{}
	for rows.Next() {
		var owner, key string
		require.NoError(t, rows.Scan(&owner, &key))
		owners[owner] = key
	}
	require.NoError(t, rows.Err())
	require.Len(t, owners, 2, "one row per owner")
	assert.Regexp(t, `^media/`+f.userA+`/[0-9a-f]{32}\.jpg$`, owners[f.userA])
	assert.Regexp(t, `^media/`+f.userB+`/[0-9a-f]{32}\.jpg$`, owners[f.userB])
}

func TestOwnership_TripUpdateWithForeignTripID(t *testing.T) {
	f := newOwnershipFixture(t)
	beforeA := snapshotMedia(t, f.pool, f.mediaAUnlocated)

	rec := do(t, f.router, f.userA, http.MethodPatch, "/api/trips/"+f.tripB,
		`{"start_date":"2024-05-01","end_date":"2024-05-31"}`)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, beforeA, snapshotMedia(t, f.pool, f.mediaAUnlocated), "no media reassigned into B's trip")
	assert.Equal(t, f.tripB, *snapshotMedia(t, f.pool, f.mediaB).tripID)
}

func TestOwnership_DateRangeEditDoesNotMoveOtherOwnersMedia(t *testing.T) {
	f := newOwnershipFixture(t)
	bUnassigned := insertMedia(t, f.pool, mediaFixture{owner: f.userB, filename: "b2.jpg", takenAt: "2024-05-10T09:00:00Z"})

	rec := do(t, f.router, f.userA, http.MethodPatch, "/api/trips/"+f.tripA,
		`{"start_date":"2024-05-01","end_date":"2024-05-31"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assert.Nil(t, snapshotMedia(t, f.pool, bUnassigned).tripID, "B's unassigned media in the range stays unassigned")

	assert.Equal(t, f.tripA, *snapshotMedia(t, f.pool, f.mediaAUnlocated).tripID, "A's media in range joins A's trip")
	assert.Equal(t, f.tripB, *snapshotMedia(t, f.pool, f.mediaB).tripID, "B's media in the same range stays put")
	assert.Equal(t, "Trip 2024-05-03", tripName(t, f.pool, f.tripB), "B's auto trip is not deleted as empty")
}

func TestOwnership_CompositeFKRejectsForeignTrip(t *testing.T) {
	f := newOwnershipFixture(t)

	_, err := f.pool.Exec(context.Background(), `UPDATE media SET trip_id = $1 WHERE id = $2`, f.tripB, f.mediaA)

	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "want FK violation, got %v", err)
	assert.Equal(t, "23503", pgErr.Code)
	assert.Equal(t, "media_trip_owner_fkey", pgErr.ConstraintName)
}

func TestOwnership_GPSImportIsOwnedByCaller(t *testing.T) {
	f := newOwnershipFixture(t)

	rec := do(t, f.router, f.userB, http.MethodPost, "/api/gps-tracks/import",
		`[{"recorded_at":"2024-05-03T12:20:00Z","lat":1,"lng":2,"source":"google_timeline"}]`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"inserted":1,"skipped":0,"errors":[]}`, rec.Body.String(),
		"same (source, recorded_at) as A's point is not a conflict across owners")

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM gps_tracks WHERE owner_id = $1`, f.userB).Scan(&n))
	assert.Equal(t, 2, n)
	assert.Nil(t, snapshotMedia(t, f.pool, f.mediaAUnlocated).lat, "B's import never geolocates A's media")
}

// Positive controls for the mutating routes in TestOwnership_ForeignResourcesAreInvisible, so
// those 404s cannot come from a request that would fail for the owner too.
func TestOwnership_OwnerCanMutateOwnResources(t *testing.T) {
	f := newOwnershipFixture(t)

	steps := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{"update view settings", http.MethodPatch, "/api/media/" + f.mediaA + "/view-settings", `{"initial_yaw":45}`, http.StatusNoContent},
		{"replace keyframes", http.MethodPut, "/api/media/" + f.mediaA + "/keyframes", `[{"time":1,"yaw":0,"pitch":0,"fov":90}]`, http.StatusNoContent},
		{"update location", http.MethodPatch, "/api/media/" + f.mediaA + "/location", `{"lat":1,"lng":2}`, http.StatusNoContent},
		{"rename trip", http.MethodPatch, "/api/trips/" + f.tripA, `{"name":"renamed"}`, http.StatusNoContent},
	}
	for _, s := range steps {
		rec := do(t, f.router, f.userA, s.method, s.path, s.body)
		require.Equal(t, s.wantStatus, rec.Code, "%s: %s", s.name, rec.Body.String())
	}
	got := snapshotMedia(t, f.pool, f.mediaA)
	assert.InDelta(t, 45, *got.yaw, 1e-9)
	assert.Equal(t, 1, got.keyframes)
	assert.InDelta(t, 1, *got.lat, 1e-6)
	assert.Equal(t, "manual", *got.gpsSource)
	assert.Equal(t, "renamed", tripName(t, f.pool, f.tripA))

	var manualOwner string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT owner_id::text FROM gps_tracks WHERE source = 'manual'`).Scan(&manualOwner))
	assert.Equal(t, f.userA, manualOwner)

	require.Equal(t, http.StatusNoContent, do(t, f.router, f.userA, http.MethodDelete, "/api/trips/"+f.tripA, "").Code)
	require.Equal(t, http.StatusNoContent, do(t, f.router, f.userA, http.MethodDelete, "/api/media/"+f.mediaA, "").Code)
	assert.Equal(t, []string{"media/" + f.userA + "/a1.jpg"}, f.store.deleted)
}
