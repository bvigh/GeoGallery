package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type gpsTrackDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type GPSTrackHandler struct {
	db          gpsTrackDB
	matcherPool matcherDB // same *pgxpool.Pool in production; nil in tests (runMatcher is mocked)
	matchWindow time.Duration
	runMatcher  func(ctx context.Context, db matcherDB, maxWindow time.Duration, reprocess bool) error
}

func NewGPSTrackHandler(pool *pgxpool.Pool, matchWindow time.Duration) *GPSTrackHandler {
	return &GPSTrackHandler{
		db:          pool,
		matcherPool: pool,
		matchWindow: matchWindow,
		runMatcher:  RunGPSMatcher,
	}
}

type gpsPointInput struct {
	RecordedAt string   `json:"recorded_at"`
	Lat        float64  `json:"lat"`
	Lng        float64  `json:"lng"`
	Source     string   `json:"source"`
	AltitudeM  *float64 `json:"altitude_m"`
	AccuracyM  *float64 `json:"accuracy_m"`
	Activity   *string  `json:"activity"`
}

type rowError struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
}

type importResponse struct {
	Inserted int        `json:"inserted"`
	Skipped  int        `json:"skipped"`
	Errors   []rowError `json:"errors"`
}

const importBatchSize = 1000

func (h *GPSTrackHandler) Import(w http.ResponseWriter, r *http.Request) {
	var points []gpsPointInput
	if err := json.NewDecoder(r.Body).Decode(&points); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	type validPoint struct {
		recordedAt time.Time
		lat        float64
		lng        float64
		source     string
		altitudeM  *float64
		accuracyM  *float64
		activity   *string
	}

	var valid []validPoint
	var errs []rowError

	for i, p := range points {
		t, err := time.Parse(time.RFC3339, p.RecordedAt)
		if err != nil {
			errs = append(errs, rowError{Row: i, Reason: "invalid recorded_at: must be RFC3339"})
			continue
		}
		if p.Lat < -90 || p.Lat > 90 {
			errs = append(errs, rowError{Row: i, Reason: "invalid lat: must be between -90 and 90"})
			continue
		}
		if p.Lng < -180 || p.Lng > 180 {
			errs = append(errs, rowError{Row: i, Reason: "invalid lng: must be between -180 and 180"})
			continue
		}
		if strings.TrimSpace(p.Source) == "" {
			errs = append(errs, rowError{Row: i, Reason: "invalid source: must not be empty"})
			continue
		}
		valid = append(valid, validPoint{
			recordedAt: t,
			lat:        p.Lat,
			lng:        p.Lng,
			source:     p.Source,
			altitudeM:  p.AltitudeM,
			accuracyM:  p.AccuracyM,
			activity:   p.Activity,
		})
	}

	resp := importResponse{Errors: errs}
	if resp.Errors == nil {
		resp.Errors = []rowError{}
	}

	ctx := r.Context()

	for start := 0; start < len(valid); start += importBatchSize {
		end := start + importBatchSize
		if end > len(valid) {
			end = len(valid)
		}
		batch := valid[start:end]

		var sb strings.Builder
		sb.WriteString(`INSERT INTO gps_tracks (source, recorded_at, location, altitude_m, accuracy_m, activity) VALUES `)
		args := make([]any, 0, len(batch)*7)
		for j, vp := range batch {
			if j > 0 {
				sb.WriteString(", ")
			}
			base := j*7 + 1
			sb.WriteString(fmt.Sprintf("($%d, $%d, ST_SetSRID(ST_MakePoint($%d, $%d), 4326), $%d, $%d, $%d)",
				base, base+1, base+2, base+3, base+4, base+5, base+6))
			args = append(args, vp.source, vp.recordedAt, vp.lng, vp.lat, vp.altitudeM, vp.accuracyM, vp.activity)
		}
		sb.WriteString(` ON CONFLICT DO NOTHING`)

		tag, err := h.db.Exec(ctx, sb.String(), args...)
		if err != nil {
			slog.Error("gps-tracks: batch insert failed", "batch_start", start, "err", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		inserted := int(tag.RowsAffected())
		resp.Inserted += inserted
		resp.Skipped += len(batch) - inserted
	}

	if len(valid) > 0 {
		if err := h.runMatcher(ctx, h.matcherPool, h.matchWindow, true); err != nil {
			slog.Error("gps-tracks: GPS matcher failed after import", "err", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
