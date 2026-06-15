package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/storage"
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
	Delete(ctx context.Context, key string) error
}

type poolBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type MediaHandler struct {
	db            mediaDB
	pool          poolBeginner
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
		pool:          db,
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
	ID           string     `json:"id"`
	Filename     string     `json:"filename"`
	MediaType    string     `json:"media_type"`
	Lat          *float64   `json:"lat"`
	Lng          *float64   `json:"lng"`
	Confidence   *float64   `json:"gps_confidence"`
	GpsSource    *string    `json:"gps_source"`
	TakenAt      *time.Time `json:"taken_at"`
	StorageKey   string     `json:"storage_key"`
	TripID       *string    `json:"trip_id"`
	InitialYaw   *float64   `json:"initial_yaw"`
	InitialPitch *float64   `json:"initial_pitch"`
	InitialFov   *float64   `json:"initial_fov"`
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
		       taken_at, storage_key, trip_id,
		       initial_yaw, initial_pitch, initial_fov
		FROM media
		%s
		ORDER BY taken_at DESC NULLS LAST
		LIMIT 500 OFFSET $%d
	`, where, n)

	rows, err := h.db.Query(r.Context(), sql, args...)
	if err != nil {
		slog.Error("media: list query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := []mediaItem{}
	for rows.Next() {
		var m mediaItem
		var lat, lng *float64
		if err := rows.Scan(&m.ID, &m.Filename, &m.MediaType, &lat, &lng,
			&m.Confidence, &m.GpsSource, &m.TakenAt, &m.StorageKey, &m.TripID,
			&m.InitialYaw, &m.InitialPitch, &m.InitialFov); err != nil {
			slog.Error("media: list row scan failed", "err", err)
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
	if err := r.ParseMultipartForm(32 << 20); err != nil {
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
			slog.Error("upload: open multipart file failed", "file", fh.Filename, "err", err)
			continue
		}

		var fallbackMtime *time.Time
		if i < len(mtimes) {
			if ms, err := strconv.ParseInt(mtimes[i], 10, 64); err == nil {
				t := time.UnixMilli(ms)
				fallbackMtime = &t
			}
		}

		registered, err := h.registrar.registerMedia(r.Context(), f, fh.Size, fh.Filename, fallbackMtime)
		f.Close()
		if err != nil {
			slog.Error("upload: register media failed", "file", fh.Filename, "err", err)
			continue
		}
		if registered {
			uploaded = append(uploaded, fh.Filename)
		}
	}

	if len(uploaded) > 0 {
		if err := h.runMatcher(r.Context(), h.matcherPool, h.matchWindow, true); err != nil {
			slog.Error("upload: GPS matcher failed", "err", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if err := h.runTripDetect(r.Context(), h.registrar.db, h.tripGapHours); err != nil {
			slog.Error("upload: trip detect failed", "err", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"uploaded": uploaded})
}

type viewSettingsRequest struct {
	InitialYaw   *float64 `json:"initial_yaw"`
	InitialPitch *float64 `json:"initial_pitch"`
	InitialFov   *float64 `json:"initial_fov"`
}

func (h *MediaHandler) UpdateViewSettings(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req viewSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	_, err := h.db.Exec(r.Context(),
		`UPDATE media SET initial_yaw=$1, initial_pitch=$2, initial_fov=$3 WHERE id=$4`,
		req.InitialYaw, req.InitialPitch, req.InitialFov, id,
	)
	if err != nil {
		slog.Error("media: update view settings failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type orientationKeyframe struct {
	Time  float64 `json:"time"`
	Yaw   float64 `json:"yaw"`
	Pitch float64 `json:"pitch"`
	Fov   float64 `json:"fov"`
}

func (h *MediaHandler) GetKeyframes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rows, err := h.db.Query(r.Context(),
		`SELECT time_seconds, yaw, pitch, fov FROM video_keyframes WHERE media_id=$1 ORDER BY time_seconds ASC`,
		id,
	)
	if err != nil {
		slog.Error("media: get keyframes query failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	keyframes := []orientationKeyframe{}
	for rows.Next() {
		var kf orientationKeyframe
		if err := rows.Scan(&kf.Time, &kf.Yaw, &kf.Pitch, &kf.Fov); err != nil {
			slog.Error("media: keyframe row scan failed", "id", id, "err", err)
			continue
		}
		keyframes = append(keyframes, kf)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(keyframes)
}

func (h *MediaHandler) ReplaceKeyframes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var keyframes []orientationKeyframe
	if err := json.NewDecoder(r.Body).Decode(&keyframes); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		slog.Error("media: replace keyframes begin tx failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := tx.Exec(r.Context(),
		`DELETE FROM video_keyframes WHERE media_id=$1`, id,
	); err != nil {
		slog.Error("media: delete keyframes failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	for _, kf := range keyframes {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO video_keyframes (media_id, time_seconds, yaw, pitch, fov) VALUES ($1, $2, $3, $4, $5)`,
			id, kf.Time, kf.Yaw, kf.Pitch, kf.Fov,
		); err != nil {
			slog.Error("media: insert keyframe failed", "id", id, "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("media: replace keyframes commit failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
		slog.Error("media: presign failed", "id", id, "key", storageKey, "err", err)
		http.Error(w, "presign error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}

type locationRequest struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func (h *MediaHandler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req locationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Lat < -90 || req.Lat > 90 {
		http.Error(w, "lat out of range", http.StatusBadRequest)
		return
	}
	if req.Lng < -180 || req.Lng > 180 {
		http.Error(w, "lng out of range", http.StatusBadRequest)
		return
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		slog.Error("media: update location begin tx failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var takenAt *time.Time
	if err := tx.QueryRow(r.Context(),
		`SELECT taken_at FROM media WHERE id = $1`, id,
	).Scan(&takenAt); err != nil {
		if err == pgx.ErrNoRows {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Error("media: update location query failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if _, err := tx.Exec(r.Context(),
		`UPDATE media
		 SET location       = ST_SetSRID(ST_MakePoint($1, $2), 4326),
		     gps_source     = 'manual',
		     gps_confidence = 1.0
		 WHERE id = $3`,
		req.Lng, req.Lat, id,
	); err != nil {
		slog.Error("media: update location update failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if takenAt != nil {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO gps_tracks (source, recorded_at, location)
			 VALUES ('manual', $1, ST_SetSRID(ST_MakePoint($2, $3), 4326))
			 ON CONFLICT (source, recorded_at) DO UPDATE SET location = EXCLUDED.location`,
			*takenAt, req.Lng, req.Lat,
		); err != nil {
			slog.Error("media: update location gps_tracks insert failed", "id", id, "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("media: update location commit failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if takenAt != nil {
		if err := h.runMatcher(r.Context(), h.matcherPool, h.matchWindow, true); err != nil {
			slog.Error("media: GPS matcher failed after manual location update", "id", id, "err", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *MediaHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		slog.Error("media: delete begin tx failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var storageKey string
	if err = tx.QueryRow(r.Context(),
		`SELECT storage_key FROM media WHERE id = $1`, id,
	).Scan(&storageKey); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if _, err = tx.Exec(r.Context(),
		`DELETE FROM media WHERE id = $1`, id,
	); err != nil {
		slog.Error("media: delete db failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if err = tx.Commit(r.Context()); err != nil {
		slog.Error("media: delete commit failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if err = h.storage.Delete(r.Context(), storageKey); err != nil {
		slog.Warn("media: S3 delete failed — DB row removed, object orphaned", "id", id, "key", storageKey, "err", err)
	}

	w.WriteHeader(http.StatusNoContent)
}
