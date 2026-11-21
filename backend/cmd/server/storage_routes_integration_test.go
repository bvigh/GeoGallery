package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/config"
	"github.com/bvigh/geogallery/internal/handlers"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storageStore is compressStore with the adminSession cookie as the same user, made an admin.
type storageStore struct{ compressStore }

func (s storageStore) UserForSession(ctx context.Context, h []byte) (auth.User, error) {
	if string(h) == hash(adminSession) {
		return auth.User{ID: s.userID, Role: "admin"}, nil
	}
	return s.compressStore.UserForSession(ctx, h)
}

// GET /api/me/storage answers a token and a session with the caller's own items; GET
// /api/admin/storage answers an admin's session only, with every user's.
func TestRouter_StorageRoutes(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var userID, otherID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'me', 'me@example.test')
		RETURNING id::text`).Scan(&userID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'other', 'other@example.test')
		RETURNING id::text`).Scan(&otherID))
	_, err := pool.Exec(ctx, `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, size_bytes)
		VALUES ($1, 'mine.jpg', 'photo', 'media/test/mine.jpg', 'hash-mine', 1234),
		       ($2, 'theirs.jpg', 'photo', 'media/test/theirs.jpg', 'hash-theirs', 99)`, userID, otherID)
	require.NoError(t, err)

	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	h := routeHandlers{storage: handlers.NewAdminStorageHandler(pool), myStorage: handlers.NewStorageUseHandler(pool)}
	router := newRouter(http.NewCrossOriginProtection(), auth.New(storageStore{compressStore{userID: userID}}, nil, cfg), h)

	get := func(path string, token bool, session string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token {
			req.Header.Set("Authorization", "Bearer "+testToken)
		}
		if session != "" {
			req.AddCookie(&http.Cookie{Name: "gg_session", Value: session})
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	use := func(t *testing.T, rec *httptest.ResponseRecorder) handlers.StorageUse {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got handlers.StorageUse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		return got
	}

	t.Run("a token reads the caller's own storage", func(t *testing.T) {
		assert.Equal(t, handlers.StorageUse{Items: 1, Bytes: 1234}, use(t, get("/api/me/storage", true, "")))
	})
	t.Run("so does a session", func(t *testing.T) {
		assert.Equal(t, handlers.StorageUse{Items: 1, Bytes: 1234}, use(t, get("/api/me/storage", false, userSession)))
	})
	t.Run("an admin's session reads every user's summary", func(t *testing.T) {
		assert.Equal(t, handlers.StorageUse{Items: 2, Bytes: 1333}, use(t, get("/api/admin/storage", false, adminSession)))
	})
	t.Run("a user's session and a token get no summary", func(t *testing.T) {
		for _, rec := range []*httptest.ResponseRecorder{
			get("/api/admin/storage", false, userSession),
			get("/api/admin/storage", true, ""),
		} {
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.NotContains(t, rec.Body.String(), "1333")
		}
	})
}
