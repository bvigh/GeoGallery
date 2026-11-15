package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/config"
	"github.com/bvigh/geogallery/internal/handlers"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GET and PATCH /api/me/settings sit in the RequireUser group: an API token reaches the database on
// both, and so does a session.
func TestRouter_SettingsAcceptTokens(t *testing.T) {
	pool := testdb.New(t)
	var userID string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'settings', 'settings@example.test')
		RETURNING id::text`).Scan(&userID))

	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	h := routeHandlers{settings: handlers.NewSettingsHandler(pool, ingest.CameraZone{Loc: warsaw, Set: true})}
	router := newRouter(http.NewCrossOriginProtection(), auth.New(compressStore{userID: userID}, nil, cfg), h)

	send := func(t *testing.T, token bool, method, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/me/settings", strings.NewReader(body))
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
			rec := send(t, token, http.MethodGet, "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, `{"camera_timezone":null,"defaults":{"camera_timezone":"Europe/Warsaw"}}`, rec.Body.String())

			rec = send(t, token, http.MethodPatch, `{"camera_timezone":"Asia/Tokyo"}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, `{"camera_timezone":"Asia/Tokyo","defaults":{"camera_timezone":"Europe/Warsaw"}}`, rec.Body.String())

			rec = send(t, token, http.MethodPatch, `{"camera_timezone":null}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, `{"camera_timezone":null,"defaults":{"camera_timezone":"Europe/Warsaw"}}`, rec.Body.String())
		})
	}
}
