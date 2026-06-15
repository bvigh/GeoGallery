package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/geo"
	"codeberg.org/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

type GameHandler struct {
	db      *pgxpool.Pool
	storage *storage.Client
}

func NewGameHandler(db *pgxpool.Pool, s *storage.Client) *GameHandler {
	return &GameHandler{db: db, storage: s}
}

type roundMedia struct {
	ID           string   `json:"id"`
	MediaType    string   `json:"media_type"`
	URL          string   `json:"url"`
	InitialYaw   *float64 `json:"initial_yaw"`
	InitialPitch *float64 `json:"initial_pitch"`
	InitialFov   *float64 `json:"initial_fov"`
	MaxDistKm    float64  `json:"max_dist_km"`
}

func (h *GameHandler) NewRound(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromDate := q.Get("from_date")
	toDate := q.Get("to_date")
	tripIDs := q["trip_id"]
	mediaTypes := q["media_type"]

	conds := []string{"location IS NOT NULL"}
	var args []any
	n := 1

	if fromDate != "" {
		conds = append(conds, fmt.Sprintf("taken_at >= $%d::date", n))
		args = append(args, fromDate)
		n++
	}
	if toDate != "" {
		conds = append(conds, fmt.Sprintf("taken_at < ($%d::date + INTERVAL '1 day')", n))
		args = append(args, toDate)
		n++
	}
	if len(mediaTypes) > 0 {
		conds = append(conds, fmt.Sprintf("media_type = ANY($%d)", n))
		args = append(args, mediaTypes)
		n++
	}
	if len(tripIDs) > 0 {
		var hasUnassigned bool
		var uuids []string
		for _, id := range tripIDs {
			if id == "unassigned" {
				hasUnassigned = true
			} else {
				uuids = append(uuids, id)
			}
		}
		switch {
		case hasUnassigned && len(uuids) > 0:
			conds = append(conds, fmt.Sprintf("(trip_id = ANY($%d) OR trip_id IS NULL)", n))
			args = append(args, uuids)
			n++
		case hasUnassigned:
			conds = append(conds, "trip_id IS NULL")
		default:
			conds = append(conds, fmt.Sprintf("trip_id = ANY($%d)", n))
			args = append(args, uuids)
			n++
		}
	}

	where := "WHERE " + strings.Join(conds, " AND ")
	sql := fmt.Sprintf(`
		SELECT id, media_type, storage_key,
		       ST_Y(location::geometry), ST_X(location::geometry),
		       taken_at::text,
		       initial_yaw, initial_pitch, initial_fov
		FROM media
		%s
		ORDER BY RANDOM()
		LIMIT 1
	`, where)

	var m struct {
		ID           string   `json:"id"`
		MediaType    string   `json:"media_type"`
		StorageKey   string   `json:"storage_key"`
		Lat          *float64 `json:"lat"`
		Lng          *float64 `json:"lng"`
		TakenAt      *string  `json:"taken_at"`
		InitialYaw   *float64
		InitialPitch *float64
		InitialFov   *float64
	}

	err := h.db.QueryRow(r.Context(), sql, args...).Scan(&m.ID, &m.MediaType, &m.StorageKey, &m.Lat, &m.Lng, &m.TakenAt, &m.InitialYaw, &m.InitialPitch, &m.InitialFov)
	if err != nil {
		http.Error(w, "no media available", http.StatusNotFound)
		return
	}

	url, err := h.storage.PresignGet(r.Context(), m.StorageKey)
	if err != nil {
		slog.Error("game: presign failed", "id", m.ID, "key", m.StorageKey, "err", err)
		http.Error(w, "presign error", http.StatusInternalServerError)
		return
	}

	maxDistKm, err := h.computeMaxDistKm(r.Context(), conds, args)
	if err != nil {
		slog.Warn("game: max_dist_km computation failed, using default", "err", err)
		maxDistKm = 1.0
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(roundMedia{
		ID:           m.ID,
		MediaType:    m.MediaType,
		URL:          url,
		InitialYaw:   m.InitialYaw,
		InitialPitch: m.InitialPitch,
		InitialFov:   m.InitialFov,
		MaxDistKm:    maxDistKm,
	})
}

func (h *GameHandler) computeMaxDistKm(ctx context.Context, conds []string, args []any) (float64, error) {
	// ST_LongestLine finds the two farthest points (degree-space, fast via convex hull).
	// Casting the resulting linestring to geography gives geodesic length in meters.
	sql := fmt.Sprintf(`
		SELECT COALESCE(
			ST_Length(
				ST_LongestLine(
					ST_Collect(location::geometry),
					ST_Collect(location::geometry)
				)::geography
			) / 1000.0,
			0
		)
		FROM media
		WHERE %s
	`, strings.Join(conds, " AND "))
	var maxDist float64
	if err := h.db.QueryRow(ctx, sql, args...).Scan(&maxDist); err != nil {
		return 1.0, err
	}
	if maxDist < 1.0 {
		maxDist = 1.0
	}
	return maxDist, nil
}

type guessRequest struct {
	MediaID   string  `json:"media_id"`
	GuessLat  float64 `json:"guess_lat"`
	GuessLng  float64 `json:"guess_lng"`
	GuessDate string  `json:"guess_date"` // YYYY-MM-DD
	MaxDistKm float64 `json:"max_dist_km"`
}

type guessResult struct {
	ActualLat     float64  `json:"actual_lat"`
	ActualLng     float64  `json:"actual_lng"`
	ActualDate    *string  `json:"actual_date"`
	DistanceKm    float64  `json:"distance_km"`
	DateDiffDays  *float64 `json:"date_diff_days"`
	LocationScore float64  `json:"location_score"`
	TimeScore     *float64 `json:"time_score"`
	TotalScore    float64  `json:"total_score"`
}

func (h *GameHandler) SubmitGuess(w http.ResponseWriter, r *http.Request) {
	var req guessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	var actual struct {
		Lat           float64
		Lng           float64
		TakenAt       *string
		GpsConfidence float64
	}
	err := h.db.QueryRow(r.Context(), `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), taken_at::text,
		       COALESCE(gps_confidence, 1.0)
		FROM media WHERE id=$1
	`, req.MediaID).Scan(&actual.Lat, &actual.Lng, &actual.TakenAt, &actual.GpsConfidence)
	if err != nil {
		slog.Error("game: submit guess media lookup failed", "media_id", req.MediaID, "err", err)
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}

	distKm := geo.HaversineKm(req.GuessLat, req.GuessLng, actual.Lat, actual.Lng)
	maxDistKm := req.MaxDistKm
	if maxDistKm < 1.0 {
		maxDistKm = 1.0
	}
	locScore := locationScore(distKm, maxDistKm, actual.GpsConfidence)

	result := guessResult{
		ActualLat:     actual.Lat,
		ActualLng:     actual.Lng,
		ActualDate:    actual.TakenAt,
		DistanceKm:    distKm,
		LocationScore: locScore,
		TotalScore:    locScore,
	}

	if actual.TakenAt != nil && req.GuessDate != "" {
		actualT, err1 := time.Parse("2006-01-02", (*actual.TakenAt)[:10])
		guessT, err2 := time.Parse("2006-01-02", req.GuessDate)
		if err1 == nil && err2 == nil {
			diffDays := math.Abs(actualT.Sub(guessT).Hours() / 24)
			ts := timeScore(diffDays)
			result.DateDiffDays = &diffDays
			result.TimeScore = &ts
			result.TotalScore = (locScore + ts) / 2
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

const toleranceScaleKm = 10.0

// locationScore decays linearly from 5000 to 0 as distance grows from 0 to maxDistKm.
// A confidence < 1 subtracts a tolerance from the raw distance before scoring,
// so low-confidence locations don't penalise close guesses unfairly.
func locationScore(km, maxDistKm, confidence float64) float64 {
	toleranceKm := (1.0 - confidence) * toleranceScaleKm
	adjusted := math.Max(0, km-toleranceKm)
	return math.Max(0, 5000*(1-adjusted/maxDistKm))
}

// 5000 points max, decays to 0 at 365 days off
func timeScore(days float64) float64 {
	return math.Max(0, 5000*(1-days/365))
}
