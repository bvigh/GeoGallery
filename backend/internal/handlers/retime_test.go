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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSameWallClock(t *testing.T) {
	warsaw := mustLoadLocation(t, "Europe/Warsaw")
	// The EXIF wall clock 2018:10:23 11:50:53, as ingest reads it today.
	now := time.Date(2018, 10, 23, 11, 50, 53, 0, warsaw)
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339Nano, s)
		require.NoError(t, err)
		return v
	}
	tests := []struct {
		name   string
		stored time.Time
		want   bool
	}{
		{"read as UTC (Docker)", at("2018-10-23T11:50:53Z"), true},
		{"read as Warsaw", at("2018-10-23T09:50:53Z"), true},
		{"read as +05:45", at("2018-10-23T06:05:53Z"), true},
		{"read as -12:00", at("2018-10-23T23:50:53Z"), true},
		{"14 hours east", at("2018-10-22T21:50:53Z"), true},
		{"14 hours east and 5 seconds", at("2018-10-22T21:50:48Z"), true},
		{"14 hours east and 6 seconds", at("2018-10-22T21:50:47Z"), false},
		{"more than 14 hours away", at("2018-10-22T20:50:53Z"), false},
		{"a Panasonic container time: the wall clock as UTC, a second later", at("2018-10-23T11:50:54Z"), true},
		{"a second early, read as Warsaw", at("2018-10-23T09:50:52Z"), true},
		{"5 seconds off", at("2018-10-23T11:50:58Z"), true},
		{"6 seconds off", at("2018-10-23T11:50:59Z"), false},
		{"a quarter hour and 6 seconds off", at("2018-10-23T11:35:47Z"), false},
		{"a file time with milliseconds, within the tolerance", at("2018-10-23T11:50:53.123Z"), true},
		{"seven minutes off: edited by hand", at("2018-10-23T11:57:53Z"), false},
		{"a different day", at("2018-10-24T11:50:53Z").Add(15 * time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sameWallClock(tt.stored, now))
		})
	}
}

func TestWallClock(t *testing.T) {
	plus2 := time.Date(2019, 3, 13, 19, 28, 15, 0, time.FixedZone("", 2*3600))
	assert.Equal(t, time.Date(2019, 3, 13, 19, 28, 15, 0, time.UTC), wallClock(plus2))
	minus330 := time.Date(2019, 3, 13, 1, 2, 3, 4, time.FixedZone("", -(3*3600+30*60)))
	assert.Equal(t, time.Date(2019, 3, 13, 1, 2, 3, 4, time.UTC), wallClock(minus330))
}

// retimeTestZone reads a TZ200 with the owner's summer-time rule, in Warsaw, with the shipped
// Panasonic camera rule.
func retimeTestZone(t *testing.T) ingest.CameraZone {
	return ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicVideoRules,
		Clock: ingest.ClockRules{tz200ClockRule}}
}

// The evidence a retimed row gets: the EXIF part from the re-read, the rest as ingest recorded it.
func TestRetimedEvidence(t *testing.T) {
	zone := retimeTestZone(t)
	winter := ingest.ExtractExif(bytes.NewReader(exiftest.JPEG(exiftest.Photo{
		Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:03:13 19:28:15", OffsetTimeOriginal: "+01:00",
	})), zone)
	require.Equal(t, ingest.ZoneClockRule, winter.TakenAtZone)
	oldTakenAt := time.Date(2019, 3, 13, 18, 28, 15, 0, time.UTC)
	tz200Snapshot := map[string]any{
		"id": tz200ClockRule.ID, "make": "Panasonic", "model": "DC-TZ200", "offset_min": 120.0, "starts_on": "2018-07-01", "ends_on": nil,
	}
	reread := map[string]any{
		"by": "retime", "retimed_from": "2019-03-13T18:28:15Z", "src": "exif_original", "zone": "clock_rule",
		"wall": "2019:03:13 19:28:15", "offset_tag": "+01:00", "offset_ignored": true, "offset_used_min": 120.0,
		"clock_rule": tz200Snapshot, "make": "Panasonic", "model": "DC-TZ200",
	}
	with := func(base, overrides map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range overrides {
			out[k] = v
		}
		return out
	}

	// What ingest recorded for the same file before the rule existed, read in Warsaw's winter time.
	mtime := time.Date(2019, 3, 14, 7, 0, 0, 0, time.UTC)
	noRule := zone
	noRule.Clock = nil
	ingested := newCaptureEvidence(ingest.ExtractExif(bytes.NewReader(exiftest.JPEG(exiftest.Photo{
		Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:03:13 19:28:15", OffsetTimeOriginal: "+01:00",
	})), noRule), ingest.VideoProbeResult{}, &mtime, noRule).encode()

	// A video ingest dated from its container, with every container input recorded.
	created := time.Date(2019, 3, 13, 19, 28, 16, 0, time.UTC)
	duration := 12.5
	container := newCaptureEvidence(ingest.ExifData{}, ingest.VideoProbeResult{
		CreatedAt: &created, DurationS: &duration, Encoder: "Panasonic", CodecName: "h264",
	}, nil, ingest.CameraZone{Loc: time.UTC}).encode()

	tests := []struct {
		name string
		old  []byte
		want map[string]any
	}{
		{"no evidence: a whole object, the container and file inputs null", nil, evidenceWith(reread)},
		{"JSON null counts as none", []byte("null"), evidenceWith(reread)},
		{"an array counts as none", []byte("[1,2]"), evidenceWith(reread)},
		{"a string counts as none", []byte(`"x"`), evidenceWith(reread)},
		{"ingest's evidence: the EXIF part replaced, the file time kept", ingested,
			evidenceWith(with(reread, map[string]any{"file_mtime": "2019-03-14T07:00:00Z"}))},
		{"a container-dated video: the container inputs kept as the ones that lost", container,
			evidenceWith(with(reread, map[string]any{
				"creation_time": "2019-03-13T19:28:16Z", "duration_s": 12.5, "encoder": "Panasonic", "codec": "h264",
			}))},
		{"a key this code does not know is kept, and the version", []byte(`{"v": 1, "by": "ingest", "src": "mtime", "later_key": [1], "transcoded": false, "file_mtime": "2020-01-01T00:00:00Z"}`),
			with(evidenceWith(with(reread, map[string]any{"file_mtime": "2020-01-01T00:00:00Z"})), map[string]any{"later_key": []any{1.0}})},
		{"an older object without every key gets them", []byte(`{"v": 1, "by": "ingest", "src": "exif_original"}`), evidenceWith(reread)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := retimedEvidence(tt.old, winter, zone, oldTakenAt)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(raw, &got))
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("a rule from an earlier retime goes when the re-read uses none", func(t *testing.T) {
		summer := ingest.ExtractExif(bytes.NewReader(exiftest.JPEG(exiftest.Photo{
			Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2017:12:01 10:00:00",
		})), zone)
		require.Equal(t, ingest.ZoneCamera, summer.TakenAtZone, "before the rule's first day")
		old, err := retimedEvidence(nil, winter, zone, oldTakenAt)
		require.NoError(t, err)

		raw, err := retimedEvidence(old, summer, zone, time.Date(2017, 12, 1, 8, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Nil(t, got["clock_rule"])
		assert.Equal(t, "camera", got["zone"])
		assert.Equal(t, "2017-12-01T08:00:00Z", got["retimed_from"], "the time this retime moved")
	})
}

// retimeReadStore serves object heads from memory and records the keys read.
type retimeReadStore struct {
	objects map[string][]byte
	err     error
	reads   []string
}

func (s *retimeReadStore) ReadHead(_ context.Context, key string, n int64) ([]byte, error) {
	s.reads = append(s.reads, key)
	if s.err != nil {
		return nil, s.err
	}
	b := s.objects[key]
	return b[:min(int64(len(b)), n)], nil
}

func TestRereadExifTime(t *testing.T) {
	zone := retimeTestZone(t)
	noVideoRule := zone
	noVideoRule.Rules = panasonicOffsetRule
	photo := exiftest.JPEG(exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:03:13 19:28:15"})
	video := exiftest.PanasonicMP4(exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2019:03:13 19:28:15", OffsetTimeOriginal: "+01:00"})
	store := &retimeReadStore{objects: map[string][]byte{
		"photo": photo, "video": video, "bare": []byte("\x00\x00\x00\x18ftypmp42"), "farFuture": exiftest.JPEG(exiftest.Photo{DateTimeOriginal: "9999:06:01 12:00:00"}),
	}}
	ctx := context.Background()

	tests := []struct {
		name      string
		item      retimeCandidate
		zone      ingest.CameraZone
		wantFound bool
		wantAt    string
		wantZone  string
		wantRead  bool
	}{
		{"a photo, by the owner's rule", retimeCandidate{storageKey: "photo", mediaType: "photo"}, zone, true, "2019-03-13T17:28:15Z", ingest.ZoneClockRule, true},
		{"a 360° photo is a photo", retimeCandidate{storageKey: "photo", mediaType: "photo_360"}, zone, true, "2019-03-13T17:28:15Z", ingest.ZoneClockRule, true},
		{"a Panasonic video from its PANA EXIF, by the rule", retimeCandidate{storageKey: "video", mediaType: "video"}, zone, true, "2019-03-13T17:28:15Z", ingest.ZoneClockRule, true},
		{"a 360° video too", retimeCandidate{storageKey: "video", mediaType: "video_360"}, zone, true, "2019-03-13T17:28:15Z", ingest.ZoneClockRule, true},
		{"a video without EXIF", retimeCandidate{storageKey: "bare", mediaType: "video"}, zone, false, "", "", true},
		{"no rule dates videos from EXIF: nothing read, the clock rule notwithstanding", retimeCandidate{storageKey: "video", mediaType: "video"}, noVideoRule, false, "", "", false},
		{"a year JSON cannot carry is no time", retimeCandidate{storageKey: "farFuture", mediaType: "photo"}, zone, false, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store.reads = nil
			e, found, err := rereadExifTime(ctx, store, tt.item, tt.zone)
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
			if tt.wantRead {
				assert.Equal(t, []string{tt.item.storageKey}, store.reads)
			} else {
				assert.Empty(t, store.reads)
			}
			if !tt.wantFound {
				return
			}
			assert.Equal(t, tt.wantAt, e.TakenAt.UTC().Format(time.RFC3339))
			assert.Equal(t, tt.wantZone, e.TakenAtZone)
			require.NotNil(t, e.ClockRule)
			assert.Equal(t, tz200ClockRule.ID, e.ClockRule.ID)
			assert.Equal(t, "DC-TZ200", e.CameraModel)
			assert.Nil(t, e.Lat)
		})
	}

	t.Run("a read error is returned", func(t *testing.T) {
		boom := errors.New("storage unavailable")
		_, found, err := rereadExifTime(ctx, &retimeReadStore{err: boom}, retimeCandidate{storageKey: "photo", mediaType: "photo"}, zone)
		assert.ErrorIs(t, err, boom)
		assert.False(t, found)
	})

	t.Run("a cancelled wait for a decode slot is returned", func(t *testing.T) {
		for range cap(exifDecodeSlots) {
			exifDecodeSlots <- struct{}{}
		}
		defer func() {
			for range cap(exifDecodeSlots) {
				<-exifDecodeSlots
			}
		}()
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, found, err := rereadExifTime(cancelled, store, retimeCandidate{storageKey: "photo", mediaType: "photo"}, zone)
		assert.ErrorIs(t, err, context.Canceled)
		assert.False(t, found)
	})
}
