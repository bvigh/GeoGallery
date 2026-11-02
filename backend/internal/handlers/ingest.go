package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IngestDeps holds what RunIngestScan needs; the operator `server scan` command builds it.
type IngestDeps struct {
	registrar     *MediaRegistrar
	matcherPool   matcherDB
	matchWindow   time.Duration
	runMatcher    func(ctx context.Context, db matcherDB, ownerID string, maxWindow time.Duration, reprocess bool) error
	tripGapHours  int
	runTripDetect func(ctx context.Context, db tripDetectDB, ownerID string, gapHours int) error
}

func NewIngestDeps(pool *pgxpool.Pool, s *storage.Client, matchWindow time.Duration, gapHours int) IngestDeps {
	return IngestDeps{
		registrar:     &MediaRegistrar{db: pool, storage: s},
		matcherPool:   pool,
		matchWindow:   matchWindow,
		tripGapHours:  gapHours,
		runMatcher:    RunGPSMatcher,
		runTripDetect: RunTripDetect,
	}
}

type ScanResult struct {
	Ingested int
	Skipped  int
	Failed   int
}

var supportedExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".heic": true, ".heif": true, ".webp": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".3gp": true,
}

// RunIngestScan walks dir and ingests every supported file into ownerID's library, then runs
// the GPS matcher and trip detection for that owner. Per-file failures are logged, counted in
// Failed and skipped. On cancellation the walk stops after the current file, the matcher and
// trip detection are skipped, and ctx.Err() is returned.
func RunIngestScan(ctx context.Context, deps IngestDeps, dir, ownerID string) (ScanResult, error) {
	slog.Info("ingest: scan started", "dir", dir, "owner_id", ownerID)
	var res ScanResult

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
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
			res.Failed++
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			slog.Error("ingest: open file failed", "path", path, "err", err)
			res.Failed++
			return nil
		}
		mtime := info.ModTime()
		_, inserted, err := deps.registrar.registerMedia(ctx, ownerID, f, info.Size(), d.Name(), &mtime)
		f.Close()
		if err != nil {
			slog.Error("ingest: register media failed", "path", path, "err", err)
			res.Failed++
			return nil
		}
		if inserted {
			res.Ingested++
		} else {
			res.Skipped++
		}
		return nil
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		slog.Warn("ingest: scan interrupted", "ingested", res.Ingested, "skipped", res.Skipped, "failed", res.Failed)
		return res, ctxErr
	}
	if err != nil {
		slog.Error("ingest: walk dir failed", "dir", dir, "err", err)
		return res, fmt.Errorf("walk %s: %w", dir, err)
	}

	if err := deps.runMatcher(ctx, deps.matcherPool, ownerID, deps.matchWindow, true); err != nil {
		slog.Error("ingest: GPS matcher failed", "err", err)
		return res, fmt.Errorf("gps matcher: %w", err)
	}
	if err := deps.runTripDetect(ctx, deps.registrar.db, ownerID, deps.tripGapHours); err != nil {
		slog.Error("ingest: trip detect failed", "err", err)
		return res, fmt.Errorf("trip detect: %w", err)
	}

	slog.Info("ingest: scan complete", "ingested", res.Ingested, "skipped", res.Skipped, "failed", res.Failed)
	return res, nil
}
