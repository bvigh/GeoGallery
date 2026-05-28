package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MediaHandler struct {
	db      *pgxpool.Pool
	storage *storage.Client
}

func NewMediaHandler(db *pgxpool.Pool, s *storage.Client) *MediaHandler {
	return &MediaHandler{db: db, storage: s}
}

type mediaItem struct {
	ID          string   `json:"id"`
	Filename    string   `json:"filename"`
	MediaType   string   `json:"media_type"`
	Lat         *float64 `json:"lat"`
	Lng         *float64 `json:"lng"`
	Confidence  *float64 `json:"gps_confidence"`
	GpsSource   *string  `json:"gps_source"`
	TakenAt     *string  `json:"taken_at"`
	StorageKey  string   `json:"storage_key"`
	TripID      *string  `json:"trip_id"`
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

	var uploaded []string
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			continue
		}

		ext := strings.ToLower(filepath.Ext(fh.Filename))
		mediaType := detectMediaType(ext)
		exifData := ingest.ExtractExif(f)
		f.Seek(0, 0)

		key := fmt.Sprintf("media/%d/%s", time.Now().UnixNano(), fh.Filename)
		if err := h.storage.Upload(r.Context(), key, f, contentTypeFor(ext)); err != nil {
			f.Close()
			continue
		}
		f.Close()

		var takenAt *time.Time
		if exifData.TakenAt != nil {
			takenAt = exifData.TakenAt
		} else {
			// fall back to file modification time via form field if provided
			t := time.Now()
			takenAt = &t
		}

		_, err = h.db.Exec(r.Context(), `
			INSERT INTO media (filename, media_type, location, gps_confidence, gps_source, taken_at, storage_key)
			VALUES ($1, $2,
			        CASE WHEN $3::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($4, $3), 4326) END,
			        $5, $6, $7, $8)
		`,
			fh.Filename, mediaType,
			exifData.Lat, exifData.Lng,
			nil, // confidence populated by background GPS matcher
			gpsSource(exifData),
			takenAt, key,
		)
		if err == nil {
			uploaded = append(uploaded, fh.Filename)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"uploaded": uploaded})
}

func detectMediaType(ext string) string {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".heic", ".heif", ".webp":
		return "photo"
	case ".mp4", ".mov", ".avi", ".mkv":
		return "video"
	default:
		return "photo"
	}
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
