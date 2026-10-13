package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TripHandler struct {
	db       *pgxpool.Pool
	gapHours int
}

func NewTripHandler(db *pgxpool.Pool, gapHours int) *TripHandler {
	return &TripHandler{db: db, gapHours: gapHours}
}

type trip struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	StartDate  *string `json:"start_date"`
	EndDate    *string `json:"end_date"`
	MediaCount int     `json:"media_count"`
	CoverKey   *string `json:"cover_key"`
	UserEdited bool    `json:"user_edited"`
}

func (h *TripHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT t.id, t.name, t.start_date::text, t.end_date::text,
		       COUNT(m.id) AS media_count,
		       MIN(m.storage_key) AS cover_key,
		       t.user_edited
		FROM trips t
		LEFT JOIN media m ON m.trip_id = t.id
		WHERE t.owner_id = $1
		GROUP BY t.id
		ORDER BY t.start_date DESC NULLS LAST
	`, auth.MustUser(r.Context()).ID)
	if err != nil {
		slog.Error("trips: list query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	trips := []trip{}
	for rows.Next() {
		var t trip
		if err := rows.Scan(&t.ID, &t.Name, &t.StartDate, &t.EndDate, &t.MediaCount, &t.CoverKey, &t.UserEdited); err != nil {
			slog.Error("trips: list row scan failed", "err", err)
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
	ownerID := auth.MustUser(r.Context()).ID
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
		slog.Error("trips: update begin tx failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	tag, err := tx.Exec(r.Context(), `
		UPDATE trips
		SET name        = COALESCE($1, name),
		    start_date  = COALESCE($2::date, start_date),
		    end_date    = COALESCE($3::date, end_date),
		    user_edited = TRUE
		WHERE id = $4 AND owner_id = $5
	`, body.Name, body.StartDate, body.EndDate, id, ownerID)
	if err != nil {
		slog.Error("trips: update exec failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	// Must precede the reassign step: it would otherwise move the caller's media into a foreign trip ID.
	if tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if body.StartDate != nil || body.EndDate != nil {
		var startDate, endDate *string
		if err = tx.QueryRow(r.Context(),
			`SELECT start_date::text, end_date::text FROM trips WHERE id = $1`, id,
		).Scan(&startDate, &endDate); err != nil {
			slog.Error("trips: update read dates failed", "id", id, "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}

		if startDate != nil && endDate != nil {
			if _, err = tx.Exec(r.Context(), `
				UPDATE media
				SET trip_id = $1
				WHERE owner_id = $4
				  AND taken_at::date >= $2::date
				  AND taken_at::date <= $3::date
				  AND (trip_id IS NULL
				       OR trip_id IN (SELECT id FROM trips WHERE user_edited = FALSE AND owner_id = $4))
			`, id, *startDate, *endDate, ownerID); err != nil {
				slog.Error("trips: update reassign media failed", "id", id, "err", err)
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}

			if _, err = tx.Exec(r.Context(), `
				DELETE FROM trips
				WHERE owner_id = $1 AND user_edited = FALSE
				  AND id NOT IN (SELECT trip_id FROM media WHERE trip_id IS NOT NULL AND owner_id = $1)
			`, ownerID); err != nil {
				slog.Error("trips: update delete empty auto-trips failed", "err", err)
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}

			if _, err = tx.Exec(r.Context(), `
				UPDATE trips t
				SET start_date = (SELECT MIN(taken_at)::date FROM media WHERE trip_id = t.id),
				    end_date   = (SELECT MAX(taken_at)::date FROM media WHERE trip_id = t.id)
				WHERE user_edited = FALSE AND owner_id = $1
			`, ownerID); err != nil {
				slog.Error("trips: update recompute dates failed", "err", err)
				http.Error(w, "db error", http.StatusInternalServerError)
				return
			}
		}
	}

	if err = tx.Commit(r.Context()); err != nil {
		slog.Error("trips: update commit failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TripHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ownerID := auth.MustUser(r.Context()).ID

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		slog.Error("trips: delete begin tx failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var userEdited bool
	if err = tx.QueryRow(r.Context(),
		`SELECT user_edited FROM trips WHERE id = $1 AND owner_id = $2`, id, ownerID,
	).Scan(&userEdited); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !userEdited {
		http.Error(w, "only user-edited trips can be deleted", http.StatusForbidden)
		return
	}

	if _, err = tx.Exec(r.Context(),
		`UPDATE media SET trip_id = NULL WHERE trip_id = $1 AND owner_id = $2`, id, ownerID,
	); err != nil {
		slog.Error("trips: delete unassign media failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if _, err = tx.Exec(r.Context(),
		`DELETE FROM trips WHERE id = $1 AND owner_id = $2`, id, ownerID,
	); err != nil {
		slog.Error("trips: delete exec failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if err = tx.Commit(r.Context()); err != nil {
		slog.Error("trips: delete commit failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if err = RunTripDetect(r.Context(), h.db, ownerID, h.gapHours); err != nil {
		slog.Error("trips: post-delete auto-detect failed", "err", err)
		http.Error(w, "trip deleted but auto-detect failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
