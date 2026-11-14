package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/config"
	"github.com/bvigh/geogallery/internal/handlers"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The camera clock rule routes and GET /api/cameras sit in the RequireUser group: an API token
// reaches the database on every one of them, and so does a session.
func TestRouter_CameraClockRulesAcceptTokens(t *testing.T) {
	pool := testdb.New(t)
	var userID string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'clock', 'clock@example.test')
		RETURNING id::text`).Scan(&userID))

	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	h := routeHandlers{clockRules: handlers.NewCameraClockRuleHandler(pool)}
	router := newRouter(http.NewCrossOriginProtection(), auth.New(compressStore{userID: userID}, nil, cfg), h)

	send := func(t *testing.T, token bool, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token {
			req.Header.Set("Authorization", "Bearer "+testToken)
		} else {
			req.AddCookie(&http.Cookie{Name: "gg_session", Value: userSession})
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	for _, token := range []bool{true, false} {
		name := "session"
		if token {
			name = "token"
		}
		t.Run(name, func(t *testing.T) {
			rec := send(t, token, http.MethodGet, "/api/camera-clock-rules", "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, `[]`, rec.Body.String())

			rec = send(t, token, http.MethodPost, "/api/camera-clock-rules",
				`{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":120,"starts_on":null,"ends_on":null}`)
			require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			var created struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

			rec = send(t, token, http.MethodPut, "/api/camera-clock-rules/"+created.ID,
				`{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":60,"starts_on":"2019-01-01","ends_on":null}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			rec = send(t, token, http.MethodGet, "/api/cameras", "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, `[]`, rec.Body.String())

			rec = send(t, token, http.MethodDelete, "/api/camera-clock-rules/"+created.ID, "")
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		})
	}
}
