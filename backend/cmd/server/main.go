package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bvigh/geogallery/internal/config"
	"github.com/bvigh/geogallery/internal/db"
	"github.com/bvigh/geogallery/internal/handlers"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	migrationsPath := envOr("MIGRATIONS_PATH", "./migrations")
	if err := db.RunMigrations(cfg.DatabaseURL, migrationsPath); err != nil {
		log.Fatalf("migrations: %v", err)
	}
	log.Println("migrations applied")

	s3Client, err := storage.NewClient(cfg)
	if err != nil {
		log.Fatalf("s3 client: %v", err)
	}

	matchWindow := time.Duration(cfg.GPSMatchWindowHours) * time.Hour
	mediaH := handlers.NewMediaHandler(pool, s3Client, matchWindow, cfg.TripGapHours)
	tripH := handlers.NewTripHandler(pool)
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

		r.Get("/trips", tripH.List)
		r.Patch("/trips/{id}", tripH.Update)

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
	log.Printf("server listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
