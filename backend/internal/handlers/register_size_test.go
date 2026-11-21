package handlers

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registerKept runs content through registerMedia and returns the media INSERT's arguments with
// the storage that kept the uploaded object.
func registerKept(t *testing.T, content []byte, filename string) ([]any, *keepingUploadStorage) {
	t.Helper()
	db := &mockRegistrarDB{}
	store := &keepingUploadStorage{}
	reg := &MediaRegistrar{db: db, storage: store, cameraZone: ingest.CameraZone{Loc: time.UTC, Set: true}}
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(content), int64(len(content)), filename, &mtime)
	require.NoError(t, err)
	require.True(t, res.inserted)
	for i, sql := range db.sqls {
		if strings.Contains(sql, "INSERT INTO media") {
			require.Contains(t, sql, "size_bytes")
			return db.args[i], store
		}
	}
	require.Fail(t, "no media INSERT")
	return nil, nil
}

// size_bytes is the size of the object ingest uploaded, which is the upload's declared length: the
// file's own, or for a video re-encoded at upload, the transcode's.
func TestRegisterMedia_SizeBytes(t *testing.T) {
	t.Run("a photo is stored as uploaded", func(t *testing.T) {
		content := exiftest.JPEG(exiftest.Photo{Make: "Canon", Model: "EOS R5"})
		insert, store := registerKept(t, content, "photo.jpg")
		assert.Equal(t, int64(len(content)), insert[insertArgSize])
		assert.Equal(t, int64(len(content)), store.size)
		assert.Len(t, store.body, len(content))
	})
	t.Run("a video kept as uploaded", func(t *testing.T) {
		content := writeClip(t, "libx264")
		insert, store := registerKept(t, content, "clip.mp4")
		assert.Equal(t, int64(len(content)), insert[insertArgSize])
		assert.Equal(t, int64(len(content)), store.size)
	})
	t.Run("a video re-encoded at upload counts the transcode", func(t *testing.T) {
		content := writeClip(t, "mpeg4")
		insert, store := registerKept(t, content, "clip.mp4")
		require.NotEmpty(t, store.body)
		assert.Equal(t, int64(len(store.body)), insert[insertArgSize])
		assert.Equal(t, int64(len(store.body)), store.size, "the upload's length")
		assert.NotEqual(t, int64(len(content)), insert[insertArgSize], "not the original's size")
	})
}
