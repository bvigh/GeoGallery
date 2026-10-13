package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionReq(method, target, body string, u User) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(WithUser(req.Context(), u, MethodSession))
}

func withURLParam(r *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestCreateToken_Validation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantDays   int
	}{
		{"missing expiry defaults to 90", `{"name":"cron"}`, http.StatusCreated, 90},
		{"explicit 30", `{"name":"cron","expires_in_days":30}`, http.StatusCreated, 30},
		{"min 1", `{"name":"cron","expires_in_days":1}`, http.StatusCreated, 1},
		{"max 365", `{"name":"cron","expires_in_days":365}`, http.StatusCreated, 365},
		{"zero", `{"name":"cron","expires_in_days":0}`, http.StatusBadRequest, 0},
		{"366", `{"name":"cron","expires_in_days":366}`, http.StatusBadRequest, 0},
		{"null", `{"name":"cron","expires_in_days":null}`, http.StatusBadRequest, 0},
		{"negative", `{"name":"cron","expires_in_days":-1}`, http.StatusBadRequest, 0},
		{"fraction", `{"name":"cron","expires_in_days":1.5}`, http.StatusBadRequest, 0},
		{"string", `{"name":"cron","expires_in_days":"90"}`, http.StatusBadRequest, 0},
		{"empty name", `{"name":""}`, http.StatusBadRequest, 0},
		{"missing name", `{}`, http.StatusBadRequest, 0},
		{"100 char name", `{"name":"` + strings.Repeat("é", 100) + `"}`, http.StatusCreated, 90},
		{"101 char name", `{"name":"` + strings.Repeat("a", 101) + `"}`, http.StatusBadRequest, 0},
		{"invalid json", `{`, http.StatusBadRequest, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			u := store.addUser("iss", "s", "a@example.test", "user", false)
			a := newTestAuth(store, nil)
			rec := httptest.NewRecorder()
			a.CreateToken(rec, sessionReq(http.MethodPost, "/api/tokens", tt.body, u))
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantStatus != http.StatusCreated {
				assert.Empty(t, store.tokens)
				return
			}
			var got map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			exp, err := time.Parse(time.RFC3339Nano, got["expires_at"].(string))
			require.NoError(t, err)
			assert.WithinDuration(t, time.Now().Add(time.Duration(tt.wantDays)*24*time.Hour), exp, time.Minute)
		})
	}
}

func TestCreateToken_ResponseShape(t *testing.T) {
	store := newFakeStore()
	u := store.addUser("iss", "s", "a@example.test", "user", false)
	a := newTestAuth(store, nil)
	rec := httptest.NewRecorder()
	a.CreateToken(rec, sessionReq(http.MethodPost, "/api/tokens", `{"name":"cron"}`, u))
	require.Equal(t, http.StatusCreated, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	for _, k := range []string{"id", "name", "display_hint", "created_at", "expires_at", "last_used_at", "revoked_at", "token"} {
		assert.Contains(t, got, k)
	}
	assert.Len(t, got, 8, "no hash or other fields")
	assert.Nil(t, got["last_used_at"])
	assert.Nil(t, got["revoked_at"])
	assert.Equal(t, "cron", got["name"])
	token := got["token"].(string)
	assert.True(t, strings.HasPrefix(token, "gg_pat_"))
	assert.Len(t, token, len("gg_pat_")+43)
	hint := got["display_hint"].(string)
	assert.Equal(t, token[:len("gg_pat_")+6], hint)

	// The created token authenticates.
	_, _, err := store.UserForAPIToken(context.Background(), hashToken(token))
	require.NoError(t, err)

	// Plaintext never appears in the list.
	rec = httptest.NewRecorder()
	a.ListTokens(rec, sessionReq(http.MethodGet, "/api/tokens", "", u))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), token)
	assert.NotContains(t, rec.Body.String(), `"token"`)
	assert.NotContains(t, rec.Body.String(), "hash")
}

func TestCreateToken_Cap(t *testing.T) {
	store := newFakeStore()
	u := store.addUser("iss", "s", "a@example.test", "user", false)
	store.addAPIToken(u.ID, time.Now().Add(time.Hour), true)   // revoked: not counted
	store.addAPIToken(u.ID, time.Now().Add(-time.Hour), false) // expired: not counted
	for range maxActiveAPITokens - 1 {
		store.addAPIToken(u.ID, time.Now().Add(time.Hour), false)
	}
	a := newTestAuth(store, nil)

	rec := httptest.NewRecorder()
	a.CreateToken(rec, sessionReq(http.MethodPost, "/api/tokens", `{"name":"25th"}`, u))
	require.Equal(t, http.StatusCreated, rec.Code)

	rec = httptest.NewRecorder()
	a.CreateToken(rec, sessionReq(http.MethodPost, "/api/tokens", `{"name":"26th"}`, u))
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestListTokens(t *testing.T) {
	store := newFakeStore()
	alice := store.addUser("iss", "a", "a@example.test", "user", false)
	bob := store.addUser("iss", "b", "b@example.test", "user", false)
	a := newTestAuth(store, nil)

	rec := httptest.NewRecorder()
	a.ListTokens(rec, sessionReq(http.MethodGet, "/api/tokens", "", alice))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `[]`, rec.Body.String())

	rawA, idA := store.addAPIToken(alice.ID, time.Now().Add(time.Hour), false)
	store.addAPIToken(bob.ID, time.Now().Add(time.Hour), false)

	rec = httptest.NewRecorder()
	a.ListTokens(rec, sessionReq(http.MethodGet, "/api/tokens", "", alice))
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, idA, got[0]["id"])
	assert.Len(t, got[0], 7)
	assert.NotContains(t, rec.Body.String(), rawA)
}

func TestRevokeToken(t *testing.T) {
	store := newFakeStore()
	alice := store.addUser("iss", "a", "a@example.test", "user", false)
	bob := store.addUser("iss", "b", "b@example.test", "user", false)
	_, aliceTok := store.addAPIToken(alice.ID, time.Now().Add(time.Hour), false)
	_, aliceRevoked := store.addAPIToken(alice.ID, time.Now().Add(time.Hour), true)
	_, bobTok := store.addAPIToken(bob.ID, time.Now().Add(time.Hour), false)
	a := newTestAuth(store, nil)

	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{"someone else's token", bobTok, http.StatusNotFound},
		{"already revoked", aliceRevoked, http.StatusNotFound},
		{"non-uuid", "not-a-uuid", http.StatusNotFound},
		{"unknown uuid", "00000000-0000-0000-0000-000000000000", http.StatusNotFound},
		{"own token", aliceTok, http.StatusNoContent},
		{"own token uppercase id, now revoked", strings.ToUpper(aliceTok), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.RevokeToken(rec, withURLParam(sessionReq(http.MethodDelete, "/api/tokens/"+tt.id, "", alice), "id", tt.id))
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
	assert.Nil(t, store.tokens[bobTok].RevokedAt)
	assert.NotNil(t, store.tokens[aliceTok].RevokedAt)
}
