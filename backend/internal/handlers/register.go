package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/jackc/pgx/v5/pgconn"
)

type registrarDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type registrarStorage interface {
	Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
}

type MediaRegistrar struct {
	db      registrarDB
	storage registrarStorage
}

// tailCapture wraps a reader and keeps the last 64KB of bytes seen,
// used to compute the tail chunk of partialHashStream after streaming.
type tailCapture struct {
	r    io.Reader
	tail []byte
}

func (tc *tailCapture) Read(p []byte) (int, error) {
	n, err := tc.r.Read(p)
	if n > 0 {
		const keep = 64 * 1024
		tc.tail = append(tc.tail, p[:n]...)
		if len(tc.tail) > keep {
			tc.tail = tc.tail[len(tc.tail)-keep:]
		}
	}
	return n, err
}

// registerMedia stores one media file. Returns (true, nil) if newly inserted,
// (false, nil) if deduplicated by file_hash, or (false, err) on failure.
func (r *MediaRegistrar) registerMedia(
	ctx context.Context,
	body io.Reader,
	size int64,
	filename string,
	fallbackMtime *time.Time,
) (bool, error) {
	const headSize = 1024 * 1024 // 1MB — safe margin for EXIF
	head := make([]byte, headSize)
	n, err := io.ReadAtLeast(body, head, 0)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false, err
	}
	head = head[:n]

	ext := strings.ToLower(filepath.Ext(filename))
	exifData := ingest.ExtractExif(bytes.NewReader(head))
	mediaType := ingest.DetectMediaType(exifData, filename, ext)

	tc := &tailCapture{r: body}
	uploadBody := io.MultiReader(bytes.NewReader(head), tc)

	key := fmt.Sprintf("media/%d/%s", time.Now().UnixNano(), filename)
	if err := r.storage.Upload(ctx, key, uploadBody, size, contentTypeFor(ext)); err != nil {
		return false, err
	}

	const chunk = 64 * 1024
	var fileHash string
	if size <= 2*chunk {
		fileHash = partialHash(head)
	} else {
		fileHash = partialHashStream(size, head[:chunk], tc.tail)
	}

	takenAt := exifData.TakenAt
	if takenAt == nil {
		takenAt = fallbackMtime
	}

	tag, err := r.db.Exec(ctx, `
		INSERT INTO media (filename, media_type, location, gps_confidence, gps_source, taken_at, storage_key, file_hash)
		VALUES ($1, $2,
		        CASE WHEN $3::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($4, $3), 4326) END,
		        $5, $6, $7, $8, $9)
		ON CONFLICT (file_hash) DO NOTHING
	`,
		filename, mediaType,
		exifData.Lat, exifData.Lng,
		nil, // confidence populated by background GPS matcher
		gpsSource(exifData),
		takenAt, key, fileHash,
	)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		if exifData.Lat != nil && takenAt != nil {
			insertExifGPSTrack(ctx, r.db, *exifData.Lat, *exifData.Lng, *takenAt)
		}
		return true, nil
	}
	return false, nil
}

func gpsSource(e ingest.ExifData) *string {
	if e.Lat != nil {
		s := "exif"
		return &s
	}
	return nil
}

func contentTypeFor(ext string) string {
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".heic", ".heif":
		return "image/heic"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	default:
		return "application/octet-stream"
	}
}

// partialHash hashes the first and last 64 KB plus the file size.
// This is fast for large files while still being collision-resistant for deduplication.
func partialHash(data []byte) string {
	const chunk = 64 * 1024
	h := sha256.New()
	var sizeBuf [8]byte
	binary.LittleEndian.PutUint64(sizeBuf[:], uint64(len(data)))
	h.Write(sizeBuf[:])
	if len(data) <= 2*chunk {
		h.Write(data)
	} else {
		h.Write(data[:chunk])
		h.Write(data[len(data)-chunk:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// partialHashStream produces the same hash as partialHash for files larger than 128KB,
// using the pre-captured first and last 64KB chunks instead of a full []byte.
func partialHashStream(size int64, firstChunk, lastChunk []byte) string {
	h := sha256.New()
	var sizeBuf [8]byte
	binary.LittleEndian.PutUint64(sizeBuf[:], uint64(size))
	h.Write(sizeBuf[:])
	h.Write(firstChunk)
	h.Write(lastChunk)
	return hex.EncodeToString(h.Sum(nil))
}
