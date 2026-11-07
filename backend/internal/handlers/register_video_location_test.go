package handlers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterMedia_VideoLocationTag runs real ffprobe over a real container: the tag becomes the
// item's own location, labelled as coming from the file, and never a track point.
func TestRegisterMedia_VideoLocationTag(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		location   string
		wantLat    float64
		wantLng    float64
		wantSource bool
	}{
		{"a location tag", "+51.5074-000.1278/", 51.5074, -0.1278, true},
		{"a phone without a fix writes zeros", "+00.0000+000.0000/", 0, 0, false},
		{"no tag", "", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clip.mp4")
			args := []string{"-y", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.1:r=10", "-c:v", "libx264",
				"-metadata", "creation_time=2025-07-14T14:30:12Z"}
			if tt.location != "" {
				args = append(args, "-metadata", "location="+tt.location)
			}
			out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput()
			require.NoError(t, err, string(out))
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()
			info, err := f.Stat()
			require.NoError(t, err)

			db := &mockRegistrarDB{}
			reg := &MediaRegistrar{db: db, storage: &mockUploadStorage{}}
			_, inserted, err := reg.registerMedia(context.Background(), "owner-1", f, info.Size(), "clip.mp4", &mtime)
			require.NoError(t, err)
			require.True(t, inserted)

			var insert []any
			for i, sql := range db.sqls {
				assert.NotContains(t, sql, "INSERT INTO gps_tracks", "a video's tag makes no track point")
				if strings.Contains(sql, "INSERT INTO media") {
					insert = db.args[i]
				}
			}
			require.NotNil(t, insert)
			lat, lng, source := insert[3].(*float64), insert[4].(*float64), insert[6].(*string)
			if !tt.wantSource {
				assert.Nil(t, lat)
				assert.Nil(t, lng)
				assert.Nil(t, source)
				return
			}
			require.NotNil(t, lat)
			require.NotNil(t, lng)
			assert.InDelta(t, tt.wantLat, *lat, 1e-9)
			assert.InDelta(t, tt.wantLng, *lng, 1e-9)
			require.NotNil(t, source)
			assert.Equal(t, "exif", *source)
		})
	}
}
