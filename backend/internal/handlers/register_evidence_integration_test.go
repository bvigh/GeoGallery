package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterMedia_Integration_StoresEvidence goes through real PostgreSQL, which is what refuses
// invalid UTF-8 in TEXT, a NUL in jsonb and an out-of-range INT. A mock accepts all three.
func TestRegisterMedia_Integration_StoresEvidence(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeObjectStore{}
	reg := &MediaRegistrar{db: pool, storage: store, cameraZone: ingest.CameraZone{Loc: time.UTC, Set: true}}
	owner := createUser(t, pool, "alice")
	mtime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	farFuture := time.UnixMilli(math.MaxInt64)
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	const wall = "2001:02:03 04:05:06"

	tests := []struct {
		name         string
		photo        exiftest.Photo
		mtime        *time.Time
		wantModel    *string
		wantWidth    *int
		wantHeight   *int
		wantEvidence map[string]any
	}{
		{"EXIF columns and evidence",
			exiftest.Photo{Make: "Acme", Model: "One", DateTimeOriginal: wall, PixelX: 4000, PixelY: 3000}, &mtime,
			str("One"), num(4000), num(3000),
			map[string]any{"src": "exif_original", "wall": wall, "make": "Acme", "model": "One", "file_mtime": "2001-02-03T04:05:06Z"}},
		{"nothing in the file leaves the columns NULL",
			exiftest.Photo{}, &mtime,
			nil, nil, nil,
			map[string]any{"src": "mtime", "wall": nil, "make": nil, "model": nil}},
		{"invalid UTF-8 in every text tag",
			exiftest.Photo{Make: "Ac\xffme", Model: "O\xfe\xffne", DateTimeOriginal: "2001:02:03 \xff04:05:06", OffsetTimeOriginal: "+01\xff:00"}, &mtime,
			str("One"), nil, nil,
			map[string]any{"src": "mtime", "wall": wall, "make": "Acme", "model": "One", "offset_tag": "+01:00"}},
		// imagemeta drops the NUL, so the wall clock parses; it must never reach jsonb either way.
		{"a NUL inside the wall clock",
			exiftest.Photo{DateTimeOriginal: "2001:02\x00:03 04:05:06"}, nil,
			nil, nil, nil,
			map[string]any{"src": "exif_original", "wall": wall}},
		{"a pixel size INT cannot hold",
			exiftest.Photo{DateTimeOriginal: wall, PixelX: math.MaxUint32, PixelY: 3000}, &mtime,
			nil, nil, num(3000),
			map[string]any{"src": "exif_original"}},
		{"a file time no timestamp can hold, when EXIF won",
			exiftest.Photo{DateTimeOriginal: wall}, &farFuture,
			nil, nil, nil,
			map[string]any{"src": "exif_original", "file_mtime": nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := exiftest.JPEG(tt.photo)
			name := strings.ReplaceAll(tt.name, " ", "-") + ".jpg"
			id, inserted, err := reg.registerMedia(context.Background(), owner, bytes.NewReader(content), int64(len(content)), name, tt.mtime)
			require.NoError(t, err, "a debug field must never fail an upload")
			require.True(t, inserted)

			var model *string
			var width, height *int
			var duration *float64
			var evidence []byte
			require.NoError(t, pool.QueryRow(context.Background(), `
				SELECT camera_model, width_px, height_px, duration_sec, capture_evidence
				FROM media WHERE id = $1`, id).Scan(&model, &width, &height, &duration, &evidence))
			assert.Equal(t, tt.wantModel, model)
			assert.Equal(t, tt.wantWidth, width)
			assert.Equal(t, tt.wantHeight, height)
			assert.Nil(t, duration)

			var got map[string]any
			require.NoError(t, json.Unmarshal(evidence, &got))
			assert.Len(t, got, 21, "every key is always present")
			for key, want := range tt.wantEvidence {
				assert.Equal(t, want, got[key], key)
			}
		})
	}
}

func TestRegisterMedia_Integration_VideoColumns(t *testing.T) {
	content := writeClip(t, "libx264")
	pool := testdb.New(t)
	reg := &MediaRegistrar{db: pool, storage: &fakeObjectStore{}}
	owner := createUser(t, pool, "alice")

	id, inserted, err := reg.registerMedia(context.Background(), owner, bytes.NewReader(content), int64(len(content)), "clip.mp4", nil)
	require.NoError(t, err)
	require.True(t, inserted)

	var width, height int
	var duration float64
	var src, encoder string
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT width_px, height_px, duration_sec, capture_evidence->>'src', capture_evidence->>'encoder'
		FROM media WHERE id = $1`, id).Scan(&width, &height, &duration, &src, &encoder))
	assert.Equal(t, 32, width)
	assert.Equal(t, 16, height)
	assert.InDelta(t, 1.0, duration, 0.2)
	assert.Equal(t, "container", src)
	assert.Contains(t, encoder, "Lavf")
}
