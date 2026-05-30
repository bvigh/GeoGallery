package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IngestHandler struct {
	db         *pgxpool.Pool
	storage    *storage.Client
	watchDir   string
}

func NewIngestHandler(db *pgxpool.Pool, s *storage.Client, watchDir string) *IngestHandler {
	return &IngestHandler{db: db, storage: s, watchDir: watchDir}
}

var supportedExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".heic": true, ".heif": true, ".webp": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
}

func (h *IngestHandler) Scan(w http.ResponseWriter, r *http.Request) {
	var ingested, skipped int

	err := filepath.WalkDir(h.watchDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !supportedExts[ext] {
			return nil
		}

		// Skip already-ingested files
		var exists bool
		h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM media WHERE filename=$1)`, d.Name()).Scan(&exists)
		if exists {
			skipped++
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		exifData := ingest.ExtractExif(f)
		f.Seek(0, 0)

		key := fmt.Sprintf("media/%d/%s", time.Now().UnixNano(), d.Name())
		if err := h.storage.Upload(r.Context(), key, f, contentTypeFor(ext)); err != nil {
			return nil
		}

		takenAt := exifData.TakenAt
		if takenAt == nil {
			info, _ := d.Info()
			t := info.ModTime()
			takenAt = &t
		}

		h.db.Exec(r.Context(), `
			INSERT INTO media (filename, media_type, location, gps_source, taken_at, storage_key)
			VALUES ($1, $2,
			        CASE WHEN $3::float8 IS NOT NULL THEN ST_SetSRID(ST_MakePoint($4, $3), 4326) END,
			        $5, $6, $7)
			ON CONFLICT (storage_key) DO NOTHING
		`,
			d.Name(), ingest.DetectMediaType(exifData, d.Name(), ext),
			exifData.Lat, exifData.Lng,
			gpsSource(exifData),
			takenAt, key,
		)
		ingested++
		return nil
	})
	if err != nil {
		http.Error(w, "scan error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"ingested": ingested, "skipped": skipped})
}
