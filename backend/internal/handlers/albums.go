package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxAlbumNameChars = 200
	maxAlbumMediaIDs  = 5000
	// 5000 quoted UUIDs are about 200 KB, well over maxBodyBytes.
	maxAlbumMediaBodyBytes = 1 << 20
)

// AlbumHandler manages albums: who is in them and what they hold. Whether a member can read an
// item is readableMediaCond's job. A caller who is not a member gets 404 on everything
// album-scoped; a member attempting an owner-only action gets 403.
type AlbumHandler struct {
	db *pgxpool.Pool
}

func NewAlbumHandler(db *pgxpool.Pool) *AlbumHandler {
	return &AlbumHandler{db: db}
}

type albumItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerID     string    `json:"owner_id"`
	OwnerName   *string   `json:"owner_name"` // display_name, else email; null when the owner has neither
	IsOwner     bool      `json:"is_owner"`
	MemberCount int       `json:"member_count"`
	MediaCount  int       `json:"media_count"`
	StartDate   *string   `json:"start_date"` // min/max taken_at as YYYY-MM-DD; null without dated media
	EndDate     *string   `json:"end_date"`
	CreatedAt   time.Time `json:"created_at"`
	// LastAddedAt is when the caller last added one of their items, for ordering the album
	// pickers by recent use; null when none of their items is in the album.
	LastAddedAt *time.Time `json:"last_added_at"`
}

// albumSelect yields one albumItem row per album user $1 belongs to. Every member can read every
// item, so the counts and dates are the same for all members; last_added_at is the caller's own.
const albumSelect = `
	SELECT a.id, a.name, a.owner_id, COALESCE(u.display_name, u.email), a.owner_id = $1,
	       (SELECT COUNT(*) FROM album_members mm WHERE mm.album_id = a.id),
	       s.media_count, s.start_date, s.end_date, a.created_at,
	       (SELECT MAX(am.added_at) FROM album_media am WHERE am.album_id = a.id AND am.owner_id = $1)
	FROM albums a
	JOIN album_members me ON me.album_id = a.id AND me.user_id = $1
	JOIN users u ON u.id = a.owner_id
	CROSS JOIN LATERAL (
		SELECT COUNT(*) AS media_count,
		       MIN(m.taken_at)::date::text AS start_date, MAX(m.taken_at)::date::text AS end_date
		FROM album_media am JOIN media m ON m.id = am.media_id
		WHERE am.album_id = a.id) s`

func scanAlbum(row pgx.Row) (albumItem, error) {
	var a albumItem
	err := row.Scan(&a.ID, &a.Name, &a.OwnerID, &a.OwnerName, &a.IsOwner,
		&a.MemberCount, &a.MediaCount, &a.StartDate, &a.EndDate, &a.CreatedAt, &a.LastAddedAt)
	return a, err
}

type albumMember struct {
	UserID      string    `json:"user_id"`
	DisplayName *string   `json:"display_name"`
	Email       *string   `json:"email"`
	IsOwner     bool      `json:"is_owner"`
	AddedAt     time.Time `json:"added_at"`
}

type albumRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type directoryUser struct {
	ID          string  `json:"id"`
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
}

func writeAlbumJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("albums: encode response failed", "err", err)
	}
}

// albumRole is the caller's standing in one album, ordered so that a higher role allows more.
type albumRole int

const (
	roleNone albumRole = iota // not a member, or no such album
	roleMember
	roleOwner
)

func (h *AlbumHandler) role(ctx context.Context, albumID, userID string) (albumRole, error) {
	var isOwner bool
	err := h.db.QueryRow(ctx, `
		SELECT a.owner_id = $2
		FROM albums a JOIN album_members mem ON mem.album_id = a.id AND mem.user_id = $2
		WHERE a.id = $1`, albumID, userID).Scan(&isOwner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return roleNone, nil
	case err != nil:
		return roleNone, err
	case isOwner:
		return roleOwner, nil
	}
	return roleMember, nil
}

// authorize returns the caller's role in the album, or writes 404 (not a member) or 403 (a
// member, but need is roleOwner) and returns false.
func (h *AlbumHandler) authorize(w http.ResponseWriter, r *http.Request, albumID, userID string, need albumRole) (albumRole, bool) {
	role, err := h.role(r.Context(), albumID, userID)
	if err != nil {
		slog.Error("albums: role lookup failed", "album_id", albumID, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return roleNone, false
	}
	switch {
	case role == roleNone:
		http.Error(w, "not found", http.StatusNotFound)
		return role, false
	case role < need:
		http.Error(w, "only the album owner can do this", http.StatusForbidden)
		return role, false
	}
	return role, true
}

// albumIDParam returns the canonical {id} path param, or writes 404 for a malformed one.
func albumIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
	}
	return id, ok
}

// albumName trims raw and checks it against the albums.name constraint. Postgres TEXT cannot hold
// NUL, so it is rejected here rather than failing the insert.
func albumName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxAlbumNameChars || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("name must be 1-%d characters", maxAlbumNameChars)
	}
	return name, nil
}

// decodeAlbumName reads {"name"} and returns the trimmed name, or writes 400.
func decodeAlbumName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return "", false
	}
	name, err := albumName(body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	return name, true
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func (h *AlbumHandler) List(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	rows, err := h.db.Query(r.Context(), albumSelect+` ORDER BY a.created_at DESC, a.id`, me)
	if err != nil {
		slog.Error("albums: list query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	albums := []albumItem{}
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			slog.Error("albums: list row scan failed", "err", err)
			continue
		}
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		slog.Error("albums: list rows failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeAlbumJSON(w, http.StatusOK, albums)
}

// Create inserts the album and the owner's member row together: readableMediaCond and every
// membership check rely on the owner being a member.
func (h *AlbumHandler) Create(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	name, ok := decodeAlbumName(w, r)
	if !ok {
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		slog.Error("albums: create begin tx failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var id string
	if err := tx.QueryRow(r.Context(),
		`INSERT INTO albums (owner_id, name) VALUES ($1, $2) RETURNING id`, me, name,
	).Scan(&id); err != nil {
		slog.Error("albums: create insert failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO album_members (album_id, user_id) VALUES ($1, $2)`, id, me,
	); err != nil {
		slog.Error("albums: create owner member insert failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	album, err := scanAlbum(tx.QueryRow(r.Context(), albumSelect+` WHERE a.id = $2`, me, id))
	if err != nil {
		slog.Error("albums: create read back failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("albums: create commit failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Info("albums: created", "album_id", id, "owner", me)
	writeAlbumJSON(w, http.StatusCreated, album)
}

// Update renames the album: {"name"}. Owner only.
func (h *AlbumHandler) Update(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := albumIDParam(w, r)
	if !ok {
		return
	}
	name, ok := decodeAlbumName(w, r)
	if !ok {
		return
	}
	if _, ok := h.authorize(w, r, id, me, roleOwner); !ok {
		return
	}

	tag, err := h.db.Exec(r.Context(),
		`UPDATE albums SET name = $1 WHERE id = $2 AND owner_id = $3`, name, id, me)
	if err != nil {
		slog.Error("albums: rename failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 { // deleted since the role check
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete removes the album; members and contributions cascade, the media themselves stay.
// Owner only.
func (h *AlbumHandler) Delete(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := albumIDParam(w, r)
	if !ok {
		return
	}
	if _, ok := h.authorize(w, r, id, me, roleOwner); !ok {
		return
	}

	tag, err := h.db.Exec(r.Context(), `DELETE FROM albums WHERE id = $1 AND owner_id = $2`, id, me)
	if err != nil {
		slog.Error("albums: delete failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("albums: deleted", "album_id", id, "by", me)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlbumHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := albumIDParam(w, r)
	if !ok {
		return
	}
	if _, ok := h.authorize(w, r, id, me, roleMember); !ok {
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT mem.user_id, u.display_name, u.email, mem.user_id = a.owner_id, mem.added_at
		FROM album_members mem
		JOIN albums a ON a.id = mem.album_id
		JOIN users u ON u.id = mem.user_id
		WHERE mem.album_id = $1
		ORDER BY mem.user_id = a.owner_id DESC, mem.added_at, mem.user_id`, id)
	if err != nil {
		slog.Error("albums: list members query failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	members := []albumMember{}
	for rows.Next() {
		var m albumMember
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Email, &m.IsOwner, &m.AddedAt); err != nil {
			slog.Error("albums: member row scan failed", "album_id", id, "err", err)
			continue
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		slog.Error("albums: list members rows failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeAlbumJSON(w, http.StatusOK, members)
}

// AddMember adds {"user_id"} to the album. Owner only. The target must be an active, claimed
// user (otherwise 404); adding an existing member is a no-op.
func (h *AlbumHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := albumIDParam(w, r)
	if !ok {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	target, ok := auth.CanonicalUUID(body.UserID)
	if !ok {
		http.Error(w, `body must be {"user_id": "<uuid>"}`, http.StatusBadRequest)
		return
	}
	if _, ok := h.authorize(w, r, id, me, roleOwner); !ok {
		return
	}

	var active bool
	if err := h.db.QueryRow(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND issuer IS NOT NULL AND disabled_at IS NULL)`,
		target).Scan(&active); err != nil {
		slog.Error("albums: add member user lookup failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if !active {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	tag, err := h.db.Exec(r.Context(),
		`INSERT INTO album_members (album_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, target)
	if isForeignKeyViolation(err) { // the album was deleted since the role check
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("albums: add member failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("albums: member added", "album_id", id, "by", me, "user_id", target)
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveMember removes {user_id} from the album. The owner may remove anyone but themselves (they
// delete the album instead); any other member may remove only themselves, which is leaving. The
// cascade on album_media takes that member's contributions with them.
func (h *AlbumHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := albumIDParam(w, r)
	if !ok {
		return
	}
	target, ok := auth.CanonicalUUID(chi.URLParam(r, "user_id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	role, ok := h.authorize(w, r, id, me, roleMember)
	if !ok {
		return
	}
	switch {
	case target == me && role == roleOwner:
		http.Error(w, "the album owner cannot leave; delete the album instead", http.StatusBadRequest)
		return
	case target != me && role != roleOwner:
		http.Error(w, "only the album owner can remove other members", http.StatusForbidden)
		return
	}

	// The owner_id guard keeps the owner's own member row undeletable here whatever the checks above say.
	tag, err := h.db.Exec(r.Context(), `
		DELETE FROM album_members
		WHERE album_id = $1 AND user_id = $2
		  AND user_id <> (SELECT owner_id FROM albums WHERE id = $1)`, id, target)
	if err != nil {
		slog.Error("albums: remove member failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("albums: member removed", "album_id", id, "by", me, "user_id", target, "left", target == me)
	w.WriteHeader(http.StatusNoContent)
}

// albumMediaBody is the body of POST /api/albums/{id}/media and /media/remove. Pointers tell a
// missing field from an empty one, so exactly one can be required.
type albumMediaBody struct {
	MediaIDs *[]string `json:"media_ids"`
	TripID   *string   `json:"trip_id"`
}

// albumMediaSelection is a validated albumMediaBody: either mediaIDs or tripID is set, in
// canonical form.
type albumMediaSelection struct {
	mediaIDs []string
	tripID   string
}

func parseAlbumMediaSelection(b albumMediaBody) (albumMediaSelection, error) {
	if (b.MediaIDs == nil) == (b.TripID == nil) {
		return albumMediaSelection{}, errors.New("body must have exactly one of media_ids and trip_id")
	}
	if b.TripID != nil {
		id, ok := auth.CanonicalUUID(*b.TripID)
		if !ok {
			return albumMediaSelection{}, errors.New("invalid trip_id")
		}
		return albumMediaSelection{tripID: id}, nil
	}
	if n := len(*b.MediaIDs); n < 1 || n > maxAlbumMediaIDs {
		return albumMediaSelection{}, fmt.Errorf("media_ids must hold 1-%d IDs", maxAlbumMediaIDs)
	}
	ids := make([]string, 0, len(*b.MediaIDs))
	for _, raw := range *b.MediaIDs {
		id, ok := auth.CanonicalUUID(raw)
		if !ok {
			return albumMediaSelection{}, errors.New("invalid media_ids")
		}
		ids = append(ids, id)
	}
	return albumMediaSelection{mediaIDs: ids}, nil
}

// cond returns the condition on media aliased as m that selects the items, with its single arg as $n.
func (s albumMediaSelection) cond(n int) string {
	if s.tripID != "" {
		return fmt.Sprintf("m.trip_id = $%d", n)
	}
	return fmt.Sprintf("m.id = ANY($%d)", n)
}

func (s albumMediaSelection) arg() any {
	if s.tripID != "" {
		return s.tripID
	}
	return s.mediaIDs
}

// albumMediaRequest decodes the album media body and checks membership and, for a trip_id, that
// the trip is the caller's. It writes the error response and returns false on failure.
func (h *AlbumHandler) albumMediaRequest(w http.ResponseWriter, r *http.Request, me string) (string, albumRole, albumMediaSelection, bool) {
	id, ok := albumIDParam(w, r)
	if !ok {
		return "", roleNone, albumMediaSelection{}, false
	}
	var body albumMediaBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAlbumMediaBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return "", roleNone, albumMediaSelection{}, false
	}
	sel, err := parseAlbumMediaSelection(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", roleNone, albumMediaSelection{}, false
	}
	role, ok := h.authorize(w, r, id, me, roleMember)
	if !ok {
		return "", roleNone, albumMediaSelection{}, false
	}
	if sel.tripID != "" {
		// Trips are personal: a trip_id only ever resolves to the caller's own trip.
		var owns bool
		if err := h.db.QueryRow(r.Context(),
			`SELECT EXISTS (SELECT 1 FROM trips WHERE id = $1 AND owner_id = $2)`, sel.tripID, me,
		).Scan(&owns); err != nil {
			slog.Error("albums: trip lookup failed", "album_id", id, "trip_id", sel.tripID, "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return "", roleNone, albumMediaSelection{}, false
		}
		if !owns {
			http.Error(w, "trip not found", http.StatusNotFound)
			return "", roleNone, albumMediaSelection{}, false
		}
	}
	return id, role, sel, true
}

// AddMedia contributes the caller's own media to the album: {"media_ids"} or {"trip_id"} (a
// one-time snapshot of the trip's current media). Items the caller does not own are skipped, so
// no member can re-share what they only see; already contained items are skipped too.
func (h *AlbumHandler) AddMedia(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, _, sel, ok := h.albumMediaRequest(w, r, me)
	if !ok {
		return
	}

	tag, err := h.db.Exec(r.Context(), `
		INSERT INTO album_media (album_id, media_id, owner_id)
		SELECT $1, m.id, m.owner_id FROM media m
		WHERE m.owner_id = $2 AND `+sel.cond(3)+`
		ON CONFLICT DO NOTHING`, id, me, sel.arg())
	if isForeignKeyViolation(err) { // membership ended, or an item was deleted, since the checks above
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("albums: add media failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeAlbumJSON(w, http.StatusOK, map[string]int64{"added": tag.RowsAffected()})
}

// RemoveMedia takes items out of the album (the media stay). Same body as AddMedia. The album
// owner may remove any item; another member only their own, and a request naming someone else's
// item is refused whole with 403.
func (h *AlbumHandler) RemoveMedia(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, role, sel, ok := h.albumMediaRequest(w, r, me)
	if !ok {
		return
	}

	sql := `DELETE FROM album_media am USING media m
		WHERE am.media_id = m.id AND am.album_id = $1 AND ` + sel.cond(2)
	args := []any{id, sel.arg()}
	if role != roleOwner {
		var foreign bool
		if err := h.db.QueryRow(r.Context(), `
			SELECT EXISTS (
				SELECT 1 FROM album_media am JOIN media m ON m.id = am.media_id
				WHERE am.album_id = $1 AND am.owner_id <> $2 AND `+sel.cond(3)+`)`,
			id, me, sel.arg()).Scan(&foreign); err != nil {
			slog.Error("albums: remove media ownership check failed", "album_id", id, "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if foreign {
			http.Error(w, "only the album owner can remove other members' items", http.StatusForbidden)
			return
		}
		// Also bounds the delete itself, so an item added by someone else meanwhile stays.
		sql += ` AND am.owner_id = $3`
		args = append(args, me)
	}

	tag, err := h.db.Exec(r.Context(), sql, args...)
	if err != nil {
		slog.Error("albums: remove media failed", "album_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeAlbumJSON(w, http.StatusOK, map[string]int64{"removed": tag.RowsAffected()})
}

// MediaAlbums lists the albums the caller belongs to that hold media {id}. Only an item's owner
// contributes it, and only while a member, so an item the caller cannot read is in none of their
// albums and yields [] (a malformed ID too).
func (h *AlbumHandler) MediaAlbums(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	albums := []albumRef{}
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		writeAlbumJSON(w, http.StatusOK, albums)
		return
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT a.id, a.name
		FROM album_media am
		JOIN albums a ON a.id = am.album_id
		JOIN album_members mem ON mem.album_id = am.album_id AND mem.user_id = $2
		WHERE am.media_id = $1
		ORDER BY a.created_at DESC, a.id`, id, me)
	if err != nil {
		slog.Error("albums: media albums query failed", "media_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var a albumRef
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			slog.Error("albums: media albums row scan failed", "media_id", id, "err", err)
			continue
		}
		albums = append(albums, a)
	}
	if err := rows.Err(); err != nil {
		slog.Error("albums: media albums rows failed", "media_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeAlbumJSON(w, http.StatusOK, albums)
}

// ListUsers is the directory album owners pick members from: every active, claimed user.
func (h *AlbumHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT id, display_name, email FROM users
		WHERE issuer IS NOT NULL AND disabled_at IS NULL
		ORDER BY COALESCE(display_name, email), id`)
	if err != nil {
		slog.Error("albums: user directory query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	users := []directoryUser{}
	for rows.Next() {
		var u directoryUser
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email); err != nil {
			slog.Error("albums: user directory row scan failed", "err", err)
			continue
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		slog.Error("albums: user directory rows failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeAlbumJSON(w, http.StatusOK, users)
}
