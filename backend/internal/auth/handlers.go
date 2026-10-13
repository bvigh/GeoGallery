package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"codeberg.org/bvigh/geogallery/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	loginStateCookie = "gg_oidc"
	loginStatePath   = "/api/auth"
	loginStateMaxAge = 600
)

type Auth struct {
	store             Store
	p                 *Provider
	sessionCookieName string
	secure            bool
	sessionTTL        time.Duration
	publicOrigin      string
}

func New(store Store, p *Provider, cfg *config.Config) *Auth {
	name := "gg_session"
	if cfg.CookieSecure {
		// __Host- forces Secure, Path=/ and no Domain, so a sibling subdomain cannot plant it.
		name = "__Host-gg_session"
	}
	return &Auth{
		store:             store,
		p:                 p,
		sessionCookieName: name,
		secure:            cfg.CookieSecure,
		sessionTTL:        time.Duration(cfg.SessionTTLHours) * time.Hour,
		publicOrigin:      cfg.PublicOrigin,
	}
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Invite   string `json:"i,omitempty"`
}

func (a *Auth) writeLoginState(w http.ResponseWriter, st loginState) {
	b, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{
		Name: loginStateCookie, Value: base64.RawURLEncoding.EncodeToString(b),
		Path: loginStatePath, MaxAge: loginStateMaxAge,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) clearLoginState(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: loginStateCookie, Path: loginStatePath, MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func readLoginState(r *http.Request) (loginState, error) {
	var st loginState
	c, err := r.Cookie(loginStateCookie)
	if err != nil {
		return st, err
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(b, &st)
	return st, err
}

func (a *Auth) setSessionCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name: a.sessionCookieName, Value: raw, Path: "/",
		MaxAge:   int(a.sessionTTL / time.Second),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: a.sessionCookieName, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st := loginState{
		State:    randomString(),
		Nonce:    randomString(),
		Verifier: oauth2.GenerateVerifier(),
		ReturnTo: safeReturnTo(q.Get("return_to")),
		Invite:   q.Get("invite"),
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(st.Verifier), oidc.Nonce(st.Nonce)}
	prompt := q.Get("prompt")
	if st.Invite != "" {
		// Never let an existing IdP session for the wrong account silently claim an invite.
		prompt = "login"
	}
	if prompt == "login" || prompt == "select_account" {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", prompt))
	}
	a.writeLoginState(w, st)
	http.Redirect(w, r, a.p.oauth.AuthCodeURL(st.State, opts...), http.StatusFound)
}

func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	st, err := readLoginState(r)
	if err != nil || st.State == "" || q.Get("state") != st.State {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	if e := q.Get("error"); e != "" {
		slog.Warn("auth: issuer returned an error", "error", e)
		http.Error(w, "login failed at the identity provider", http.StatusBadRequest)
		return
	}
	code := q.Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	tok, err := a.p.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		slog.Warn("auth: code exchange failed", "err", err)
		http.Error(w, "login failed", http.StatusBadRequest)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		slog.Warn("auth: token response has no id_token")
		http.Error(w, "login failed", http.StatusBadRequest)
		return
	}
	idt, err := a.p.verifier.Verify(ctx, rawID)
	if err != nil {
		slog.Warn("auth: id token verification failed", "err", err)
		http.Error(w, "login failed", http.StatusBadRequest)
		return
	}
	if st.Nonce == "" || idt.Nonce != st.Nonce {
		http.Error(w, "login failed", http.StatusBadRequest)
		return
	}
	// go-oidc does not require sub; an empty one would bind every sub-less token to one user.
	if idt.Subject == "" {
		slog.Warn("auth: id token has no sub claim")
		http.Error(w, "login failed", http.StatusBadRequest)
		return
	}

	var c struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	_ = idt.Claims(&c)
	if c.Email == "" && a.p.oidc.UserInfoEndpoint() != "" {
		// e.g. ZITADEL without "User Info inside ID Token".
		ui, err := a.p.oidc.UserInfo(ctx, a.p.oauth.TokenSource(ctx, tok))
		switch {
		case err != nil:
			slog.Warn("auth: userinfo fallback failed", "err", err)
		case ui.Subject == idt.Subject:
			_ = ui.Claims(&c)
		default:
			slog.Warn("auth: userinfo subject mismatch, ignoring userinfo")
		}
	}
	displayName := firstNonEmpty(c.Name, c.PreferredUsername, c.Email)

	// The configured issuer, not idt.Issuer: Google may emit "accounts.google.com" without a scheme.
	iss := a.p.issuer
	u, err := a.store.FindUserByIdentity(ctx, iss, idt.Subject)
	switch {
	case err == nil && u.Disabled:
		a.authErrorRedirect(w, r, "disabled")
		return
	case err == nil:
	case errors.Is(err, ErrNoUser) && st.Invite != "":
		u, err = a.store.RegisterWithInvite(ctx, hashToken(st.Invite), iss, idt.Subject, c.Email, displayName)
		if errors.Is(err, ErrInviteInvalid) {
			a.authErrorRedirect(w, r, "invite_invalid")
			return
		}
		if err != nil {
			slog.Error("auth: register with invite failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		slog.Info("auth: invite redeemed", "user_id", u.ID, "role", u.Role)
	case errors.Is(err, ErrNoUser):
		a.authErrorRedirect(w, r, "not_invited")
		return
	default:
		slog.Error("auth: user lookup failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := a.store.TouchLogin(ctx, u.ID, c.Email, displayName); err != nil {
		slog.Error("auth: touch login failed", "user_id", u.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if old, err := r.Cookie(a.sessionCookieName); err == nil && old.Value != "" {
		if err := a.store.DeleteSession(ctx, hashToken(old.Value)); err != nil {
			slog.Warn("auth: deleting previous session failed", "err", err)
		}
	}
	raw, hash := newToken()
	if err := a.store.CreateSession(ctx, hash, u.ID, rawID, time.Now().Add(a.sessionTTL)); err != nil {
		slog.Error("auth: create session failed", "user_id", u.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := a.store.DeleteExpiredSessions(ctx); err != nil {
		slog.Warn("auth: deleting expired sessions failed", "err", err)
	}
	a.setSessionCookie(w, raw)
	a.clearLoginState(w)
	// Re-validated: gg_oidc is unsigned.
	http.Redirect(w, r, safeReturnTo(st.ReturnTo), http.StatusFound)
}

func (a *Auth) authErrorRedirect(w http.ResponseWriter, r *http.Request, code string) {
	a.clearLoginState(w)
	http.Redirect(w, r, "/?auth_error="+code, http.StatusFound)
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	redirect := "/"
	if c, err := r.Cookie(a.sessionCookieName); err == nil && c.Value != "" {
		hash := hashToken(c.Value)
		idToken, err := a.store.SessionIDToken(r.Context(), hash)
		if err != nil && !errors.Is(err, ErrNoSession) {
			slog.Error("auth: logout session lookup failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := a.store.DeleteSession(r.Context(), hash); err != nil {
			slog.Error("auth: logout delete session failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if idToken != "" && a.p.endSessionURL != "" {
			redirect = a.endSessionRedirect(idToken)
		}
	}
	a.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"redirect_url": redirect})
}

func (a *Auth) endSessionRedirect(idToken string) string {
	u, err := url.Parse(a.p.endSessionURL)
	if err != nil {
		slog.Warn("auth: invalid end_session_endpoint", "err", err)
		return "/"
	}
	v := u.Query()
	v.Set("id_token_hint", idToken)
	v.Set("client_id", a.p.oauth.ClientID)
	v.Set("post_logout_redirect_uri", a.publicOrigin+"/")
	u.RawQuery = v.Encode()
	return u.String()
}

type meResponse struct {
	ID          string  `json:"id"`
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
	Role        string  `json:"role"`
}

func (a *Auth) Me(w http.ResponseWriter, r *http.Request) {
	u := MustUser(r.Context())
	writeJSON(w, http.StatusOK, meResponse{ID: u.ID, Email: nullIfEmpty(u.Email), DisplayName: nullIfEmpty(u.DisplayName), Role: u.Role})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("auth: encode response failed", "err", err)
	}
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
