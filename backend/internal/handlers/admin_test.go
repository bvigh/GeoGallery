package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAdminStore implements only what AdminHandler uses; any other call panics on the nil
// embedded interface.
type fakeAdminStore struct {
	auth.Store
	users       []auth.UserRecord
	err         error
	disabledArg map[string]bool
	invites     []auth.Invite
}

func (f *fakeAdminStore) ListUsers(context.Context) ([]auth.UserRecord, error) {
	return f.users, f.err
}

func (f *fakeAdminStore) SetDisabled(_ context.Context, id string, disabled bool) error {
	if f.err != nil {
		return f.err
	}
	for _, u := range f.users {
		if u.ID == id {
			f.disabledArg[id] = disabled
			return nil
		}
	}
	return auth.ErrNoUser
}

func (f *fakeAdminStore) CreateInvite(_ context.Context, inv auth.Invite) error {
	if f.err != nil {
		return f.err
	}
	f.invites = append(f.invites, inv)
	return nil
}

const (
	adminID = "11111111-1111-1111-1111-111111111111"
	otherID = "22222222-2222-2222-2222-222222222222"
)

func newFakeAdminStore() *fakeAdminStore {
	login := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &fakeAdminStore{
		disabledArg: map[string]bool{},
		users: []auth.UserRecord{
			{User: auth.User{ID: adminID, Email: "admin@example.test", DisplayName: "Admin", Role: "admin"}, Claimed: true, CreatedAt: created, LastLoginAt: &login},
			{User: auth.User{ID: otherID, Role: "user"}, Disabled: true, CreatedAt: created},
		},
	}
}

func adminRequest(method, target, body, idParam string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := auth.WithUser(r.Context(), auth.User{ID: adminID, Role: "admin"}, auth.MethodSession)
	if idParam != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", idParam)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return r.WithContext(ctx)
}

func TestAdmin_ListUsers(t *testing.T) {
	store := newFakeAdminStore()
	h := NewAdminHandler(store, "http://localhost:5173")
	rec := httptest.NewRecorder()
	h.ListUsers(rec, adminRequest(http.MethodGet, "/api/admin/users", "", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `[
		{"id":"`+adminID+`","email":"admin@example.test","display_name":"Admin","role":"admin","disabled":false,
		 "last_login_at":"2026-01-02T03:04:05Z","created_at":"2026-01-01T00:00:00Z"},
		{"id":"`+otherID+`","email":null,"display_name":null,"role":"user","disabled":true,
		 "last_login_at":null,"created_at":"2026-01-01T00:00:00Z"}
	]`, rec.Body.String())

	t.Run("empty is []", func(t *testing.T) {
		rec := httptest.NewRecorder()
		NewAdminHandler(&fakeAdminStore{}, "").ListUsers(rec, adminRequest(http.MethodGet, "/", "", ""))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `[]`, rec.Body.String())
	})

	t.Run("db error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		NewAdminHandler(&fakeAdminStore{err: errors.New("down")}, "").ListUsers(rec, adminRequest(http.MethodGet, "/", "", ""))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func TestAdmin_UpdateUser(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		body         string
		storeErr     error
		wantStatus   int
		wantDisabled *bool
	}{
		{"disable other", otherID, `{"disabled":true}`, nil, http.StatusNoContent, ptr(true)},
		{"enable other", otherID, `{"disabled":false}`, nil, http.StatusNoContent, ptr(false)},
		{"uppercase id", strings.ToUpper(otherID), `{"disabled":true}`, nil, http.StatusNoContent, ptr(true)},
		{"self-disable rejected", adminID, `{"disabled":true}`, nil, http.StatusBadRequest, nil},
		{"self-disable uppercase rejected", strings.ToUpper(adminID), `{"disabled":true}`, nil, http.StatusBadRequest, nil},
		{"unknown id", "33333333-3333-3333-3333-333333333333", `{"disabled":true}`, nil, http.StatusNotFound, nil},
		{"non-uuid id", "not-a-uuid", `{"disabled":true}`, nil, http.StatusNotFound, nil},
		{"missing field", otherID, `{}`, nil, http.StatusBadRequest, nil},
		{"null field", otherID, `{"disabled":null}`, nil, http.StatusBadRequest, nil},
		{"role change not allowed", otherID, `{"disabled":true,"role":"admin"}`, nil, http.StatusBadRequest, nil},
		{"invalid json", otherID, `{`, nil, http.StatusBadRequest, nil},
		{"db error", otherID, `{"disabled":true}`, errors.New("down"), http.StatusInternalServerError, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeAdminStore()
			store.err = tt.storeErr
			h := NewAdminHandler(store, "http://localhost:5173")
			rec := httptest.NewRecorder()
			h.UpdateUser(rec, adminRequest(http.MethodPatch, "/api/admin/users/"+tt.id, tt.body, tt.id))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantDisabled != nil {
				assert.Equal(t, *tt.wantDisabled, store.disabledArg[otherID])
			} else {
				assert.Empty(t, store.disabledArg)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestAdmin_CreateInvite(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		storeErr   error
		wantStatus int
		wantRole   string
	}{
		{"user invite", `{"role":"user"}`, nil, http.StatusCreated, "user"},
		{"admin invite", `{"role":"admin"}`, nil, http.StatusCreated, "admin"},
		{"claim_user_id rejected", `{"role":"admin","claim_user_id":"00000000-0000-0000-0000-000000000001"}`, nil, http.StatusBadRequest, ""},
		{"bad role", `{"role":"owner"}`, nil, http.StatusBadRequest, ""},
		{"missing role", `{}`, nil, http.StatusBadRequest, ""},
		{"invalid json", `{`, nil, http.StatusBadRequest, ""},
		{"db error", `{"role":"user"}`, errors.New("down"), http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeAdminStore()
			store.err = tt.storeErr
			h := NewAdminHandler(store, "https://g.example.com")
			rec := httptest.NewRecorder()
			h.CreateInvite(rec, adminRequest(http.MethodPost, "/api/admin/invites", tt.body, ""))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus != http.StatusCreated {
				assert.Empty(t, store.invites)
				return
			}
			var body struct {
				URL string `json:"url"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			u, err := url.Parse(body.URL)
			require.NoError(t, err)
			assert.Equal(t, "https://g.example.com/api/auth/login", u.Scheme+"://"+u.Host+u.Path)
			assert.NotEmpty(t, u.Query().Get("invite"))

			require.Len(t, store.invites, 1)
			inv := store.invites[0]
			assert.Equal(t, tt.wantRole, inv.Role)
			assert.Empty(t, inv.ClaimUserID)
			assert.Equal(t, adminID, inv.CreatedBy)
			assert.WithinDuration(t, time.Now().Add(72*time.Hour), inv.ExpiresAt, time.Minute)
			assert.NotContains(t, string(inv.TokenHash), u.Query().Get("invite"), "only the hash is stored")
		})
	}
}
