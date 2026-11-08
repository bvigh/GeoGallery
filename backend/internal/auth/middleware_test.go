package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAuth(store Store, p *Provider) *Auth {
	return New(store, p, &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720})
}

type seen struct {
	called bool
	user   User
	method Method
}

func capture(s *seen) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.called = true
		s.user, _ = UserFrom(r.Context())
		s.method = MethodFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireUser(t *testing.T) {
	store := newFakeStore()
	alice := store.addUser("iss", "alice", "alice@example.test", "user", false)
	disabled := store.addUser("iss", "dis", "d@example.test", "user", true)
	unclaimed := store.addUser("", "", "", "admin", false)

	validSession := store.addSession(alice.ID, time.Now().Add(time.Hour))
	expiredSession := store.addSession(alice.ID, time.Now().Add(-time.Minute))
	disabledSession := store.addSession(disabled.ID, time.Now().Add(time.Hour))
	validToken, validTokenID := store.addAPIToken(alice.ID, time.Now().Add(time.Hour), false)
	revokedToken, _ := store.addAPIToken(alice.ID, time.Now().Add(time.Hour), true)
	expiredToken, _ := store.addAPIToken(alice.ID, time.Now().Add(-time.Minute), false)
	disabledToken, _ := store.addAPIToken(disabled.ID, time.Now().Add(time.Hour), false)
	unclaimedToken, _ := store.addAPIToken(unclaimed.ID, time.Now().Add(time.Hour), false)

	tests := []struct {
		name       string
		auth       string
		cookie     string
		wantStatus int
		wantMethod Method
	}{
		{"no cookie", "", "", http.StatusUnauthorized, MethodNone},
		{"unknown session", "", "nope", http.StatusUnauthorized, MethodNone},
		{"expired session", "", expiredSession, http.StatusUnauthorized, MethodNone},
		{"disabled user session", "", disabledSession, http.StatusUnauthorized, MethodNone},
		{"valid session", "", validSession, http.StatusOK, MethodSession},
		{"valid token", "Bearer " + validToken, "", http.StatusOK, MethodToken},
		{"lowercase bearer", "bearer " + validToken, "", http.StatusOK, MethodToken},
		{"uppercase bearer", "BEARER " + validToken, "", http.StatusOK, MethodToken},
		{"unknown token", "Bearer gg_pat_unknown", "", http.StatusUnauthorized, MethodNone},
		{"revoked token", "Bearer " + revokedToken, "", http.StatusUnauthorized, MethodNone},
		{"expired token", "Bearer " + expiredToken, "", http.StatusUnauthorized, MethodNone},
		{"disabled user token", "Bearer " + disabledToken, "", http.StatusUnauthorized, MethodNone},
		{"unclaimed user token", "Bearer " + unclaimedToken, "", http.StatusUnauthorized, MethodNone},
		{"bearer junk", "Bearer junk", "", http.StatusUnauthorized, MethodNone},
		{"empty bearer", "Bearer ", "", http.StatusUnauthorized, MethodNone},
		// net/http trims "Bearer " on the wire to "Bearer".
		{"bare bearer with valid cookie: no fallback", "Bearer", validSession, http.StatusUnauthorized, MethodNone},
		{"bearer tab token", "Bearer\t" + validToken, "", http.StatusOK, MethodToken},
		{"bearer tab junk with valid cookie: no fallback", "Bearer\tjunk", validSession, http.StatusUnauthorized, MethodNone},
		{"bearerish scheme with valid cookie", "Bearerx " + validToken, validSession, http.StatusOK, MethodSession},
		{"bearer junk with valid cookie: no fallback", "Bearer junk", validSession, http.StatusUnauthorized, MethodNone},
		{"bearer unknown token with valid cookie: no fallback", "Bearer gg_pat_unknown", validSession, http.StatusUnauthorized, MethodNone},
		{"basic with valid cookie", "Basic dXNlcjpwYXNz", validSession, http.StatusOK, MethodSession},
		{"basic without cookie", "Basic dXNlcjpwYXNz", "", http.StatusUnauthorized, MethodNone},
	}
	a := newTestAuth(store, nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s seen
			req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "gg_session", Value: tt.cookie})
			}
			rec := httptest.NewRecorder()
			a.RequireUser(capture(&s)).ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantStatus == http.StatusOK, s.called)
			if s.called {
				assert.Equal(t, tt.wantMethod, s.method)
				assert.Equal(t, alice.ID, s.user.ID)
			}
		})
	}

	t.Run("valid token is touched", func(t *testing.T) {
		store.touched = nil
		req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		a.RequireUser(capture(&seen{})).ServeHTTP(httptest.NewRecorder(), req)
		assert.Equal(t, []string{validTokenID}, store.touched)
	})
}

func TestRequireUser_SecureCookieName(t *testing.T) {
	store := newFakeStore()
	alice := store.addUser("iss", "alice", "a@example.test", "user", false)
	raw := store.addSession(alice.ID, time.Now().Add(time.Hour))
	a := New(store, nil, &config.Config{PublicOrigin: "https://g.example.com", CookieSecure: true, SessionTTLHours: 1})

	for _, tt := range []struct {
		cookie string
		want   int
	}{{"__Host-gg_session", http.StatusOK}, {"gg_session", http.StatusUnauthorized}} {
		t.Run(tt.cookie, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
			req.AddCookie(&http.Cookie{Name: tt.cookie, Value: raw})
			rec := httptest.NewRecorder()
			a.RequireUser(capture(&seen{})).ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestRequireUser_StoreErrors(t *testing.T) {
	store := newFakeStore()
	alice := store.addUser("iss", "alice", "a@example.test", "user", false)
	session := store.addSession(alice.ID, time.Now().Add(time.Hour))
	token, _ := store.addAPIToken(alice.ID, time.Now().Add(time.Hour), false)
	a := newTestAuth(store, nil)

	t.Run("touch error does not fail the request", func(t *testing.T) {
		store.touchErr = errors.New("db down")
		defer func() { store.touchErr = nil }()
		var s seen
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		a.RequireUser(capture(&s)).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, MethodToken, s.method)
	})

	store.err = errors.New("db down")
	for _, tt := range []struct {
		name   string
		auth   string
		cookie string
	}{{"session lookup error", "", session}, {"token lookup error", "Bearer " + token, ""}} {
		t.Run(tt.name, func(t *testing.T) {
			var s seen
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "gg_session", Value: tt.cookie})
			}
			rec := httptest.NewRecorder()
			a.RequireUser(capture(&s)).ServeHTTP(rec, req)
			assert.Equal(t, http.StatusInternalServerError, rec.Code)
			assert.False(t, s.called)
		})
	}
}

func TestRequireSessionAndAdmin(t *testing.T) {
	admin := User{ID: "a", Role: "admin"}
	user := User{ID: "u", Role: "user"}
	tests := []struct {
		name       string
		withUser   bool
		user       User
		method     Method
		mw         func(http.Handler) http.Handler
		wantStatus int
	}{
		{"session ok", true, user, MethodSession, RequireSession, http.StatusOK},
		{"token rejected", true, user, MethodToken, RequireSession, http.StatusForbidden},
		{"no method rejected", true, user, MethodNone, RequireSession, http.StatusForbidden},
		{"no user rejected", false, User{}, MethodNone, RequireSession, http.StatusForbidden},
		{"admin ok", true, admin, MethodSession, RequireAdmin, http.StatusOK},
		{"non-admin rejected", true, user, MethodSession, RequireAdmin, http.StatusForbidden},
		{"admin without user rejected", false, User{}, MethodNone, RequireAdmin, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s seen
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.withUser {
				req = req.WithContext(WithUser(req.Context(), tt.user, tt.method))
			}
			rec := httptest.NewRecorder()
			tt.mw(capture(&s)).ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantStatus == http.StatusOK, s.called)
		})
	}
}

func TestMustUser_PanicsWithoutUser(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Panics(t, func() { MustUser(req.Context()) })
	u := User{ID: "x"}
	require.Equal(t, u, MustUser(WithUser(req.Context(), u, MethodSession)))
	assert.Equal(t, MethodNone, MethodFrom(req.Context()))
}
