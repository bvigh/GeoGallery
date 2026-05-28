package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/storage"
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
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	URL       string `json:"url"`
}

func (h *GameHandler) NewRound(w http.ResponseWriter, r *http.Request) {
	var m struct {
		ID         string   `json:"id"`
		MediaType  string   `json:"media_type"`
		StorageKey string   `json:"storage_key"`
		Lat        *float64 `json:"lat"`
		Lng        *float64 `json:"lng"`
		TakenAt    *string  `json:"taken_at"`
	}

	err := h.db.QueryRow(r.Context(), `
		SELECT id, media_type, storage_key,
		       ST_Y(location::geometry), ST_X(location::geometry),
		       taken_at::text
		FROM media
		WHERE location IS NOT NULL
		ORDER BY RANDOM()
		LIMIT 1
	`).Scan(&m.ID, &m.MediaType, &m.StorageKey, &m.Lat, &m.Lng, &m.TakenAt)
	if err != nil {
		http.Error(w, "no media available", http.StatusNotFound)
		return
	}

	url, err := h.storage.PresignGet(r.Context(), m.StorageKey)
	if err != nil {
		http.Error(w, "presign error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(roundMedia{ID: m.ID, MediaType: m.MediaType, URL: url})
}

type guessRequest struct {
	MediaID string  `json:"media_id"`
	GuessLat float64 `json:"guess_lat"`
	GuessLng float64 `json:"guess_lng"`
	GuessDate string  `json:"guess_date"` // YYYY-MM-DD
}

type guessResult struct {
	ActualLat    float64  `json:"actual_lat"`
	ActualLng    float64  `json:"actual_lng"`
	ActualDate   *string  `json:"actual_date"`
	DistanceKm   float64  `json:"distance_km"`
	DateDiffDays *float64 `json:"date_diff_days"`
	LocationScore float64 `json:"location_score"`
	TimeScore     *float64 `json:"time_score"`
	TotalScore   float64  `json:"total_score"`
}

func (h *GameHandler) SubmitGuess(w http.ResponseWriter, r *http.Request) {
	var req guessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	var actual struct {
		Lat     float64
		Lng     float64
		TakenAt *string
	}
	err := h.db.QueryRow(r.Context(), `
		SELECT ST_Y(location::geometry), ST_X(location::geometry), taken_at::text
		FROM media WHERE id=$1
	`, req.MediaID).Scan(&actual.Lat, &actual.Lng, &actual.TakenAt)
	if err != nil {
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}

	distKm := geo.HaversineKm(req.GuessLat, req.GuessLng, actual.Lat, actual.Lng)
	locScore := locationScore(distKm)

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

// 5000 points max, decays to 0 at 2500km
func locationScore(km float64) float64 {
	return math.Max(0, 5000*(1-km/2500))
}

// 5000 points max, decays to 0 at 365 days off
func timeScore(days float64) float64 {
	return math.Max(0, 5000*(1-days/365))
}
