package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

const (
	maxActiveAPITokens   = 25
	defaultTokenLifetime = 90
	maxTokenLifetime     = 365
)

type createdAPIToken struct {
	APIToken
	Token string `json:"token"`
}

func (a *Auth) ListTokens(w http.ResponseWriter, r *http.Request) {
	u := MustUser(r.Context())
	tokens, err := a.store.ListAPITokens(r.Context(), u.ID)
	if err != nil {
		slog.Error("auth: list api tokens failed", "user_id", u.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if tokens == nil {
		tokens = []APIToken{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (a *Auth) CreateToken(w http.ResponseWriter, r *http.Request) {
	u := MustUser(r.Context())
	var body struct {
		Name string `json:"name"`
		// RawMessage distinguishes a missing field (default) from an explicit null (rejected).
		ExpiresInDays json.RawMessage `json:"expires_in_days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if n := utf8.RuneCountInString(body.Name); !utf8.ValidString(body.Name) || n < 1 || n > 100 {
		http.Error(w, "name must be 1-100 characters", http.StatusBadRequest)
		return
	}
	days := defaultTokenLifetime
	if body.ExpiresInDays != nil {
		if bytes.Equal(bytes.TrimSpace(body.ExpiresInDays), []byte("null")) ||
			json.Unmarshal(body.ExpiresInDays, &days) != nil || days < 1 || days > maxTokenLifetime {
			http.Error(w, "expires_in_days must be an integer from 1 to 365", http.StatusBadRequest)
			return
		}
	}

	raw, hash, hint := newAPIToken()
	t, err := a.store.CreateAPIToken(r.Context(), u.ID, body.Name, hash, hint,
		time.Now().Add(time.Duration(days)*24*time.Hour), maxActiveAPITokens)
	if errors.Is(err, ErrTokenLimit) {
		http.Error(w, "too many active tokens: revoke one first", http.StatusConflict)
		return
	}
	if err != nil {
		slog.Error("auth: create api token failed", "user_id", u.ID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("auth: api token created", "user_id", u.ID, "token_id", t.ID, "name", t.Name)
	writeJSON(w, http.StatusCreated, createdAPIToken{APIToken: t, Token: raw})
}

func (a *Auth) RevokeToken(w http.ResponseWriter, r *http.Request) {
	u := MustUser(r.Context())
	id, ok := CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	err := a.store.RevokeAPIToken(r.Context(), u.ID, id)
	if errors.Is(err, ErrNoToken) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("auth: revoke api token failed", "user_id", u.ID, "token_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("auth: api token revoked", "user_id", u.ID, "token_id", id)
	w.WriteHeader(http.StatusNoContent)
}
