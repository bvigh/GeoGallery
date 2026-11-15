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
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// refusingPool is a real pool whose every connection attempt fails before it reaches the network:
// a handler over it answers 500, which shows that a request got past the auth middleware.
func refusingPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://route-test@db.invalid/route-test")
	require.NoError(t, err)
	cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		return errors.New("no database in route tests")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	cop := http.NewCrossOriginProtection()
	require.NoError(t, cop.AddTrustedOrigin(cfg.PublicOrigin))
	store := routeStore{}
	h := routeHandlers{
		media:      handlers.NewMediaHandler(nil, nil, time.Hour, 24, ingest.CameraZone{}),
		trips:      handlers.NewTripHandler(nil, 24),
		game:       handlers.NewGameHandler(nil, nil),
		gpsTracks:  handlers.NewGPSTrackHandler(nil, time.Hour),
		gpsMatcher: handlers.NewGPSMatcherHandler(nil, time.Hour),
		albums:     handlers.NewAlbumHandler(nil),
		admin:      handlers.NewAdminHandler(store, cfg.PublicOrigin),
		provenance: handlers.NewProvenanceHandler(nil, nil, time.Hour, ingest.CameraZone{}),
		checks:     handlers.NewChecksHandler(refusingPool(t), time.Hour),
		clockRules: handlers.NewCameraClockRuleHandler(nil),
		settings:   handlers.NewSettingsHandler(refusingPool(t), ingest.CameraZone{}),
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
		{"token provenance", http.MethodGet, "/api/media/" + mediaID + "/provenance", true, "", nil, http.StatusForbidden},
		{"token provenance file", http.MethodGet, "/api/media/" + mediaID + "/provenance/file", true, "", nil, http.StatusForbidden},
		{"anonymous provenance", http.MethodGet, "/api/media/" + mediaID + "/provenance", false, "", nil, http.StatusUnauthorized},
		{"anonymous provenance file", http.MethodGet, "/api/media/" + mediaID + "/provenance/file", false, "", nil, http.StatusUnauthorized},
		// No DB here either: a malformed ID is 404 from the handler's own check.
		{"session provenance, malformed id", http.MethodGet, "/api/media/" + badID + "/provenance", false, userSession, nil, http.StatusNotFound},
		{"session provenance file, malformed id", http.MethodGet, "/api/media/" + badID + "/provenance/file", false, userSession, nil, http.StatusNotFound},
		{"token checks", http.MethodGet, "/api/checks", true, "", nil, http.StatusForbidden},
		{"token checks agreement", http.MethodGet, "/api/checks/agreement", true, "", nil, http.StatusForbidden},
		{"anonymous checks", http.MethodGet, "/api/checks", false, "", nil, http.StatusUnauthorized},
		{"anonymous checks agreement", http.MethodGet, "/api/checks/agreement", false, "", nil, http.StatusUnauthorized},
		// The checks handler's pool refuses every connection: its 500 shows a session reached it,
		// and a 400 that the agreement checks from before touching the database.
		{"session checks", http.MethodGet, "/api/checks", false, userSession, nil, http.StatusInternalServerError},
		{"session checks agreement", http.MethodGet, "/api/checks/agreement", false, userSession, nil, http.StatusInternalServerError},
		{"session checks agreement, malformed from", http.MethodGet, "/api/checks/agreement?from=-1", false, userSession, nil, http.StatusBadRequest},
		{"token checks agreement, malformed from", http.MethodGet, "/api/checks/agreement?from=-1", true, "", nil, http.StatusForbidden},

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

		// Camera clock rules are open to tokens. Without a DB these statuses come from the handler's
		// input checks; the token reaching the DB is in clock_rules_routes_integration_test.go.
		{"token creates clock rule", http.MethodPost, "/api/camera-clock-rules", true, "", nil, http.StatusBadRequest},
		{"token updates clock rule, malformed id", http.MethodPut, "/api/camera-clock-rules/" + badID, true, "", nil, http.StatusNotFound},
		{"token deletes clock rule, malformed id", http.MethodDelete, "/api/camera-clock-rules/" + badID, true, "", nil, http.StatusNotFound},
		{"session updates clock rule, malformed id", http.MethodPut, "/api/camera-clock-rules/" + badID, false, userSession, nil, http.StatusNotFound},
		{"session deletes clock rule, malformed id", http.MethodDelete, "/api/camera-clock-rules/" + badID, false, userSession, nil, http.StatusNotFound},
		{"anonymous clock rules", http.MethodGet, "/api/camera-clock-rules", false, "", nil, http.StatusUnauthorized},
		{"anonymous clock rule create", http.MethodPost, "/api/camera-clock-rules", false, "", nil, http.StatusUnauthorized},
		{"anonymous clock rule update", http.MethodPut, "/api/camera-clock-rules/" + mediaID, false, "", nil, http.StatusUnauthorized},
		{"anonymous clock rule delete", http.MethodDelete, "/api/camera-clock-rules/" + mediaID, false, "", nil, http.StatusUnauthorized},
		{"anonymous cameras", http.MethodGet, "/api/cameras", false, "", nil, http.StatusUnauthorized},
		{"anonymous user directory", http.MethodGet, "/api/users", false, "", nil, http.StatusUnauthorized},

		// Settings are open to tokens. The settings handler's pool refuses every connection, so a 500
		// shows a request reached it, and a 400 that the body is checked before the database.
		{"token reads settings", http.MethodGet, "/api/me/settings", true, "", nil, http.StatusInternalServerError},
		{"session reads settings", http.MethodGet, "/api/me/settings", false, userSession, nil, http.StatusInternalServerError},
		{"token changes settings, no body", http.MethodPatch, "/api/me/settings", true, "", nil, http.StatusBadRequest},
		{"anonymous settings", http.MethodGet, "/api/me/settings", false, "", nil, http.StatusUnauthorized},
		{"anonymous settings change", http.MethodPatch, "/api/me/settings", false, "", nil, http.StatusUnauthorized},

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

func TestCameraZone(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	rules := ingest.CameraRules{{Make: "Acme", IgnoreOffsetTag: true, Why: "test"}}
	const server = ingest.CameraZoneFromServer
	tests := []struct {
		name string
		cfg  config.Config
		want ingest.CameraZone
	}{
		{"set", config.Config{CameraTimezone: warsaw, CameraTimezoneSet: true}, ingest.CameraZone{Loc: warsaw, Set: true, From: server}},
		{"set to UTC is not a fallback", config.Config{CameraTimezone: time.UTC, CameraTimezoneSet: true}, ingest.CameraZone{Loc: time.UTC, Set: true, From: server}},
		{"unset on a server with a zone", config.Config{CameraTimezone: warsaw}, ingest.CameraZone{Loc: warsaw, From: server}},
		{"unset on a UTC server", config.Config{CameraTimezone: time.UTC}, ingest.CameraZone{Loc: time.UTC, From: server, UTCFallback: true}},
		{"the camera rules come along", config.Config{CameraTimezone: warsaw, CameraTimezoneSet: true, CameraRules: rules},
			ingest.CameraZone{Loc: warsaw, Set: true, From: server, Rules: rules}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cameraZone(&tt.cfg))
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
		{"scan unknown --tz", "scan", []string{"--user", "u", "--tz", "Europe/Nowhere"}},
		{"scan --tz Local", "scan", []string{"--user", "u", "--tz", "Local"}},
		{"retime without --user", "retime", nil},
		{"retime --apply without --user", "retime", []string{"--apply"}},
		{"retime stray argument", "retime", []string{"--user", "u", "extra"}},
		{"retime unknown flag", "retime", []string{"--user", "u", "--dir", "/x"}},
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

func TestRunRetime(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	rule := &ingest.ClockRule{ID: "r-1", Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120}
	byRule := handlers.RetimeChange{
		MediaID: "m-1", Filename: "P1033627.JPG",
		Old:  time.Date(2019, 3, 13, 18, 28, 15, 0, time.UTC),
		New:  time.Date(2019, 3, 13, 19, 28, 15, 0, time.FixedZone("", 2*3600)),
		Zone: ingest.ZoneClockRule, ClockRule: rule, GPSSource: "interpolated",
	}
	byZone := handlers.RetimeChange{
		MediaID: "m-2", Filename: "IMG 0001.JPG",
		Old:  time.Date(2018, 10, 23, 11, 50, 53, 0, time.UTC),
		New:  time.Date(2018, 10, 23, 11, 50, 53, 0, time.FixedZone("", 2*3600)),
		Zone: ingest.ZoneCamera,
	}
	const line1 = "2019-03-13T18:28:15Z -> 2019-03-13T17:28:15Z  -1h0m0s  clock_rule(Panasonic DC-TZ200 UTC+02:00)  interpolated  m-1  P1033627.JPG\n"
	const line2 = "2018-10-23T11:50:53Z -> 2018-10-23T09:50:53Z  -2h0m0s  camera  -  m-2  IMG 0001.JPG\n"
	const dryRun = "dry run: nothing written; re-run with --apply\n"

	tests := []struct {
		name       string
		ctx        context.Context
		userID     string
		apply      bool
		changes    []handlers.RetimeChange
		res        handlers.RetimeResult
		retimeErr  error
		wantCalled bool
		wantOut    string
		wantErr    string
	}{
		{"dry run lists the changes", context.Background(), "active-user", false,
			[]handlers.RetimeChange{byRule, byZone}, handlers.RetimeResult{Checked: 5, Retimed: 2, Unchanged: 1, NotExif: 1, Mismatch: 1}, nil,
			true, line1 + line2 + "checked=5 retimed=2 unchanged=1 not_exif=1 mismatched=1 failed=0\n" + dryRun, ""},
		{"apply", context.Background(), "active-user", true,
			[]handlers.RetimeChange{byRule}, handlers.RetimeResult{Checked: 1, Retimed: 1}, nil,
			true, line1 + "checked=1 retimed=1 unchanged=0 not_exif=0 mismatched=0 failed=0\n", ""},
		{"nothing to change", context.Background(), "active-user", true,
			nil, handlers.RetimeResult{Checked: 3, Unchanged: 3}, nil,
			true, "checked=3 retimed=0 unchanged=3 not_exif=0 mismatched=0 failed=0\n", ""},
		{"failed items exit non-zero", context.Background(), "active-user", true,
			nil, handlers.RetimeResult{Checked: 2, Unchanged: 1, Failed: 1}, nil,
			true, "checked=2 retimed=0 unchanged=1 not_exif=0 mismatched=0 failed=1\n", "1 item(s) failed"},
		{"a failed dry run still says nothing was written", context.Background(), "active-user", false,
			nil, handlers.RetimeResult{Checked: 1, Failed: 1}, nil,
			true, "checked=1 retimed=0 unchanged=0 not_exif=0 mismatched=0 failed=1\n" + dryRun, "1 item(s) failed"},
		{"retime error", context.Background(), "active-user", true,
			nil, handlers.RetimeResult{}, errors.New("load clock rules: boom"),
			true, "checked=0 retimed=0 unchanged=0 not_exif=0 mismatched=0 failed=0\n", "load clock rules: boom"},
		{"interrupted", cancelled, "active-user", true,
			[]handlers.RetimeChange{byRule}, handlers.RetimeResult{Checked: 2, Retimed: 1}, context.Canceled,
			true, line1 + "checked=2 retimed=1 unchanged=0 not_exif=0 mismatched=0 failed=0\n", "interrupted; re-run retime to finish"},
		{"unclaimed owner of pre-auth data accepted", context.Background(), auth.LegacyOwnerID, true,
			nil, handlers.RetimeResult{}, nil,
			true, "checked=0 retimed=0 unchanged=0 not_exif=0 mismatched=0 failed=0\n", ""},
		{"disabled user accepted", context.Background(), "disabled-user", true,
			nil, handlers.RetimeResult{}, nil,
			true, "checked=0 retimed=0 unchanged=0 not_exif=0 mismatched=0 failed=0\n", ""},
		{"unknown user refused before reading anything", context.Background(), "nobody", true,
			nil, handlers.RetimeResult{}, nil,
			false, "", `unknown user "nobody"`},
		{"uppercase id normalised", context.Background(), "ACTIVE-USER", true,
			nil, handlers.RetimeResult{}, nil,
			true, "checked=0 retimed=0 unchanged=0 not_exif=0 mismatched=0 failed=0\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			retime := func(_ context.Context, ownerID string, report func(handlers.RetimeChange)) (handlers.RetimeResult, error) {
				called = true
				assert.Equal(t, strings.ToLower(tt.userID), ownerID)
				for _, c := range tt.changes {
					report(c)
				}
				return tt.res, tt.retimeErr
			}
			var out bytes.Buffer
			err := runRetime(tt.ctx, &out, retime, scanUsers(), tt.userID, tt.apply)
			assert.Equal(t, tt.wantCalled, called)
			assert.Equal(t, tt.wantOut, out.String())
			if tt.wantErr == "" {
				assert.NoError(t, err, "exit 0")
				return
			}
			require.Error(t, err, "runSubcommand exits 1 on an error from run")
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestFormatRetimeChange(t *testing.T) {
	old := time.Date(2022, 8, 6, 14, 33, 49, 0, time.UTC)
	tests := []struct {
		name   string
		change handlers.RetimeChange
		want   string
	}{
		{"the GPS clock",
			handlers.RetimeChange{MediaID: "m", Filename: "tokyo.jpg", Old: old, New: old.Add(-9 * time.Hour), Zone: ingest.ZoneGPS, GPSSource: "exif"},
			"2022-08-06T14:33:49Z -> 2022-08-06T05:33:49Z  -9h0m0s  gps  exif  m  tokyo.jpg"},
		{"a negative offset in a rule, and a shift with seconds",
			handlers.RetimeChange{MediaID: "m", Filename: "a.mp4", Old: old, New: old.Add(3*time.Hour + 30*time.Minute - time.Second), Zone: ingest.ZoneClockRule,
				ClockRule: &ingest.ClockRule{Make: "Arashi Vision", Model: "Insta360 OneRS", OffsetMin: -210}},
			"2022-08-06T14:33:49Z -> 2022-08-06T18:03:48Z  +3h29m59s  clock_rule(Arashi Vision Insta360 OneRS UTC-03:30)  -  m  a.mp4"},
		{"a clock_rule zone without its rule still prints the zone",
			handlers.RetimeChange{MediaID: "m", Filename: "a.jpg", Old: old, New: old.Add(time.Hour), Zone: ingest.ZoneClockRule},
			"2022-08-06T14:33:49Z -> 2022-08-06T15:33:49Z  +1h0m0s  clock_rule  -  m  a.jpg"},
		{"a file name with a line break is quoted",
			handlers.RetimeChange{MediaID: "m", Filename: "a\nchecked=0.jpg", Old: old, New: old.Add(-time.Hour), Zone: ingest.ZoneOffset, GPSSource: "manual"},
			`2022-08-06T14:33:49Z -> 2022-08-06T13:33:49Z  -1h0m0s  offset  manual  m  "a\nchecked=0.jpg"`},
		{"a terminal escape is quoted",
			handlers.RetimeChange{MediaID: "m", Filename: "\x1b[2Ja.jpg", Old: old, New: old.Add(-time.Hour), Zone: ingest.ZoneCamera},
			`2022-08-06T14:33:49Z -> 2022-08-06T13:33:49Z  -1h0m0s  camera  -  m  "\x1b[2Ja.jpg"`},
		{"a rule's camera with a control character is quoted",
			handlers.RetimeChange{MediaID: "m", Filename: "a.jpg", Old: old, New: old.Add(-time.Hour), Zone: ingest.ZoneClockRule,
				ClockRule: &ingest.ClockRule{Make: "Acme\r", Model: "X 1", OffsetMin: 0}},
			`2022-08-06T14:33:49Z -> 2022-08-06T13:33:49Z  -1h0m0s  clock_rule("Acme\r" X 1 UTC+00:00)  -  m  a.jpg`},
		{"a stored time with milliseconds keeps them",
			handlers.RetimeChange{MediaID: "m", Filename: "a.jpg", Old: old.Add(123 * time.Millisecond), New: old.Add(-2 * time.Hour), Zone: ingest.ZoneCamera},
			"2022-08-06T14:33:49.123Z -> 2022-08-06T12:33:49Z  -2h0m0.123s  camera  -  m  a.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatRetimeChange(tt.change))
		})
	}
}

func TestFormatShift(t *testing.T) {
	assert.Equal(t, "-1h0m0s", formatShift(-time.Hour))
	assert.Equal(t, "+2h0m1s", formatShift(2*time.Hour+time.Second))
	assert.Equal(t, "-5h30m0s", formatShift(-(5*time.Hour + 30*time.Minute)))
	assert.Equal(t, "0s", formatShift(0))
}

func TestFormatUTCOffset(t *testing.T) {
	tests := []struct {
		minutes int
		want    string
	}{
		{120, "UTC+02:00"},
		{0, "UTC+00:00"},
		{-210, "UTC-03:30"},
		{345, "UTC+05:45"},
		{840, "UTC+14:00"},
		{-720, "UTC-12:00"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, formatUTCOffset(tt.minutes), tt.minutes)
	}
}

func TestRunScan(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	// The scan's time report is printed as it comes, after the counts, whatever the outcome.
	report := handlers.TimeReport{CameraZone: "Europe/Warsaw", CameraZoneFrom: "server", FileDateNames: []string{}}
	const reportOut = "by_source: exif_original=0 exif_ifd0=0 container=0 mtime=0 none=0\n" +
		"by_zone: gps=0 clock_rule=0 offset=0 camera=0 camera_zone=\"Europe/Warsaw\" camera_zone_from=server\n"
	everySource := handlers.TimeReport{
		BySource:       handlers.TimeSourceCounts{ExifOriginal: 6, ExifIFD0: 4, Container: 3, Mtime: 2, None: 1},
		ByZone:         handlers.TimeZoneCounts{GPS: 1, ClockRule: 2, Offset: 3, Camera: 4},
		CameraZone:     "Asia/Tokyo",
		CameraZoneFrom: "batch",
		FileDateNames:  []string{"a.jpg", "b.jpg"},
	}
	assumedUTC := handlers.TimeReport{
		BySource:         handlers.TimeSourceCounts{ExifOriginal: 1},
		ByZone:           handlers.TimeZoneCounts{Camera: 1},
		CameraZone:       "server zone (unset)",
		CameraZoneFrom:   "server",
		AssumedZoneIsUTC: true,
		FileDateNames:    []string{},
	}
	userUTC := handlers.TimeReport{
		BySource:         handlers.TimeSourceCounts{ExifOriginal: 1},
		ByZone:           handlers.TimeZoneCounts{Camera: 1},
		CameraZone:       "UTC",
		CameraZoneFrom:   "user",
		AssumedZoneIsUTC: true,
		FileDateNames:    []string{},
	}

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
		{"success", context.Background(), "active-user", handlers.ScanResult{Ingested: 2, Skipped: 1, TimeReport: report}, nil,
			true, "ingested=2 skipped=1 failed=0\n" + reportOut, ""},
		{"failed files exit non-zero", context.Background(), "active-user", handlers.ScanResult{Ingested: 1, Failed: 1, TimeReport: report}, nil,
			true, "ingested=1 skipped=0 failed=1\n" + reportOut, "1 file(s) failed"},
		{"scan error", context.Background(), "active-user", handlers.ScanResult{TimeReport: report}, errors.New("gps matcher: boom"),
			true, "ingested=0 skipped=0 failed=0\n" + reportOut, "gps matcher: boom"},
		{"interrupted", cancelled, "active-user", handlers.ScanResult{Ingested: 1, TimeReport: report}, context.Canceled,
			true, "ingested=1 skipped=0 failed=0\n" + reportOut, "interrupted; re-run scan to finish"},
		{"unclaimed user refused before scanning", context.Background(), auth.LegacyOwnerID, handlers.ScanResult{}, nil,
			false, "", "user is unclaimed"},
		{"uppercase id normalised", context.Background(), "ACTIVE-USER", handlers.ScanResult{Ingested: 1, TimeReport: report}, nil,
			true, "ingested=1 skipped=0 failed=0\n" + reportOut, ""},
		{"every source and zone rule counted", context.Background(), "active-user", handlers.ScanResult{Ingested: 16, TimeReport: everySource}, nil,
			true, "ingested=16 skipped=0 failed=0\n" +
				"by_source: exif_original=6 exif_ifd0=4 container=3 mtime=2 none=1\n" +
				"by_zone: gps=1 clock_rule=2 offset=3 camera=4 camera_zone=\"Asia/Tokyo\" camera_zone_from=batch\n", ""},
		{"a camera zone at UTC all year warns", context.Background(), "ACTIVE-USER", handlers.ScanResult{Ingested: 1, TimeReport: assumedUTC}, nil,
			true, "ingested=1 skipped=0 failed=0\n" +
				"by_source: exif_original=1 exif_ifd0=0 container=0 mtime=0 none=0\n" +
				"by_zone: gps=0 clock_rule=0 offset=0 camera=1 camera_zone=\"server zone (unset)\" camera_zone_from=server\n" +
				"warning: the EXIF times of 1 file(s) were read in camera zone \"server zone (unset)\", which is UTC all year. " +
				"If your camera clocks show another zone, set CAMERA_TIMEZONE to it (e.g. Europe/Warsaw) or the user's camera zone in Settings, " +
				"then run `server retime --user active-user` to list the times that would move, and again with --apply to move them.\n", ""},
		{"the user's camera zone at UTC warns, naming Settings", context.Background(), "active-user", handlers.ScanResult{Ingested: 1, TimeReport: userUTC}, nil,
			true, "ingested=1 skipped=0 failed=0\n" +
				"by_source: exif_original=1 exif_ifd0=0 container=0 mtime=0 none=0\n" +
				"by_zone: gps=0 clock_rule=0 offset=0 camera=1 camera_zone=\"UTC\" camera_zone_from=user\n" +
				"warning: the EXIF times of 1 file(s) were read in camera zone \"UTC\", which is UTC all year. " +
				"If your camera clocks show another zone, change the user's camera zone in Settings, " +
				"then run `server retime --user active-user` to list the times that would move, and again with --apply to move them.\n", ""},
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
