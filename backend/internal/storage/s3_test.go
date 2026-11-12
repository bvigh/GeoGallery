package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
