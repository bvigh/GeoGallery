package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/ingest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type registrarDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type registrarStorage interface {
	Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Delete(ctx context.Context, key string) error
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

// registerMedia stores one media file for ownerID and returns its media ID. inserted is false when
// the file is a duplicate by (owner_id, file_hash): mediaID is then the existing row's ID, so the
// caller can still add it to an album. The hash is only known once the file has streamed to S3, so
// the object uploaded for a duplicate, or for an INSERT the database rejected, is deleted again.
func (r *MediaRegistrar) registerMedia(
	ctx context.Context,
	ownerID string,
	body io.Reader,
	size int64,
	filename string,
	fallbackMtime *time.Time,
) (mediaID string, inserted bool, err error) {
	const headSize = 1024 * 1024 // 1MB — safe margin for EXIF
	head := make([]byte, headSize)
	n, err := io.ReadFull(body, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		// io.ErrUnexpectedEOF means the file is smaller than headSize, which is fine
		return "", false, err
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
			return "", false, err
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
				return "", false, terr
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
			return "", false, err
		}
		uploadBody = tmpFile
	} else {
		tc = &tailCapture{r: body}
		uploadBody = io.MultiReader(bytes.NewReader(head), tc)
	}

	key := newStorageKey(ownerID, ext)
	if err := r.storage.Upload(ctx, key, uploadBody, size, contentTypeFor(ext)); err != nil {
		slog.Error("register: S3 upload failed", "key", key, "err", err)
		return "", false, err
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

	err = r.db.QueryRow(ctx, `
		INSERT INTO media (owner_id, filename, media_type, location, gps_confidence, gps_source, taken_at, storage_key, file_hash)
		VALUES ($1, $2, $3,
		        CASE WHEN $4::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($5, $4), 4326) END,
		        $6, $7, $8, $9, $10)
		ON CONFLICT (owner_id, file_hash) DO NOTHING
		RETURNING id
	`,
		ownerID, filename, mediaType,
		exifData.Lat, exifData.Lng,
		nil, // confidence populated by background GPS matcher
		gpsSource(exifData),
		takenAt, key, fileHash,
	).Scan(&mediaID)
	if errors.Is(err, pgx.ErrNoRows) {
		// DO NOTHING returns no row for a duplicate, so nothing references the object just uploaded.
		// The ID comes from the row that already exists.
		r.deleteObject(ctx, key)
		if err := r.db.QueryRow(ctx,
			`SELECT id FROM media WHERE owner_id = $1 AND file_hash = $2`, ownerID, fileHash,
		).Scan(&mediaID); err != nil {
			return "", false, fmt.Errorf("look up duplicate: %w", err)
		}
		return mediaID, false, nil
	}
	if err != nil {
		if insertRejected(err) {
			r.deleteObject(ctx, key)
		} else {
			slog.Warn("register: media insert failed, keeping the object in case the row was stored", "key", key, "err", err)
		}
		return "", false, err
	}
	if exifData.Lat != nil && takenAt != nil {
		if err := insertExifGPSTrack(ctx, r.db, ownerID, *exifData.Lat, *exifData.Lng, *takenAt); err != nil {
			slog.Warn("register: exif GPS track insert failed", "err", err)
		}
	}
	return mediaID, true, nil
}

// deleteObject removes an uploaded object that no media row references. A failure only leaves an
// orphan in the private bucket, so it is logged and does not fail the file.
func (r *MediaRegistrar) deleteObject(ctx context.Context, key string) {
	if err := r.storage.Delete(ctx, key); err != nil {
		slog.Error("register: delete unreferenced object failed", "key", key, "err", err)
	}
}

// insertRejected reports whether err from the media INSERT means no row was stored: the server
// rejected the statement, or it was never sent. Other errors, such as a dropped connection or a
// cancelled context, can arrive after the commit, and deleting the object then would leave the row
// pointing at nothing.
func insertRejected(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) || pgconn.SafeToRetry(err)
}

// newStorageKey returns an opaque object key. The filename and upload time never enter it:
// presigned URLs expose the key, and either would hint at the answer in the guessing game.
func newStorageKey(ownerID, ext string) string {
	ext = strings.ToLower(ext)
	if !supportedExts[ext] {
		ext = ""
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error since Go 1.24
	return fmt.Sprintf("media/%s/%s%s", ownerID, hex.EncodeToString(b), ext)
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
