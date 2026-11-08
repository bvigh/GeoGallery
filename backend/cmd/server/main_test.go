package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/config"
	"github.com/bvigh/geogallery/internal/handlers"
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
		media:      handlers.NewMediaHandler(nil, nil, time.Hour, 24, time.UTC),
		trips:      handlers.NewTripHandler(nil, 24),
		game:       handlers.NewGameHandler(nil, nil),
		gpsTracks:  handlers.NewGPSTrackHandler(nil, time.Hour),
		gpsMatcher: handlers.NewGPSMatcherHandler(nil, time.Hour),
		albums:     handlers.NewAlbumHandler(nil),
		admin:      handlers.NewAdminHandler(store, cfg.PublicOrigin),
	}
	return newRouter(cop, auth.New(store, nil, cfg), h)
}

func TestRouter_AuthWiring(t *testing.T) {
	const (
		mediaID = "00000000-0000-0000-0000-000000000000"
		badID   = "not-a-uuid"
	)
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
		{"ingest scan route removed", http.MethodPost, "/api/ingest/scan", false, userSession, nil, http.StatusNotFound},
		{"token me", http.MethodGet, "/api/auth/me", true, "", nil, http.StatusOK},
		{"session me", http.MethodGet, "/api/auth/me", false, userSession, nil, http.StatusOK},

		{"token delete media", http.MethodDelete, "/api/media/" + mediaID, true, "", nil, http.StatusForbidden},
		{"token delete trip", http.MethodDelete, "/api/trips/" + mediaID, true, "", nil, http.StatusForbidden},
		{"token list tokens", http.MethodGet, "/api/tokens", true, "", nil, http.StatusForbidden},
		{"token create token", http.MethodPost, "/api/tokens", true, "", nil, http.StatusForbidden},
		{"token revoke token", http.MethodDelete, "/api/tokens/" + mediaID, true, "", nil, http.StatusForbidden},
		{"admin token on admin route", http.MethodGet, "/api/admin/users", true, "", nil, http.StatusForbidden},
		{"admin token creates invite", http.MethodPost, "/api/admin/invites", true, "", nil, http.StatusForbidden},
		{"token delete album", http.MethodDelete, "/api/albums/" + mediaID, true, "", nil, http.StatusForbidden},
		{"token add album member", http.MethodPost, "/api/albums/" + mediaID + "/members", true, "", nil, http.StatusForbidden},
		{"token remove album member", http.MethodDelete, "/api/albums/" + mediaID + "/members/" + mediaID, true, "", nil, http.StatusForbidden},
		{"token user directory", http.MethodGet, "/api/users", true, "", nil, http.StatusForbidden},

		// The album handler has no DB here: these statuses come from its input checks, which shows
		// the request got past the auth middleware.
		{"token creates album", http.MethodPost, "/api/albums", true, "", nil, http.StatusBadRequest},
		{"token renames album", http.MethodPatch, "/api/albums/" + badID, true, "", nil, http.StatusNotFound},
		{"token lists album members", http.MethodGet, "/api/albums/" + badID + "/members", true, "", nil, http.StatusNotFound},
		{"token adds album media", http.MethodPost, "/api/albums/" + badID + "/media", true, "", nil, http.StatusNotFound},
		{"token removes album media", http.MethodPost, "/api/albums/" + badID + "/media/remove", true, "", nil, http.StatusNotFound},
		{"token lists media albums", http.MethodGet, "/api/media/" + badID + "/albums", true, "", nil, http.StatusOK},
		{"token checks known hashes", http.MethodPost, "/api/media/known-hashes", true, "", nil, http.StatusBadRequest},
		{"anonymous known hashes", http.MethodPost, "/api/media/known-hashes", false, "", nil, http.StatusUnauthorized},
		// No multipart body: 400 from the handler shows the route is wired and open to tokens.
		{"token uploads GPS tracks", http.MethodPost, "/api/gps-tracks/upload", true, "", nil, http.StatusBadRequest},
		{"anonymous GPS track upload", http.MethodPost, "/api/gps-tracks/upload", false, "", nil, http.StatusUnauthorized},
		{"session deletes album", http.MethodDelete, "/api/albums/" + badID, false, userSession, nil, http.StatusNotFound},
		{"session adds album member", http.MethodPost, "/api/albums/" + badID + "/members", false, userSession, nil, http.StatusNotFound},
		{"session removes album member", http.MethodDelete, "/api/albums/" + badID + "/members/" + badID, false, userSession, nil, http.StatusNotFound},
		{"anonymous albums", http.MethodGet, "/api/albums", false, "", nil, http.StatusUnauthorized},
		{"anonymous user directory", http.MethodGet, "/api/users", false, "", nil, http.StatusUnauthorized},

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
		{"scan without --user", "scan", nil},
		{"scan stray argument", "scan", []string{"--user", "u", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			assert.Equal(t, 2, runSubcommand(tt.cmd, tt.args, &out))
			assert.Empty(t, out.String())
		})
	}
}

func scanUsers() []auth.UserRecord {
	return []auth.UserRecord{
		{User: auth.User{ID: auth.LegacyOwnerID, Role: "admin"}},
		{User: auth.User{ID: "active-user", Role: "user"}, Claimed: true},
		{User: auth.User{ID: "disabled-user", Role: "user"}, Claimed: true, Disabled: true},
	}
}

func TestScanTargetError(t *testing.T) {
	tests := []struct {
		name    string
		userID  string
		wantErr string
	}{
		{"active user accepted", "active-user", ""},
		{"unknown user", "nobody", `unknown user "nobody"`},
		{"disabled user", "disabled-user", "user disabled-user is disabled"},
		{"unclaimed user", auth.LegacyOwnerID, "user is unclaimed: run `server invite --admin` and sign in first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := scanTargetError(scanUsers(), tt.userID)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestRunScan(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name       string
		ctx        context.Context
		userID     string
		res        handlers.ScanResult
		scanErr    error
		wantCalled bool
		wantOut    string
		wantErr    string
	}{
		{"success", context.Background(), "active-user", handlers.ScanResult{Ingested: 2, Skipped: 1}, nil,
			true, "ingested=2 skipped=1 failed=0\n", ""},
		{"failed files exit non-zero", context.Background(), "active-user", handlers.ScanResult{Ingested: 1, Failed: 1}, nil,
			true, "ingested=1 skipped=0 failed=1\n", "1 file(s) failed"},
		{"scan error", context.Background(), "active-user", handlers.ScanResult{}, errors.New("gps matcher: boom"),
			true, "ingested=0 skipped=0 failed=0\n", "gps matcher: boom"},
		{"interrupted", cancelled, "active-user", handlers.ScanResult{Ingested: 1}, context.Canceled,
			true, "ingested=1 skipped=0 failed=0\n", "interrupted; re-run scan to finish"},
		{"unclaimed user refused before scanning", context.Background(), auth.LegacyOwnerID, handlers.ScanResult{}, nil,
			false, "", "user is unclaimed"},
		{"uppercase id normalised", context.Background(), "ACTIVE-USER", handlers.ScanResult{Ingested: 1}, nil,
			true, "ingested=1 skipped=0 failed=0\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			scan := func(_ context.Context, dir, ownerID string) (handlers.ScanResult, error) {
				called = true
				assert.Equal(t, "/ingest", dir)
				assert.Equal(t, strings.ToLower(tt.userID), ownerID)
				return tt.res, tt.scanErr
			}
			var out bytes.Buffer
			err := runScan(tt.ctx, &out, scan, scanUsers(), tt.userID, "/ingest")
			assert.Equal(t, tt.wantCalled, called)
			assert.Equal(t, tt.wantOut, out.String())
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
