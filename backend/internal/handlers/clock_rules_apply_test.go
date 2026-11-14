package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clockRuleRows returns rules as the rows of loadClockRules' query.
func clockRuleRows(rules ingest.ClockRules) *mockRows {
	day := func(d *time.Time) any {
		if d == nil {
			return nil
		}
		return *d
	}
	rows := &mockRows{}
	for _, r := range rules {
		rows.rows = append(rows.rows, []any{r.ID, r.Make, r.Model, r.OffsetMin, day(r.StartsOn), day(r.EndsOn)})
	}
	return rows
}

// clockRulesQuery answers loadClockRules' query with rules, as a mockMatcherDB's queryFn.
func clockRulesQuery(rules ingest.ClockRules) func(context.Context, string, ...any) (pgx.Rows, error) {
	return func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
		if !strings.Contains(sql, "FROM camera_clock_rules") {
			panic("unexpected Query: " + sql)
		}
		return clockRuleRows(rules), nil
	}
}

// withClockRules gives a handler from newRereadHandler the owner's rules.
func withClockRules(h *ProvenanceHandler, rules ...ingest.ClockRule) *ProvenanceHandler {
	h.db.(*mockMatcherDB).queryFn = clockRulesQuery(rules)
	return h
}

// The real case: a DC-TZ200 shows UTC+2 all year, writes +01:00, and the Panasonic camera rule
// ignores that tag, so Warsaw's winter offset applied and its winter photos came in an hour late.
var (
	tz200Winter = exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:03:13 19:28:15", OffsetTimeOriginal: "+01:00"}
	tz200Summer = exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:07:14 12:00:00", OffsetTimeOriginal: "+01:00"}
)

// tz200Snapshot is tz200ClockRule as capture_evidence records it.
func tz200Snapshot() map[string]any {
	return map[string]any{
		"id": tz200ClockRule.ID, "make": "Panasonic", "model": "DC-TZ200", "offset_min": 120.0, "starts_on": "2018-07-01", "ends_on": nil,
	}
}

// registerWithRules runs content through registerMedia for owner-1, whose clock rules are rules, and
// returns the media INSERT's arguments and the database.
func registerWithRules(t *testing.T, zone ingest.CameraZone, rules ingest.ClockRules, content []byte, filename string) ([]any, *mockRegistrarDB) {
	t.Helper()
	db := &mockRegistrarDB{clockRules: rules}
	reg := &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: zone}
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, inserted, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(content), int64(len(content)), filename, &mtime)
	require.NoError(t, err)
	require.True(t, inserted)
	for i, sql := range db.sqls {
		if strings.Contains(sql, "INSERT INTO media") {
			return db.args[i], db
		}
	}
	require.Fail(t, "no media INSERT")
	return nil, nil
}

func takenAtUTC(t *testing.T, insert []any) string {
	t.Helper()
	at, ok := insert[insertArgTakenAt].(*time.Time)
	require.True(t, ok)
	require.NotNil(t, at)
	return at.UTC().Format(time.RFC3339)
}

func TestRegisterMedia_AppliesTheOwnersClockRules(t *testing.T) {
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicOffsetRule}
	otherModel := tz200ClockRule
	otherModel.Model = "DC-TZ100"

	tests := []struct {
		name      string
		photo     exiftest.Photo
		rules     ingest.ClockRules
		want      string
		wantZone  string
		wantRule  any
		wantTrack bool
	}{
		{"a winter photo read by the rule is wall minus 2 h", tz200Winter, ingest.ClockRules{tz200ClockRule},
			"2019-03-13T17:28:15Z", "clock_rule", tz200Snapshot(), false},
		{"without rules, the camera zone's winter offset", tz200Winter, nil,
			"2019-03-13T18:28:15Z", "camera", nil, false},
		{"a rule for another model does nothing", tz200Winter, ingest.ClockRules{otherModel},
			"2019-03-13T18:28:15Z", "camera", nil, false},
		{"a summer photo is the same instant with the rule", tz200Summer, ingest.ClockRules{tz200ClockRule},
			"2019-07-14T10:00:00Z", "clock_rule", tz200Snapshot(), false},
		{"and without it", tz200Summer, nil,
			"2019-07-14T10:00:00Z", "camera", nil, false},
		{"the EXIF track point is at the rule's instant",
			exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:03:13 19:28:15", GPS: &exiftest.GPS{Lat: 47.4979, Lng: 19.0402}},
			ingest.ClockRules{tz200ClockRule}, "2019-03-13T17:28:15Z", "clock_rule", tz200Snapshot(), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			insert, db := registerWithRules(t, zone, tt.rules, exiftest.JPEG(tt.photo), "P1033627.JPG")

			assert.Equal(t, tt.want, takenAtUTC(t, insert))
			got := decodeEvidence(t, insert)
			assert.Equal(t, tt.wantZone, got["zone"])
			assert.Equal(t, tt.wantRule, got["clock_rule"])
			assert.Equal(t, [][]any{{"owner-1"}}, db.ruleLoads, "the owner's rules, loaded once")

			var trackAt *time.Time
			for i, sql := range db.sqls {
				if strings.Contains(sql, "INSERT INTO gps_tracks") {
					at := db.args[i][1].(time.Time)
					trackAt = &at
				}
			}
			if !tt.wantTrack {
				assert.Nil(t, trackAt)
				return
			}
			require.NotNil(t, trackAt)
			assert.Equal(t, tt.want, trackAt.UTC().Format(time.RFC3339))
		})
	}

	t.Run("the registrar's own clock rules are never used", func(t *testing.T) {
		withClock := zone
		withClock.Clock = ingest.ClockRules{tz200ClockRule}
		insert, _ := registerWithRules(t, withClock, nil, exiftest.JPEG(tz200Winter), "P1033627.JPG")
		assert.Equal(t, "2019-03-13T18:28:15Z", takenAtUTC(t, insert))
	})
	t.Run("a duplicate still loads the rules, before its EXIF is decoded", func(t *testing.T) {
		photo := exiftest.JPEG(tz200Winter)
		db := &mockRegistrarDB{known: map[string]bool{partialHash(photo): true}}
		reg := &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: zone}

		_, inserted, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(photo), int64(len(photo)), "P1033627.JPG", nil)

		require.NoError(t, err)
		assert.False(t, inserted)
		assert.Len(t, db.ruleLoads, 1)
	})
}

// A Panasonic video dated from the EXIF in its PANA box gets the owner's rule for the model that EXIF
// names. A clock rule alone never dates a video from its EXIF: only the camera rule does.
func TestRegisterMedia_VideoGetsTheOwnersClockRule(t *testing.T) {
	warsaw := mustLoadLocation(t, "Europe/Warsaw")
	always := ingest.ClockRule{ID: "6f1c1d1e-0000-4000-8000-000000000003", Make: "panasonic", Model: "dc-tz200", OffsetMin: 120}
	clip := exiftest.PanasonicMP4(panasonicCamera) // 2001-02-03 04:05:06 on the camera's clock

	t.Run("with the rule", func(t *testing.T) {
		insert, _ := registerWithRules(t, ingest.CameraZone{Loc: warsaw, Set: true, Rules: panasonicVideoRules},
			ingest.ClockRules{always}, clip, "P1000001.MP4")

		assert.Equal(t, "2001-02-03T02:05:06Z", takenAtUTC(t, insert))
		assert.Equal(t, "DC-TZ200", *insert[insertArgCameraModel].(*string))
		got := decodeEvidence(t, insert)
		assert.Equal(t, "exif_original", got["src"])
		assert.Equal(t, "clock_rule", got["zone"])
		assert.Equal(t, 120.0, got["offset_used_min"])
		assert.Equal(t, map[string]any{
			"id": always.ID, "make": "panasonic", "model": "dc-tz200", "offset_min": 120.0, "starts_on": nil, "ends_on": nil,
		}, got["clock_rule"], "the rule as the owner wrote it")
	})
	t.Run("without it, the camera zone", func(t *testing.T) {
		insert, _ := registerWithRules(t, ingest.CameraZone{Loc: warsaw, Set: true, Rules: panasonicVideoRules}, nil, clip, "P1000001.MP4")

		assert.Equal(t, "2001-02-03T03:05:06Z", takenAtUTC(t, insert))
		assert.Equal(t, "camera", decodeEvidence(t, insert)["zone"])
	})
	t.Run("a clock rule does not date a video from its EXIF by itself", func(t *testing.T) {
		insert, _ := registerWithRules(t, ingest.CameraZone{Loc: warsaw, Set: true, Rules: panasonicOffsetRule},
			ingest.ClockRules{always}, clip, "P1000001.MP4")

		got := decodeEvidence(t, insert)
		assert.Equal(t, "mtime", got["src"], "this head holds no container time")
		assert.Nil(t, got["clock_rule"])
		assert.Nil(t, insert[insertArgCameraModel])
	})
}

// Read without its owner's rules, a file would be stored at a time its owner has said is wrong, and
// re-uploading it later would be skipped as a duplicate. So the file fails, and is never read.
func TestRegisterMedia_ClockRuleLoadFailureFailsTheFile(t *testing.T) {
	boom := errors.New("connection refused")
	photo := exiftest.JPEG(tz200Winter)

	t.Run("registerMedia", func(t *testing.T) {
		db := &mockRegistrarDB{clockRulesErr: boom}
		store := &mockUploadStorage{}
		reg := &MediaRegistrar{db: db, storage: store, cameraZone: utcZone}

		id, inserted, err := reg.registerMedia(context.Background(), testOwnerID, bytes.NewReader(photo), int64(len(photo)), "P1033627.JPG", nil)

		require.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "load clock rules")
		assert.Empty(t, id)
		assert.False(t, inserted)
		assert.Empty(t, db.sqls, "no media row is looked up or written")
		assert.Empty(t, store.keys, "nothing is stored")
		assert.Empty(t, exifDecodeSlots)
	})
	t.Run("an upload lists the file as failed, with a log line", func(t *testing.T) {
		logs := captureLogs(t)
		h := &MediaHandler{
			registrar:     &MediaRegistrar{db: &mockRegistrarDB{clockRulesErr: boom}, storage: &mockUploadStorage{}},
			runMatcher:    noopMatcher,
			runTripDetect: noopTripDetect,
		}
		w := httptest.NewRecorder()

		h.Upload(w, uploadFiles("a.jpg", "b.jpg"))

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var got struct {
			Uploaded, Skipped, Failed []string
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Empty(t, got.Uploaded)
		assert.Empty(t, got.Skipped)
		assert.Equal(t, []string{"a.jpg", "b.jpg"}, got.Failed)
		assert.Contains(t, logs.String(), "upload: register media failed")
		assert.Contains(t, logs.String(), "load clock rules: connection refused")
	})
	t.Run("a scan counts the file as failed", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "P1033627.JPG", string(photo))
		deps := IngestDeps{
			registrar:     &MediaRegistrar{db: &mockRegistrarDB{clockRulesErr: boom}, storage: &mockUploadStorage{}},
			runMatcher:    noopMatcher,
			runTripDetect: noopTripDetect,
		}

		res, err := RunIngestScan(context.Background(), deps, dir, testOwnerID)

		require.NoError(t, err)
		assert.Equal(t, ScanResult{Failed: 1}, res)
	})
}

// Every file of an upload or a scan is read with the rules of the owner it is ingested for.
func TestIngest_ClockRulesFollowTheOwner(t *testing.T) {
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicOffsetRule}
	photo := exiftest.JPEG(tz200Winter)
	// takenAt lists the times stored, by filename.
	takenAt := func(t *testing.T, db *mockRegistrarDB) map[string]*string {
		t.Helper()
		got := map[string]*string{}
		for i, sql := range db.sqls {
			if strings.Contains(sql, "INSERT INTO media") {
				got[db.args[i][1].(string)] = rfc3339UTC(db.args[i][insertArgTakenAt].(*time.Time))
			}
		}
		return got
	}

	t.Run("upload", func(t *testing.T) {
		db := &mockRegistrarDB{clockRules: ingest.ClockRules{tz200ClockRule}}
		h := &MediaHandler{
			registrar:     &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: zone},
			runMatcher:    noopMatcher,
			runTripDetect: noopTripDetect,
		}
		w := httptest.NewRecorder()

		h.Upload(w, uploadContents(t, map[string][]byte{"P1033627.JPG": photo, "other.jpg": []byte("no exif")}))

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, [][]any{{testOwnerID}, {testOwnerID}}, db.ruleLoads, "once per file, for the caller")
		assert.Equal(t, map[string]*string{"P1033627.JPG": ptr("2019-03-13T17:28:15Z"), "other.jpg": nil}, takenAt(t, db))
	})
	t.Run("scan", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "P1033627.JPG", string(photo))
		db := &mockRegistrarDB{clockRules: ingest.ClockRules{tz200ClockRule}}
		deps := IngestDeps{
			registrar:     &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: zone},
			runMatcher:    noopMatcher,
			runTripDetect: noopTripDetect,
		}

		res, err := RunIngestScan(context.Background(), deps, dir, testOwnerID)

		require.NoError(t, err)
		assert.Equal(t, ScanResult{Ingested: 1}, res)
		assert.Equal(t, [][]any{{testOwnerID}}, db.ruleLoads)
		assert.Equal(t, map[string]*string{"P1033627.JPG": ptr("2019-03-13T17:28:15Z")}, takenAt(t, db))
	})
}

// uploadContents is uploadFiles with chosen contents.
func uploadContents(t *testing.T, files map[string][]byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		fw, err := mw.CreateFormFile("files", name)
		require.NoError(t, err)
		_, err = fw.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	r := httptest.NewRequest(http.MethodPost, "/api/media/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return withTestUser(r)
}

// The re-read applies the owner's rules as they are today, as ingest applied them on the day.
func TestProvenanceFile_ClockRules(t *testing.T) {
	zone := ingest.CameraZone{Loc: time.FixedZone("Test/Zone", 3600), Set: true, Rules: panasonicOffsetRule}
	photo := exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: rereadWallNoon, OffsetTimeOriginal: "+01:00"}
	rule := ingest.ClockRule{ID: "6f1c1d1e-0000-4000-8000-000000000004", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120,
		StartsOn: ruleDay(2001, 1, 1), EndsOn: ruleDay(2001, 12, 31)}
	reread := func(t *testing.T, stored *time.Time, rules ...ingest.ClockRule) FileReread {
		t.Helper()
		store := &stubRereadStorage{head: exiftest.JPEG(photo)}
		return callFile(t, withClockRules(newRereadHandler(photoRow(stored), store, zone, probeMustNotRun(t)), rules...))
	}

	t.Run("an item ingested under the rule reads the same", func(t *testing.T) {
		got := reread(t, noonPlus(-2*time.Hour), rule)

		require.NotNil(t, got.RuleToday)
		assert.Equal(t, ptr(ingest.ZoneClockRule), got.RuleToday.ZoneRule)
		assert.Equal(t, ptr(120), got.RuleToday.OffsetMin)
		assert.Nil(t, got.RuleToday.ZoneName, "the zone name is the camera zone's only")
		assert.Equal(t, &evidenceClockRule{ID: rule.ID, Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120,
			StartsOn: ptr("2001-01-01"), EndsOn: ptr("2001-12-31")}, got.RuleToday.ClockRule)
		assert.True(t, rereadNoon.Add(-2*time.Hour).Equal(got.RuleToday.TakenAt))
		assert.Equal(t, rereadSame, got.Class)
	})
	t.Run("an item ingested before the rule is an hour late", func(t *testing.T) {
		got := reread(t, noonPlus(-time.Hour), rule)

		assert.Equal(t, rereadWholeOffset, got.Class)
		assert.Equal(t, ptr(-3600.0), got.DeltaS)
	})
	t.Run("once the rule is deleted, an item ingested under it is an hour early", func(t *testing.T) {
		got := reread(t, noonPlus(-2*time.Hour))

		require.NotNil(t, got.RuleToday)
		assert.Equal(t, ptr(ingest.ZoneCamera), got.RuleToday.ZoneRule)
		assert.Nil(t, got.RuleToday.ClockRule)
		assert.Equal(t, rereadWholeOffset, got.Class)
		assert.Equal(t, ptr(3600.0), got.DeltaS)
	})
	t.Run("the rule as JSON", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.JPEG(photo)}
		h := withClockRules(newRereadHandler(photoRow(noonPlus(-2*time.Hour)), store, zone, probeMustNotRun(t)), rule)
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			RuleToday map[string]any `json:"rule_today"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, map[string]any{
			"source": "exif_original", "zone_rule": "clock_rule", "offset_min": 120.0, "zone_name": nil,
			"clock_rule": map[string]any{"id": rule.ID, "make": "Panasonic", "model": "DC-TZ200", "offset_min": 120.0,
				"starts_on": "2001-01-01", "ends_on": "2001-12-31"},
			"taken_at": "2001-02-03T10:00:00Z",
		}, body.RuleToday)
	})
	t.Run("the rules are the owner's", func(t *testing.T) {
		var gotArgs [][]any
		h := newRereadHandler(photoRow(noonPlus(0)), &stubRereadStorage{head: exiftest.JPEG(photo)}, zone, probeMustNotRun(t))
		h.db.(*mockMatcherDB).queryFn = func(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
			gotArgs = append(gotArgs, args)
			return clockRulesQuery(nil)(ctx, sql, args...)
		}

		callFile(t, h)

		assert.Equal(t, [][]any{{testOwnerID}}, gotArgs)
	})
	t.Run("the server's own clock rules are never used", func(t *testing.T) {
		withClock := zone
		withClock.Clock = ingest.ClockRules{rule}
		store := &stubRereadStorage{head: exiftest.JPEG(photo)}
		got := callFile(t, newRereadHandler(photoRow(noonPlus(-time.Hour)), store, withClock, probeMustNotRun(t)))

		assert.Equal(t, ptr(ingest.ZoneCamera), got.RuleToday.ZoneRule)
		assert.Equal(t, rereadSame, got.Class)
	})
	t.Run("a video dated from its EXIF gets the rule too", func(t *testing.T) {
		videoZone := zone
		videoZone.Rules = panasonicVideoRules
		created := rereadNoon // the camera's clock, marked UTC
		store := &stubRereadStorage{head: exiftest.PanasonicMP4(photo)}
		h := withClockRules(newRereadHandler(videoRow(noonPlus(-2*time.Hour), recordedNoCopy), store, videoZone,
			probeReturns(ingest.VideoProbeResult{CreatedAt: &created, CodecName: "h264"})), rule)

		got := callFile(t, h)

		require.NotNil(t, got.RuleToday)
		assert.Equal(t, ptr(ingest.ZoneClockRule), got.RuleToday.ZoneRule)
		require.NotNil(t, got.RuleToday.ClockRule)
		assert.Equal(t, rule.ID, got.RuleToday.ClockRule.ID)
		assert.Equal(t, rereadSame, got.Class)
	})
}

func TestProvenanceFile_ClockRuleLoadFails(t *testing.T) {
	t.Run("a database error is a 500 with a log line, and the file is never read", func(t *testing.T) {
		logs := captureLogs(t)
		store := &stubRereadStorage{head: exiftest.JPEG(tz200Winter)}
		h := newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t))
		h.db.(*mockMatcherDB).queryFn = func(context.Context, string, ...any) (pgx.Rows, error) {
			return nil, errors.New("boom")
		}
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "boom", "details stay in the log")
		assert.Contains(t, logs.String(), "provenance file: load clock rules failed")
		assert.Contains(t, logs.String(), "boom")
		assert.Empty(t, store.headKeys)
	})
	t.Run("running out of time is the ordinary 504", func(t *testing.T) {
		h := newRereadHandler(photoRow(noonPlus(0)), &stubRereadStorage{}, utcZone, probeMustNotRun(t))
		h.fileTimeout = 10 * time.Millisecond
		h.db.(*mockMatcherDB).queryFn = func(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	})
}
