package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/bvigh/geogallery/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig(endpoint, public string) *config.Config {
	return &config.Config{
		S3Endpoint:       endpoint,
		S3PublicEndpoint: public,
		S3AccessKey:      "access",
		S3SecretKey:      "secret",
		S3Bucket:         "geogallery",
		S3Region:         "us-east-1",
	}
}

func TestPresignGet_SignsForPublicEndpoint(t *testing.T) {
	tests := []struct {
		name, endpoint, public string
		wantHost               string
	}{
		{"container-only endpoint", "http://minio:9000", "http://localhost:9000", "localhost:9000"},
		{"public unset", "http://minio:9000", "", "minio:9000"},
		{"same endpoint", "https://s3.example.com", "https://s3.example.com", "s3.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(testConfig(tt.endpoint, tt.public))
			require.NoError(t, err)

			raw, err := c.PresignGet(context.Background(), "media/u/abc.jpg")
			require.NoError(t, err)
			u, err := url.Parse(raw)
			require.NoError(t, err)

			assert.Equal(t, tt.wantHost, u.Host)
			assert.Equal(t, "/geogallery/media/u/abc.jpg", u.Path, "path-style, as MinIO needs")
			assert.Equal(t, "host", u.Query().Get("X-Amz-SignedHeaders"), "the signature covers the host")
			assert.Equal(t, "900", u.Query().Get("X-Amz-Expires"))
		})
	}
}

// The backend's own requests go to S3_ENDPOINT, never to the public endpoint (which it may not reach).
func TestClient_RequestsUseInternalEndpoint(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/geogallery/media/u/abc.jpg", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := NewClient(testConfig(srv.URL, "http://unreachable.invalid:9000"))
	require.NoError(t, err)

	require.NoError(t, c.Delete(context.Background(), "media/u/abc.jpg"))
	assert.Equal(t, int32(1), hits.Load())
}
