package ingest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectMediaType_Filename360_IsPhoto360(t *testing.T) {
	result := DetectMediaType(ExifData{}, "photo360.jpg", ".jpg")
	assert.Equal(t, "photo_360", result)
}

func TestDetectMediaType_StandardJpg_IsPhoto(t *testing.T) {
	result := DetectMediaType(ExifData{}, "image.jpg", ".jpg")
	assert.Equal(t, "photo", result)
}

func TestDetectMediaType_PanoMov_IsVideo360(t *testing.T) {
	result := DetectMediaType(ExifData{}, "pano.mov", ".mov")
	assert.Equal(t, "video_360", result)
}

func TestDetectMediaType_Mp4_IsVideo(t *testing.T) {
	result := DetectMediaType(ExifData{}, "clip.mp4", ".mp4")
	assert.Equal(t, "video", result)
}

func TestDetectMediaType_3gp_IsVideo(t *testing.T) {
	result := DetectMediaType(ExifData{}, "clip.3gp", ".3gp")
	assert.Equal(t, "video", result)
}

func TestDetectMediaType_NonSquareRatio_IsPhoto(t *testing.T) {
	w, h := 3000, 1600
	e := ExifData{WidthPx: &w, HeightPx: &h}

	result := DetectMediaType(e, "image.jpg", ".jpg")

	assert.Equal(t, "photo", result)
}

func TestDetectMediaType_2to1Ratio_IsPhoto360(t *testing.T) {
	w, h := 4000, 2000
	e := ExifData{WidthPx: &w, HeightPx: &h}

	result := DetectMediaType(e, "image.jpg", ".jpg")

	assert.Equal(t, "photo_360", result)
}
