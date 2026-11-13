package ingest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bvigh/geogallery/internal/ingest/exiftest"
)

// panasonicRules mirrors the Panasonic rule of backend/camera-rules.json, which
// TestLoadCameraRules_ShippedFile holds it to.
var panasonicRules = CameraRules{{Make: "Panasonic", IgnoreOffsetTag: true, VideoTimeFromExif: true, Why: "test"}}

func TestParseCameraRules(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		want    CameraRules
		wantErr string
	}{
		{"one rule", `{"cameras": [{"make": "Acme", "ignore_offset_tag": true, "why": "seen"}]}`,
			CameraRules{{Make: "Acme", IgnoreOffsetTag: true, Why: "seen"}}, ""},
		{"both behaviours", `{"cameras": [{"make": "Acme", "ignore_offset_tag": true, "video_time_from_exif": true, "why": "seen"}]}`,
			CameraRules{{Make: "Acme", IgnoreOffsetTag: true, VideoTimeFromExif: true, Why: "seen"}}, ""},
		{"no rules at all", `{"cameras": []}`, CameraRules{}, ""},

		{"not JSON", `cameras: []`, nil, "camera rules"},
		{"the list is missing", `{}`, nil, `"cameras" is missing`},
		{"an unknown top-level key", `{"cameras": [], "zones": []}`, nil, "unknown field"},
		{"a misspelt behaviour", `{"cameras": [{"make": "Acme", "ignore_offset": true, "why": "seen"}]}`, nil, "unknown field"},
		{"no make", `{"cameras": [{"ignore_offset_tag": true, "why": "seen"}]}`, nil, "rule 1 has no make"},
		{"a make of spaces", `{"cameras": [{"make": "  ", "ignore_offset_tag": true, "why": "seen"}]}`, nil, "rule 1 has no make"},
		{"no why", `{"cameras": [{"make": "Acme", "ignore_offset_tag": true}]}`, nil, "has no why"},
		{"a rule that changes nothing", `{"cameras": [{"make": "Acme", "why": "seen"}]}`, nil, "changes nothing"},
		{"two rules for one make, whatever the case", `{"cameras": [
			{"make": "Acme", "ignore_offset_tag": true, "why": "seen"},
			{"make": " ACME ", "video_time_from_exif": true, "why": "seen"}]}`, nil, "more than one rule"},
		{"data after the object", `{"cameras": []} {"cameras": []}`, nil, "unexpected data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCameraRules([]byte(tt.file))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCameraRules_For(t *testing.T) {
	rules := CameraRules{
		{Make: "Acme", IgnoreOffsetTag: true, Why: "seen"},
		{Make: "Panasonic", VideoTimeFromExif: true, Why: "seen"},
	}
	assert.Equal(t, rules[0], rules.For("Acme"))
	assert.Equal(t, rules[1], rules.For(" PANASONIC "), "case and padding are ignored")
	assert.Equal(t, CameraRule{}, rules.For("samsung"), "a make without a rule gets the zero rule")
	assert.Equal(t, CameraRule{}, rules.For(""))
	assert.Equal(t, CameraRule{}, CameraRules(nil).For("Acme"))
}

func TestLoadCameraRules(t *testing.T) {
	t.Run("a missing file is an error", func(t *testing.T) {
		_, err := LoadCameraRules(filepath.Join(t.TempDir(), "none.json"))
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("an invalid file is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rules.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"cameras": [{"make": "Acme"}]}`), 0o600))
		_, err := LoadCameraRules(path)
		assert.Error(t, err)
	})
}

// The file the backend ships with parses, and still holds the Panasonic rule the other tests assume.
func TestLoadCameraRules_ShippedFile(t *testing.T) {
	rules, err := LoadCameraRules("../../camera-rules.json")
	require.NoError(t, err)
	got := rules.For("Panasonic")
	assert.Equal(t, panasonicRules[0].IgnoreOffsetTag, got.IgnoreOffsetTag)
	assert.Equal(t, panasonicRules[0].VideoTimeFromExif, got.VideoTimeFromExif)
	assert.NotEmpty(t, got.Why)
}

// No make is special in code: what the Panasonic rule does comes from the rules alone.
func TestExtractExif_OffsetRuleComesFromTheRules(t *testing.T) {
	warsaw := mustZone(t, "Europe/Warsaw")
	panasonic := exiftest.Photo{Make: "Panasonic", DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "+01:00"}
	acme := exiftest.Photo{Make: "Acme", DateTimeOriginal: "2018:10:23 11:50:53", OffsetTimeOriginal: "+01:00"}
	acmeRule := CameraRules{{Make: "acme", IgnoreOffsetTag: true, Why: "test"}}
	videoOnly := CameraRules{{Make: "Panasonic", VideoTimeFromExif: true, Why: "test"}}

	tests := []struct {
		name        string
		photo       exiftest.Photo
		rules       CameraRules
		wantZone    string
		wantIgnored bool
	}{
		{"without rules a Panasonic's offset is trusted", panasonic, nil, ZoneOffset, false},
		{"with the rule it is ignored", panasonic, panasonicRules, ZoneCamera, true},
		{"a rule for another make ignores that make's offset", acme, acmeRule, ZoneCamera, true},
		{"and leaves the Panasonic's alone", panasonic, acmeRule, ZoneOffset, false},
		{"a rule about videos only leaves a photo's offset trusted", panasonic, videoOnly, ZoneOffset, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractExif(bytes.NewReader(exiftest.JPEG(tt.photo)), CameraZone{Loc: warsaw, Rules: tt.rules})
			assert.Equal(t, tt.wantZone, got.TakenAtZone)
			assert.Equal(t, tt.wantIgnored, got.Raw.OffsetIgnored)
			assert.Equal(t, "+01:00", got.Raw.OffsetTag, "the tag is recorded either way")
		})
	}
}
