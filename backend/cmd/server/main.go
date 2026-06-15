package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/config"
	"codeberg.org/bvigh/geogallery/internal/db"
	"codeberg.org/bvigh/geogallery/internal/handlers"
	"codeberg.org/bvigh/geogallery/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	// Setup logging first so all startup errors are structured.
	setupLogger(os.Getenv("LOG_LEVEL"), os.Getenv("LOG_FORMAT"))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	migrationsPath := envOr("MIGRATIONS_PATH", "./migrations")
	if err := db.RunMigrations(cfg.DatabaseURL, migrationsPath); err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	s3Client, err := storage.NewClient(cfg)
	if err != nil {
		slog.Error("s3 client init failed", "err", err)
		os.Exit(1)
	}
	if err := s3Client.Ping(context.Background()); err != nil {
		slog.Error("s3 connectivity check failed", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket, "err", err)
		os.Exit(1)
	}
	slog.Info("s3 connected", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)

	matchWindow := time.Duration(cfg.GPSMatchWindowHours) * time.Hour
	mediaH := handlers.NewMediaHandler(pool, s3Client, matchWindow, cfg.TripGapHours)
	tripH := handlers.NewTripHandler(pool, cfg.TripGapHours)
	gameH := handlers.NewGameHandler(pool, s3Client)
	ingestH := handlers.NewIngestHandler(pool, s3Client, cfg.IngestWatchDir, matchWindow, cfg.TripGapHours)
	gpsTrackH := handlers.NewGPSTrackHandler(pool, matchWindow)
	gpsMatcherH := handlers.NewGPSMatcherHandler(pool, matchWindow)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Content-Type"},
	}))

	r.Route("/api", func(r chi.Router) {
		r.Get("/media", mediaH.List)
		r.Post("/media/upload", mediaH.Upload)
		r.Get("/media/{id}/url", mediaH.GetURL)
		r.Patch("/media/{id}/location", mediaH.UpdateLocation)
		r.Patch("/media/{id}/view-settings", mediaH.UpdateViewSettings)
		r.Get("/media/{id}/keyframes", mediaH.GetKeyframes)
		r.Put("/media/{id}/keyframes", mediaH.ReplaceKeyframes)
		r.Delete("/media/{id}", mediaH.Delete)

		r.Get("/trips", tripH.List)
		r.Patch("/trips/{id}", tripH.Update)
		r.Delete("/trips/{id}", tripH.Delete)

		r.Get("/game/round", gameH.NewRound)
		r.Post("/game/guess", gameH.SubmitGuess)

		r.Post("/ingest/scan", ingestH.Scan)

		r.Post("/gps-tracks/import", gpsTrackH.Import)
		r.Post("/gps-matcher/run", gpsMatcherH.Run)

		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"status":"ok"}`))
		})
	})

	addr := fmt.Sprintf(":%s", cfg.ServerPort)
	slog.Info("server listening", "addr", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func setupLogger(level, format string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(handler))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
