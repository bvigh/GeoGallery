package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ruleDay(year int, month time.Month, day int) *time.Time {
	t := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return &t
}

// tz200ClockRule is the owner's rule for a DC-TZ200 left on summer time: UTC+2 from mid-2018 on.
var tz200ClockRule = ingest.ClockRule{
	ID: "6f1c1d1e-0000-4000-8000-000000000001", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120,
	StartsOn: ruleDay(2018, 7, 1),
}

func TestNewEvidenceClockRule(t *testing.T) {
	tests := []struct {
		name string
		rule *ingest.ClockRule
		want *evidenceClockRule
	}{
		{"no rule", nil, nil},
		{"both days",
			&ingest.ClockRule{ID: "r1", Make: "Arashi Vision", Model: "Insta360 OneRS", OffsetMin: 60, StartsOn: ruleDay(2022, 6, 1), EndsOn: ruleDay(2022, 12, 31)},
			&evidenceClockRule{ID: "r1", Make: "Arashi Vision", Model: "Insta360 OneRS", OffsetMin: 60, StartsOn: ptr("2022-06-01"), EndsOn: ptr("2022-12-31")}},
		{"open bounds are null",
			&ingest.ClockRule{ID: "r2", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120},
			&evidenceClockRule{ID: "r2", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120}},
		{"a negative offset",
			&ingest.ClockRule{ID: "r3", Make: "Canon", Model: "EOS R6", OffsetMin: -210, EndsOn: ruleDay(1999, 1, 9)},
			&evidenceClockRule{ID: "r3", Make: "Canon", Model: "EOS R6", OffsetMin: -210, EndsOn: ptr("1999-01-09")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, newEvidenceClockRule(tt.rule))
		})
	}
}

// The evidence of a file read with the owner's clock rules, through ExtractExif as registerMedia
// reads it. clock_rule is a snapshot of the rule exactly when the rule gave the offset, and
// retimed_from is null from ingest; both keys are always present.
func TestNewCaptureEvidence_ClockRule(t *testing.T) {
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicOffsetRule,
		Clock: ingest.ClockRules{tz200ClockRule}}
	mtime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	const mtimeUTC = "2020-01-01T00:00:00Z"
	tz200Snapshot := map[string]any{
		"id": tz200ClockRule.ID, "make": "Panasonic", "model": "DC-TZ200", "offset_min": 120.0, "starts_on": "2018-07-01", "ends_on": nil,
	}

	tests := []struct {
		name  string
		photo exiftest.Photo
		want  map[string]any
	}{
		{"a winter photo read by the rule",
			exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:01:15 12:00:00", OffsetTimeOriginal: "+01:00"},
			map[string]any{"src": "exif_original", "zone": "clock_rule", "wall": "2019:01:15 12:00:00", "offset_tag": "+01:00",
				"offset_ignored": true, "offset_used_min": 120.0, "clock_rule": tz200Snapshot, "make": "Panasonic", "model": "DC-TZ200",
				"file_mtime": mtimeUTC}},
		{"before the rule's first day, the camera zone and no rule",
			exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2018:06:30 12:00:00"},
			map[string]any{"src": "exif_original", "zone": "camera", "wall": "2018:06:30 12:00:00", "offset_used_min": 120.0,
				"make": "Panasonic", "model": "DC-TZ200", "file_mtime": mtimeUTC}},
		{"the GPS clock wins, and no rule is named",
			exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:01:15 12:00:00",
				GPS: &exiftest.GPS{Lat: 10, Lng: 20, Time: time.Date(2019, 1, 15, 11, 0, 0, 0, time.UTC)}},
			map[string]any{"src": "exif_original", "zone": "gps", "wall": "2019:01:15 12:00:00", "gps_utc": "2019-01-15T11:00:00Z",
				"wall_minus_gps_s": 3600.0, "offset_used_min": 60.0, "make": "Panasonic", "model": "DC-TZ200", "file_mtime": mtimeUTC}},
		{"another model of the make",
			exiftest.Photo{Make: "Panasonic", Model: "DC-TZ100", DateTimeOriginal: "2019:01:15 12:00:00"},
			map[string]any{"src": "exif_original", "zone": "camera", "wall": "2019:01:15 12:00:00", "offset_used_min": 60.0,
				"make": "Panasonic", "model": "DC-TZ100", "file_mtime": mtimeUTC}},
		{"no time from the file", exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200"},
			map[string]any{"src": "mtime", "make": "Panasonic", "model": "DC-TZ200", "file_mtime": mtimeUTC}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := ingest.ExtractExif(bytes.NewReader(exiftest.JPEG(tt.photo)), zone)
			raw := newCaptureEvidence(captureReading{exif: e, mtime: &mtime}, zone).encode()
			var got map[string]any
			require.NoError(t, json.Unmarshal(raw, &got))
			assert.Equal(t, evidenceWith(tt.want), got)
		})
	}

	t.Run("a rule is named only with its zone", func(t *testing.T) {
		at := time.Date(2019, 1, 15, 12, 0, 0, 0, time.FixedZone("", 3600))
		rule := tz200ClockRule
		ev := newCaptureEvidence(captureReading{exif: ingest.ExifData{TakenAt: &at, TakenAtZone: ingest.ZoneCamera, ClockRule: &rule}}, zone)
		assert.Nil(t, ev.ClockRule)
	})

	t.Run("from ingest, retimed_from is null", func(t *testing.T) {
		e := ingest.ExtractExif(bytes.NewReader(exiftest.JPEG(exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:01:15 12:00:00"})), zone)
		ev := newCaptureEvidence(captureReading{exif: e}, zone)
		assert.Equal(t, "ingest", ev.By)
		assert.Nil(t, ev.RetimedFrom)
		var got map[string]any
		require.NoError(t, json.Unmarshal(ev.encode(), &got))
		assert.Contains(t, got, "retimed_from")
		assert.Nil(t, got["retimed_from"])
	})
}

// A Panasonic video dated from the EXIF in its PANA box gets the owner's rule for the model that EXIF
// names, and the evidence records the rule while keeping the container time as the input that lost.
func TestNewCaptureEvidence_VideoClockRule(t *testing.T) {
	always := ingest.ClockRule{ID: "6f1c1d1e-0000-4000-8000-000000000002", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120}
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicVideoRules,
		Clock: ingest.ClockRules{always}}
	container := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	probe := ingest.VideoProbeResult{CreatedAt: &container, CodecName: "h264"}

	e, ok := ingest.VideoExif(exiftest.PanasonicMP4(panasonicCamera), zone)
	require.True(t, ok)
	require.NotNil(t, e.TakenAt)
	assert.Equal(t, "2001-02-03T02:05:06Z", e.TakenAt.UTC().Format(time.RFC3339), "04:05:06 at the rule's +02:00, not Warsaw's winter +01:00")

	var got map[string]any
	require.NoError(t, json.Unmarshal(newCaptureEvidence(captureReading{exif: e, probe: probe}, zone).encode(), &got))
	assert.Equal(t, evidenceWith(map[string]any{
		"src": "exif_original", "zone": "clock_rule", "wall": "2001:02:03 04:05:06", "offset_tag": "+01:00",
		"offset_ignored": true, "offset_used_min": 120.0, "make": "Panasonic", "model": "DC-TZ200",
		"clock_rule": map[string]any{
			"id": always.ID, "make": "Panasonic", "model": "DC-TZ200", "offset_min": 120.0, "starts_on": nil, "ends_on": nil,
		},
		"creation_time": "2001-02-03T04:05:06Z", "codec": "h264",
	}), got)
}

// A time JSON cannot carry is not stored, so no source, zone or rule produced taken_at.
func TestCaptureEvidence_TimeDroppedClearsClockRule(t *testing.T) {
	zone := ingest.CameraZone{Loc: time.UTC, Set: true, Clock: ingest.ClockRules{tz200ClockRule}}
	e := ingest.ExtractExif(bytes.NewReader(exiftest.JPEG(exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "9999:06:01 12:00:00"})), zone)
	require.Equal(t, ingest.ZoneClockRule, e.TakenAtZone)

	ev := newCaptureEvidence(captureReading{exif: e}, zone)
	require.NotNil(t, ev.ClockRule)
	dropped := ev.timeDropped()

	assert.Equal(t, captureSrcNone, dropped.Src)
	assert.Nil(t, dropped.Zone)
	assert.Nil(t, dropped.OffsetUsedMin)
	assert.Nil(t, dropped.ClockRule)
	assert.Equal(t, "9999:06:01 12:00:00", *dropped.Wall, "the input stays")
	assert.NotNil(t, ev.ClockRule, "the original is a value and keeps its rule")
}

// fakeClockRuleQuerier fails the query.
type fakeClockRuleQuerier struct {
	err error
}

func (f fakeClockRuleQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, f.err
}

func TestLoadClockRules_QueryError(t *testing.T) {
	boom := errors.New("connection refused")
	rules, err := loadClockRules(context.Background(), fakeClockRuleQuerier{err: boom}, "owner-1")
	assert.ErrorIs(t, err, boom)
	assert.Nil(t, rules)
}
