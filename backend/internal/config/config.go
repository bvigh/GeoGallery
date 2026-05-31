package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL         string
	S3Endpoint          string
	S3AccessKey         string
	S3SecretKey         string
	S3Bucket            string
	S3Region            string
	ServerPort          string
	IngestWatchDir      string
	GPSMatchWindowHours int
	TripGapHours        int
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:         getEnv("DATABASE_URL", ""),
		S3Endpoint:          getEnv("S3_ENDPOINT", "http://localhost:9000"),
		S3AccessKey:         getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:         getEnv("S3_SECRET_KEY", "minioadmin"),
		S3Bucket:            getEnv("S3_BUCKET", "geogallery"),
		S3Region:            getEnv("S3_REGION", "us-east-1"),
		ServerPort:          getEnv("SERVER_PORT", "8080"),
		IngestWatchDir:      getEnv("INGEST_WATCH_DIR", "./ingest"),
		GPSMatchWindowHours: getEnvInt("GPS_MATCH_WINDOW_HOURS", 1),
		TripGapHours:        getEnvInt("TRIP_GAP_HOURS", 24),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
