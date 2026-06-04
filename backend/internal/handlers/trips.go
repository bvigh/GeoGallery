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

type updateBody struct {
	Name      *string `json:"name"`
	StartDate *string `json:"start_date"`
	EndDate   *string `json:"end_date"`
}

func (h *TripHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body updateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil && body.StartDate == nil && body.EndDate == nil {
		http.Error(w, "at least one field required", http.StatusBadRequest)
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	_, err = tx.Exec(r.Context(), `
		UPDATE trips
		SET name        = COALESCE($1, name),
		    start_date  = COALESCE($2::date, start_date),
		    end_date    = COALESCE($3::date, end_date),
		    user_edited = TRUE
		WHERE id = $4
	`, body.Name, body.StartDate, body.EndDate, id)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if body.StartDate != nil || body.EndDate != nil {
		var startDate, endDate *string
		if err = tx.QueryRow(r.Context(),
			`SELECT start_date::text, end_date::text FROM trips WHERE id = $1`, id,
		).Scan(&startDate, &endDate); err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		if startDate != nil && endDate != nil {
			// Pull unassigned and auto-detected media into this trip
			if _, err = tx.Exec(r.Context(), `
				UPDATE media
				SET trip_id = $1
				WHERE taken_at::date >= $2::date
				  AND taken_at::date <= $3::date
				  AND (trip_id IS NULL
				       OR trip_id IN (SELECT id FROM trips WHERE user_edited = FALSE))
			`, id, *startDate, *endDate); err != nil {
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}

			// Delete auto-detected trips that are now empty
			if _, err = tx.Exec(r.Context(), `
				DELETE FROM trips
				WHERE user_edited = FALSE
				  AND id NOT IN (SELECT trip_id FROM media WHERE trip_id IS NOT NULL)
			`); err != nil {
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}

			// Recompute date ranges for remaining auto-detected trips
			if _, err = tx.Exec(r.Context(), `
				UPDATE trips t
				SET start_date = (SELECT MIN(taken_at)::date FROM media WHERE trip_id = t.id),
				    end_date   = (SELECT MAX(taken_at)::date FROM media WHERE trip_id = t.id)
				WHERE user_edited = FALSE
			`); err != nil {
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}
		}
	}

	if err = tx.Commit(r.Context()); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
