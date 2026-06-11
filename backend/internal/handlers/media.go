package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mediaDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type mediaStorage interface {
	PresignGet(ctx context.Context, key string) (string, error)
}

type MediaHandler struct {
	db            mediaDB
	storage       mediaStorage
	registrar     *MediaRegistrar
	matcherPool   matcherDB
	matchWindow   time.Duration
	runMatcher    func(ctx context.Context, db matcherDB, maxWindow time.Duration, reprocess bool) error
	tripGapHours  int
	runTripDetect func(ctx context.Context, db tripDetectDB, gapHours int) error
}

func NewMediaHandler(db *pgxpool.Pool, s *storage.Client, matchWindow time.Duration, tripGapHours int) *MediaHandler {
	return &MediaHandler{
		db:            db,
		storage:       s,
		registrar:     &MediaRegistrar{db: db, storage: s},
		matcherPool:   db,
		matchWindow:   matchWindow,
		tripGapHours:  tripGapHours,
		runMatcher:    RunGPSMatcher,
		runTripDetect: RunTripDetect,
	}
}

type mediaItem struct {
	ID         string     `json:"id"`
	Filename   string     `json:"filename"`
	MediaType  string     `json:"media_type"`
	Lat        *float64   `json:"lat"`
	Lng        *float64   `json:"lng"`
	Confidence *float64   `json:"gps_confidence"`
	GpsSource  *string    `json:"gps_source"`
	TakenAt    *time.Time `json:"taken_at"`
	StorageKey string     `json:"storage_key"`
	TripID     *string    `json:"trip_id"`
}

func (h *MediaHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	fromDate := q.Get("from_date")
	toDate := q.Get("to_date")
	tripIDs := q["trip_id"]
	mediaTypes := q["media_type"]
	offset, _ := strconv.Atoi(q.Get("offset"))

	var conds []string
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

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	args = append(args, offset)
	sql := fmt.Sprintf(`
		SELECT id, filename, media_type,
		       ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lng,
		       gps_confidence, gps_source,
		       taken_at, storage_key, trip_id
		FROM media
		%s
		ORDER BY taken_at DESC NULLS LAST
		LIMIT 500 OFFSET $%d
	`, where, n)

	rows, err := h.db.Query(r.Context(), sql, args...)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []mediaItem{}
	for rows.Next() {
		var m mediaItem
		var lat, lng *float64
		if err := rows.Scan(&m.ID, &m.Filename, &m.MediaType, &lat, &lng,
			&m.Confidence, &m.GpsSource, &m.TakenAt, &m.StorageKey, &m.TripID); err != nil {
			continue
		}
		m.Lat = lat
		m.Lng = lng
		items = append(items, m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (h *MediaHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		http.Error(w, "parse form", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "no files", http.StatusBadRequest)
		return
	}

	mtimes := r.MultipartForm.Value["file_mtime"]

	var uploaded []string
	for i, fh := range files {
		f, err := fh.Open()
		if err != nil {
			continue
		}

		data := make([]byte, fh.Size)
		_, err = f.Read(data)
		f.Close()
		if err != nil {
			continue
		}

		var fallbackMtime *time.Time
		if i < len(mtimes) {
			if ms, err := strconv.ParseInt(mtimes[i], 10, 64); err == nil {
				t := time.UnixMilli(ms)
				fallbackMtime = &t
			}
		}

		registered, err := h.registrar.registerMedia(r.Context(), data, fh.Filename, fallbackMtime)
		if err != nil {
			continue
		}
		if registered {
			uploaded = append(uploaded, fh.Filename)
		}
	}

	if len(uploaded) > 0 {
		if err := h.runMatcher(r.Context(), h.matcherPool, h.matchWindow, true); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if err := h.runTripDetect(r.Context(), h.registrar.db, h.tripGapHours); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"uploaded": uploaded})
}

func (h *MediaHandler) GetURL(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var storageKey string
	err := h.db.QueryRow(r.Context(),
		`SELECT storage_key FROM media WHERE id=$1`, id,
	).Scan(&storageKey)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	url, err := h.storage.PresignGet(r.Context(), storageKey)
	if err != nil {
		http.Error(w, "presign error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}
