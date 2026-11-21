package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminStorageHandler serves GET /api/admin/storage: a summary of each user's storage use, how many
// items they own, the bytes of those, and how many track points they have. It reads every user's
// rows, so it sits behind RequireAdmin, and it names nothing inside them: no trip, album or file.
// Each user sees how their own storage splits by trip and album (GET /api/me/storage). It writes
// nothing.
type AdminStorageHandler struct {
	db adminStorageDB
}

type adminStorageDB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

func NewAdminStorageHandler(pool *pgxpool.Pool) *AdminStorageHandler {
	return &AdminStorageHandler{db: pool}
}

// adminStorageTimeout bounds the summary. Counting the track points reads the whole gps_tracks
// table: 0.4–2.3 s on 6.2 million points, while the media aggregates take a few ms.
const adminStorageTimeout = 30 * time.Second

// AdminStorageResponse is GET /api/admin/storage. The top-level counts are every user's.
type AdminStorageResponse struct {
	StorageUse
	TrackPoints int64              `json:"track_points"`
	Users       []adminStorageUser `json:"users"`
}

// adminStorageUser is one user's own items and track points.
type adminStorageUser struct {
	ID          string  `json:"id"`
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Disabled    bool    `json:"disabled"`
	Claimed     bool    `json:"claimed"`
	StorageUse
	TrackPoints int64 `json:"track_points"`
}

// adminStorageUsersSQL lists every user, disabled and unclaimed ones too, with their media and
// their track points, largest first.
const adminStorageUsersSQL = `
	SELECT u.id::text, u.display_name, u.email, u.disabled_at IS NOT NULL, u.issuer IS NOT NULL,
	       COALESCE(m.items, 0), COALESCE(m.bytes, 0), COALESCE(m.unknown, 0), COALESCE(g.points, 0)
	FROM users u
	LEFT JOIN (
		SELECT owner_id,
		       count(*) AS items,
		       COALESCE(sum(size_bytes), 0)::bigint AS bytes,
		       count(*) FILTER (WHERE size_bytes IS NULL) AS unknown
		FROM media
		GROUP BY owner_id
	) m ON m.owner_id = u.id
	LEFT JOIN (SELECT owner_id, count(*) AS points FROM gps_tracks GROUP BY owner_id) g ON g.owner_id = u.id
	ORDER BY COALESCE(m.bytes, 0) DESC, COALESCE(m.items, 0) DESC, u.created_at, u.id`

// The summary is one statement, so it reads one snapshot by itself. The transaction is there to
// turn JIT off: these are plain aggregates, and compiling them would cost more than running them.
var adminStorageTxOptions = pgx.TxOptions{AccessMode: pgx.ReadOnly}

const adminStorageJITOff = "SET LOCAL jit = off"

func (h *AdminStorageHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), adminStorageTimeout)
	defer cancel()

	start := time.Now()
	resp, err := h.summary(ctx)
	if err != nil {
		if cause := ctx.Err(); cause != nil {
			// Leaving the page cancels the request, which is routine; a deadline is not.
			level := slog.LevelWarn
			if errors.Is(cause, context.Canceled) {
				level = slog.LevelDebug
			}
			slog.Log(ctx, level, "admin storage: stopped", "cause", cause, "elapsed", time.Since(start))
			writeStorageJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "storage_timeout"})
			return
		}
		slog.Error("admin storage: summary failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Debug("admin storage: done", "users", len(resp.Users), "elapsed", time.Since(start))
	writeStorageJSON(w, http.StatusOK, resp)
}

func (h *AdminStorageHandler) summary(ctx context.Context) (AdminStorageResponse, error) {
	tx, err := h.db.BeginTx(ctx, adminStorageTxOptions)
	if err != nil {
		return AdminStorageResponse{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, adminStorageJITOff); err != nil {
		return AdminStorageResponse{}, fmt.Errorf("jit off: %w", err)
	}

	rows, err := tx.Query(ctx, adminStorageUsersSQL)
	if err != nil {
		return AdminStorageResponse{}, fmt.Errorf("users: %w", err)
	}
	defer rows.Close()
	resp := AdminStorageResponse{Users: []adminStorageUser{}}
	for rows.Next() {
		var u adminStorageUser
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Disabled, &u.Claimed,
			&u.Items, &u.Bytes, &u.UnknownItems, &u.TrackPoints); err != nil {
			return AdminStorageResponse{}, fmt.Errorf("users: %w", err)
		}
		resp.Users = append(resp.Users, u)
		resp.add(u.StorageUse)
		resp.TrackPoints += u.TrackPoints
	}
	if err := rows.Err(); err != nil {
		return AdminStorageResponse{}, fmt.Errorf("users: %w", err)
	}
	return resp, nil
}
