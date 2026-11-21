package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StorageUse is what a set of media items takes: how many items, and the bytes of those whose size
// is known. UnknownItems are items stored before sizes were recorded (`server backfill-sizes` fills
// them in); their bytes are not in Bytes.
type StorageUse struct {
	Items        int64 `json:"items"`
	Bytes        int64 `json:"bytes"`
	UnknownItems int64 `json:"unknown_items"`
}

func (u *StorageUse) add(o StorageUse) {
	u.Items += o.Items
	u.Bytes += o.Bytes
	u.UnknownItems += o.UnknownItems
}

// StorageUseHandler serves GET /api/me/storage: the caller's own storage use, in all, per trip and
// per album. Every query filters on the caller's literal owner_id, never on readableMediaCond: an
// item shared with the caller through an album is its owner's storage, and an album is listed only
// for the caller's own items in it, which the caller put there as a member.
type StorageUseHandler struct {
	db storageUseDB
}

type storageUseDB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

func NewStorageUseHandler(pool *pgxpool.Pool) *StorageUseHandler {
	return &StorageUseHandler{db: pool}
}

// MyStorageResponse is GET /api/me/storage. The trips and NoTrip add up to the top-level counts.
// An item counts in every album it is in: albums hold references, not copies, so they take no
// storage beyond the items' own.
type MyStorageResponse struct {
	StorageUse
	Trips  []storageTrip  `json:"trips"`
	NoTrip StorageUse     `json:"no_trip"`
	Albums []storageAlbum `json:"albums"`
}

type storageTrip struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	StartDate  *string `json:"start_date"`
	EndDate    *string `json:"end_date"`
	UserEdited bool    `json:"user_edited"`
	StorageUse
}

// storageAlbum is an album holding some of the caller's items; its counts are those items only,
// never the other members'.
type storageAlbum struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	OwnerID   string  `json:"owner_id"`
	OwnerName *string `json:"owner_name"` // display name, else email; null when the owner has neither
	IsOwner   bool    `json:"is_owner"`
	StorageUse
}

// myStorageTotalsSQL is the caller's items in all, then those in no trip.
const myStorageTotalsSQL = `
	SELECT count(*),
	       COALESCE(sum(size_bytes), 0)::bigint,
	       count(*) FILTER (WHERE size_bytes IS NULL),
	       count(*) FILTER (WHERE trip_id IS NULL),
	       COALESCE(sum(size_bytes) FILTER (WHERE trip_id IS NULL), 0)::bigint,
	       count(*) FILTER (WHERE trip_id IS NULL AND size_bytes IS NULL)
	FROM media
	WHERE owner_id = $1`

// myStorageTripsSQL is every trip of the caller's with their items, largest first, an empty named
// trip too. The composite foreign key keeps a trip's media its owner's; the filter on the media
// says so anyway.
const myStorageTripsSQL = `
	SELECT t.id::text, t.name, t.start_date::text, t.end_date::text, t.user_edited,
	       count(m.id) AS items,
	       COALESCE(sum(m.size_bytes), 0)::bigint AS bytes,
	       count(m.id) FILTER (WHERE m.size_bytes IS NULL)
	FROM trips t
	LEFT JOIN media m ON m.trip_id = t.id AND m.owner_id = $1
	WHERE t.owner_id = $1
	GROUP BY t.id
	ORDER BY bytes DESC, items DESC, t.start_date DESC NULLS LAST, t.id`

// myStorageAlbumsSQL is every album holding the caller's items, with those items only, largest
// first. album_media.owner_id is the item's owner, who put it there.
const myStorageAlbumsSQL = `
	SELECT a.id::text, a.name, a.owner_id::text, COALESCE(o.display_name, o.email), a.owner_id = $1,
	       count(*) AS items,
	       COALESCE(sum(m.size_bytes), 0)::bigint AS bytes,
	       count(*) FILTER (WHERE m.size_bytes IS NULL)
	FROM album_media am
	JOIN media m ON m.id = am.media_id AND m.owner_id = $1
	JOIN albums a ON a.id = am.album_id
	JOIN users o ON o.id = a.owner_id
	WHERE am.owner_id = $1
	GROUP BY a.id, o.display_name, o.email
	ORDER BY bytes DESC, items DESC, a.created_at DESC, a.id`

// The three queries read one snapshot, so an upload meanwhile cannot make the trips disagree with
// the total.
var myStorageTxOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

func (h *StorageUseHandler) Mine(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	resp, err := h.report(r.Context(), ownerID)
	if err != nil {
		if cause := r.Context().Err(); cause != nil {
			// Leaving the page cancels the request: nobody waits for an answer.
			slog.Debug("storage: stopped", "cause", cause)
			return
		}
		slog.Error("storage: report failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeStorageJSON(w, http.StatusOK, resp)
}

func (h *StorageUseHandler) report(ctx context.Context, ownerID string) (MyStorageResponse, error) {
	tx, err := h.db.BeginTx(ctx, myStorageTxOptions)
	if err != nil {
		return MyStorageResponse{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resp := MyStorageResponse{Trips: []storageTrip{}, Albums: []storageAlbum{}}
	if err := tx.QueryRow(ctx, myStorageTotalsSQL, ownerID).Scan(
		&resp.Items, &resp.Bytes, &resp.UnknownItems,
		&resp.NoTrip.Items, &resp.NoTrip.Bytes, &resp.NoTrip.UnknownItems); err != nil {
		return MyStorageResponse{}, fmt.Errorf("totals: %w", err)
	}

	rows, err := tx.Query(ctx, myStorageTripsSQL, ownerID)
	if err != nil {
		return MyStorageResponse{}, fmt.Errorf("trips: %w", err)
	}
	for rows.Next() {
		var t storageTrip
		if err := rows.Scan(&t.ID, &t.Name, &t.StartDate, &t.EndDate, &t.UserEdited,
			&t.Items, &t.Bytes, &t.UnknownItems); err != nil {
			rows.Close()
			return MyStorageResponse{}, fmt.Errorf("trips: %w", err)
		}
		resp.Trips = append(resp.Trips, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return MyStorageResponse{}, fmt.Errorf("trips: %w", err)
	}

	rows, err = tx.Query(ctx, myStorageAlbumsSQL, ownerID)
	if err != nil {
		return MyStorageResponse{}, fmt.Errorf("albums: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a storageAlbum
		if err := rows.Scan(&a.ID, &a.Name, &a.OwnerID, &a.OwnerName, &a.IsOwner,
			&a.Items, &a.Bytes, &a.UnknownItems); err != nil {
			return MyStorageResponse{}, fmt.Errorf("albums: %w", err)
		}
		resp.Albums = append(resp.Albums, a)
	}
	if err := rows.Err(); err != nil {
		return MyStorageResponse{}, fmt.Errorf("albums: %w", err)
	}
	return resp, nil
}

func writeStorageJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("storage: encode response failed", "err", err)
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.Warn("storage: write response failed", "err", err)
	}
}
