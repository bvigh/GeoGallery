package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"codeberg.org/bvigh/geogallery/internal/config"
	"codeberg.org/bvigh/geogallery/internal/handlers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routeStore authenticates one token user and two session users; every other Store method
// panics on the nil embedded interface, which would surface as a 500 via Recoverer.
type routeStore struct {
	auth.Store
}

const (
	testToken      = "gg_pat_routetest"
	userSession    = "user-session"
	adminSession   = "admin-session"
	tokenUserID    = "token-user"
	sessionUserID  = "session-user"
	sessionAdminID = "session-admin"
)

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return string(h[:])
}

func (routeStore) UserForAPIToken(_ context.Context, h []byte) (auth.User, string, error) {
	if string(h) == hash(testToken) {
		return auth.User{ID: tokenUserID, Role: "admin"}, "tok-1", nil
	}
	return auth.User{}, "", auth.ErrNoToken
}

func (routeStore) TouchAPIToken(context.Context, string) error { return nil }

func (routeStore) UserForSession(_ context.Context, h []byte) (auth.User, error) {
	switch string(h) {
	case hash(userSession):
		return auth.User{ID: sessionUserID, Role: "user"}, nil
	case hash(adminSession):
		return auth.User{ID: sessionAdminID, Role: "admin"}, nil
	}
	return auth.User{}, auth.ErrNoSession
}

func (routeStore) ListUsers(context.Context) ([]auth.UserRecord, error) {
	return []auth.UserRecord{}, nil
}

func (routeStore) ListAPITokens(context.Context, string) ([]auth.APIToken, error) {
	return []auth.APIToken{}, nil
}

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	cop := http.NewCrossOriginProtection()
	require.NoError(t, cop.AddTrustedOrigin(cfg.PublicOrigin))
	store := routeStore{}
	h := routeHandlers{
		media:      handlers.NewMediaHandler(nil, nil, time.Hour, 24),
		trips:      handlers.NewTripHandler(nil, 24),
		game:       handlers.NewGameHandler(nil, nil),
		ingest:     handlers.NewIngestHandler(nil, nil, t.TempDir(), time.Hour, 24),
		gpsTracks:  handlers.NewGPSTrackHandler(nil, time.Hour),
		gpsMatcher: handlers.NewGPSMatcherHandler(nil, time.Hour),
		admin:      handlers.NewAdminHandler(store, cfg.PublicOrigin),
	}
	return newRouter(cop, auth.New(store, nil, cfg), h)
}

func TestRouter_AuthWiring(t *testing.T) {
	const mediaID = "00000000-0000-0000-0000-000000000000"
	tests := []struct {
		name       string
		method     string
		path       string
		token      bool
		session    string
		headers    map[string]string
		wantStatus int
	}{
		{"health is public", http.MethodGet, "/api/health", false, "", nil, http.StatusOK},
		{"anonymous media list", http.MethodGet, "/api/media", false, "", nil, http.StatusUnauthorized},
		{"anonymous me", http.MethodGet, "/api/auth/me", false, "", nil, http.StatusUnauthorized},
		{"anonymous ingest scan", http.MethodPost, "/api/ingest/scan", false, "", nil, http.StatusUnauthorized},
		{"token me", http.MethodGet, "/api/auth/me", true, "", nil, http.StatusOK},
		{"session me", http.MethodGet, "/api/auth/me", false, userSession, nil, http.StatusOK},

		{"token delete media", http.MethodDelete, "/api/media/" + mediaID, true, "", nil, http.StatusForbidden},
		{"token delete trip", http.MethodDelete, "/api/trips/" + mediaID, true, "", nil, http.StatusForbidden},
		{"token list tokens", http.MethodGet, "/api/tokens", true, "", nil, http.StatusForbidden},
		{"token create token", http.MethodPost, "/api/tokens", true, "", nil, http.StatusForbidden},
		{"token revoke token", http.MethodDelete, "/api/tokens/" + mediaID, true, "", nil, http.StatusForbidden},
		{"admin token on admin route", http.MethodGet, "/api/admin/users", true, "", nil, http.StatusForbidden},
		{"admin token creates invite", http.MethodPost, "/api/admin/invites", true, "", nil, http.StatusForbidden},

		{"session lists tokens", http.MethodGet, "/api/tokens", false, userSession, nil, http.StatusOK},
		{"non-admin session admin route", http.MethodGet, "/api/admin/users", false, userSession, nil, http.StatusForbidden},
		{"admin session admin route", http.MethodGet, "/api/admin/users", false, adminSession, nil, http.StatusOK},

		{"cross-site POST rejected", http.MethodPost, "/api/auth/logout", false, "",
			map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"cross-origin POST rejected", http.MethodPost, "/api/auth/logout", false, "",
			map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"trusted origin POST allowed", http.MethodPost, "/api/auth/logout", false, "",
			map[string]string{"Origin": "http://localhost:5173"}, http.StatusOK},
		{"curl POST without origin allowed", http.MethodPost, "/api/auth/logout", false, "", nil, http.StatusOK},
	}
	router := newTestRouter(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.token {
				req.Header.Set("Authorization", "Bearer "+testToken)
			}
			if tt.session != "" {
				req.AddCookie(&http.Cookie{Name: "gg_session", Value: tt.session})
			}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}
}

func TestWriteUsersTable(t *testing.T) {
	users := []auth.UserRecord{
		{User: auth.User{ID: auth.LegacyOwnerID, Role: "admin", DisplayName: "Owner"}},
		{User: auth.User{ID: "a", Role: "user", Email: "alice@example.test", DisplayName: "Alice"}, Claimed: true},
		{User: auth.User{ID: "b", Role: "user"}, Claimed: true, Disabled: true},
	}
	var buf bytes.Buffer
	require.NoError(t, writeUsersTable(&buf, users))
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 4)
	assert.Equal(t, []string{"id", "role", "status", "email", "display_name"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{auth.LegacyOwnerID, "admin", "unclaimed", "-", "Owner"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"a", "user", "active", "alice@example.test", "Alice"}, strings.Fields(lines[2]))
	assert.Equal(t, []string{"b", "user", "disabled", "-", "-"}, strings.Fields(lines[3]))
}

func TestRunSubcommand_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
	}{
		{"unknown command", "frobnicate", nil},
		{"unknown flag", "invite", []string{"--bogus"}},
		{"stray argument", "users", []string{"extra"}},
		{"rebind without --to", "rebind-issuer", []string{"--from", "a"}},
		{"rebind same issuer", "rebind-issuer", []string{"--from", "a", "--to", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			assert.Equal(t, 2, runSubcommand(tt.cmd, tt.args, &out))
			assert.Empty(t, out.String())
		})
	}
}
