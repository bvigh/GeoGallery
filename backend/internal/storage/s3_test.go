package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bvigh/geogallery/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeS3 serves GETs of objects under /bucket/ with byte ranges, like S3 path-style requests.
func fakeS3(t *testing.T, objects map[string]string) (*Client, *[]string) {
	t.Helper()
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.URL.Path, "/bucket/")
		body, found := objects[key]
		if r.Method != http.MethodGet || !ok || !found {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		ranges = append(ranges, r.Header.Get("Range"))
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		end = min(end, len(body)-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		fmt.Fprint(w, body[start:end+1])
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(&config.Config{
		S3Endpoint: srv.URL, S3Region: "us-east-1", S3Bucket: "bucket",
		S3AccessKey: "key", S3SecretKey: "secret",
	})
	require.NoError(t, err)
	return c, &ranges
}

func TestReadHead(t *testing.T) {
	c, ranges := fakeS3(t, map[string]string{
		"media/u/abc.jpg":           "0123456789abcdef",
		"media/1000/file (2) ä.jpg": "legacy key",
	})
	ctx := context.Background()

	head, err := c.ReadHead(ctx, "media/u/abc.jpg", 4)
	require.NoError(t, err)
	assert.Equal(t, "0123", string(head))

	head, err = c.ReadHead(ctx, "media/u/abc.jpg", 1024)
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef", string(head), "a shorter object is returned whole")

	head, err = c.ReadHead(ctx, "media/1000/file (2) ä.jpg", 6)
	require.NoError(t, err)
	assert.Equal(t, "legacy", string(head), "legacy keys with spaces and accents")

	assert.Equal(t, []string{"bytes=0-3", "bytes=0-1023", "bytes=0-5"}, *ranges)

	_, err = c.ReadHead(ctx, "media/u/missing.jpg", 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media/u/missing.jpg")

	_, err = c.ReadHead(ctx, "media/u/abc.jpg", 0)
	require.Error(t, err)
}

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

// PresignGet signs for the host browsers use, PresignGetInternal for the one the backend uses.
func TestPresignGet_SignsForEachEndpoint(t *testing.T) {
	tests := []struct {
		name, endpoint, public           string
		wantPublicHost, wantInternalHost string
	}{
		{"container-only endpoint", "http://minio:9000", "http://localhost:9000", "localhost:9000", "minio:9000"},
		{"public unset", "http://minio:9000", "", "minio:9000", "minio:9000"},
		{"same endpoint", "https://s3.example.com", "https://s3.example.com", "s3.example.com", "s3.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(testConfig(tt.endpoint, tt.public))
			require.NoError(t, err)

			for _, presign := range []struct {
				name     string
				fn       func(context.Context, string) (string, error)
				wantHost string
			}{
				{"PresignGet", c.PresignGet, tt.wantPublicHost},
				{"PresignGetInternal", c.PresignGetInternal, tt.wantInternalHost},
			} {
				raw, err := presign.fn(context.Background(), "media/u/abc.jpg")
				require.NoError(t, err, presign.name)
				u, err := url.Parse(raw)
				require.NoError(t, err, presign.name)

				assert.Equal(t, presign.wantHost, u.Host, presign.name)
				assert.Equal(t, "/geogallery/media/u/abc.jpg", u.Path, "%s: path-style, as MinIO needs", presign.name)
				assert.Equal(t, "host", u.Query().Get("X-Amz-SignedHeaders"), "%s: the signature covers the host", presign.name)
				assert.Equal(t, "900", u.Query().Get("X-Amz-Expires"), presign.name)
			}
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
