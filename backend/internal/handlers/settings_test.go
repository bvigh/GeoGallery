package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSettings(t *testing.T) {
	tokyo := "Asia/Tokyo"
	tests := []struct {
		name    string
		body    string
		want    settingsPatch
		wantErr string
	}{
		{"a zone", `{"camera_timezone":"Asia/Tokyo"}`, settingsPatch{cameraTimezoneSet: true, cameraTimezone: &tokyo}, ""},
		{"null returns to the default", `{"camera_timezone":null}`, settingsPatch{cameraTimezoneSet: true}, ""},
		{"UTC is a zone", `{"camera_timezone":"UTC"}`, settingsPatch{cameraTimezoneSet: true, cameraTimezone: ptr("UTC")}, ""},
		{"nothing to change", `{}`, settingsPatch{}, "no setting to change"},
		{"an unknown key", `{"camera_timezone":null,"video_muted":true}`, settingsPatch{}, `unknown setting "video_muted"`},
		{"an unknown zone", `{"camera_timezone":"Europe/Nowhere"}`, settingsPatch{}, `camera_timezone: unknown time zone "Europe/Nowhere"`},
		{"the server's own zone by its Go name", `{"camera_timezone":"Local"}`, settingsPatch{}, `camera_timezone: unknown time zone "Local"`},
		{"an empty name", `{"camera_timezone":""}`, settingsPatch{}, `camera_timezone: unknown time zone ""`},
		{"a path", `{"camera_timezone":"../../etc/passwd"}`, settingsPatch{}, "camera_timezone: unknown time zone"},
		{"not text", `{"camera_timezone":60}`, settingsPatch{}, "camera_timezone: want an IANA time zone name or null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(tt.body), &body))
			got, err := parseSettings(body)
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

// settingsDBStub answers the settings handler's one query with zone, and records it.
type settingsDBStub struct {
	mockMatcherDB
	zone *string
	err  error
	sqls []string
	args [][]any
}

func newSettingsDBStub(zone *string, err error) *settingsDBStub {
	s := &settingsDBStub{zone: zone, err: err}
	s.queryRowFn = func(_ context.Context, sql string, args ...any) pgx.Row {
		s.sqls = append(s.sqls, sql)
		s.args = append(s.args, args)
		return &mockRow{scanFn: func(dest ...any) error {
			if s.err != nil {
				return s.err
			}
			if strings.Contains(sql, "UPDATE users") && args[1].(bool) {
				s.zone, _ = args[2].(*string)
			}
			*dest[0].(**string) = s.zone
			return nil
		}}
	}
	return s
}

func callSettings(t *testing.T, h *SettingsHandler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withTestUser(httptest.NewRequest(method, "/api/me/settings", strings.NewReader(body)))
	rec := httptest.NewRecorder()
	if method == http.MethodGet {
		h.Get(rec, req)
	} else {
		h.Update(rec, req)
	}
	return rec
}

func TestSettingsHandler(t *testing.T) {
	server := ingest.CameraZone{Loc: mustLoadLocation(t, "Europe/Warsaw"), Set: true}

	t.Run("get, with the server's zone as the default", func(t *testing.T) {
		db := newSettingsDBStub(ptr("Asia/Tokyo"), nil)
		rec := callSettings(t, &SettingsHandler{db: db, serverZone: server}, http.MethodGet, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"camera_timezone":"Asia/Tokyo","defaults":{"camera_timezone":"Europe/Warsaw"}}`, rec.Body.String())
		assert.Equal(t, [][]any{{testOwnerID}}, db.args, "the caller's own row")
	})
	t.Run("get, with CAMERA_TIMEZONE unset", func(t *testing.T) {
		db := newSettingsDBStub(nil, nil)
		rec := callSettings(t, &SettingsHandler{db: db}, http.MethodGet, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{"camera_timezone":null,"defaults":{"camera_timezone":"server zone (unset)"}}`, rec.Body.String())
	})
	t.Run("patch sets the zone", func(t *testing.T) {
		db := newSettingsDBStub(nil, nil)
		rec := callSettings(t, &SettingsHandler{db: db, serverZone: server}, http.MethodPatch, `{"camera_timezone":"Asia/Tokyo"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{"camera_timezone":"Asia/Tokyo","defaults":{"camera_timezone":"Europe/Warsaw"}}`, rec.Body.String())
		require.Len(t, db.args, 1)
		assert.Equal(t, testOwnerID, db.args[0][0], "the caller's own row")
	})
	t.Run("patch with null returns to the default", func(t *testing.T) {
		db := newSettingsDBStub(ptr("Asia/Tokyo"), nil)
		rec := callSettings(t, &SettingsHandler{db: db, serverZone: server}, http.MethodPatch, `{"camera_timezone":null}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{"camera_timezone":null,"defaults":{"camera_timezone":"Europe/Warsaw"}}`, rec.Body.String())
	})

	refused := []struct {
		name, body string
		wantStatus int
		wantBody   string
	}{
		{"an unknown zone", `{"camera_timezone":"Mars/Olympus"}`, http.StatusBadRequest, `camera_timezone: unknown time zone "Mars/Olympus"`},
		{"an unknown key", `{"theme":"dark"}`, http.StatusBadRequest, `unknown setting "theme"`},
		{"nothing to change", `{}`, http.StatusBadRequest, "no setting to change"},
		{"not an object", `["camera_timezone"]`, http.StatusBadRequest, "invalid body"},
		{"null", `null`, http.StatusBadRequest, "invalid body"},
		{"no body", ``, http.StatusBadRequest, "invalid body"},
		{"data after the object", `{"camera_timezone":null} {}`, http.StatusBadRequest, "invalid body"},
		{"too large", `{"camera_timezone":"` + strings.Repeat("A", maxSettingsBodyBytes) + `"}`, http.StatusRequestEntityTooLarge, "too large"},
	}
	for _, tt := range refused {
		t.Run("patch refuses "+tt.name+", before the database", func(t *testing.T) {
			db := newSettingsDBStub(nil, nil)
			rec := callSettings(t, &SettingsHandler{db: db, serverZone: server}, http.MethodPatch, tt.body)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			assert.Empty(t, db.sqls)
		})
	}

	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		t.Run(method+" with a database error is a 500", func(t *testing.T) {
			db := newSettingsDBStub(nil, errors.New("down"))
			rec := callSettings(t, &SettingsHandler{db: db, serverZone: server}, method, `{"camera_timezone":null}`)
			assert.Equal(t, http.StatusInternalServerError, rec.Code)
		})
	}
}
