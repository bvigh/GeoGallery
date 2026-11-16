package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// challengeItemCount is how many rounds a challenge has. The CHECK on challenge_items.position
	// allows no more.
	challengeItemCount = 5
	// maxChallengePlayers counts the owner too. Each player adds one read-access condition to the
	// query that draws the items.
	maxChallengePlayers   = 20
	maxChallengeBodyBytes = 1 << 14
	// challengeDrawGrid is the number of cells along each side of the grid the draw lays over the
	// pool's extent, and challengeDrawPeriods the number of equal periods it cuts the pool's time
	// span into (challengeDrawSQL).
	challengeDrawGrid    = 8
	challengeDrawPeriods = 8
)

// ChallengeHandler runs game challenges: five located items, fixed when the challenge is made,
// that each of its players guesses once, and a table of their scores.
//
// A challenge grants no read access. Its items are drawn from what every player can already read,
// and after that a round, its answer and the guesses on it are shown to a player only while
// readableMediaCond still lets them read the item. A caller who is not a player gets 404 on
// everything challenge-scoped; a player attempting an owner-only action gets 403.
type ChallengeHandler struct {
	db      *pgxpool.Pool
	storage gameStorage
}

func NewChallengeHandler(db *pgxpool.Pool, s *storage.Client) *ChallengeHandler {
	return &ChallengeHandler{db: db, storage: s}
}

// challengeSummary is one challenge as its player $1 sees it in the list.
type challengeSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerID     string    `json:"owner_id"`
	OwnerName   *string   `json:"owner_name"` // display_name, else email; null when the owner has neither
	IsOwner     bool      `json:"is_owner"`
	CreatedAt   time.Time `json:"created_at"`
	MemberCount int       `json:"member_count"`
	// ItemCount is the number of rounds now: a round whose media was deleted is gone.
	ItemCount int `json:"item_count"`
	// Played and Score are the caller's own: the rounds they guessed and the sum of their scores.
	Played int     `json:"played"`
	Score  float64 `json:"score"`
	// Finished: the caller has no round left to play. A round whose item they can no longer read
	// is not left to play.
	Finished bool `json:"finished"`

	// The scale and the guess map's area, for the caller's next round.
	maxDistKm float64
	bounds    [4]float64
}

// challengeSelect yields one challengeSummary row per challenge user $1 plays in.
var challengeSelect = `
	SELECT c.id, c.name, c.owner_id, COALESCE(u.display_name, u.email), c.owner_id = $1, c.created_at,
	       (SELECT COUNT(*) FROM challenge_members cm WHERE cm.challenge_id = c.id),
	       (SELECT COUNT(*) FROM challenge_items ci WHERE ci.challenge_id = c.id),
	       s.played, s.score,
	       NOT EXISTS (
	           SELECT 1 FROM challenge_items ci JOIN media m ON m.id = ci.media_id
	           WHERE ci.challenge_id = c.id AND ` + readableMediaCond("m", 1) + `
	             AND NOT EXISTS (
	                 SELECT 1 FROM challenge_scores cs
	                 WHERE cs.challenge_id = ci.challenge_id AND cs.position = ci.position AND cs.user_id = $1)),
	       c.max_dist_km, c.bounds_south, c.bounds_west, c.bounds_north, c.bounds_east
	FROM challenges c
	JOIN challenge_members me ON me.challenge_id = c.id AND me.user_id = $1
	JOIN users u ON u.id = c.owner_id
	CROSS JOIN LATERAL (
	    SELECT COUNT(*) AS played, COALESCE(SUM(cs.total_score), 0) AS score
	    FROM challenge_scores cs
	    WHERE cs.challenge_id = c.id AND cs.user_id = $1) s`

func scanChallenge(row pgx.Row) (challengeSummary, error) {
	var c challengeSummary
	err := row.Scan(&c.ID, &c.Name, &c.OwnerID, &c.OwnerName, &c.IsOwner, &c.CreatedAt,
		&c.MemberCount, &c.ItemCount, &c.Played, &c.Score, &c.Finished,
		&c.maxDistKm, &c.bounds[0], &c.bounds[1], &c.bounds[2], &c.bounds[3])
	return c, err
}

// challengeMember is one player with their standing. Members come in a fixed order (the owner,
// then by ID), so a client can give each a colour that survives a change of the ranking.
type challengeMember struct {
	UserID  string  `json:"user_id"`
	Name    *string `json:"name"` // display_name, else email
	IsOwner bool    `json:"is_owner"`
	Played  int     `json:"played"`
	Score   float64 `json:"score"`
}

// challengeRound is the caller's next round: a free round's fields, and which round it is.
type challengeRound struct {
	// Position names the round in the guess. Number counts the rounds that are left in the
	// challenge, from 1: the two differ once a round's media was deleted.
	Position int `json:"position"`
	Number   int `json:"number"`
	roundMedia
}

type challengeGuess struct {
	UserID        string   `json:"user_id"`
	Lat           float64  `json:"lat"`
	Lng           float64  `json:"lng"`
	GuessDate     *string  `json:"guess_date"` // YYYY-MM-DD; null when the player guessed no date
	DistanceKm    float64  `json:"distance_km"`
	DateDiffDays  *float64 `json:"date_diff_days"`
	LocationScore float64  `json:"location_score"`
	TimeScore     *float64 `json:"time_score"`
	TotalScore    float64  `json:"total_score"`
}

// challengeResult is one round once the caller has finished: the answer and everyone's guesses.
// For a round whose item the caller cannot read, only Position, Number and Available (false) are
// set: the answer and the guesses around it would give its location away.
type challengeResult struct {
	Position   int              `json:"position"`
	Number     int              `json:"number"`
	Available  bool             `json:"available"`
	MediaID    *string          `json:"media_id"`
	MediaType  *string          `json:"media_type"`
	URL        *string          `json:"url"`
	ActualLat  *float64         `json:"actual_lat"`
	ActualLng  *float64         `json:"actual_lng"`
	ActualDate *string          `json:"actual_date"`
	Guesses    []challengeGuess `json:"guesses"`
}

type challengeDetail struct {
	challengeSummary
	Members []challengeMember `json:"members"`
	// Round is the caller's next round, null once they have finished.
	Round *challengeRound `json:"round"`
	// Results is null until the caller has finished, so no answer is shown before their guess.
	Results []challengeResult `json:"results"`
}

// challengeIDParam returns the canonical {id} path param, or writes 404 for a malformed one.
func challengeIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
	}
	return id, ok
}

func (h *ChallengeHandler) List(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	rows, err := h.db.Query(r.Context(), challengeSelect+` ORDER BY c.created_at DESC, c.id`, me)
	if err != nil {
		slog.Error("challenges: list query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	challenges := []challengeSummary{}
	for rows.Next() {
		c, err := scanChallenge(rows)
		if err != nil {
			slog.Error("challenges: list row scan failed", "err", err)
			continue
		}
		challenges = append(challenges, c)
	}
	if err := rows.Err(); err != nil {
		slog.Error("challenges: list rows failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeAlbumJSON(w, http.StatusOK, challenges)
}

// challengeInput is the body of POST /api/challenges.
type challengeInput struct {
	Name      *string   `json:"name"`
	MemberIDs *[]string `json:"member_ids"`
}

// newChallenge is a validated challengeInput: the trimmed name, and the players beside the owner
// in canonical form, each once.
type newChallenge struct {
	name   string
	others []string
}

// validateChallenge checks a challenge's name and players. The owner always plays, so their own
// ID among member_ids is dropped; member_ids may be left out for a challenge the owner plays alone.
func validateChallenge(in challengeInput, owner string) (newChallenge, error) {
	if in.Name == nil {
		return newChallenge{}, errors.New("name is required")
	}
	name, err := albumName(*in.Name) // the same rule as an album's name
	if err != nil {
		return newChallenge{}, err
	}
	c := newChallenge{name: name, others: []string{}}
	if in.MemberIDs == nil {
		return c, nil
	}
	seen := map[string]bool{owner: true}
	for _, raw := range *in.MemberIDs {
		id, ok := auth.CanonicalUUID(raw)
		if !ok {
			return newChallenge{}, errors.New("invalid member_ids")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		c.others = append(c.others, id)
	}
	if len(c.others) > maxChallengePlayers-1 {
		return newChallenge{}, fmt.Errorf("a challenge has at most %d players", maxChallengePlayers)
	}
	return c, nil
}

// challengePoolConds returns the conditions on media aliased as m, with their args, for the set a
// challenge's items are drawn from: what the owner's filters select (buildMediaFilter, so $1 is
// the owner), located, and readable by every other player too. Each player gets readableMediaCond
// once: there is no second way to say who reads what.
func challengePoolConds(q url.Values, owner string, others []string) ([]string, []any) {
	conds, args := buildMediaFilter(q, owner)
	conds = append(conds, "m.location IS NOT NULL")
	for _, id := range others {
		args = append(args, id)
		conds = append(conds, readableMediaCond("m", len(args)))
	}
	return conds, args
}

// tooFewItemsMessage is the reason of the 409 that refuses a challenge: n items are in the pool.
func tooFewItemsMessage(n int64) string {
	const tail = " the filters and can be seen by every player. A challenge needs %d."
	switch n {
	case 0:
		return fmt.Sprintf("No located item matches"+tail, challengeItemCount)
	case 1:
		return fmt.Sprintf("Only 1 located item matches"+tail, challengeItemCount)
	}
	return fmt.Sprintf("Only %d located items match"+tail, n, challengeItemCount)
}

// challengeDrawSQL is the statement that draws a challenge's items from the pool (conds on media
// aliased as m, as challengePoolConds returns them) and stores them with their answers. $idArg is
// the challenge and $playersArg its players, the owner included.
//
// A plain random draw follows the number of items: a place with a thousand photos would fill most
// challenges, and one photographed once would never come up. This draw goes by place and time
// instead, and by what the players have had already:
//
//   - Items the players have had less often come first, so an item is repeated only when the
//     pool has nothing fresher. An item counts as often as the player who has had it most has
//     had it, in the challenges they play in. That is what keeps a place with two photos from
//     coming up in every challenge: once used, they wait until the rest has been used as often.
//     The count is read from challenge_items, so a deleted challenge is forgotten, and so is one
//     a player left, for that player.
//   - Among those, the rounds come from different cells of a grid of challengeDrawGrid cells a
//     side, laid over the pool's extent (the area the guess map opens on): one item from each
//     cell, the cells in random order, whatever the number of items in them. A second item from a
//     cell is taken only when every cell has given one.
//   - Inside a cell, the pool's time span is cut into challengeDrawPeriods equal periods (undated
//     items are a period of their own), and the item comes from a random period, then at random
//     from that period: ten years of photos of one town are not all from the year with the most.
//
// The positions are dealt at random, so the order of the rounds says nothing about the cells.
func challengeDrawSQL(conds []string, idArg, playersArg int) string {
	return fmt.Sprintf(`
		WITH pool AS (
			SELECT m.id, m.location, m.taken_at, COALESCE(m.gps_confidence, 1.0) AS confidence,
			       ST_Y(m.location::geometry) AS lat, ST_X(m.location::geometry) AS lng,
			       EXTRACT(EPOCH FROM m.taken_at) AS epoch
			FROM media m
			WHERE %[1]s
		), extent AS (
			SELECT MIN(lat) AS south, MAX(lat) AS north, MIN(lng) AS west, MAX(lng) AS east,
			       MIN(epoch) AS earliest, MAX(epoch) AS latest
			FROM pool
		), used AS (
			-- The most often one of the players has had the item.
			SELECT had.media_id, MAX(had.times) AS times
			FROM (
				SELECT ci.media_id, cm.user_id, COUNT(*) AS times
				FROM challenge_items ci
				JOIN challenge_members cm ON cm.challenge_id = ci.challenge_id
				WHERE cm.user_id = ANY($%[3]d::uuid[])
				GROUP BY ci.media_id, cm.user_id
			) had
			GROUP BY had.media_id
		), placed AS (
			-- WIDTH_BUCKET puts the upper bound itself one past the last bucket, and refuses equal
			-- bounds: a pool at one point, or of one instant, is one cell or one period.
			SELECT p.id, p.location, p.taken_at, p.confidence, COALESCE(u.times, 0) AS times,
			       CASE WHEN e.north > e.south
			            THEN LEAST(WIDTH_BUCKET(p.lat, e.south, e.north, %[4]d), %[4]d) ELSE 1 END AS gy,
			       CASE WHEN e.east > e.west
			            THEN LEAST(WIDTH_BUCKET(p.lng, e.west, e.east, %[4]d), %[4]d) ELSE 1 END AS gx,
			       CASE WHEN p.epoch IS NULL THEN 0
			            WHEN e.latest > e.earliest
			            THEN LEAST(WIDTH_BUCKET(p.epoch, e.earliest, e.latest, %[5]d), %[5]d)
			            ELSE 1 END AS period
			FROM pool p
			CROSS JOIN extent e
			LEFT JOIN used u ON u.media_id = p.id
		), in_period AS (
			SELECT pl.*, ROW_NUMBER() OVER (PARTITION BY times, gy, gx, period ORDER BY RANDOM()) AS nth_of_period
			FROM placed pl
		), in_cell AS (
			-- A cell's items in turn: one from each of its periods, the periods in random order,
			-- then a second from each.
			SELECT ip.*, ROW_NUMBER() OVER (PARTITION BY times, gy, gx ORDER BY nth_of_period, RANDOM()) AS nth_of_cell
			FROM in_period ip
		), drawn AS (
			SELECT id, location, taken_at, confidence
			FROM in_cell
			ORDER BY times, nth_of_cell, RANDOM()
			LIMIT %[6]d
		)
		INSERT INTO challenge_items (challenge_id, position, media_id, location, taken_at, gps_confidence)
		SELECT $%[2]d::uuid, ROW_NUMBER() OVER (ORDER BY RANDOM()), id, location, taken_at, confidence
		FROM drawn`,
		strings.Join(conds, " AND "), idArg, playersArg, challengeDrawGrid, challengeDrawPeriods, challengeItemCount)
}

// Create makes a challenge: {"name", "member_ids"?}, with the Game's filters in the query string,
// exactly as GET /api/game/round takes them. The five items are drawn (challengeDrawSQL) from the
// located media those filters select that every player can read. Fewer than five → 409 with the
// reason, and nothing is stored.
func (h *ChallengeHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := auth.MustUser(ctx).ID
	q := r.URL.Query()
	if err := validateMediaFilters(q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var in challengeInput
	if !decodeStrictJSON(w, r, maxChallengeBodyBytes, &in, "invalid body: want a JSON object with name and member_ids") {
		return
	}
	c, err := validateChallenge(in, me)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(c.others) > 0 {
		var active int
		if err := h.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM users
			WHERE id = ANY($1::uuid[]) AND issuer IS NOT NULL AND disabled_at IS NULL`,
			c.others).Scan(&active); err != nil {
			slog.Error("challenges: create user lookup failed", "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if active != len(c.others) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
	}

	conds, args := challengePoolConds(q, me, c.others)

	tx, err := h.db.Begin(ctx)
	if err != nil {
		slog.Error("challenges: create begin tx failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	// Unlike a free round, a challenge keeps its scale: a guess is never scored on a default.
	maxDistKm, bounds, err := measureMediaSet(ctx, tx, conds, args)
	if err != nil {
		slog.Error("challenges: measuring the pool failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if bounds == nil {
		http.Error(w, tooFewItemsMessage(0), http.StatusConflict)
		return
	}

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO challenges (owner_id, name, max_dist_km, bounds_south, bounds_west, bounds_north, bounds_east)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		me, c.name, maxDistKm, bounds[0], bounds[1], bounds[2], bounds[3]).Scan(&id); err != nil {
		slog.Error("challenges: create insert failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	players := append([]string{me}, c.others...)
	if _, err := tx.Exec(ctx, `
		INSERT INTO challenge_members (challenge_id, user_id)
		SELECT $1, unnest($2::uuid[])`, id, players); err != nil {
		slog.Error("challenges: create members insert failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	// Drawn and stored in one statement, so an item deleted meanwhile is simply not drawn. The
	// answer is copied here: every player is scored against the same one.
	itemArgs := append(append([]any{}, args...), id, players)
	tag, err := tx.Exec(ctx, challengeDrawSQL(conds, len(itemArgs)-1, len(itemArgs)), itemArgs...)
	if err != nil {
		slog.Error("challenges: create items insert failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if n := tag.RowsAffected(); n < challengeItemCount {
		// The deferred rollback drops the challenge and its members again.
		http.Error(w, tooFewItemsMessage(n), http.StatusConflict)
		return
	}

	created, err := scanChallenge(tx.QueryRow(ctx, challengeSelect+` WHERE c.id = $2`, me, id))
	if err != nil {
		slog.Error("challenges: create read back failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("challenges: create commit failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Info("challenges: created", "challenge_id", id, "owner", me, "players", created.MemberCount)
	writeAlbumJSON(w, http.StatusCreated, created)
}

// challengeItem is one round as the caller stands to it.
type challengeItem struct {
	position                  int
	mediaID, mediaType, key   string
	yaw, pitch, fov           *float64
	lat, lng                  float64
	takenAt                   *string
	readable, guessedByCaller bool
}

// Get returns one challenge to a player: the standings, and either their next round or, once they
// have none left, every round's answer and guesses.
func (h *ChallengeHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := auth.MustUser(ctx).ID
	id, ok := challengeIDParam(w, r)
	if !ok {
		return
	}

	summary, err := scanChallenge(h.db.QueryRow(ctx, challengeSelect+` WHERE c.id = $2`, me, id))
	if errors.Is(err, pgx.ErrNoRows) { // no such challenge, or the caller does not play in it
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("challenges: get failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	detail := challengeDetail{challengeSummary: summary}

	if detail.Members, err = h.members(ctx, id); err != nil {
		slog.Error("challenges: members query failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	items, err := h.items(ctx, id, me)
	if err != nil {
		slog.Error("challenges: items query failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	for i, it := range items {
		if !it.readable || it.guessedByCaller {
			continue
		}
		url, err := h.storage.PresignGet(ctx, it.key)
		if err != nil {
			slog.Error("challenges: presign failed", "challenge_id", id, "media_id", it.mediaID, "err", err)
			http.Error(w, "presign error", http.StatusInternalServerError)
			return
		}
		detail.Round = &challengeRound{
			Position: it.position,
			Number:   i + 1,
			roundMedia: roundMedia{
				ID: it.mediaID, MediaType: it.mediaType, URL: url,
				InitialYaw: it.yaw, InitialPitch: it.pitch, InitialFov: it.fov,
				MaxDistKm: summary.maxDistKm, Bounds: &summary.bounds,
			},
		}
		// The items are as of this query; the summary's own check may be a moment older.
		detail.Finished = false
		writeAlbumJSON(w, http.StatusOK, detail)
		return
	}

	detail.Finished = true
	guesses, err := h.guesses(ctx, id)
	if err != nil {
		slog.Error("challenges: guesses query failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	detail.Results = make([]challengeResult, 0, len(items))
	for i, it := range items {
		res := challengeResult{Position: it.position, Number: i + 1, Guesses: []challengeGuess{}}
		if it.readable {
			url, err := h.storage.PresignGet(ctx, it.key)
			if err != nil {
				slog.Error("challenges: presign failed", "challenge_id", id, "media_id", it.mediaID, "err", err)
				http.Error(w, "presign error", http.StatusInternalServerError)
				return
			}
			res.Available = true
			res.MediaID, res.MediaType, res.URL = &it.mediaID, &it.mediaType, &url
			res.ActualLat, res.ActualLng, res.ActualDate = &it.lat, &it.lng, it.takenAt
			if g := guesses[it.position]; g != nil {
				res.Guesses = g
			}
		}
		detail.Results = append(detail.Results, res)
	}
	writeAlbumJSON(w, http.StatusOK, detail)
}

func (h *ChallengeHandler) members(ctx context.Context, id string) ([]challengeMember, error) {
	rows, err := h.db.Query(ctx, `
		SELECT cm.user_id, COALESCE(u.display_name, u.email), cm.user_id = c.owner_id, s.played, s.score
		FROM challenge_members cm
		JOIN challenges c ON c.id = cm.challenge_id
		JOIN users u ON u.id = cm.user_id
		CROSS JOIN LATERAL (
		    SELECT COUNT(*) AS played, COALESCE(SUM(cs.total_score), 0) AS score
		    FROM challenge_scores cs
		    WHERE cs.challenge_id = cm.challenge_id AND cs.user_id = cm.user_id) s
		WHERE cm.challenge_id = $1
		ORDER BY cm.user_id = c.owner_id DESC, cm.user_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := []challengeMember{}
	for rows.Next() {
		var m challengeMember
		if err := rows.Scan(&m.UserID, &m.Name, &m.IsOwner, &m.Played, &m.Score); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// items returns the challenge's rounds in order, each with whether user can read its item now and
// has guessed it. The read check is readableMediaCond, as for every other read of media.
func (h *ChallengeHandler) items(ctx context.Context, id, user string) ([]challengeItem, error) {
	rows, err := h.db.Query(ctx, `
		SELECT ci.position, ci.media_id, m.media_type, m.storage_key,
		       m.initial_yaw, m.initial_pitch, m.initial_fov,
		       ST_Y(ci.location::geometry), ST_X(ci.location::geometry), ci.taken_at::text,
		       `+readableMediaCond("m", 2)+`,
		       EXISTS (
		           SELECT 1 FROM challenge_scores cs
		           WHERE cs.challenge_id = ci.challenge_id AND cs.position = ci.position AND cs.user_id = $2)
		FROM challenge_items ci
		JOIN media m ON m.id = ci.media_id
		WHERE ci.challenge_id = $1
		ORDER BY ci.position`, id, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []challengeItem
	for rows.Next() {
		var it challengeItem
		if err := rows.Scan(&it.position, &it.mediaID, &it.mediaType, &it.key, &it.yaw, &it.pitch, &it.fov,
			&it.lat, &it.lng, &it.takenAt, &it.readable, &it.guessedByCaller); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// guesses returns every player's guesses by round, best first. The caller decides which rounds'
// guesses a player may see.
func (h *ChallengeHandler) guesses(ctx context.Context, id string) (map[int][]challengeGuess, error) {
	rows, err := h.db.Query(ctx, `
		SELECT cs.position, cs.user_id, ST_Y(cs.guess::geometry), ST_X(cs.guess::geometry), cs.guess_date::text,
		       cs.distance_km, cs.date_diff_days, cs.location_score, cs.time_score, cs.total_score
		FROM challenge_scores cs
		WHERE cs.challenge_id = $1
		ORDER BY cs.position, cs.total_score DESC, cs.user_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byRound := map[int][]challengeGuess{}
	for rows.Next() {
		var position int
		var g challengeGuess
		if err := rows.Scan(&position, &g.UserID, &g.Lat, &g.Lng, &g.GuessDate,
			&g.DistanceKm, &g.DateDiffDays, &g.LocationScore, &g.TimeScore, &g.TotalScore); err != nil {
			return nil, err
		}
		byRound[position] = append(byRound[position], g)
	}
	return byRound, rows.Err()
}

// challengeGuessInput is the body of POST /api/challenges/{id}/guess.
type challengeGuessInput struct {
	Position  *int     `json:"position"`
	GuessLat  *float64 `json:"guess_lat"`
	GuessLng  *float64 `json:"guess_lng"`
	GuessDate string   `json:"guess_date"` // YYYY-MM-DD, or "" for no date
}

// challengeGuessValue is a validated challengeGuessInput. date is nil when no date was guessed.
type challengeGuessValue struct {
	position int
	lat, lng float64
	date     *string
}

// validateChallengeGuess checks a guess. Unlike a free round's, a date that cannot be read is
// refused, not dropped: the guess is the player's only one. A longitude from a map scrolled round
// the world is brought back into -180..180, where the results map draws it.
func validateChallengeGuess(in challengeGuessInput) (challengeGuessValue, error) {
	if in.Position == nil || *in.Position < 1 || *in.Position > challengeItemCount {
		return challengeGuessValue{}, fmt.Errorf("position must be 1-%d", challengeItemCount)
	}
	if in.GuessLat == nil || in.GuessLng == nil {
		return challengeGuessValue{}, errors.New("guess_lat and guess_lng are required")
	}
	// Written this way round so that NaN, which compares false with everything, is refused too.
	if !(math.Abs(*in.GuessLat) <= 90) {
		return challengeGuessValue{}, errors.New("guess_lat must be between -90 and 90")
	}
	if math.IsNaN(*in.GuessLng) || math.IsInf(*in.GuessLng, 0) {
		return challengeGuessValue{}, errors.New("guess_lng must be a number")
	}
	v := challengeGuessValue{position: *in.Position, lat: *in.GuessLat, lng: wrapLongitude(*in.GuessLng)}
	if in.GuessDate != "" {
		// Postgres has no year 0, and time.Parse reads one.
		d, err := time.Parse(time.DateOnly, in.GuessDate)
		if err != nil || d.Year() < 1 {
			return challengeGuessValue{}, errors.New("guess_date must be YYYY-MM-DD, or left out")
		}
		v.date = &in.GuessDate
	}
	return v, nil
}

// wrapLongitude brings a longitude into -180..180. One in range is returned as it is.
func wrapLongitude(lng float64) float64 {
	if lng >= -180 && lng <= 180 {
		return lng
	}
	wrapped := math.Mod(lng+180, 360)
	if wrapped < 0 {
		wrapped += 360
	}
	return wrapped - 180
}

// Guess scores and stores the caller's guess for one round: {"position", "guess_lat",
// "guess_lng", "guess_date"?}. A player has one guess per round: a second is 409 and changes
// nothing. The round must be one the caller can still read (404 otherwise, as for a caller who
// does not play in the challenge).
func (h *ChallengeHandler) Guess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := auth.MustUser(ctx).ID
	id, ok := challengeIDParam(w, r)
	if !ok {
		return
	}
	var in challengeGuessInput
	if !decodeStrictJSON(w, r, maxChallengeBodyBytes, &in,
		"invalid body: want a JSON object with position, guess_lat, guess_lng and guess_date") {
		return
	}
	g, err := validateChallengeGuess(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// One lookup answers three questions: the caller plays here, the round exists, and they can
	// read its item now. A challenge is no second way to read media.
	var answer guessAnswer
	var maxDistKm float64
	err = h.db.QueryRow(ctx, `
		SELECT ST_Y(ci.location::geometry), ST_X(ci.location::geometry), ci.taken_at::text,
		       ci.gps_confidence, c.max_dist_km
		FROM challenge_items ci
		JOIN challenges c ON c.id = ci.challenge_id
		JOIN challenge_members cm ON cm.challenge_id = c.id AND cm.user_id = $3
		JOIN media m ON m.id = ci.media_id
		WHERE ci.challenge_id = $1 AND ci.position = $2 AND `+readableMediaCond("m", 3),
		id, g.position, me).Scan(&answer.lat, &answer.lng, &answer.takenAt, &answer.confidence, &maxDistKm)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "round not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("challenges: guess lookup failed", "challenge_id", id, "position", g.position, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	guessDate := ""
	if g.date != nil {
		guessDate = *g.date
	}
	result := scoreGuess(answer, g.lat, g.lng, guessDate, maxDistKm)

	tag, err := h.db.Exec(ctx, `
		INSERT INTO challenge_scores (challenge_id, position, user_id, guess, guess_date,
		                              distance_km, date_diff_days, location_score, time_score, total_score)
		VALUES ($1, $2, $3, ST_SetSRID(ST_MakePoint($5, $4), 4326), $6::date, $7, $8, $9, $10, $11)
		ON CONFLICT (challenge_id, position, user_id) DO NOTHING`,
		id, g.position, me, g.lat, g.lng, g.date,
		result.DistanceKm, result.DateDiffDays, result.LocationScore, result.TimeScore, result.TotalScore)
	if isForeignKeyViolation(err) { // the round or the caller's membership went since the lookup
		http.Error(w, "round not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("challenges: guess insert failed", "challenge_id", id, "position", g.position, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "you have already guessed this round", http.StatusConflict)
		return
	}
	writeAlbumJSON(w, http.StatusOK, result)
}

// isOwner reports whether user owns the challenge; found is false when they do not play in it
// (or there is no such challenge).
func (h *ChallengeHandler) isOwner(ctx context.Context, id, user string) (owner, found bool, err error) {
	err = h.db.QueryRow(ctx, `
		SELECT c.owner_id = $2
		FROM challenges c JOIN challenge_members cm ON cm.challenge_id = c.id AND cm.user_id = $2
		WHERE c.id = $1`, id, user).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return owner, err == nil, err
}

// Delete removes the challenge with its rounds and everyone's guesses. Owner only: another player
// gets 403 and leaves instead.
func (h *ChallengeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := challengeIDParam(w, r)
	if !ok {
		return
	}
	owner, found, err := h.isOwner(r.Context(), id, me)
	switch {
	case err != nil:
		slog.Error("challenges: delete role lookup failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	case !found:
		http.Error(w, "not found", http.StatusNotFound)
		return
	case !owner:
		http.Error(w, "only the challenge owner can do this", http.StatusForbidden)
		return
	}

	tag, err := h.db.Exec(r.Context(), `DELETE FROM challenges WHERE id = $1 AND owner_id = $2`, id, me)
	if err != nil {
		slog.Error("challenges: delete failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 { // deleted since the role check
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("challenges: deleted", "challenge_id", id, "by", me)
	w.WriteHeader(http.StatusNoContent)
}

// Leave takes the caller out of the challenge, and their guesses with them (a cascade). The owner
// cannot leave: they delete the challenge instead.
func (h *ChallengeHandler) Leave(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := challengeIDParam(w, r)
	if !ok {
		return
	}
	owner, found, err := h.isOwner(r.Context(), id, me)
	switch {
	case err != nil:
		slog.Error("challenges: leave role lookup failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	case !found:
		http.Error(w, "not found", http.StatusNotFound)
		return
	case owner:
		http.Error(w, "the challenge owner cannot leave; delete the challenge instead", http.StatusBadRequest)
		return
	}

	// The owner_id guard keeps the owner's own member row undeletable here whatever the check above says.
	tag, err := h.db.Exec(r.Context(), `
		DELETE FROM challenge_members
		WHERE challenge_id = $1 AND user_id = $2
		  AND user_id <> (SELECT owner_id FROM challenges WHERE id = $1)`, id, me)
	if err != nil {
		slog.Error("challenges: leave failed", "challenge_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("challenges: player left", "challenge_id", id, "user_id", me)
	w.WriteHeader(http.StatusNoContent)
}
