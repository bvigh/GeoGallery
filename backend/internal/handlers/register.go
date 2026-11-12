package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
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
	// cameraZone is the zone EXIF capture times are read in when the photo's own clocks do not say
	// otherwise (ingest.ExtractExif). A zero value reads them as UTC.
	cameraZone ingest.CameraZone
}

// exifDecodeSlots is how many files this process can have their EXIF decoded at once, so what
// decoding can take in memory and CPU follows this number and not the number of uploads running.
// server scan is a process of its own, with its own slots. Decoding a normal photo is quick, so
// nothing queues in normal use.
var exifDecodeSlots = make(chan struct{}, 4)

// inExifDecodeSlot runs decode in one of the exifDecodeSlots. Waiting for a slot ends with ctx; the
// decode itself cannot be cancelled.
func inExifDecodeSlot[T any](ctx context.Context, decode func() T) (T, error) {
	select {
	case exifDecodeSlots <- struct{}{}:
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
	defer func() { <-exifDecodeSlots }()
	return decode(), nil
}

// extractExif is ingest.ExtractExif in one of the exifDecodeSlots.
func (r *MediaRegistrar) extractExif(ctx context.Context, head []byte) (ingest.ExifData, error) {
	return inExifDecodeSlot(ctx, func() ingest.ExifData {
		return ingest.ExtractExif(bytes.NewReader(head), r.cameraZone.Loc)
	})
}

// registerMedia stores one media file for ownerID and returns its media ID. inserted is false when
// the file is a duplicate by (owner_id, file_hash): mediaID is then the existing row's ID, so the
// caller can still add it to an album. The hash only needs the head and the tail of the file, so a
// duplicate is found before the file is probed, transcoded or uploaded. Two concurrent calls with
// the same file can both miss that lookup: the INSERT's ON CONFLICT settles it, and the object
// uploaded for the losing call, or for an INSERT the database rejected, is deleted again.
func (r *MediaRegistrar) registerMedia(
	ctx context.Context,
	ownerID string,
	body io.ReaderAt,
	size int64,
	filename string,
	fallbackMtime *time.Time,
) (mediaID string, inserted bool, err error) {
	if size <= 0 {
		return "", false, errors.New("empty file")
	}
	head := make([]byte, min(size, mediaHeadSize))
	if _, err := io.ReadFull(io.NewSectionReader(body, 0, size), head); err != nil {
		return "", false, fmt.Errorf("read head: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(filename))
	exifData, err := r.extractExif(ctx, head)
	if err != nil {
		return "", false, fmt.Errorf("wait to read exif: %w", err)
	}
	// Insta360 and similar cameras write moov at end of file, so EXIF dimension extraction from
	// the head alone fails, and the video is probed with ffprobe instead.
	probeVideo := ingest.IsVideoExt(ext) && exifData.WidthPx == nil

	fileHash, err := mediaHash(body, size, head, probeVideo)
	if err != nil {
		return "", false, fmt.Errorf("hash: %w", err)
	}
	err = r.db.QueryRow(ctx,
		`SELECT id FROM media WHERE owner_id = $1 AND file_hash = $2`, ownerID, fileHash,
	).Scan(&mediaID)
	if err == nil {
		return mediaID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("look up duplicate: %w", err)
	}

	var mediaType string
	var tmpFile *os.File
	var probe ingest.VideoProbeResult
	if probeVideo {
		// Buffer the full video to a temp file so ffprobe can read the container metadata.
		tmpFile, probe, err = ingest.ProbeVideo(io.NewSectionReader(body, 0, size))
		if err != nil {
			return "", false, err
		}
		mediaType = ingest.DetectMediaTypeFromProbe(exifData, filename, ext, probe)
	} else {
		mediaType = ingest.DetectMediaType(exifData, filename, ext)
	}

	var uploadBody io.Reader
	if tmpFile != nil {
		if probe.NeedsTranscode() {
			slog.Info("register: transcoding to H.264", "filename", filename, "codec", probe.CodecName)
			transcoded, terr := ingest.TranscodeH264(tmpFile.Name(), probe.CreatedAt)
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
		uploadBody = io.NewSectionReader(body, 0, size)
	}

	key := newStorageKey(ownerID, ext)
	if err := r.storage.Upload(ctx, key, uploadBody, size, contentTypeFor(ext)); err != nil {
		slog.Error("register: S3 upload failed", "key", key, "err", err)
		return "", false, err
	}

	takenAt := exifData.TakenAt
	if takenAt == nil {
		takenAt = probe.CreatedAt
	}
	if takenAt == nil {
		takenAt = fallbackMtime
	}
	evidence := newCaptureEvidence(exifData, probe, fallbackMtime, r.cameraZone)
	if takenAt != nil && !jsonCanCarry(*takenAt) {
		slog.Warn("register: capture time outside the years JSON can carry, storing none", "filename", filename, "year", takenAt.UTC().Year())
		takenAt = nil
		evidence = evidence.timeDropped()
	}

	// EXIF position, or a video's own location tag (probe is empty for a photo).
	lat, lng := exifData.Lat, exifData.Lng
	if lat == nil {
		lat, lng = probe.Lat, probe.Lng
	}

	err = r.db.QueryRow(ctx, `
		INSERT INTO media (owner_id, filename, media_type, location, gps_confidence, gps_source, taken_at, storage_key, file_hash,
		                   camera_model, width_px, height_px, duration_sec, capture_evidence)
		VALUES ($1, $2, $3,
		        CASE WHEN $4::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($5, $4), 4326) END,
		        $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (owner_id, file_hash) DO NOTHING
		RETURNING id
	`,
		ownerID, filename, mediaType,
		lat, lng,
		nil, // confidence populated by background GPS matcher
		gpsSource(lat),
		takenAt, key, fileHash,
		nilIfEmpty(exifData.CameraModel),
		dimension(exifData.WidthPx, probe.WidthPx),
		dimension(exifData.HeightPx, probe.HeightPx),
		probe.DurationS,
		evidence.encode(),
	).Scan(&mediaID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent call stored the same file after the lookup above. DO NOTHING returns no row,
		// so nothing references the object just uploaded. The ID comes from the row that won.
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
	// A video's location tag makes no track point. The tag can be the phone's last known fix, and
	// taken_at is when the recording ended, so the pair is too loose to place other media.
	if exifData.Lat != nil && takenAt != nil {
		if err := insertExifGPSTrack(ctx, r.db, ownerID, *exifData.Lat, *exifData.Lng, *takenAt); err != nil {
			slog.Warn("register: exif GPS track insert failed", "err", err)
		}
	}
	return mediaID, true, nil
}

// The years time.Time marshals to JSON.
const (
	jsonFirstYear = 0
	jsonLastYear  = 9999
)

// jsonCanCarry reports whether t can be sent as JSON wherever it is read back. One year is kept in
// reserve at each end of what time.Time marshals, because pgx returns a time in the server's zone.
func jsonCanCarry(t time.Time) bool {
	y := t.UTC().Year()
	return y > jsonFirstYear && y < jsonLastYear
}

// What produced taken_at (captureEvidence.Src).
const (
	captureSrcExifOriginal = "exif_original"
	captureSrcExifIFD0     = "exif_ifd0"
	captureSrcContainer    = "container"
	captureSrcMtime        = "mtime"
	captureSrcNone         = "none"
)

// captureEvidence is media.capture_evidence: how taken_at was derived, with the inputs that lost.
// Those are computed at ingest and cannot be recovered later. Every key is always present.
type captureEvidence struct {
	V                     int      `json:"v"`
	By                    string   `json:"by"`
	Src                   string   `json:"src"`
	Zone                  *string  `json:"zone"`
	Wall                  *string  `json:"wall"`
	GPSUTC                *string  `json:"gps_utc"`
	WallMinusGPSS         *float64 `json:"wall_minus_gps_s"`
	OffsetTag             *string  `json:"offset_tag"`
	OffsetIgnored         bool     `json:"offset_ignored"`
	OffsetUsedMin         *int     `json:"offset_used_min"`
	CameraZone            string   `json:"camera_zone"`
	CameraZoneUTCFallback bool     `json:"camera_zone_utc_fallback"`
	Make                  *string  `json:"make"`
	Model                 *string  `json:"model"`
	ExifPartial           bool     `json:"exif_partial"`
	CreationTime          *string  `json:"creation_time"`
	FileMtime             *string  `json:"file_mtime"`
	DurationS             *float64 `json:"duration_s"`
	Encoder               *string  `json:"encoder"`
	Codec                 *string  `json:"codec"`
	Transcoded            bool     `json:"transcoded"`
}

// newCaptureEvidence records how registerMedia derived taken_at. mtime is the caller's fallback
// time. A wall tag that exists but does not parse records the origin that actually applied, while
// Wall still shows its text.
func newCaptureEvidence(e ingest.ExifData, probe ingest.VideoProbeResult, mtime *time.Time, zone ingest.CameraZone) captureEvidence {
	ev := captureEvidence{
		V:                     1,
		By:                    "ingest",
		Src:                   captureSource(e, probe, mtime),
		Wall:                  nilIfEmpty(e.Raw.WallClock),
		GPSUTC:                rfc3339UTC(e.Raw.GPSStamp),
		OffsetTag:             nilIfEmpty(e.Raw.OffsetTag),
		OffsetIgnored:         e.Raw.OffsetIgnored,
		CameraZone:            zone.Name(),
		CameraZoneUTCFallback: zone.UTCFallback,
		Make:                  nilIfEmpty(e.Raw.Make),
		Model:                 nilIfEmpty(e.CameraModel),
		ExifPartial:           e.Raw.Partial,
		CreationTime:          rfc3339UTC(probe.CreatedAt),
		FileMtime:             rfc3339UTC(mtime),
		DurationS:             probe.DurationS,
		Encoder:               nilIfEmpty(probe.Encoder),
		Codec:                 nilIfEmpty(ingest.CleanTag(probe.CodecName)),
		Transcoded:            probe.NeedsTranscode(),
	}
	if ev.GPSUTC != nil && e.Raw.WallMinusGPS != nil {
		seconds := e.Raw.WallMinusGPS.Seconds()
		ev.WallMinusGPSS = &seconds
	}
	if e.TakenAt != nil {
		zoneRule := e.TakenAtZone
		ev.Zone = &zoneRule
		_, seconds := e.TakenAt.Zone()
		minutes := seconds / 60
		ev.OffsetUsedMin = &minutes
	}
	return ev
}

// captureSource names what produced taken_at, in the order registerMedia picks it.
func captureSource(e ingest.ExifData, probe ingest.VideoProbeResult, mtime *time.Time) string {
	switch {
	case e.TakenAt != nil && e.Raw.WallTag == ingest.WallTagIFD0:
		return captureSrcExifIFD0
	case e.TakenAt != nil:
		return captureSrcExifOriginal
	case probe.CreatedAt != nil:
		return captureSrcContainer
	case mtime != nil:
		return captureSrcMtime
	default:
		return captureSrcNone
	}
}

// timeDropped records that no taken_at was stored although a source gave one, because JSON could not
// carry its year (jsonCanCarry). No source applied, so none is named, while the inputs stay: wall,
// creation_time and file_mtime still show what was refused.
func (c captureEvidence) timeDropped() captureEvidence {
	c.Src = captureSrcNone
	c.Zone = nil
	c.OffsetUsedMin = nil
	return c
}

// encode returns the column value. It is NULL in the case that should not happen, because a debug
// field must never fail an upload.
func (c captureEvidence) encode() []byte {
	b, err := json.Marshal(c)
	if err != nil {
		slog.Error("register: encode capture evidence failed", "err", err)
		return nil
	}
	return b
}

// dimension picks a stored pixel size: EXIF, else the probe, where 0 means unknown. A value the INT
// column cannot hold counts as unknown too, because a debug field must never fail an upload.
func dimension(fromExif *int, fromProbe int) *int {
	if fromExif != nil && validDimension(*fromExif) {
		return fromExif
	}
	if validDimension(fromProbe) {
		return &fromProbe
	}
	return nil
}

func validDimension(v int) bool {
	return v > 0 && v <= math.MaxInt32
}

// nilIfEmpty turns a value cleaned by ingest.CleanTag into a NULL when nothing is left of it.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// rfc3339UTC formats t in UTC. A year RFC 3339 cannot express (a client can send any file_mtime)
// gives nil.
func rfc3339UTC(t *time.Time) *string {
	u := utcTime(t)
	if u == nil {
		return nil
	}
	s := u.Format(time.RFC3339Nano)
	return &s
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

// gpsSource labels a position read from the file itself. 'exif' covers a video's container tag too:
// what matters to the matcher and the UI is that it came from the file, not from a track.
func gpsSource(lat *float64) *string {
	if lat != nil {
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

const (
	mediaHeadSize  = 1024 * 1024 // read for EXIF; also bounds what mediaHash reads of a video
	mediaHashChunk = 64 * 1024
)

// mediaHash returns the dedup key stored in media.file_hash, from the file's first
// min(size, mediaHeadSize) bytes (head) and, when needed, its tail read from body.
//
// The definition is frozen: every existing row was hashed this way, so a change would make
// re-uploads of the whole library count as new files. It is not quite "the size plus the first
// and last 64 KB", because it reproduces what the original streaming code hashed:
//   - up to 128 KB: the size and the whole file;
//   - a probed video (probeVideo): partialHash of the head alone, so for a video over 1 MB the
//     hashed "size" is 1 MB and the end of the file is never read;
//   - any other file: the size, the first 64 KB and the last 64 KB of what follows the head
//     (nothing for a file up to 1 MB, fewer bytes for one up to 1 MB + 64 KB).
//
// frontend/src/lib/mediaHash.ts computes the same hash in the browser so the Upload page can skip
// files the library already has; both check frontend/src/lib/mediaHash.vectors.json.
func mediaHash(body io.ReaderAt, size int64, head []byte, probeVideo bool) (string, error) {
	if probeVideo || size <= 2*mediaHashChunk {
		return partialHash(head), nil
	}
	tailStart := max(size-mediaHashChunk, int64(len(head)))
	tail := make([]byte, size-tailStart)
	if len(tail) > 0 {
		// ReaderAt may report io.EOF along with a full read that ends at the end of the file.
		if n, err := body.ReadAt(tail, tailStart); n < len(tail) {
			return "", err
		}
	}
	return partialHashStream(size, head[:mediaHashChunk], tail), nil
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

// partialHashStream hashes the size, firstChunk and lastChunk. It equals partialHash for a file
// larger than 128KB when the chunks are its first and last 64KB.
func partialHashStream(size int64, firstChunk, lastChunk []byte) string {
	h := sha256.New()
	var sizeBuf [8]byte
	binary.LittleEndian.PutUint64(sizeBuf[:], uint64(size))
	h.Write(sizeBuf[:])
	h.Write(firstChunk)
	h.Write(lastChunk)
	return hex.EncodeToString(h.Sum(nil))
}
