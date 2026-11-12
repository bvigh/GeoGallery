package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/config"
	"github.com/bvigh/geogallery/internal/handlers"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compressStore authenticates testToken as a real user row, so the media list can query.
type compressStore struct {
	routeStore
	userID string
}

func (s compressStore) UserForAPIToken(_ context.Context, h []byte) (auth.User, string, error) {
	if string(h) == hash(testToken) {
		return auth.User{ID: s.userID, Role: "user"}, "tok-1", nil
	}
	return auth.User{}, "", auth.ErrNoToken
}

// UserForSession authenticates the userSession cookie as the same user, for session-only routes.
func (s compressStore) UserForSession(_ context.Context, h []byte) (auth.User, error) {
	if string(h) == hash(userSession) {
		return auth.User{ID: s.userID, Role: "user"}, nil
	}
	return auth.User{}, auth.ErrNoSession
}

func TestRouter_ProvenanceIsGzipped(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var userID, mediaID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'gz', 'gz@example.test')
		RETURNING id::text`).Scan(&userID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash)
		VALUES ($1, 'item.jpg', 'photo', 'media/test/item.jpg', 'hash-item')
		RETURNING id::text`, userID).Scan(&mediaID))

	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	h := routeHandlers{provenance: handlers.NewProvenanceHandler(pool, nil, time.Hour, ingest.CameraZone{})}
	router := newRouter(http.NewCrossOriginProtection(), auth.New(compressStore{userID: userID}, nil, cfg), h)

	get := func(acceptEncoding string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/media/"+mediaID+"/provenance", nil)
		req.AddCookie(&http.Cookie{Name: "gg_session", Value: userSession})
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}

	t.Run("gzip when accepted", func(t *testing.T) {
		rec := get("gzip")
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
		zr, err := gzip.NewReader(rec.Body)
		require.NoError(t, err)
		body, err := io.ReadAll(zr)
		require.NoError(t, err)
		var got struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		}
		require.NoError(t, json.Unmarshal(body, &got), string(body))
		assert.Equal(t, mediaID, got.Item.ID)
	})
	t.Run("plain otherwise", func(t *testing.T) {
		rec := get("")
		assert.Empty(t, rec.Header().Get("Content-Encoding"))
		assert.Contains(t, rec.Body.String(), mediaID)
	})
}

func TestRouter_MediaListIsGzipped(t *testing.T) {
	pool := testdb.New(t)
	var userID string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'gz', 'gz@example.test')
		RETURNING id::text`).Scan(&userID))

	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	store := compressStore{userID: userID}
	h := routeHandlers{
		media:  handlers.NewMediaHandler(pool, nil, time.Hour, 24, ingest.CameraZone{}),
		albums: handlers.NewAlbumHandler(pool),
	}
	router := newRouter(http.NewCrossOriginProtection(), auth.New(store, nil, cfg), h)

	get := func(path, acceptEncoding string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec
	}

	t.Run("gzip when accepted", func(t *testing.T) {
		rec := get("/api/media", "gzip")
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
		zr, err := gzip.NewReader(rec.Body)
		require.NoError(t, err)
		body, err := io.ReadAll(zr)
		require.NoError(t, err)
		assert.JSONEq(t, `[]`, string(body))
	})
	t.Run("plain otherwise", func(t *testing.T) {
		rec := get("/api/media", "")
		assert.Empty(t, rec.Header().Get("Content-Encoding"))
		assert.JSONEq(t, `[]`, rec.Body.String())
	})
	t.Run("other routes stay plain", func(t *testing.T) {
		rec := get("/api/albums", "gzip")
		assert.Empty(t, rec.Header().Get("Content-Encoding"))
	})
}
