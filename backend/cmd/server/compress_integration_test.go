package main

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"codeberg.org/bvigh/geogallery/internal/config"
	"codeberg.org/bvigh/geogallery/internal/handlers"
	"codeberg.org/bvigh/geogallery/internal/testdb"
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

func TestRouter_MediaListIsGzipped(t *testing.T) {
	pool := testdb.New(t)
	var userID string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'gz', 'gz@example.test')
		RETURNING id::text`).Scan(&userID))

	cfg := &config.Config{PublicOrigin: "http://localhost:5173", SessionTTLHours: 720}
	store := compressStore{userID: userID}
	h := routeHandlers{
		media:  handlers.NewMediaHandler(pool, nil, time.Hour, 24, time.UTC),
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
