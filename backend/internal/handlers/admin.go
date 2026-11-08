package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/go-chi/chi/v5"
)

const (
	adminInviteTTL = 72 * time.Hour
	maxBodyBytes   = 1 << 16
)

type AdminHandler struct {
	store        auth.Store
	publicOrigin string
}

func NewAdminHandler(store auth.Store, publicOrigin string) *AdminHandler {
	return &AdminHandler{store: store, publicOrigin: publicOrigin}
}

type adminUser struct {
	ID          string     `json:"id"`
	Email       *string    `json:"email"`
	DisplayName *string    `json:"display_name"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("admin: encode response failed", "err", err)
	}
}

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		slog.Error("admin: list users failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	out := make([]adminUser, 0, len(users))
	for _, u := range users {
		out = append(out, adminUser{
			ID: u.ID, Email: optString(u.Email), DisplayName: optString(u.DisplayName), Role: u.Role,
			Disabled: u.Disabled, LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt,
		})
	}
	writeAdminJSON(w, http.StatusOK, out)
}

// UpdateUser accepts only {"disabled": bool}.
func (h *AdminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context())
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Disabled *bool `json:"disabled"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Disabled == nil {
		http.Error(w, `body must be {"disabled": true|false}`, http.StatusBadRequest)
		return
	}
	if *body.Disabled && id == me.ID {
		http.Error(w, "you cannot disable yourself", http.StatusBadRequest)
		return
	}
	err := h.store.SetDisabled(r.Context(), id, *body.Disabled)
	if errors.Is(err, auth.ErrNoUser) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("admin: set disabled failed", "user_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Info("admin: user updated", "by", me.ID, "user_id", id, "disabled", *body.Disabled)
	w.WriteHeader(http.StatusNoContent)
}

// CreateInvite accepts only {"role": "user"|"admin"}; claim invites are CLI-only.
func (h *AdminHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context())
	var body struct {
		Role string `json:"role"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || (body.Role != "user" && body.Role != "admin") {
		http.Error(w, `body must be {"role": "user"|"admin"}`, http.StatusBadRequest)
		return
	}
	url, err := auth.CreateInvite(r.Context(), h.store, h.publicOrigin, body.Role, me.ID, adminInviteTTL)
	if err != nil {
		slog.Error("admin: create invite failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Info("admin: invite created", "by", me.ID, "role", body.Role)
	writeAdminJSON(w, http.StatusCreated, map[string]string{"url": url})
}
