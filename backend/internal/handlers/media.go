package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mediaDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type mediaStorage interface {
	Upload(ctx context.Context, key string, body io.Reader, contentType string) error
	PresignGet(ctx context.Context, key string) (string, error)
}

type MediaHandler struct {
	db            mediaDB
	storage       mediaStorage
	matcherPool   matcherDB
	matchWindow   time.Duration
	runMatcher    func(ctx context.Context, db matcherDB, maxWindow time.Duration, reprocess bool) error
	tripGapHours  int
	runTripDetect func(ctx context.Context, db tripDetectDB, gapHours int) error
}

func NewMediaHandler(db *pgxpool.Pool, s *storage.Client, matchWindow time.Duration, tripGapHours int) *MediaHandler {
	return &MediaHandler{
		db:            db,
		storage:       s,
		matcherPool:   db,
		matchWindow:   matchWindow,
		runMatcher:    RunGPSMatcher,
		tripGapHours:  tripGapHours,
		runTripDetect: RunTripDetect,
	}
}

type mediaItem struct {
	ID         string     `json:"id"`
	Filename   string     `json:"filename"`
	MediaType  string     `json:"media_type"`
	Lat        *float64   `json:"lat"`
	Lng        *float64   `json:"lng"`
	Confidence *float64   `json:"gps_confidence"`
	GpsSource  *string    `json:"gps_source"`
	TakenAt    *time.Time `json:"taken_at"`
	StorageKey string     `json:"storage_key"`
	TripID     *string    `json:"trip_id"`
}

func (h *MediaHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT id, filename, media_type,
		       ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lng,
		       gps_confidence, gps_source,
		       taken_at, storage_key, trip_id
		FROM media
		ORDER BY taken_at DESC NULLS LAST
		LIMIT 500
	`)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var items []mediaItem
	for rows.Next() {
		var m mediaItem
		var lat, lng *float64
		if err := rows.Scan(&m.ID, &m.Filename, &m.MediaType, &lat, &lng,
			&m.Confidence, &m.GpsSource, &m.TakenAt, &m.StorageKey, &m.TripID); err != nil {
			continue
		}
		m.Lat = lat
		m.Lng = lng
		items = append(items, m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (h *MediaHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		http.Error(w, "parse form", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "no files", http.StatusBadRequest)
		return
	}

	mtimes := r.MultipartForm.Value["file_mtime"]

	var uploaded []string
	for i, fh := range files {
		f, err := fh.Open()
		if err != nil {
			continue
		}

		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			continue
		}

		h256 := sha256.Sum256(data)
		fileHash := hex.EncodeToString(h256[:])

		ext := strings.ToLower(filepath.Ext(fh.Filename))
		exifData := ingest.ExtractExif(bytes.NewReader(data))
		mediaType := ingest.DetectMediaType(exifData, fh.Filename, ext)

		key := fmt.Sprintf("media/%d/%s", time.Now().UnixNano(), fh.Filename)
		if err := h.storage.Upload(r.Context(), key, bytes.NewReader(data), contentTypeFor(ext)); err != nil {
			continue
		}

		var takenAt *time.Time
		if exifData.TakenAt != nil {
			takenAt = exifData.TakenAt
		} else if i < len(mtimes) {
			if ms, err := strconv.ParseInt(mtimes[i], 10, 64); err == nil {
				t := time.UnixMilli(ms)
				takenAt = &t
			}
		}

		tag, err := h.db.Exec(r.Context(), `
			INSERT INTO media (filename, media_type, location, gps_confidence, gps_source, taken_at, storage_key, file_hash)
			VALUES ($1, $2,
			        CASE WHEN $3::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($4, $3), 4326) END,
			        $5, $6, $7, $8, $9)
			ON CONFLICT (file_hash) DO NOTHING
		`,
			fh.Filename, mediaType,
			exifData.Lat, exifData.Lng,
			nil, // confidence populated by background GPS matcher
			gpsSource(exifData),
			takenAt, key, fileHash,
		)
		if err == nil && tag.RowsAffected() == 1 {
			if exifData.Lat != nil && takenAt != nil {
				insertExifGPSTrack(r.Context(), h.db, *exifData.Lat, *exifData.Lng, *takenAt)
			}
			uploaded = append(uploaded, fh.Filename)
		}
	}

	if len(uploaded) > 0 {
		if err := h.runMatcher(r.Context(), h.matcherPool, h.matchWindow, true); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if err := h.runTripDetect(r.Context(), h.matcherPool, h.tripGapHours); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"uploaded": uploaded})
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

func gpsSource(e ingest.ExifData) *string {
	if e.Lat != nil {
		s := "exif"
		return &s
	}
	return nil
}

func (h *MediaHandler) GetURL(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var storageKey string
	err := h.db.QueryRow(r.Context(),
		`SELECT storage_key FROM media WHERE id=$1`, id,
	).Scan(&storageKey)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	url, err := h.storage.PresignGet(r.Context(), storageKey)
	if err != nil {
		http.Error(w, "presign error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}
