package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TripHandler struct {
	db *pgxpool.Pool
}

func NewTripHandler(db *pgxpool.Pool) *TripHandler {
	return &TripHandler{db: db}
}

type trip struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	StartDate  *string `json:"start_date"`
	EndDate    *string `json:"end_date"`
	MediaCount int     `json:"media_count"`
	CoverKey   *string `json:"cover_key"`
}

func (h *TripHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT t.id, t.name, t.start_date::text, t.end_date::text,
		       COUNT(m.id) AS media_count,
		       MIN(m.storage_key) AS cover_key
		FROM trips t
		LEFT JOIN media m ON m.trip_id = t.id
		GROUP BY t.id
		ORDER BY t.start_date DESC NULLS LAST
	`)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var trips []trip
	for rows.Next() {
		var t trip
		if err := rows.Scan(&t.ID, &t.Name, &t.StartDate, &t.EndDate, &t.MediaCount, &t.CoverKey); err != nil {
			continue
		}
		trips = append(trips, t)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trips)
}

func (h *TripHandler) Rename(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	_, err := h.db.Exec(r.Context(), `UPDATE trips SET name=$1 WHERE id=$2`, body.Name, id)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
