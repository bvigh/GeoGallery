package handlers

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
)

// panasonicVideoRules is the shipped Panasonic rule (backend/camera-rules.json).
var panasonicVideoRules = ingest.CameraRules{{Make: "Panasonic", IgnoreOffsetTag: true, VideoTimeFromExif: true, Why: "test"}}

// panasonicCamera is what a Panasonic camera writes inside its MP4: the same wall clock as writeClip's
// container time, 2001-02-03T04:05:06Z, which a Panasonic marks UTC although it is the camera's clock.
var panasonicCamera = exiftest.Photo{
	Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2001:02:03 04:05:06", OffsetTimeOriginal: "+01:00",
}

// panasonicClip is a real clip from ffmpeg with the camera's PANA box added.
func panasonicClip(t *testing.T, codec string) []byte {
	t.Helper()
	clip, ok := exiftest.WithPANA(writeClip(t, codec), panasonicCamera)
	require.True(t, ok, "ffmpeg wrote moov last")
	return clip
}

// A video whose make's rule says so is dated from the EXIF inside it, read like a photo's (February
// in Warsaw is +01:00), and not from its container time. Real ffmpeg and ffprobe.
func TestRegisterMedia_VideoDatedFromItsExif(t *testing.T) {
	warsaw := mustLoadLocation(t, "Europe/Warsaw")
	withRule := ingest.CameraZone{Loc: warsaw, Set: true, Rules: panasonicVideoRules}
	withoutRule := ingest.CameraZone{Loc: warsaw, Set: true}
	clip := panasonicClip(t, "libx264")

	t.Run("with the rule", func(t *testing.T) {
		insert := registerFile(t, withRule, clip, "clip.mp4", nil)

		require.NotNil(t, insert[insertArgTakenAt])
		assert.Equal(t, "2001-02-03T03:05:06Z", insert[insertArgTakenAt].(*time.Time).UTC().Format(time.RFC3339))
		assert.Equal(t, "DC-TZ200", *insert[insertArgCameraModel].(*string), "the model comes from the EXIF")
		assert.Equal(t, 32, *insert[insertArgWidth].(*int), "the size still comes from ffprobe")

		got := decodeEvidence(t, insert)
		assert.Equal(t, "exif_original", got["src"])
		assert.Equal(t, "camera", got["zone"])
		assert.Equal(t, "2001:02:03 04:05:06", got["wall"])
		assert.Equal(t, "+01:00", got["offset_tag"])
		assert.Equal(t, true, got["offset_ignored"])
		assert.Equal(t, 60.0, got["offset_used_min"])
		assert.Equal(t, "Panasonic", got["make"])
		assert.Equal(t, "DC-TZ200", got["model"])
		assert.Equal(t, "2001-02-03T04:05:06Z", got["creation_time"], "the container time is kept as the input that lost")
		assert.Equal(t, "h264", got["codec"])
	})

	t.Run("without it, the container time", func(t *testing.T) {
		insert := registerFile(t, withoutRule, clip, "clip.mp4", nil)

		assert.Equal(t, "2001-02-03T04:05:06Z", insert[insertArgTakenAt].(*time.Time).UTC().Format(time.RFC3339))
		assert.Nil(t, insert[insertArgCameraModel])
		assert.Equal(t, "container", decodeEvidence(t, insert)["src"])
	})

	t.Run("the dedup key does not depend on the rules", func(t *testing.T) {
		with := registerFile(t, withRule, clip, "clip.mp4", nil)
		without := registerFile(t, withoutRule, clip, "clip.mp4", nil)

		assert.Equal(t, without[insertArgFileHash], with[insertArgFileHash])
		assert.Equal(t, partialHash(clip[:min(len(clip), mediaHeadSize)]), with[insertArgFileHash], "a probed video's hash")
	})
}

// A transcode stores the time ingest read from the EXIF as its container time, since the EXIF does
// not survive it.
func TestRegisterMedia_TranscodedVideoKeepsItsExifTime(t *testing.T) {
	zone := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true, Rules: panasonicVideoRules}
	clip := panasonicClip(t, "mpeg4")
	store := &keepingUploadStorage{}
	reg := &MediaRegistrar{db: &mockRegistrarDB{}, storage: store, cameraZone: zone}

	_, inserted, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(clip), int64(len(clip)), "clip.mp4", nil)
	require.NoError(t, err)
	require.True(t, inserted)

	tmp, stored, err := ingest.ProbeVideo(bytes.NewReader(store.body))
	require.NoError(t, err)
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	assert.Equal(t, "h264", stored.CodecName, "the object is the transcode")
	require.NotNil(t, stored.CreatedAt)
	assert.Equal(t, "2001-02-03T03:05:06Z", stored.CreatedAt.UTC().Format(time.RFC3339))
	_, kept := ingest.VideoExif(store.body, zone)
	assert.False(t, kept, "the transcode holds no PANA box")
}

// The re-read applies the same rule as ingest, so a video dated from its EXIF reads as unchanged.
func TestProvenanceFile_VideoDatedFromItsExif(t *testing.T) {
	zone := ingest.CameraZone{Loc: time.FixedZone("Test/Zone", 3600), Set: true, Rules: panasonicVideoRules}
	camera := panasonicCamera
	camera.DateTimeOriginal = rereadWallNoon
	// The container holds the camera's clock, marked UTC.
	probe := ingest.VideoProbeResult{CreatedAt: &rereadNoon, CodecName: "h264", Encoder: "Panasonic"}

	t.Run("dated as ingest does", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.PanasonicMP4(camera)}
		got := callFile(t, newRereadHandler(videoRow(noonPlus(-time.Hour), recordedNoCopy), store, zone, probeReturns(probe)))

		require.NotNil(t, got.RuleToday)
		assert.Equal(t, "exif_original", got.RuleToday.Source)
		assert.Equal(t, ptr("camera"), got.RuleToday.ZoneRule)
		assert.Equal(t, ptr(60), got.RuleToday.OffsetMin)
		assert.Equal(t, ptr("Test/Zone"), got.RuleToday.ZoneName)
		assert.True(t, rereadNoon.Add(-time.Hour).Equal(got.RuleToday.TakenAt))
		assert.Equal(t, rereadSame, got.Class)

		assert.Equal(t, ptr(rereadWallNoon), got.File.WallClock)
		assert.Equal(t, ptr("+01:00"), got.File.OffsetTag)
		assert.True(t, got.File.OffsetIgnored)
		assert.Equal(t, ptr("Panasonic"), got.File.Make)
		assert.Equal(t, ptr("DC-TZ200"), got.File.Model)
		require.NotNil(t, got.File.Video)
		assert.True(t, rereadNoon.Equal(*got.File.Video.CreationTime), "the container time is still shown")
		assert.Equal(t, []string{checkOffsetIgnored}, checkIDs(got), "not the end-of-clip remark of a container time")
	})

	t.Run("a row stored from the container time differs by the camera's offset", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.PanasonicMP4(camera)}
		got := callFile(t, newRereadHandler(videoRow(noonPlus(0), ""), store, zone, probeReturns(probe)))

		assert.Equal(t, rereadWholeOffset, got.Class)
		assert.Equal(t, ptr(-3600.0), got.DeltaS)
	})

	t.Run("a video without the EXIF falls back to its container", func(t *testing.T) {
		store := &stubRereadStorage{head: []byte("not an MP4 with a PANA box")}
		got := callFile(t, newRereadHandler(videoRow(noonPlus(0), ""), store, zone, probeReturns(probe)))

		require.NotNil(t, got.RuleToday)
		assert.Equal(t, captureSrcContainer, got.RuleToday.Source)
		assert.Equal(t, rereadSame, got.Class)
		assert.Nil(t, got.File.Make)
	})
}
