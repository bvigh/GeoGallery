package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// CameraRule says what one camera make writes that ingest must not take at face value. The rules
// live in a file (CAMERA_RULES_FILE, backend/camera-rules.json), not in code. A make without a rule
// is read by the general rules alone.
type CameraRule struct {
	// Make is matched against the file's EXIF Make without case and surrounding space.
	Make string `json:"make"`
	// IgnoreOffsetTag: the make's OffsetTimeOriginal is not trusted, so its photos are read in the
	// camera zone unless their GPS clock says otherwise.
	IgnoreOffsetTag bool `json:"ignore_offset_tag"`
	// VideoTimeFromExif: the make's videos carry the camera's EXIF (VideoExif), and their container
	// creation_time is not UTC, so a video is dated from that EXIF, read like a photo's.
	VideoTimeFromExif bool `json:"video_time_from_exif"`
	// Why says what was observed, for whoever reads or changes the file. It is required.
	Why string `json:"why"`
}

// CameraRules is the parsed rules file. The zero value holds no rules.
type CameraRules []CameraRule

// cameraRulesFile is the file's shape. Unknown keys are refused, so a misspelt rule fails at startup
// instead of silently not applying.
type cameraRulesFile struct {
	Cameras CameraRules `json:"cameras"`
}

// LoadCameraRules reads and checks the rules file at path.
func LoadCameraRules(path string) (CameraRules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCameraRules(data)
}

// ParseCameraRules parses the rules file. Every rule needs a make, a reason and at least one
// behaviour, and a make may have only one rule.
func ParseCameraRules(data []byte) (CameraRules, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var file cameraRulesFile
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("camera rules: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("camera rules: unexpected data after the rules object")
	}
	if file.Cameras == nil {
		return nil, errors.New(`camera rules: "cameras" is missing; an empty list is "cameras": []`)
	}
	seen := map[string]bool{}
	for i, rule := range file.Cameras {
		key := normalMake(rule.Make)
		switch {
		case key == "":
			return nil, fmt.Errorf("camera rules: rule %d has no make", i+1)
		case strings.TrimSpace(rule.Why) == "":
			return nil, fmt.Errorf("camera rules: the rule for %q has no why", rule.Make)
		case !rule.IgnoreOffsetTag && !rule.VideoTimeFromExif:
			return nil, fmt.Errorf("camera rules: the rule for %q changes nothing", rule.Make)
		case seen[key]:
			return nil, fmt.Errorf("camera rules: %q has more than one rule", rule.Make)
		}
		seen[key] = true
	}
	return file.Cameras, nil
}

// For returns the rule for a make, or the zero rule when there is none.
func (rs CameraRules) For(cameraMake string) CameraRule {
	key := normalMake(cameraMake)
	if key == "" {
		return CameraRule{}
	}
	for _, rule := range rs {
		if normalMake(rule.Make) == key {
			return rule
		}
	}
	return CameraRule{}
}

// normalMake is how makes are compared: cameras disagree on case ("samsung", "Panasonic") and some
// pad the tag with spaces.
func normalMake(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
