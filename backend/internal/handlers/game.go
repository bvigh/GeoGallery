package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type gameStorage interface {
	PresignGet(ctx context.Context, key string) (string, error)
}

type gameDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type GameHandler struct {
	db      gameDB
	storage gameStorage
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
	// [south, west, north, east] of the filtered set, for the guess map; null if not measured.
	Bounds *[4]float64 `json:"bounds"`
}

func (h *GameHandler) NewRound(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := validateMediaFilters(q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	conds, args := buildMediaFilter(q, auth.MustUser(r.Context()).ID)
	conds = append(conds, "m.location IS NOT NULL")
	sql := fmt.Sprintf(`
		SELECT m.id, m.media_type, m.storage_key,
		       ST_Y(m.location::geometry), ST_X(m.location::geometry),
		       m.taken_at::text,
		       m.initial_yaw, m.initial_pitch, m.initial_fov
		FROM media m
		WHERE %s
		ORDER BY RANDOM()
		LIMIT 1
	`, strings.Join(conds, " AND "))

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
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "no media available", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("game: new round query failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	url, err := h.storage.PresignGet(r.Context(), m.StorageKey)
	if err != nil {
		slog.Error("game: presign failed", "id", m.ID, "key", m.StorageKey, "err", err)
		http.Error(w, "presign error", http.StatusInternalServerError)
		return
	}

	maxDistKm, bounds, err := h.measureFilteredSet(r.Context(), conds, args)
	if err != nil {
		slog.Warn("game: measuring the filtered set failed, using defaults", "err", err)
		maxDistKm, bounds = 1.0, nil
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
		Bounds:       bounds,
	})
}

// measureFilteredSet returns the longest distance across the set and its bounds for the guess map
// (nil if the set emptied since the round was picked). It reuses NewRound's conds and args, so it
// expects media aliased as m.
func (h *GameHandler) measureFilteredSet(ctx context.Context, conds []string, args []any) (float64, *[4]float64, error) {
	// ST_LongestLine finds the two farthest points (degree-space, fast via convex hull).
	// Casting the resulting linestring to geography gives geodesic length in meters.
	// The bounds are plain degrees too: a set across the antimeridian gets a box around the world.
	sql := fmt.Sprintf(`
		SELECT COALESCE(
			ST_Length(
				ST_LongestLine(
					ST_Collect(m.location::geometry),
					ST_Collect(m.location::geometry)
				)::geography
			) / 1000.0,
			0
		),
		MIN(ST_Y(m.location::geometry)), MIN(ST_X(m.location::geometry)),
		MAX(ST_Y(m.location::geometry)), MAX(ST_X(m.location::geometry))
		FROM media m
		WHERE %s
	`, strings.Join(conds, " AND "))
	var maxDist float64
	var south, west, north, east *float64
	if err := h.db.QueryRow(ctx, sql, args...).Scan(&maxDist, &south, &west, &north, &east); err != nil {
		return 1.0, nil, err
	}
	if maxDist < 1.0 {
		maxDist = 1.0
	}
	if south == nil {
		return maxDist, nil, nil
	}
	return maxDist, guessMapBounds(*south, *west, *north, *east), nil
}

// boundsGridDeg is the grid a round's bounds are rounded out to: about 55 km north to south.
const boundsGridDeg = 0.5

// guessMapBounds rounds a bounding box out to the grid, so a set at one place (or a tiny one)
// opens on an area instead of street level. Grid lines, not a margin around the set: a margin
// would put a single photo at the exact centre of the map.
func guessMapBounds(south, west, north, east float64) *[4]float64 {
	south, north = roundOutToGrid(south, north, 90)
	west, east = roundOutToGrid(west, east, 180)
	return &[4]float64{south, west, north, east}
}

// roundOutToGrid returns the grid lines at or below lo and strictly above hi, so a point on a line
// still gets a whole cell. Only at limit is there no line above, and the cell below it is used.
func roundOutToGrid(lo, hi, limit float64) (float64, float64) {
	lo = math.Floor(lo/boundsGridDeg) * boundsGridDeg
	hi = (math.Floor(hi/boundsGridDeg) + 1) * boundsGridDeg
	if hi > limit {
		return math.Min(lo, limit-boundsGridDeg), limit
	}
	return lo, hi
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
	mediaID, ok := auth.CanonicalUUID(req.MediaID)
	if !ok {
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}

	var actual struct {
		Lat           float64
		Lng           float64
		TakenAt       *string
		GpsConfidence float64
	}
	err := h.db.QueryRow(r.Context(), `
		SELECT ST_Y(m.location::geometry), ST_X(m.location::geometry), m.taken_at::text,
		       COALESCE(m.gps_confidence, 1.0)
		FROM media m WHERE m.id=$1 AND `+readableMediaCond("m", 2)+`
	`, mediaID, auth.MustUser(r.Context()).ID).Scan(&actual.Lat, &actual.Lng, &actual.TakenAt, &actual.GpsConfidence)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("game: submit guess media lookup failed", "media_id", mediaID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
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
