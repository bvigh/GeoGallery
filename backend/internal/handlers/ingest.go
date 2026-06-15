package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IngestHandler struct {
	registrar     *MediaRegistrar
	watchDir      string
	matcherPool   matcherDB
	matchWindow   time.Duration
	runMatcher    func(ctx context.Context, db matcherDB, maxWindow time.Duration, reprocess bool) error
	tripGapHours  int
	runTripDetect func(ctx context.Context, db tripDetectDB, gapHours int) error
}

func NewIngestHandler(db *pgxpool.Pool, s *storage.Client, watchDir string, matchWindow time.Duration, tripGapHours int) *IngestHandler {
	return &IngestHandler{
		registrar:     &MediaRegistrar{db: db, storage: s},
		watchDir:      watchDir,
		matcherPool:   db,
		matchWindow:   matchWindow,
		tripGapHours:  tripGapHours,
		runMatcher:    RunGPSMatcher,
		runTripDetect: RunTripDetect,
	}
}

var supportedExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".heic": true, ".heif": true, ".webp": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
}

func (h *IngestHandler) Scan(w http.ResponseWriter, r *http.Request) {
	slog.Info("ingest: scan started", "dir", h.watchDir)
	var ingested, skipped int

	err := filepath.WalkDir(h.watchDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !supportedExts[ext] {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			slog.Error("ingest: stat file failed", "path", path, "err", err)
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			slog.Error("ingest: open file failed", "path", path, "err", err)
			return nil
		}
		mtime := info.ModTime()
		registered, err := h.registrar.registerMedia(r.Context(), f, info.Size(), d.Name(), &mtime)
		f.Close()
		if err != nil {
			slog.Error("ingest: register media failed", "path", path, "err", err)
			return nil
		}
		if registered {
			ingested++
		} else {
			skipped++
		}
		return nil
	})
	if err != nil {
		slog.Error("ingest: walk dir failed", "dir", h.watchDir, "err", err)
		http.Error(w, "scan error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.runMatcher(r.Context(), h.matcherPool, h.matchWindow, true); err != nil {
		slog.Error("ingest: GPS matcher failed", "err", err)
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	if err := h.runTripDetect(r.Context(), h.registrar.db, h.tripGapHours); err != nil {
		slog.Error("ingest: trip detect failed", "err", err)
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	slog.Info("ingest: scan complete", "ingested", ingested, "skipped", skipped)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"ingested": ingested, "skipped": skipped})
}
