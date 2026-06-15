package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/ingest"
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
	n, err := io.ReadFull(body, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		// io.ErrUnexpectedEOF means the file is smaller than headSize, which is fine
		return false, err
	}
	head = head[:n]

	ext := strings.ToLower(filepath.Ext(filename))
	exifData := ingest.ExtractExif(bytes.NewReader(head))

	var mediaType string
	var tmpFile *os.File
	var probe ingest.VideoProbeResult
	if ingest.IsVideoExt(ext) && exifData.WidthPx == nil {
		// Insta360 and similar cameras write moov at end of file, so EXIF
		// dimension extraction from the head alone fails. Buffer the full
		// video to a temp file so ffprobe can read the container metadata.
		rest := io.MultiReader(bytes.NewReader(head), body)
		tmpFile, probe, err = ingest.ProbeVideo(rest)
		if err != nil {
			return false, err
		}
		mediaType = ingest.DetectMediaTypeFromProbe(exifData, filename, ext, probe)
	} else {
		mediaType = ingest.DetectMediaType(exifData, filename, ext)
	}

	var uploadBody io.Reader
	var tc *tailCapture
	if tmpFile != nil {
		if probe.NeedsTranscode() {
			slog.Info("register: transcoding to H.264", "filename", filename, "codec", probe.CodecName)
			transcoded, terr := ingest.TranscodeH264(tmpFile.Name())
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			if terr != nil {
				slog.Error("register: transcode failed", "filename", filename, "err", terr)
				return false, terr
			}
			tmpFile = transcoded
			// Update size from the transcoded file so the S3 upload length is correct.
			if fi, ferr := tmpFile.Stat(); ferr == nil {
				size = fi.Size()
			}
		}
		defer func() {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
		}()
		if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		uploadBody = tmpFile
	} else {
		tc = &tailCapture{r: body}
		uploadBody = io.MultiReader(bytes.NewReader(head), tc)
	}

	key := fmt.Sprintf("media/%d/%s", time.Now().UnixNano(), filename)
	if err := r.storage.Upload(ctx, key, uploadBody, size, contentTypeFor(ext)); err != nil {
		slog.Error("register: S3 upload failed", "key", key, "err", err)
		return false, err
	}

	const chunk = 64 * 1024
	var fileHash string
	if tc != nil && size > 2*chunk {
		fileHash = partialHashStream(size, head[:chunk], tc.tail)
	} else {
		fileHash = partialHash(head)
	}

	takenAt := exifData.TakenAt
	if takenAt == nil {
		takenAt = probe.CreatedAt
	}
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
			if err := insertExifGPSTrack(ctx, r.db, *exifData.Lat, *exifData.Lng, *takenAt); err != nil {
				slog.Warn("register: exif GPS track insert failed", "err", err)
			}
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
	case ".3gp":
		return "video/3gpp"
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
