package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateHashVectors = flag.Bool("update-hash-vectors", false, "rewrite frontend/src/lib/mediaHash.vectors.json")

// hashVectorsPath lives in the frontend, which imports it: the frontend Docker image is built from
// frontend/ alone, while go build never compiles this test.
const hashVectorsPath = "../../../frontend/src/lib/mediaHash.vectors.json"

// hashVectors is mediaHash.vectors.json. frontend/src/lib/mediaHash.test.ts checks the same
// file, so the browser and the server agree on every case of mediaHash.
type hashVectors struct {
	Content string       `json:"content"`
	Vectors []hashVector `json:"vectors"`
}

type hashVector struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"`
}

// vectorContent is the file every vector hashes: byte i is i % 251, so no two 64 KB chunks match.
func vectorContent(size int64) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// streamingHash is the hash registerMedia computed before it hashed ahead of the upload: it read
// the head, then captured the tail while streaming the rest to S3. mediaHash must match it, or
// files already in a library would no longer count as duplicates.
func streamingHash(data []byte, probeVideo bool) string {
	body := bytes.NewReader(data)
	head := make([]byte, mediaHeadSize)
	n, _ := io.ReadFull(body, head)
	head = head[:n]
	if probeVideo {
		return partialHash(head)
	}
	var tail []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		tail = append(tail, buf[:n]...)
		if len(tail) > mediaHashChunk {
			tail = tail[len(tail)-mediaHashChunk:]
		}
		if err != nil {
			break
		}
	}
	if int64(len(data)) > 2*mediaHashChunk {
		return partialHashStream(int64(len(data)), head[:mediaHashChunk], tail)
	}
	return partialHash(head)
}

// hashOf runs mediaHash the way registerMedia does.
func hashOf(t *testing.T, data []byte, probeVideo bool) string {
	t.Helper()
	head := data[:min(len(data), mediaHeadSize)]
	h, err := mediaHash(bytes.NewReader(data), int64(len(data)), head, probeVideo)
	require.NoError(t, err)
	return h
}

func TestMediaHash_MatchesStreamingHash(t *testing.T) {
	const kb, mb = 1024, 1024 * 1024
	sizes := []int{1, 1000, 128 * kb, 128*kb + 1, 500 * kb, mb - 1, mb, mb + 1, mb + 10*kb,
		mb + 64*kb - 1, mb + 64*kb, mb + 64*kb + 1, 3 * mb}
	for _, size := range sizes {
		data := vectorContent(int64(size))
		for _, video := range []bool{false, true} {
			assert.Equal(t, streamingHash(data, video), hashOf(t, data, video), "size %d, video %v", size, video)
		}
	}
}

// failingReaderAt fails every read past the head.
type failingReaderAt struct{ head []byte }

func (f failingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) <= int64(len(f.head)) {
		return copy(p, f.head[off:]), nil
	}
	return 0, errors.New("disk gone")
}

func TestMediaHash_TailReadError(t *testing.T) {
	data := vectorContent(2 * mediaHeadSize)
	head := data[:mediaHeadSize]
	_, err := mediaHash(failingReaderAt{head}, int64(len(data)), head, false)
	assert.ErrorContains(t, err, "disk gone")

	_, err = mediaHash(failingReaderAt{head}, int64(len(data)), head, true)
	assert.NoError(t, err, "a probed video hashes the head alone")
}

func TestMediaHash_Vectors(t *testing.T) {
	const kb, mb = 1024, 1024 * 1024
	cases := []struct {
		filename string
		size     int64
	}{
		{"tiny.jpg", 1}, {"small.jpg", 1000}, {"edge.jpg", 128 * kb}, {"edge-plus-one.jpg", 128*kb + 1},
		{"mid.heic", 500 * kb}, {"head.jpg", mb}, {"head-plus-one.jpg", mb + 1}, {"partial-tail.png", mb + 10*kb},
		{"full-tail.jpg", mb + 64*kb}, {"large.jpg", 3 * mb},
		{"tiny.mp4", 1000}, {"mid.MOV", 500 * kb}, {"head.mkv", mb}, {"large.mp4", 3 * mb}, {"large.3gp", 3*mb + 7},
	}
	got := hashVectors{Content: "byte i of the file is i % 251"}
	for _, c := range cases {
		ext := filepath.Ext(c.filename)
		// No vector has EXIF, so every video takes the ffprobe path, as real videos do.
		h := hashOf(t, vectorContent(c.size), ingest.IsVideoExt(ext))
		got.Vectors = append(got.Vectors, hashVector{c.filename, c.size, h})
	}

	if *updateHashVectors {
		out, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(hashVectorsPath, append(out, '\n'), 0o644))
	}
	raw, err := os.ReadFile(hashVectorsPath)
	require.NoError(t, err, "run go test ./internal/handlers -run TestMediaHash_Vectors -update-hash-vectors")
	var want hashVectors
	require.NoError(t, json.Unmarshal(raw, &want))
	assert.Equal(t, want, got)
}
