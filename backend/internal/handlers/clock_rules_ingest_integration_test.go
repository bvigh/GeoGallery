package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clockRuleFixture is ingest and the provenance routes against a real database, with the server
// configured as the hosted one is: Warsaw and the shipped Panasonic rule.
type clockRuleFixture struct {
	pool   *pgxpool.Pool
	store  *fakeObjectStore
	reg    *MediaRegistrar
	router http.Handler
}

func newClockRuleFixture(t *testing.T) *clockRuleFixture {
	t.Helper()
	pool := testdb.New(t)
	store := &fakeObjectStore{objects: map[string][]byte{}}
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicVideoRules}
	provenance := &ProvenanceHandler{
		db: pool, storage: store, matchWindow: time.Hour, cameraZone: zone,
		probeURL: store.probeURL, probeSlots: make(chan struct{}, maxConcurrentProbes),
		photoSlots: make(chan struct{}, maxConcurrentPhotoReads),
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.User{ID: r.Header.Get(testUserHeader), Role: "user"}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u, auth.MethodSession)))
		})
	})
	r.Get("/api/media/{id}/provenance", provenance.Get)
	r.Get("/api/media/{id}/provenance/file", provenance.File)
	return &clockRuleFixture{
		pool: pool, store: store, router: r,
		reg: &MediaRegistrar{db: pool, storage: store, cameraZone: zone},
	}
}

// ingest stores content for owner through registerMedia, and keeps the object so it can be re-read.
func (f *clockRuleFixture) ingest(t *testing.T, owner, filename string, content []byte) string {
	t.Helper()
	id, inserted, err := f.reg.registerMedia(context.Background(), owner, bytes.NewReader(content), int64(len(content)), filename, nil)
	require.NoError(t, err)
	require.True(t, inserted)
	f.keep(t, id, content)
	return id
}

func (f *clockRuleFixture) keep(t *testing.T, id string, content []byte) {
	t.Helper()
	var key string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT storage_key FROM media WHERE id = $1`, id).Scan(&key))
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	f.store.objects[key] = content
}

// stored is the row's taken_at in UTC and its capture evidence.
func (f *clockRuleFixture) stored(t *testing.T, id string) (string, map[string]any) {
	t.Helper()
	var takenAt time.Time
	var raw []byte
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT taken_at, capture_evidence FROM media WHERE id = $1`, id).Scan(&takenAt, &raw))
	var evidence map[string]any
	require.NoError(t, json.Unmarshal(raw, &evidence))
	assert.Len(t, evidence, 23, "every key is always present")
	return takenAt.UTC().Format(time.RFC3339), evidence
}

func (f *clockRuleFixture) reread(t *testing.T, owner, id string) FileReread {
	t.Helper()
	rec := do(t, f.router, owner, http.MethodGet, "/api/media/"+id+"/provenance/file", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got FileReread
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
	return got
}

// tz200Video is the head of a DC-TZ200 MP4 recorded on a winter noon by its own clock.
var tz200Video = exiftest.PanasonicMP4(exiftest.Photo{
	Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:01:15 12:00:00", OffsetTimeOriginal: "+01:00",
})

func TestClockRules_Integration_Ingest(t *testing.T) {
	f := newClockRuleFixture(t)
	alice := createUser(t, f.pool, "alice") // her DC-TZ200 shows UTC+2 from mid-2018 on
	bob := createUser(t, f.pool, "bob")     // no rules
	carol := createUser(t, f.pool, "carol") // a rule for another model of the make
	aliceRule := insertClockRule(t, f.pool, alice, "Panasonic", "DC-TZ200", 120, ptr("2018-07-01"), nil)
	insertClockRule(t, f.pool, carol, "Panasonic", "DC-TZ100", 120, nil, nil)
	snapshot := map[string]any{
		"id": aliceRule, "make": "Panasonic", "model": "DC-TZ200", "offset_min": 120.0, "starts_on": "2018-07-01", "ends_on": nil,
	}
	winter, summer := exiftest.JPEG(tz200Winter), exiftest.JPEG(tz200Summer)

	t.Run("a winter photo under the owner's rule is wall minus 2 h", func(t *testing.T) {
		takenAt, evidence := f.stored(t, f.ingest(t, alice, "P1033627.JPG", winter))

		assert.Equal(t, "2019-03-13T17:28:15Z", takenAt)
		assert.Equal(t, "exif_original", evidence["src"])
		assert.Equal(t, "clock_rule", evidence["zone"])
		assert.Equal(t, 120.0, evidence["offset_used_min"])
		assert.Equal(t, "+01:00", evidence["offset_tag"])
		assert.Equal(t, true, evidence["offset_ignored"])
		assert.Equal(t, snapshot, evidence["clock_rule"])
		assert.Nil(t, evidence["retimed_from"])
		assert.Equal(t, "ingest", evidence["by"])
	})
	t.Run("rules never cross owners", func(t *testing.T) {
		takenAt, evidence := f.stored(t, f.ingest(t, bob, "P1033627.JPG", winter))

		assert.Equal(t, "2019-03-13T18:28:15Z", takenAt, "Warsaw's winter offset")
		assert.Equal(t, "camera", evidence["zone"])
		assert.Nil(t, evidence["clock_rule"])
	})
	t.Run("a rule for another model does nothing", func(t *testing.T) {
		takenAt, evidence := f.stored(t, f.ingest(t, carol, "P1033627.JPG", winter))

		assert.Equal(t, "2019-03-13T18:28:15Z", takenAt)
		assert.Equal(t, "camera", evidence["zone"])
		assert.Nil(t, evidence["clock_rule"])
	})
	t.Run("a summer photo is the same instant with and without the rule", func(t *testing.T) {
		withRule, withEvidence := f.stored(t, f.ingest(t, alice, "P1040001.JPG", summer))
		without, withoutEvidence := f.stored(t, f.ingest(t, bob, "P1040001.JPG", summer))

		assert.Equal(t, "2019-07-14T10:00:00Z", withRule)
		assert.Equal(t, withRule, without)
		assert.Equal(t, "clock_rule", withEvidence["zone"])
		assert.Equal(t, "camera", withoutEvidence["zone"])
	})
	t.Run("a Panasonic video dated from its EXIF gets the rule", func(t *testing.T) {
		aliceID := f.ingest(t, alice, "P1000001.MP4", tz200Video)
		takenAt, evidence := f.stored(t, aliceID)
		assert.Equal(t, "2019-01-15T10:00:00Z", takenAt)
		assert.Equal(t, "clock_rule", evidence["zone"])
		assert.Equal(t, snapshot, evidence["clock_rule"])
		var model, mediaType string
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT camera_model, media_type FROM media WHERE id = $1`, aliceID).Scan(&model, &mediaType))
		assert.Equal(t, "DC-TZ200", model)
		assert.Equal(t, "video", mediaType)

		takenAt, evidence = f.stored(t, f.ingest(t, bob, "P1000001.MP4", tz200Video))
		assert.Equal(t, "2019-01-15T11:00:00Z", takenAt)
		assert.Equal(t, "camera", evidence["zone"])
	})
	t.Run("a scan applies the rule too", func(t *testing.T) {
		dir := t.TempDir()
		photo := exiftest.JPEG(exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2020:12:24 18:00:00"})
		writeFile(t, dir, "P1050001.JPG", string(photo))
		deps := IngestDeps{
			registrar: f.reg, matcherPool: f.pool, matchWindow: time.Hour, runMatcher: RunGPSMatcher,
			tripGapHours: 24, runTripDetect: RunTripDetect,
		}

		res, err := RunIngestScan(context.Background(), deps, dir, alice)

		require.NoError(t, err)
		assert.Equal(t, ScanResult{Ingested: 1}, res)
		takenAt, evidence := f.stored(t, mediaIDByName(t, f.pool, alice, "P1050001.JPG"))
		assert.Equal(t, "2020-12-24T16:00:00Z", takenAt)
		assert.Equal(t, "clock_rule", evidence["zone"])
	})
}

// The re-read applies the owner's rules as they are today: an item ingested under a rule reads the
// same, and once the rule is gone it reads an hour late, because the stored time does not move.
func TestClockRules_Integration_FileReread(t *testing.T) {
	f := newClockRuleFixture(t)
	alice := createUser(t, f.pool, "alice")
	rule := insertClockRule(t, f.pool, alice, "Panasonic", "DC-TZ200", 120, ptr("2018-07-01"), nil)
	photo := f.ingest(t, alice, "P1033627.JPG", exiftest.JPEG(tz200Winter))
	video := f.ingest(t, alice, "P1000001.MP4", tz200Video)

	for _, id := range []string{photo, video} {
		got := f.reread(t, alice, id)

		require.NotNil(t, got.RuleToday)
		assert.Equal(t, ptr(ingest.ZoneClockRule), got.RuleToday.ZoneRule)
		assert.Equal(t, ptr(120), got.RuleToday.OffsetMin)
		assert.Nil(t, got.RuleToday.ZoneName)
		assert.Equal(t, &evidenceClockRule{ID: rule, Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120, StartsOn: ptr("2018-07-01")},
			got.RuleToday.ClockRule)
		assert.Equal(t, rereadSame, got.Class)
		assert.Equal(t, ptr(0.0), got.DeltaS)
	}

	_, err := f.pool.Exec(context.Background(), `DELETE FROM camera_clock_rules WHERE id = $1`, rule)
	require.NoError(t, err)

	for _, id := range []string{photo, video} {
		got := f.reread(t, alice, id)

		require.NotNil(t, got.RuleToday)
		assert.Equal(t, ptr(ingest.ZoneCamera), got.RuleToday.ZoneRule)
		assert.Equal(t, ptr("Europe/Warsaw"), got.RuleToday.ZoneName)
		assert.Nil(t, got.RuleToday.ClockRule)
		assert.Equal(t, rereadWholeOffset, got.Class)
		assert.Equal(t, ptr(3600.0), got.DeltaS, "today's rules read it an hour later than stored")
	}

	t.Run("another owner's rule is not today's rule", func(t *testing.T) {
		bob := createUser(t, f.pool, "bob")
		insertClockRule(t, f.pool, bob, "Panasonic", "DC-TZ200", 120, nil, nil)

		got := f.reread(t, alice, photo)

		assert.Equal(t, ptr(ingest.ZoneCamera), got.RuleToday.ZoneRule)
		assert.Equal(t, rereadWholeOffset, got.Class)
	})
}

// The provenance route returns the evidence as stored, the clock rule snapshot included, and counts
// evidence server retime rewrote as recorded.
func TestClockRules_Integration_ProvenanceRecorded(t *testing.T) {
	f := newClockRuleFixture(t)
	alice := createUser(t, f.pool, "alice")
	rule := insertClockRule(t, f.pool, alice, "Panasonic", "DC-TZ200", 120, ptr("2018-07-01"), nil)
	id := f.ingest(t, alice, "P1033627.JPG", exiftest.JPEG(tz200Winter))
	provenance := func(t *testing.T) (string, map[string]any) {
		t.Helper()
		rec := do(t, f.router, alice, http.MethodGet, "/api/media/"+id+"/provenance", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got struct {
			Time struct {
				Recorded string         `json:"recorded"`
				Evidence map[string]any `json:"evidence"`
			} `json:"time"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got.Time.Recorded, got.Time.Evidence
	}

	recorded, evidence := provenance(t)
	assert.Equal(t, "ingest", recorded)
	_, stored := f.stored(t, id)
	assert.Equal(t, stored, evidence)
	assert.Equal(t, rule, evidence["clock_rule"].(map[string]any)["id"])

	t.Run("evidence from server retime is recorded as retime", func(t *testing.T) {
		_, err := f.pool.Exec(context.Background(), `
			UPDATE media SET capture_evidence = capture_evidence
				|| '{"by": "retime", "retimed_from": "2019-03-13T18:28:15Z"}'::jsonb
			WHERE id = $1`, id)
		require.NoError(t, err)

		recorded, evidence := provenance(t)

		assert.Equal(t, "retime", recorded)
		assert.Equal(t, "2019-03-13T18:28:15Z", evidence["retimed_from"])
		assert.Equal(t, "clock_rule", evidence["zone"])
		_, stored := f.stored(t, id)
		assert.Equal(t, stored, evidence)
	})
}

// A rule load that fails, here because the table is gone, fails the file instead of reading it
// without the owner's rules, and the re-read answers 500.
func TestClockRules_Integration_LoadFailure(t *testing.T) {
	f := newClockRuleFixture(t)
	alice := createUser(t, f.pool, "alice")
	id := f.ingest(t, alice, "P1040001.JPG", exiftest.JPEG(tz200Summer))
	_, err := f.pool.Exec(context.Background(), `ALTER TABLE camera_clock_rules RENAME TO camera_clock_rules_gone`)
	require.NoError(t, err)
	uploadedBefore := len(f.store.keys)

	photo := exiftest.JPEG(tz200Winter)
	_, inserted, err := f.reg.registerMedia(context.Background(), alice, bytes.NewReader(photo), int64(len(photo)), "P1033627.JPG", nil)

	require.ErrorContains(t, err, "load clock rules")
	assert.False(t, inserted)
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM media WHERE owner_id = $1`, alice).Scan(&count))
	assert.Equal(t, 1, count, "no row for the file")
	assert.Len(t, f.store.keys, uploadedBefore, "and no object")

	rec := do(t, f.router, alice, http.MethodGet, "/api/media/"+id+"/provenance/file", "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
