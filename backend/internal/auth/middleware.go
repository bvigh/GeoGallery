package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

func unauthorized(w http.ResponseWriter) {
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func authError(w http.ResponseWriter) {
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// RequireUser authenticates with a Bearer API token or, failing that, the session cookie.
func (a *Auth) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw, ok := bearerToken(r.Header.Get("Authorization")); ok {
			// A Bearer header is authoritative: never fall back to the cookie. Other schemes
			// (e.g. Basic from a reverse proxy in front of the app) fall through to the session cookie.
			if !strings.HasPrefix(raw, apiTokenPrefix) {
				unauthorized(w)
				return
			}
			u, tokenID, err := a.store.UserForAPIToken(r.Context(), hashToken(raw))
			if errors.Is(err, ErrNoToken) {
				unauthorized(w)
				return
			}
			if err != nil {
				slog.Error("auth: api token lookup failed", "err", err)
				authError(w)
				return
			}
			if err := a.store.TouchAPIToken(r.Context(), tokenID); err != nil {
				slog.Warn("auth: api token last_used update failed", "token_id", tokenID, "err", err)
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u, MethodToken)))
			return
		}
		c, err := r.Cookie(a.sessionCookieName)
		if err != nil || c.Value == "" {
			unauthorized(w)
			return
		}
		u, err := a.store.UserForSession(r.Context(), hashToken(c.Value))
		if errors.Is(err, ErrNoSession) {
			unauthorized(w)
			return
		}
		if err != nil {
			slog.Error("auth: session lookup failed", "err", err)
			authError(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u, MethodSession)))
	})
}

// bearerToken reports whether h uses the Bearer scheme, even with an empty value: net/http
// trims trailing whitespace, so "Bearer " on the wire arrives as "Bearer".
func bearerToken(h string) (string, bool) {
	scheme, rest := h, ""
	if i := strings.IndexAny(h, " \t"); i >= 0 {
		scheme, rest = h[:i], h[i+1:]
	}
	if !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// RequireSession rejects API-token requests, so a leaked token cannot delete media, mint new
// tokens or use admin routes.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if MethodFrom(r.Context()) != MethodSession {
			http.Error(w, "this endpoint requires a browser session", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin relies on the role RequireUser loaded from the database for this request, so
// role changes and disables take effect immediately.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFrom(r.Context())
		if !ok || !u.IsAdmin() {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
