package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxSettingsBodyBytes bounds a PATCH /api/me/settings body.
const maxSettingsBodyBytes = 16 << 10

// settingKeys are the keys PATCH /api/me/settings takes, each a column of users. A new setting adds
// its key here, a field to UserSettings and to SettingsDefaults, a case to parseSettings, and its
// column to the SQL of Get and Update.
var settingKeys = []string{"camera_timezone", "videos_start_muted"}

// defaultVideosStartMuted is how a video starts where the caller has not chosen: with sound.
const defaultVideosStartMuted = false

// UserSettings is GET and PATCH /api/me/settings: the caller's own settings, each null where the
// server's default applies, and those defaults.
type UserSettings struct {
	// CameraTimezone is the IANA zone the caller's camera clocks show: ingest reads an EXIF wall
	// clock in it when the file gives no offset of its own, unless an upload names a zone.
	CameraTimezone *string `json:"camera_timezone"`
	// VideosStartMuted is whether a video the caller opens full size starts muted (true) or with
	// sound (false). Only the browser applies it, as each video starts.
	VideosStartMuted *bool            `json:"videos_start_muted"`
	Defaults         SettingsDefaults `json:"defaults"`
}

// SettingsDefaults are what applies where a setting is null.
type SettingsDefaults struct {
	CameraTimezone   string `json:"camera_timezone"`    // the server's zone, as ingest.CameraZone.Name writes it
	VideosStartMuted bool   `json:"videos_start_muted"` // defaultVideosStartMuted
}

type settingsDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// SettingsHandler serves the caller's own settings, kept on their users row. Every query filters on
// the caller's literal id. The routes accept API tokens: the camera zone only changes how the
// caller's future uploads are dated and the video sound how their videos start, and neither is
// location history.
type SettingsHandler struct {
	db         settingsDB
	serverZone ingest.CameraZone // what applies where camera_timezone is null
}

func NewSettingsHandler(pool *pgxpool.Pool, serverZone ingest.CameraZone) *SettingsHandler {
	return &SettingsHandler{db: pool, serverZone: serverZone}
}

// Get serves GET /api/me/settings.
func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	var s UserSettings
	if err := h.db.QueryRow(r.Context(), `SELECT camera_timezone, videos_start_muted FROM users WHERE id = $1`, me).
		Scan(&s.CameraTimezone, &s.VideosStartMuted); err != nil {
		slog.Error("settings: load failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	h.write(w, s)
}

// Update serves PATCH /api/me/settings: a JSON object with at least one of settingKeys, each a value
// or null to return to the default. Keys left out keep their value. A refused body is 400 with a
// short plain-text reason, and writes nothing. The answer is the settings as GET returns them.
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	patch, ok := decodeSettings(w, r)
	if !ok {
		return
	}
	var s UserSettings
	if err := h.db.QueryRow(r.Context(), `
		UPDATE users
		SET camera_timezone = CASE WHEN $2 THEN $3 ELSE camera_timezone END,
		    videos_start_muted = CASE WHEN $4 THEN $5 ELSE videos_start_muted END
		WHERE id = $1
		RETURNING camera_timezone, videos_start_muted`,
		me, patch.cameraTimezoneSet, patch.cameraTimezone, patch.videosStartMutedSet, patch.videosStartMuted).
		Scan(&s.CameraTimezone, &s.VideosStartMuted); err != nil {
		slog.Error("settings: update failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	h.write(w, s)
}

func (h *SettingsHandler) write(w http.ResponseWriter, s UserSettings) {
	s.Defaults = SettingsDefaults{CameraTimezone: h.serverZone.Name(), VideosStartMuted: defaultVideosStartMuted}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s); err != nil {
		slog.Warn("settings: write response failed", "err", err)
	}
}

// settingsPatch is a decoded PATCH body: for each setting, whether the body names it and its value.
type settingsPatch struct {
	cameraTimezoneSet   bool
	cameraTimezone      *string // nil returns to the default
	videosStartMutedSet bool
	videosStartMuted    *bool // nil returns to the default
}

// decodeSettings reads a PATCH body, or writes 400 (413 when it is too large) and reports false.
func decodeSettings(w http.ResponseWriter, r *http.Request) (settingsPatch, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBodyBytes))
	var body map[string]json.RawMessage
	err := dec.Decode(&body)
	if err == nil {
		if _, next := dec.Token(); next != io.EOF {
			err = errors.Join(errors.New("data after the JSON object"), next)
		}
	}
	if err != nil || body == nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return settingsPatch{}, false
		}
		http.Error(w, "invalid body: want a JSON object with "+strings.Join(settingKeys, ", "), http.StatusBadRequest)
		return settingsPatch{}, false
	}
	patch, err := parseSettings(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return settingsPatch{}, false
	}
	return patch, true
}

// parseSettings checks every key of a PATCH body. The reasons are short plain text for the Settings
// page to show.
func parseSettings(body map[string]json.RawMessage) (settingsPatch, error) {
	if len(body) == 0 {
		return settingsPatch{}, errors.New("no setting to change")
	}
	var patch settingsPatch
	for key, raw := range body {
		if !slices.Contains(settingKeys, key) {
			return settingsPatch{}, fmt.Errorf("unknown setting %q", key)
		}
		switch key {
		case "camera_timezone":
			zone, err := parseCameraTimezone(raw)
			if err != nil {
				return settingsPatch{}, fmt.Errorf("camera_timezone: %w", err)
			}
			patch.cameraTimezoneSet, patch.cameraTimezone = true, zone
		case "videos_start_muted":
			muted, err := parseVideosStartMuted(raw)
			if err != nil {
				return settingsPatch{}, fmt.Errorf("videos_start_muted: %w", err)
			}
			patch.videosStartMutedSet, patch.videosStartMuted = true, muted
		}
	}
	return patch, nil
}

// parseVideosStartMuted reads true (muted), false (with sound) or null (the server's default). Text
// and numbers are refused, "true" and 1 included.
func parseVideosStartMuted(raw json.RawMessage) (*bool, error) {
	var muted *bool
	if err := json.Unmarshal(raw, &muted); err != nil {
		return nil, errors.New("want true, false or null")
	}
	return muted, nil
}

// parseCameraTimezone reads null (the server's zone) or an IANA name this server can load. The name
// is stored as given, which is what time.LoadLocation names the zone.
func parseCameraTimezone(raw json.RawMessage) (*string, error) {
	var name *string
	if err := json.Unmarshal(raw, &name); err != nil {
		return nil, errors.New("want an IANA time zone name or null")
	}
	if name == nil {
		return nil, nil
	}
	if _, err := ingest.LoadZone(*name); err != nil {
		return nil, err
	}
	return name, nil
}
