package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
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
	runMatcher    func(ctx context.Context, db matcherDB, ownerID string, maxWindow time.Duration, reprocess bool) error
	tripGapHours  int
	runTripDetect func(ctx context.Context, db tripDetectDB, ownerID string, gapHours int) error
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
	TripID       *string    `json:"trip_id"` // always null on media the caller does not own: trips are personal
	OwnerID      string     `json:"owner_id"`
	OwnerName    *string    `json:"owner_name"` // display_name, else email; null when the owner has neither
	InitialYaw   *float64   `json:"initial_yaw"`
	InitialPitch *float64   `json:"initial_pitch"`
	InitialFov   *float64   `json:"initial_fov"`
}

// Page sizes for GET /api/media. The map asks for the maximum to load a large library in few
// requests: every page re-runs the whole filtered sort, so OFFSET pages are not cheaper.
const (
	defaultListLimit = 500
	maxListLimit     = 5000
)

func (h *MediaHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := validateMediaFilters(q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := defaultListLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			http.Error(w, fmt.Sprintf("invalid limit: want 1-%d", maxListLimit), http.StatusBadRequest)
			return
		}
		limit = n
	}
	offset, _ := strconv.Atoi(q.Get("offset"))

	conds, args := buildMediaFilter(q, auth.MustUser(r.Context()).ID)
	args = append(args, limit, offset)
	sql := fmt.Sprintf(`
		SELECT m.id, m.filename, m.media_type,
		       ST_Y(m.location::geometry) AS lat, ST_X(m.location::geometry) AS lng,
		       m.gps_confidence, m.gps_source,
		       m.taken_at, m.storage_key,
		       CASE WHEN m.owner_id = $1 THEN m.trip_id END AS trip_id,
		       m.owner_id, COALESCE(u.display_name, u.email) AS owner_name,
		       m.initial_yaw, m.initial_pitch, m.initial_fov
		FROM media m JOIN users u ON u.id = m.owner_id
		WHERE %s
		ORDER BY m.taken_at DESC NULLS LAST, m.id -- id breaks taken_at ties, so OFFSET pages never overlap
		LIMIT $%d OFFSET $%d
	`, strings.Join(conds, " AND "), len(args)-1, len(args))

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
			&m.OwnerID, &m.OwnerName,
			&m.InitialYaw, &m.InitialPitch, &m.InitialFov); err != nil {
			slog.Error("media: list row scan failed", "err", err)
			continue
		}
		m.Lat = lat
		m.Lng = lng
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		// Errors raised while the query runs (such as a failed cast) surface here, not from Query.
		slog.Error("media: list rows failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

var maxUploadBodyBytes int64 = 4 << 30 // var so tests can lower it

// uploadFormMaxMemory is how much of a multipart form is kept in memory; the rest is spooled to
// temp files. A var so tests can force spooling.
var uploadFormMaxMemory int64 = 32 << 20

func (h *MediaHandler) Upload(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	start := time.Now()
	// ContentLength is -1 for chunked bodies; nginx/Caddy limits apply before this point.
	slog.Info("upload: request received", "owner", ownerID, "content_length", r.ContentLength, "limit", maxUploadBodyBytes)

	albumID, ok := h.uploadAlbum(w, r, ownerID)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBodyBytes)
	if err := r.ParseMultipartForm(uploadFormMaxMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			slog.Warn("upload: request body too large", "owner", ownerID,
				"content_length", r.ContentLength, "limit", maxUploadBodyBytes)
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// Includes client disconnects and a full temp dir while spooling parts to disk.
		slog.Warn("upload: parse multipart form failed", "owner", ownerID,
			"content_length", r.ContentLength, "elapsed", time.Since(start), "err", err)
		http.Error(w, "parse form", http.StatusBadRequest)
		return
	}
	// net/http removes spooled parts only for the request it created, and RequireUser hands us a
	// copy (r.WithContext), so without this every upload would stay in the temp dir for good.
	defer func() {
		if err := r.MultipartForm.RemoveAll(); err != nil {
			slog.Error("upload: remove multipart temp files failed", "err", err)
		}
	}()

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		slog.Warn("upload: no files in form", "owner", ownerID)
		http.Error(w, "no files", http.StatusBadRequest)
		return
	}
	slog.Info("upload: form parsed", "owner", ownerID, "files", len(files), "elapsed", time.Since(start))

	mtimes := r.MultipartForm.Value["file_mtime"]

	var uploaded, skipped, failed []string
	var mediaIDs []string // uploaded and duplicate files alike, for the album
	for i, fh := range files {
		fileStart := time.Now()
		f, err := fh.Open()
		if err != nil {
			slog.Error("upload: open multipart file failed", "file", fh.Filename, "err", err)
			failed = append(failed, fh.Filename)
			continue
		}

		var fallbackMtime *time.Time
		if i < len(mtimes) {
			if ms, err := strconv.ParseInt(mtimes[i], 10, 64); err == nil {
				t := time.UnixMilli(ms)
				fallbackMtime = &t
			}
		}

		mediaID, inserted, err := h.registrar.registerMedia(r.Context(), ownerID, f, fh.Size, fh.Filename, fallbackMtime)
		f.Close()
		if err != nil {
			slog.Error("upload: register media failed", "file", fh.Filename, "size", fh.Size, "err", err)
			failed = append(failed, fh.Filename)
			continue
		}
		mediaIDs = append(mediaIDs, mediaID)
		if inserted {
			uploaded = append(uploaded, fh.Filename)
			slog.Info("upload: file stored", "file", fh.Filename, "size", fh.Size, "elapsed", time.Since(fileStart))
		} else {
			skipped = append(skipped, fh.Filename)
			slog.Info("upload: duplicate skipped", "file", fh.Filename, "size", fh.Size)
		}
	}

	if len(uploaded) > 0 {
		if err := h.runMatcher(r.Context(), h.matcherPool, ownerID, h.matchWindow, true); err != nil {
			slog.Error("upload: GPS matcher failed", "err", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if err := h.runTripDetect(r.Context(), h.registrar.db, ownerID, h.tripGapHours); err != nil {
			slog.Error("upload: trip detect failed", "err", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	var albumAdded int64
	if albumID != "" && len(mediaIDs) > 0 {
		// Duplicates go in too, so re-uploading a folder into an album fills it.
		tag, err := h.db.Exec(r.Context(), `
			INSERT INTO album_media (album_id, media_id, owner_id)
			SELECT $1, unnest($2::uuid[]), $3
			ON CONFLICT DO NOTHING`, albumID, mediaIDs, ownerID)
		if err != nil {
			slog.Error("upload: add to album failed", "album_id", albumID, "media", len(mediaIDs), "err", err)
			http.Error(w, `{"error":"add to album failed"}`, http.StatusInternalServerError)
			return
		}
		albumAdded = tag.RowsAffected()
	}

	slog.Info("upload: done", "owner", ownerID, "uploaded", len(uploaded), "duplicates", len(skipped),
		"failed", len(failed), "album_id", albumID, "album_added", albumAdded, "elapsed", time.Since(start))
	resp := map[string]any{"uploaded": uploaded, "skipped": skipped, "failed": failed}
	if albumID != "" {
		resp["album_added"] = albumAdded
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// maxKnownHashes caps one known-hashes request, as maxListLimit caps a media page.
const maxKnownHashes = 5000

var fileHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// KnownHashes reports which of the given file hashes (see mediaHash) the caller's own library
// already holds, as {"known": {hash: media ID}}, so the Upload page sends only new files and can
// still add the rest to an album. Dedup is per owner, so shared media never count.
func (h *MediaHandler) KnownHashes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hashes []string `json:"hashes"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(req.Hashes) == 0 || len(req.Hashes) > maxKnownHashes {
		http.Error(w, fmt.Sprintf("hashes: want 1-%d", maxKnownHashes), http.StatusBadRequest)
		return
	}
	for _, hash := range req.Hashes {
		if !fileHashPattern.MatchString(hash) {
			http.Error(w, "hashes: want 64 lower-case hex characters each", http.StatusBadRequest)
			return
		}
	}

	rows, err := h.db.Query(r.Context(),
		`SELECT file_hash, id FROM media WHERE owner_id = $1 AND file_hash = ANY($2)`,
		auth.MustUser(r.Context()).ID, req.Hashes)
	if err != nil {
		slog.Error("media: known hashes query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	known := map[string]string{}
	for rows.Next() {
		var hash, id string
		if err := rows.Scan(&hash, &id); err != nil {
			slog.Error("media: known hashes row scan failed", "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		known[hash] = id
	}
	if err := rows.Err(); err != nil {
		slog.Error("media: known hashes rows failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"known": known})
}

// uploadAlbum returns the canonical ?album_id, or "" when the param is absent. It is a query
// param, not a form field, so membership is settled before the (up to 4 GiB) body is read: a
// malformed ID or an album the caller is not a member of gets 404 with the body unread.
func (h *MediaHandler) uploadAlbum(w http.ResponseWriter, r *http.Request, ownerID string) (string, bool) {
	q := r.URL.Query()
	if !q.Has("album_id") {
		return "", true
	}
	albumID, ok := auth.CanonicalUUID(q.Get("album_id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return "", false
	}
	var member bool
	if err := h.db.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM album_members WHERE album_id = $1 AND user_id = $2)`,
		albumID, ownerID).Scan(&member); err != nil {
		slog.Error("upload: album membership lookup failed", "album_id", albumID, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return "", false
	}
	if !member {
		http.Error(w, "not found", http.StatusNotFound)
		return "", false
	}
	return albumID, true
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

	tag, err := h.db.Exec(r.Context(),
		`UPDATE media SET initial_yaw=$1, initial_pitch=$2, initial_fov=$3 WHERE id=$4 AND owner_id=$5`,
		req.InitialYaw, req.InitialPitch, req.InitialFov, id, auth.MustUser(r.Context()).ID,
	)
	if err != nil {
		slog.Error("media: update view settings failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
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

	// Foreign and missing IDs both yield [], so they stay indistinguishable.
	rows, err := h.db.Query(r.Context(),
		`SELECT k.time_seconds, k.yaw, k.pitch, k.fov
		 FROM video_keyframes k JOIN media m ON m.id = k.media_id
		 WHERE k.media_id=$1 AND `+readableMediaCond("m", 2)+`
		 ORDER BY k.time_seconds ASC`,
		id, auth.MustUser(r.Context()).ID,
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

	var one int
	if err := tx.QueryRow(r.Context(),
		`SELECT 1 FROM media WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, auth.MustUser(r.Context()).ID,
	).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Error("media: replace keyframes owner check failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

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
		`SELECT m.storage_key FROM media m WHERE m.id=$1 AND `+readableMediaCond("m", 2), id, auth.MustUser(r.Context()).ID,
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
	ownerID := auth.MustUser(r.Context()).ID

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
		`SELECT taken_at FROM media WHERE id = $1 AND owner_id = $2 FOR UPDATE`, id, ownerID,
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
		 WHERE id = $3 AND owner_id = $4`,
		req.Lng, req.Lat, id, ownerID,
	); err != nil {
		slog.Error("media: update location update failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	if takenAt != nil {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
			 VALUES ($1, 'manual', $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
			 ON CONFLICT (owner_id, source, recorded_at) DO UPDATE SET location = EXCLUDED.location`,
			ownerID, *takenAt, req.Lng, req.Lat,
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
		if err := h.runMatcher(r.Context(), h.matcherPool, ownerID, h.matchWindow, true); err != nil {
			slog.Error("media: GPS matcher failed after manual location update", "id", id, "err", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *MediaHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ownerID := auth.MustUser(r.Context()).ID

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		slog.Error("media: delete begin tx failed", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var storageKey string
	if err = tx.QueryRow(r.Context(),
		`SELECT storage_key FROM media WHERE id = $1 AND owner_id = $2`, id, ownerID,
	).Scan(&storageKey); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if _, err = tx.Exec(r.Context(),
		`DELETE FROM media WHERE id = $1 AND owner_id = $2`, id, ownerID,
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
