package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth/oidctest"
	"codeberg.org/bvigh/geogallery/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type loginEnv struct {
	iss   *oidctest.Issuer
	store Store
	fake  *fakeStore
	cfg   *config.Config
	a     *Auth
}

func newLoginEnv(t *testing.T, mutate func(i *oidctest.Issuer, cfg *config.Config)) *loginEnv {
	t.Helper()
	iss := oidctest.New(t)
	cfg := providerConfig(iss)
	if mutate != nil {
		iss.Set(func(i *oidctest.Issuer) { mutate(i, cfg) })
	}
	p, err := NewProvider(context.Background(), cfg)
	require.NoError(t, err)
	fake := newFakeStore()
	return &loginEnv{iss: iss, store: fake, fake: fake, cfg: cfg, a: New(fake, p, cfg)}
}

func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// startLogin calls Login and returns its recorder and the parsed authorize URL.
func (e *loginEnv) startLogin(t *testing.T, query string) (*httptest.ResponseRecorder, *url.URL) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.a.Login(rec, httptest.NewRequest(http.MethodGet, "/api/auth/login?"+query, nil))
	require.Equal(t, http.StatusFound, rec.Code)
	authURL, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(authURL.String(), e.iss.URL+"/authorize"), authURL.String())
	return rec, authURL
}

// login runs Login → issuer /authorize → Callback, following Location and cookies by hand.
func (e *loginEnv) login(t *testing.T, query string, extra ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	loginRec, authURL := e.startLogin(t, query)
	resp, err := noRedirect.Get(authURL.String())
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	cb, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "/api/auth/callback", cb.Path)

	req := httptest.NewRequest(http.MethodGet, cb.RequestURI(), nil)
	if c := findCookie(loginRec, e.a.loginStateName); c != nil {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	for _, c := range extra {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.a.Callback(rec, req)
	return rec
}

func sessionValue(rec *httptest.ResponseRecorder) string {
	if c := findCookie(rec, "gg_session"); c != nil && c.MaxAge > 0 {
		return c.Value
	}
	return ""
}

func TestLogin_StateCookieAndAuthorizeURL(t *testing.T) {
	e := newLoginEnv(t, nil)
	rec, authURL := e.startLogin(t, "return_to=%2Ftrips")
	q := authURL.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.NotEmpty(t, q.Get("code_challenge"))
	assert.NotEmpty(t, q.Get("nonce"))
	assert.NotEmpty(t, q.Get("state"))
	assert.Equal(t, "http://localhost:5173/api/auth/callback", q.Get("redirect_uri"))
	assert.Equal(t, "openid profile email", q.Get("scope"))

	c := findCookie(rec, loginStateCookie)
	require.NotNil(t, c)
	assert.Equal(t, "/api/auth", c.Path)
	assert.Equal(t, 600, c.MaxAge)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.False(t, c.Secure)
}

func TestLogin_Prompt(t *testing.T) {
	tests := []struct {
		query      string
		wantPrompt string
	}{
		{"", ""},
		{"prompt=login", "login"},
		{"prompt=select_account", "select_account"},
		{"prompt=none", ""},
		{"prompt=consent", ""},
		{"invite=abc", "login"},
		{"invite=abc&prompt=select_account", "login"},
		{"invite=abc&prompt=none", "login"},
	}
	e := newLoginEnv(t, nil)
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			_, authURL := e.startLogin(t, tt.query)
			assert.Equal(t, tt.wantPrompt, authURL.Query().Get("prompt"))
			_, has := authURL.Query()["prompt"]
			assert.Equal(t, tt.wantPrompt != "", has)
		})
	}
}

func TestCallback_Outcomes(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(i *oidctest.Issuer, cfg *config.Config)
		seed         func(e *loginEnv) (query string)
		wantStatus   int
		wantLocation string
		wantSession  bool
		check        func(t *testing.T, e *loginEnv)
	}{
		{
			name:         "unknown user without invite",
			wantStatus:   http.StatusFound,
			wantLocation: "/?auth_error=not_invited",
		},
		{
			name: "known user",
			seed: func(e *loginEnv) string {
				e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)
				return "return_to=%2Ftrips%3Fx%3D1"
			},
			wantStatus: http.StatusFound, wantLocation: "/trips?x=1", wantSession: true,
		},
		{
			name: "disabled user",
			seed: func(e *loginEnv) string {
				e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", true)
				return ""
			},
			wantStatus: http.StatusFound, wantLocation: "/?auth_error=disabled",
		},
		{
			name: "same sub under another issuer is a different identity",
			seed: func(e *loginEnv) string {
				e.fake.addUser("https://other.example.com", "user-sub-1", "", "user", false)
				return ""
			},
			wantStatus: http.StatusFound, wantLocation: "/?auth_error=not_invited",
		},
		{
			name: "valid invite registers new user",
			seed: func(e *loginEnv) string {
				return "invite=" + e.fake.addInvite("admin", "", time.Now().Add(time.Hour))
			},
			wantStatus: http.StatusFound, wantLocation: "/", wantSession: true,
			check: func(t *testing.T, e *loginEnv) {
				require.Len(t, e.fake.users, 1)
				require.Len(t, e.fake.invites, 1)
				for _, u := range e.fake.users {
					assert.Equal(t, "admin", u.Role, "role comes from the invite")
					for _, inv := range e.fake.invites {
						assert.NotNil(t, inv.usedAt, "invite consumed")
						assert.Equal(t, u.ID, inv.usedBy)
					}
				}
			},
		},
		{
			name: "claim invite for a disabled unclaimed user",
			seed: func(e *loginEnv) string {
				owner := e.fake.addUser("", "", "", "admin", true)
				return "invite=" + e.fake.addInvite("admin", owner.ID, time.Now().Add(time.Hour))
			},
			wantStatus: http.StatusFound, wantLocation: "/?auth_error=invite_invalid",
			check: func(t *testing.T, e *loginEnv) {
				for _, inv := range e.fake.invites {
					assert.Nil(t, inv.usedAt, "invite not consumed")
				}
				for _, u := range e.fake.users {
					assert.False(t, u.Claimed)
				}
			},
		},
		{
			name: "invite created by a disabled admin",
			seed: func(e *loginEnv) string {
				creator := e.fake.addUser(e.cfg.OIDCIssuerURL, "admin-sub", "", "admin", true)
				raw := e.fake.addInvite("user", "", time.Now().Add(time.Hour))
				e.fake.invites[string(hashToken(raw))].CreatedBy = creator.ID
				return "invite=" + raw
			},
			wantStatus: http.StatusFound, wantLocation: "/?auth_error=invite_invalid",
			check: func(t *testing.T, e *loginEnv) {
				assert.Len(t, e.fake.users, 1, "no user registered")
			},
		},
		{
			name: "invite revoked by disabling its creator",
			seed: func(e *loginEnv) string {
				creator := e.fake.addUser(e.cfg.OIDCIssuerURL, "admin-sub", "", "admin", false)
				raw := e.fake.addInvite("user", "", time.Now().Add(time.Hour))
				e.fake.invites[string(hashToken(raw))].CreatedBy = creator.ID
				if err := e.fake.SetDisabled(context.Background(), creator.ID, true); err != nil {
					panic(err)
				}
				return "invite=" + raw
			},
			wantStatus: http.StatusFound, wantLocation: "/?auth_error=invite_invalid",
			check: func(t *testing.T, e *loginEnv) {
				assert.Len(t, e.fake.users, 1, "no user registered")
				for _, inv := range e.fake.invites {
					assert.NotNil(t, inv.usedAt, "SetDisabled revoked the invite")
					assert.Empty(t, inv.usedBy)
				}
			},
		},
		{
			name: "expired invite",
			seed: func(e *loginEnv) string {
				return "invite=" + e.fake.addInvite("user", "", time.Now().Add(-time.Hour))
			},
			wantStatus: http.StatusFound, wantLocation: "/?auth_error=invite_invalid",
		},
		{
			name:       "unknown invite",
			seed:       func(e *loginEnv) string { return "invite=bogus" },
			wantStatus: http.StatusFound, wantLocation: "/?auth_error=invite_invalid",
		},
		{
			name:       "nonce mismatch",
			mutate:     func(i *oidctest.Issuer, _ *config.Config) { i.WrongNonce = true },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "empty sub",
			mutate: func(i *oidctest.Issuer, _ *config.Config) { i.Sub = "" },
			seed: func(e *loginEnv) string {
				return "invite=" + e.fake.addInvite("user", "", time.Now().Add(time.Hour))
			},
			wantStatus: http.StatusBadRequest,
			check: func(t *testing.T, e *loginEnv) {
				assert.Empty(t, e.fake.users)
			},
		},
		{
			name:       "bad signature",
			mutate:     func(i *oidctest.Issuer, _ *config.Config) { i.WrongSigningKey = true },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "wrong token issuer",
			mutate:     func(i *oidctest.Issuer, _ *config.Config) { i.TokenIssuer = "https://evil.example.com" },
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLoginEnv(t, tt.mutate)
			query := ""
			if tt.seed != nil {
				query = tt.seed(e)
			}
			rec := e.login(t, query)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantLocation != "" {
				assert.Equal(t, tt.wantLocation, rec.Header().Get("Location"))
			}
			raw := sessionValue(rec)
			if tt.wantSession {
				require.NotEmpty(t, raw)
				assert.True(t, e.fake.hasSession(raw))
				assert.Equal(t, 1, e.fake.sessionCount())
				if c := findCookie(rec, loginStateCookie); assert.NotNil(t, c) {
					assert.Negative(t, c.MaxAge, "gg_oidc cleared")
				}
			} else {
				assert.Empty(t, raw)
				assert.Equal(t, 0, e.fake.sessionCount())
			}
			if tt.check != nil {
				tt.check(t, e)
			}
		})
	}
}

func TestCallback_SecureCookies(t *testing.T) {
	e := newLoginEnv(t, func(_ *oidctest.Issuer, cfg *config.Config) { cfg.CookieSecure = true })
	e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)

	assertHostStateCookie := func(t *testing.T, rec *httptest.ResponseRecorder, wantMaxAge func(int) bool) {
		t.Helper()
		assert.Nil(t, findCookie(rec, loginStateCookie))
		c := findCookie(rec, "__Host-gg_oidc")
		require.NotNil(t, c)
		assert.True(t, c.Secure)
		assert.True(t, c.HttpOnly)
		assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
		assert.Equal(t, "/", c.Path)
		assert.Empty(t, c.Domain)
		assert.True(t, wantMaxAge(c.MaxAge), "MaxAge %d", c.MaxAge)
	}

	loginRec, _ := e.startLogin(t, "")
	assertHostStateCookie(t, loginRec, func(m int) bool { return m == loginStateMaxAge })

	rec := e.login(t, "")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Nil(t, findCookie(rec, "gg_session"))
	c := findCookie(rec, "__Host-gg_session")
	require.NotNil(t, c)
	assert.True(t, c.Secure)
	assert.Equal(t, "/", c.Path)
	assert.Empty(t, c.Domain)
	assert.True(t, e.fake.hasSession(c.Value))
	assertHostStateCookie(t, rec, func(m int) bool { return m < 0 })

	e.iss.Set(func(i *oidctest.Issuer) { i.Sub = "stranger" })
	rejected := e.login(t, "")
	require.Equal(t, "/?auth_error=not_invited", rejected.Header().Get("Location"))
	assertHostStateCookie(t, rejected, func(m int) bool { return m < 0 })
}

func TestCallback_UserDisabledBeforeSessionInsert(t *testing.T) {
	e := newLoginEnv(t, nil)
	e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)
	e.fake.afterTouchLogin = func(u *fakeUser) { u.Disabled = true }

	rec := e.login(t, "")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/?auth_error=disabled", rec.Header().Get("Location"))
	assert.Empty(t, sessionValue(rec))
	assert.Zero(t, e.fake.sessionCount())
	if c := findCookie(rec, loginStateCookie); assert.NotNil(t, c) {
		assert.Negative(t, c.MaxAge)
	}
}

func TestCallback_StateChecks(t *testing.T) {
	e := newLoginEnv(t, nil)
	encode := func(st loginState) string {
		rec := httptest.NewRecorder()
		e.a.writeLoginState(rec, st)
		return findCookie(rec, loginStateCookie).Value
	}
	tests := []struct {
		name   string
		cookie string
		query  string
	}{
		{"no state cookie", "", "state=abc&code=x"},
		{"state mismatch", encode(loginState{State: "abc", Nonce: "n", Verifier: "v"}), "state=xyz&code=x"},
		{"empty state in cookie and query", encode(loginState{State: "", Nonce: "n", Verifier: "v"}), "state=&code=x"},
		{"empty state, no query state", encode(loginState{Nonce: "n"}), "code=x"},
		{"garbage cookie", "!!!", "state=abc&code=x"},
		{"missing code", encode(loginState{State: "abc", Nonce: "n", Verifier: "v"}), "state=abc"},
		{"idp error", encode(loginState{State: "abc", Nonce: "n", Verifier: "v"}), "state=abc&error=access_denied"},
		{"bogus code", encode(loginState{State: "abc", Nonce: "n", Verifier: "v"}), "state=abc&code=bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?"+tt.query, nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: loginStateCookie, Value: tt.cookie})
			}
			rec := httptest.NewRecorder()
			e.a.Callback(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Empty(t, sessionValue(rec))
		})
	}
}

func TestCallback_ReturnTo(t *testing.T) {
	tests := []struct {
		returnTo string
		want     string
	}{
		{"/trips", "/trips"},
		{"//evil.com", "/"},
		{"/%09/evil.com", "/"},
		{"/%0a/evil.com", "/"},
		{"/\\evil.com", "/"},
		{"/%5Cevil.com", "/"},
		{"https://evil.com", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.returnTo, func(t *testing.T) {
			e := newLoginEnv(t, nil)
			e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)
			// The raw value is placed in the query as-is so that %09/%0a/%5C decode to the
			// control byte / backslash the browser would send.
			rec := e.login(t, "return_to="+strings.ReplaceAll(tt.returnTo, "\\", "%5C"))
			require.Equal(t, http.StatusFound, rec.Code)
			assert.Equal(t, tt.want, rec.Header().Get("Location"))
		})
	}
}

func TestCallback_ReturnToRevalidatedFromCookie(t *testing.T) {
	e := newLoginEnv(t, nil)
	e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)
	loginRec, authURL := e.startLogin(t, "")
	st, err := e.readLoginStateFromRecorder(loginRec)
	require.NoError(t, err)
	st.ReturnTo = "//evil.com" // gg_oidc is unsigned: a tampered cookie must still be safe
	tampered := httptest.NewRecorder()
	e.a.writeLoginState(tampered, st)

	resp, err := noRedirect.Get(authURL.String())
	require.NoError(t, err)
	resp.Body.Close()
	cb, _ := url.Parse(resp.Header.Get("Location"))
	req := httptest.NewRequest(http.MethodGet, cb.RequestURI(), nil)
	req.AddCookie(findCookie(tampered, loginStateCookie))
	rec := httptest.NewRecorder()
	e.a.Callback(rec, req)
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))
}

func (e *loginEnv) readLoginStateFromRecorder(rec *httptest.ResponseRecorder) (loginState, error) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(findCookie(rec, e.a.loginStateName))
	return e.a.readLoginState(req)
}

func TestCallback_StoresConfiguredIssuer(t *testing.T) {
	// Google may put "accounts.google.com" (no scheme) in iss; go-oidc accepts that only for
	// Google. Users must still be keyed by the configured issuer.
	const google = "https://accounts.google.com"
	e := newLoginEnv(t, func(i *oidctest.Issuer, cfg *config.Config) {
		i.DiscoveryIssuer = google
		i.TokenIssuer = "accounts.google.com"
		cfg.OIDCIssuerURL = google
	})
	invite := e.fake.addInvite("user", "", time.Now().Add(time.Hour))
	rec := e.login(t, "invite="+invite)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	require.NotEmpty(t, sessionValue(rec))
	require.NotEmpty(t, e.fake.issuerArg)
	for _, got := range e.fake.issuerArg {
		assert.Equal(t, google, got)
	}
	for _, u := range e.fake.users {
		assert.Equal(t, google, u.issuer)
	}
}

func TestCallback_ClaimInviteBindsExistingUser(t *testing.T) {
	e := newLoginEnv(t, nil)
	owner := e.fake.addUser("", "", "", "admin", false)
	invite := e.fake.addInvite("admin", owner.ID, time.Now().Add(time.Hour))
	rec := e.login(t, "invite="+invite)
	require.Equal(t, http.StatusFound, rec.Code)
	require.Len(t, e.fake.users, 1)
	u := e.fake.users[owner.ID]
	assert.Equal(t, e.cfg.OIDCIssuerURL, u.issuer)
	assert.Equal(t, "user-sub-1", u.subject)
	assert.Equal(t, "alice@example.test", u.Email)
	assert.Equal(t, "Alice Test", u.DisplayName)
	assert.NotNil(t, u.LastLoginAt)
}

func TestCallback_UserInfoFallback(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(i *oidctest.Issuer, _ *config.Config)
		wantEmail string
		wantName  string
	}{
		{
			name:      "profile claims in id token",
			mutate:    func(i *oidctest.Issuer, _ *config.Config) {},
			wantEmail: "alice@example.test", wantName: "Alice Test",
		},
		{
			name:      "empty email filled from userinfo",
			mutate:    func(i *oidctest.Issuer, _ *config.Config) { i.OmitProfileClaims = true },
			wantEmail: "alice@example.test", wantName: "Alice Test",
		},
		{
			name: "userinfo subject mismatch ignored",
			mutate: func(i *oidctest.Issuer, _ *config.Config) {
				i.OmitProfileClaims = true
				i.UserInfoSub = "someone-else"
			},
			wantEmail: "", wantName: "",
		},
		{
			name: "display name falls back to preferred_username",
			mutate: func(i *oidctest.Issuer, _ *config.Config) {
				i.Name = ""
			},
			wantEmail: "alice@example.test", wantName: "alice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLoginEnv(t, tt.mutate)
			invite := e.fake.addInvite("user", "", time.Now().Add(time.Hour))
			rec := e.login(t, "invite="+invite)
			require.Equal(t, http.StatusFound, rec.Code)
			require.Len(t, e.fake.users, 1)
			for _, u := range e.fake.users {
				assert.Equal(t, tt.wantEmail, u.Email)
				assert.Equal(t, tt.wantName, u.DisplayName)
			}
		})
	}
}

func TestCallback_RotatesPreviousSession(t *testing.T) {
	e := newLoginEnv(t, nil)
	alice := e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)
	old := e.fake.addSession(alice.ID, time.Now().Add(time.Hour))
	expired := e.fake.addSession(alice.ID, time.Now().Add(-time.Hour))

	rec := e.login(t, "", &http.Cookie{Name: "gg_session", Value: old})
	require.Equal(t, http.StatusFound, rec.Code)
	raw := sessionValue(rec)
	require.NotEmpty(t, raw)
	assert.NotEqual(t, old, raw)
	assert.False(t, e.fake.hasSession(old), "previous session deleted")
	assert.False(t, e.fake.hasSession(expired), "expired sessions swept")
	assert.True(t, e.fake.hasSession(raw))

	c := findCookie(rec, "gg_session")
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, 720*3600, c.MaxAge)
}

func TestLogout(t *testing.T) {
	t.Run("with end_session endpoint", func(t *testing.T) {
		e := newLoginEnv(t, nil)
		e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)
		loginRec := e.login(t, "")
		raw := sessionValue(loginRec)
		require.NotEmpty(t, raw)
		idToken, err := e.fake.SessionIDToken(context.Background(), hashToken(raw))
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: "gg_session", Value: raw})
		rec := httptest.NewRecorder()
		e.a.Logout(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.False(t, e.fake.hasSession(raw))
		c := findCookie(rec, "gg_session")
		require.NotNil(t, c)
		assert.Negative(t, c.MaxAge)

		var body struct {
			RedirectURL string `json:"redirect_url"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		u, err := url.Parse(body.RedirectURL)
		require.NoError(t, err)
		assert.Equal(t, e.iss.URL+"/end_session", u.Scheme+"://"+u.Host+u.Path)
		assert.Equal(t, idToken, u.Query().Get("id_token_hint"))
		assert.Equal(t, "geogallery", u.Query().Get("client_id"))
		assert.Equal(t, "http://localhost:5173/", u.Query().Get("post_logout_redirect_uri"))
	})

	t.Run("no end_session endpoint", func(t *testing.T) {
		e := newLoginEnv(t, func(i *oidctest.Issuer, _ *config.Config) { i.NoEndSession = true })
		e.fake.addUser(e.cfg.OIDCIssuerURL, "user-sub-1", "", "user", false)
		raw := sessionValue(e.login(t, ""))
		require.NotEmpty(t, raw)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: "gg_session", Value: raw})
		rec := httptest.NewRecorder()
		e.a.Logout(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"redirect_url":"/"}`, rec.Body.String())
		assert.False(t, e.fake.hasSession(raw))
	})

	t.Run("no session cookie", func(t *testing.T) {
		e := newLoginEnv(t, nil)
		rec := httptest.NewRecorder()
		e.a.Logout(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"redirect_url":"/"}`, rec.Body.String())
	})

	t.Run("unknown session cookie", func(t *testing.T) {
		e := newLoginEnv(t, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: "gg_session", Value: "stale"})
		rec := httptest.NewRecorder()
		e.a.Logout(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"redirect_url":"/"}`, rec.Body.String())
	})
}

func TestMe(t *testing.T) {
	a := newTestAuth(newFakeStore(), nil)
	tests := []struct {
		name string
		user User
		want string
	}{
		{"full", User{ID: "u1", Email: "a@example.test", DisplayName: "Alice", Role: "admin"},
			`{"id":"u1","email":"a@example.test","display_name":"Alice","role":"admin"}`},
		{"nulls", User{ID: "u2", Role: "user"}, `{"id":"u2","email":null,"display_name":null,"role":"user"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.Me(rec, sessionReq(http.MethodGet, "/api/auth/me", "", tt.user))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.JSONEq(t, tt.want, rec.Body.String())
		})
	}
}
