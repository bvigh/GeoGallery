package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// challengeFixture: Alice shares six located photos and an unlocated one with Bob through the
// family album, to which Bob adds a located photo of his own. Alice keeps three more located
// photos to herself. Carol plays with nobody: she is in no album.
type challengeFixture struct {
	pool              *pgxpool.Pool
	store             *fakeObjectStore
	api               http.Handler
	alice, bob, carol string
	family            string
	shared            []string // Alice's six located photos in the family album, oldest first
	private           []string // Alice's three located photos in no album
	bobShared         string   // Bob's located photo in the family album
	unlocated         string   // Alice's unlocated photo in the family album
}

// newChallengeRouter serves the challenge routes, authenticating by X-Test-User as
// newOwnershipRouter does; the session-only wiring is tested in cmd/server.
func newChallengeRouter(pool *pgxpool.Pool, store *fakeObjectStore) http.Handler {
	challenges := &ChallengeHandler{db: pool, storage: store}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.User{ID: r.Header.Get(testUserHeader), Role: "user"}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u, auth.MethodSession)))
		})
	})
	r.Get("/api/challenges", challenges.List)
	r.Post("/api/challenges", challenges.Create)
	r.Get("/api/challenges/{id}", challenges.Get)
	r.Post("/api/challenges/{id}/guess", challenges.Guess)
	r.Delete("/api/challenges/{id}", challenges.Delete)
	r.Post("/api/challenges/{id}/leave", challenges.Leave)
	return r
}

func newChallengeFixture(t *testing.T) *challengeFixture {
	t.Helper()
	pool := testdb.New(t)
	f := &challengeFixture{pool: pool, store: &fakeObjectStore{}}
	f.api = newChallengeRouter(pool, f.store)
	f.alice = createUser(t, pool, "alice")
	f.bob = createUser(t, pool, "bob")
	f.carol = createUser(t, pool, "carol")
	f.family = insertAlbum(t, pool, f.alice, "Family")
	insertAlbumMember(t, pool, f.family, f.bob)

	// A photo a day from 2024-05-01, half a degree apart, so the set has a size to measure.
	located := func(owner, name string, i int) string {
		return insertMedia(t, pool, mediaFixture{
			owner: owner, filename: name, takenAt: fmt.Sprintf("2024-05-%02dT12:00:00Z", i+1),
			lat: ptr(47.0 + 0.5*float64(i)), lng: ptr(16.0 + 0.5*float64(i)),
		})
	}
	for i := range 6 {
		id := located(f.alice, fmt.Sprintf("shared%d.jpg", i), i)
		insertAlbumMedia(t, pool, f.family, id)
		f.shared = append(f.shared, id)
	}
	for i := range 3 {
		f.private = append(f.private, located(f.alice, fmt.Sprintf("private%d.jpg", i), 10+i))
	}
	f.bobShared = located(f.bob, "bob.jpg", 20)
	insertAlbumMedia(t, pool, f.family, f.bobShared)
	f.unlocated = insertMedia(t, pool, mediaFixture{owner: f.alice, filename: "nowhere.jpg", takenAt: "2024-05-09T12:00:00Z"})
	insertAlbumMedia(t, pool, f.family, f.unlocated)
	return f
}

// create posts a challenge and returns it; the request must succeed.
func (f *challengeFixture) create(t *testing.T, user, query, body string) challengeSummary {
	t.Helper()
	rec := do(t, f.api, user, http.MethodPost, "/api/challenges"+query, body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var c challengeSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c), rec.Body.String())
	return c
}

// withBob is the body of a challenge Alice makes for herself and Bob.
func (f *challengeFixture) withBob(name string) string {
	return fmt.Sprintf(`{"name":%q,"member_ids":[%q]}`, name, f.bob)
}

func (f *challengeFixture) get(t *testing.T, user, id string) challengeDetail {
	t.Helper()
	rec := do(t, f.api, user, http.MethodGet, "/api/challenges/"+id, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var d challengeDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d), rec.Body.String())
	return d
}

func (f *challengeFixture) list(t *testing.T, user string) []challengeSummary {
	t.Helper()
	rec := do(t, f.api, user, http.MethodGet, "/api/challenges", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list []challengeSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list), rec.Body.String())
	require.NotNil(t, list, "an empty list is [], never null")
	return list
}

func (f *challengeFixture) guess(user, id string, position int, lat, lng float64, date string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"position":%d,"guess_lat":%v,"guess_lng":%v,"guess_date":%q}`, position, lat, lng, date)
	req := httptest.NewRequest(http.MethodPost, "/api/challenges/"+id+"/guess", strings.NewReader(body))
	req.Header.Set(testUserHeader, user)
	rec := httptest.NewRecorder()
	f.api.ServeHTTP(rec, req)
	return rec
}

// mustGuess guesses and returns the score; the guess must be accepted.
func (f *challengeFixture) mustGuess(t *testing.T, user, id string, position int, lat, lng float64, date string) guessResult {
	t.Helper()
	rec := f.guess(user, id, position, lat, lng, date)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res guessResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res), rec.Body.String())
	return res
}

// storedRound is one challenge_items row, with the answer as the handler reads it.
type storedRound struct {
	position int
	mediaID  string
	answer   guessAnswer
}

func (f *challengeFixture) rounds(t *testing.T, id string) []storedRound {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT position, media_id::text, ST_Y(location::geometry), ST_X(location::geometry), taken_at::text, gps_confidence
		FROM challenge_items WHERE challenge_id = $1 ORDER BY position`, id)
	require.NoError(t, err)
	defer rows.Close()
	var out []storedRound
	for rows.Next() {
		var r storedRound
		require.NoError(t, rows.Scan(&r.position, &r.mediaID, &r.answer.lat, &r.answer.lng, &r.answer.takenAt, &r.answer.confidence))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func (f *challengeFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

func (f *challengeFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

// requireNothingStored checks that a refused challenge left no row behind.
func (f *challengeFixture) requireNothingStored(t *testing.T) {
	t.Helper()
	for _, table := range []string{"challenges", "challenge_members", "challenge_items", "challenge_scores"} {
		require.Zero(t, f.count(t, table), table)
	}
}

func roundMediaIDs(rounds []storedRound) []string {
	ids := make([]string, len(rounds))
	for i, r := range rounds {
		ids[i] = r.mediaID
	}
	return ids
}

func TestChallenge_CreateDrawsFromWhatEveryPlayerReads(t *testing.T) {
	f := newChallengeFixture(t)
	ctx := context.Background()
	bothRead := append(append([]string{}, f.shared...), f.bobShared)

	t.Run("five different located items both players can read, with the answer copied", func(t *testing.T) {
		// The draw is random: several challenges, the same rules each time.
		for i := range 8 {
			c := f.create(t, f.alice, "", f.withBob(fmt.Sprintf("  Round %d \n", i)))
			assert.Equal(t, fmt.Sprintf("Round %d", i), c.Name, "the name is trimmed")
			assert.Equal(t, f.alice, c.OwnerID)
			assert.Equal(t, ptr("alice"), c.OwnerName)
			assert.True(t, c.IsOwner)
			assert.Equal(t, 2, c.MemberCount)
			assert.Equal(t, challengeItemCount, c.ItemCount)
			assert.Zero(t, c.Played)
			assert.Zero(t, c.Score)
			assert.False(t, c.Finished)

			rounds := f.rounds(t, c.ID)
			require.Len(t, rounds, challengeItemCount)
			seen := map[string]bool{}
			for n, r := range rounds {
				assert.Equal(t, n+1, r.position, "positions run from 1")
				assert.Contains(t, bothRead, r.mediaID, "never a private or an unlocated item")
				assert.False(t, seen[r.mediaID], "no item twice")
				seen[r.mediaID] = true

				var lat, lng float64
				var takenAt *string
				require.NoError(t, f.pool.QueryRow(ctx, `
					SELECT ST_Y(location::geometry), ST_X(location::geometry), taken_at::text FROM media WHERE id = $1`,
					r.mediaID).Scan(&lat, &lng, &takenAt))
				assert.InDelta(t, lat, r.answer.lat, 1e-9)
				assert.InDelta(t, lng, r.answer.lng, 1e-9)
				assert.Equal(t, takenAt, r.answer.takenAt)
				assert.Equal(t, 1.0, r.answer.confidence, "a location without a confidence counts as certain")
			}
		}
	})

	t.Run("the scale and the guess map's area are the pool's, kept with the challenge", func(t *testing.T) {
		c := f.create(t, f.alice, "", f.withBob("Scale"))
		conds, args := challengePoolConds(url.Values{}, f.alice, []string{f.bob})
		wantDist, wantBounds, err := measureMediaSet(ctx, f.pool, conds, args)
		require.NoError(t, err)
		require.NotNil(t, wantBounds)

		var dist float64
		var bounds [4]float64
		require.NoError(t, f.pool.QueryRow(ctx, `
			SELECT max_dist_km, bounds_south, bounds_west, bounds_north, bounds_east FROM challenges WHERE id = $1`,
			c.ID).Scan(&dist, &bounds[0], &bounds[1], &bounds[2], &bounds[3]))
		assert.InDelta(t, wantDist, dist, 1e-6)
		assert.Equal(t, *wantBounds, bounds)
		// Bob's photo lies 10 degrees from Alice's first: far more than her private ones would add.
		assert.Greater(t, dist, 1000.0)
		assert.Equal(t, [4]float64{47, 16, 57.5, 26.5}, bounds, "rounded out to the half-degree grid")
	})

	t.Run("the owner alone draws from everything they read", func(t *testing.T) {
		readable := append(append([]string{}, bothRead...), f.private...)
		drewPrivate := false
		for range 12 {
			c := f.create(t, f.alice, "", `{"name":"Solo"}`)
			assert.Equal(t, 1, c.MemberCount)
			for _, id := range roundMediaIDs(f.rounds(t, c.ID)) {
				assert.Contains(t, readable, id)
				drewPrivate = drewPrivate || contains(f.private, id)
			}
		}
		assert.True(t, drewPrivate, "five of ten, twelve times: a private item is drawn at least once")
	})

	t.Run("the Game's filters narrow the pool", func(t *testing.T) {
		c := f.create(t, f.alice, "?owner=mine", f.withBob("Mine"))
		for _, id := range roundMediaIDs(f.rounds(t, c.ID)) {
			assert.Contains(t, f.shared, id, "owner=mine leaves Bob's photo out")
		}

		c = f.create(t, f.alice, "?album_id="+f.family+"&from_date=2024-05-02&to_date=2024-05-06&media_type=photo", f.withBob("Dated"))
		assert.ElementsMatch(t, f.shared[1:], roundMediaIDs(f.rounds(t, c.ID)),
			"those five days hold exactly five of the album's photos: all of them are drawn")
	})
}

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func TestChallenge_CreateRefusals(t *testing.T) {
	f := newChallengeFixture(t)
	const tail = " the filters and can be seen by every player. A challenge needs 5."
	disabled := createUser(t, f.pool, "dora")
	f.exec(t, `UPDATE users SET disabled_at = NOW() WHERE id = $1`, disabled)
	var unclaimed string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`INSERT INTO users (email) VALUES ('later@example.test') RETURNING id::text`).Scan(&unclaimed))
	members := func(ids ...string) string {
		quoted := make([]string, len(ids))
		for i, id := range ids {
			quoted[i] = fmt.Sprintf("%q", id)
		}
		return `{"name":"x","member_ids":[` + strings.Join(quoted, ",") + `]}`
	}

	tests := []struct {
		name       string
		query      string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"a player who reads none of the owner's media", "", members(f.carol), http.StatusConflict, "No located item matches" + tail},
		{"one such player among others", "", members(f.bob, f.carol), http.StatusConflict, "No located item matches" + tail},
		{"only Bob's own photo is shared with Alice", "?owner=shared", members(f.bob), http.StatusConflict, "Only 1 located item matches" + tail},
		{"four items after the date filter", "?owner=mine&from_date=2024-05-03", members(f.bob), http.StatusConflict, "Only 4 located items match" + tail},
		{"the unlocated item never counts", "?from_date=2024-05-03&to_date=2024-05-09", members(f.bob), http.StatusConflict, "Only 4 located items match" + tail},
		{"a filter that matches nothing", "?from_date=2030-01-01", `{"name":"x"}`, http.StatusConflict, "No located item matches" + tail},
		{"another user's album as a filter", "?album_id=" + unknownUUID, members(f.bob), http.StatusConflict, "No located item matches" + tail},

		{"an unknown user", "", members(f.bob, unknownUUID), http.StatusNotFound, "user not found"},
		{"a disabled user", "", members(disabled), http.StatusNotFound, "user not found"},
		{"a user who never signed in", "", members(unclaimed), http.StatusNotFound, "user not found"},

		{"a malformed filter", "?owner=everyone", members(f.bob), http.StatusBadRequest, "invalid owner: want mine or shared"},
		{"a malformed member", "", `{"name":"x","member_ids":["bob"]}`, http.StatusBadRequest, "invalid member_ids"},
		{"no name", "", `{"member_ids":[]}`, http.StatusBadRequest, "name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.api, f.alice, http.MethodPost, "/api/challenges"+tt.query, tt.body)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantBody, strings.TrimSpace(rec.Body.String()))
			f.requireNothingStored(t)
		})
	}
}

func TestChallenge_EachPlayerGuessesEachRoundOnce(t *testing.T) {
	f := newChallengeFixture(t)
	ctx := context.Background()
	c := f.create(t, f.alice, "", f.withBob("Family five"))
	rounds := f.rounds(t, c.ID)
	var maxDistKm float64
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT max_dist_km FROM challenges WHERE id = $1`, c.ID).Scan(&maxDistKm))

	t.Run("a player who is not in the challenge sees and guesses nothing", func(t *testing.T) {
		assert.Empty(t, f.list(t, f.carol))
		rec := do(t, f.api, f.carol, http.MethodGet, "/api/challenges/"+c.ID, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		rec = f.guess(f.carol, c.ID, 1, 47, 19, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		rec = do(t, f.api, f.bob, http.MethodGet, "/api/challenges/"+unknownUUID, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "no such challenge")
		assert.Zero(t, f.count(t, "challenge_scores"))
	})

	t.Run("the first round, with nothing given away", func(t *testing.T) {
		d := f.get(t, f.bob, c.ID)
		assert.Equal(t, "Family five", d.Name)
		assert.False(t, d.IsOwner)
		assert.False(t, d.Finished)
		assert.Nil(t, d.Results, "no answers before the player's own guesses")
		require.NotNil(t, d.Round)
		assert.Equal(t, 1, d.Round.Position)
		assert.Equal(t, 1, d.Round.Number)
		assert.Equal(t, rounds[0].mediaID, d.Round.ID)
		assert.Equal(t, "photo", d.Round.MediaType)
		assert.True(t, strings.HasPrefix(d.Round.URL, "https://s3.test/media/"), d.Round.URL)
		assert.InDelta(t, maxDistKm, d.Round.MaxDistKm, 1e-6)
		require.NotNil(t, d.Round.Bounds)
		assert.Equal(t, [4]float64{47, 16, 57.5, 26.5}, *d.Round.Bounds)

		require.Len(t, d.Members, 2)
		assert.Equal(t, challengeMember{UserID: f.alice, Name: ptr("alice"), IsOwner: true}, d.Members[0], "the owner first")
		assert.Equal(t, challengeMember{UserID: f.bob, Name: ptr("bob")}, d.Members[1])
	})

	var first guessResult
	t.Run("a guess is scored against the stored answer on the challenge's scale", func(t *testing.T) {
		// The longitude is from the next copy of the world: 379 is 19.
		first = f.mustGuess(t, f.bob, c.ID, 1, 47.2, 379, "2024-05-11")
		want := scoreGuess(rounds[0].answer, 47.2, 19, "2024-05-11", maxDistKm)
		assert.InDelta(t, want.TotalScore, first.TotalScore, 1e-6)
		assert.InDelta(t, want.LocationScore, first.LocationScore, 1e-6)
		assert.InDelta(t, want.DistanceKm, first.DistanceKm, 1e-6)
		require.NotNil(t, first.TimeScore)
		require.NotNil(t, first.DateDiffDays)
		assert.InDelta(t, *want.TimeScore, *first.TimeScore, 1e-6)
		assert.InDelta(t, rounds[0].answer.lat, first.ActualLat, 1e-9)
		assert.InDelta(t, rounds[0].answer.lng, first.ActualLng, 1e-9)
		assert.Equal(t, rounds[0].answer.takenAt, first.ActualDate)

		var lat, lng, total float64
		var date *string
		require.NoError(t, f.pool.QueryRow(ctx, `
			SELECT ST_Y(guess::geometry), ST_X(guess::geometry), guess_date::text, total_score
			FROM challenge_scores WHERE challenge_id = $1 AND position = 1 AND user_id = $2`, c.ID, f.bob).
			Scan(&lat, &lng, &date, &total))
		assert.InDelta(t, 47.2, lat, 1e-9)
		assert.InDelta(t, 19, lng, 1e-9, "stored inside -180..180")
		assert.Equal(t, ptr("2024-05-11"), date)
		assert.InDelta(t, first.TotalScore, total, 1e-9)
	})

	t.Run("a second guess on the round is refused and changes nothing", func(t *testing.T) {
		rec := f.guess(f.bob, c.ID, 1, rounds[0].answer.lat, rounds[0].answer.lng, "")
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "you have already guessed this round", strings.TrimSpace(rec.Body.String()))
		var lat, total float64
		require.NoError(t, f.pool.QueryRow(ctx, `
			SELECT ST_Y(guess::geometry), total_score FROM challenge_scores
			WHERE challenge_id = $1 AND position = 1 AND user_id = $2`, c.ID, f.bob).Scan(&lat, &total))
		assert.InDelta(t, 47.2, lat, 1e-9)
		assert.InDelta(t, first.TotalScore, total, 1e-9)
		assert.Equal(t, 1, f.count(t, "challenge_scores"))
	})

	t.Run("the next round is the lowest one not guessed yet", func(t *testing.T) {
		d := f.get(t, f.bob, c.ID)
		require.NotNil(t, d.Round)
		assert.Equal(t, 2, d.Round.Position)
		assert.Equal(t, rounds[1].mediaID, d.Round.ID)
		assert.Equal(t, 1, d.Played)
		assert.InDelta(t, first.TotalScore, d.Score, 1e-6)
		assert.Nil(t, d.Results)

		f.mustGuess(t, f.bob, c.ID, 4, 50, 20, "") // out of order
		d = f.get(t, f.bob, c.ID)
		require.NotNil(t, d.Round)
		assert.Equal(t, 2, d.Round.Position)
		assert.Equal(t, 2, d.Played)
	})

	t.Run("a finished player sees every round's answer and the guesses made so far", func(t *testing.T) {
		total := first.TotalScore + scoreGuess(rounds[3].answer, 50, 20, "", maxDistKm).TotalScore
		for _, pos := range []int{2, 3, 5} {
			total += f.mustGuess(t, f.bob, c.ID, pos, 48, 17, "").TotalScore
		}
		d := f.get(t, f.bob, c.ID)
		assert.True(t, d.Finished)
		assert.Nil(t, d.Round)
		assert.Equal(t, 5, d.Played)
		assert.InDelta(t, total, d.Score, 1e-6)
		require.Len(t, d.Results, 5)
		for i, res := range d.Results {
			assert.Equal(t, i+1, res.Position)
			assert.Equal(t, i+1, res.Number)
			require.True(t, res.Available)
			assert.Equal(t, &rounds[i].mediaID, res.MediaID)
			assert.Equal(t, ptr("photo"), res.MediaType)
			require.NotNil(t, res.URL)
			assert.True(t, strings.HasPrefix(*res.URL, "https://s3.test/media/"), *res.URL)
			require.NotNil(t, res.ActualLat)
			require.NotNil(t, res.ActualLng)
			assert.InDelta(t, rounds[i].answer.lat, *res.ActualLat, 1e-9)
			assert.InDelta(t, rounds[i].answer.lng, *res.ActualLng, 1e-9)
			assert.Equal(t, rounds[i].answer.takenAt, res.ActualDate)
			require.Len(t, res.Guesses, 1, "Alice has not guessed yet")
			assert.Equal(t, f.bob, res.Guesses[0].UserID)
		}
		g := d.Results[0].Guesses[0]
		assert.InDelta(t, 47.2, g.Lat, 1e-9)
		assert.InDelta(t, 19, g.Lng, 1e-9)
		assert.Equal(t, ptr("2024-05-11"), g.GuessDate)
		assert.InDelta(t, first.DistanceKm, g.DistanceKm, 1e-6)
		assert.InDelta(t, first.TotalScore, g.TotalScore, 1e-6)
		require.NotNil(t, g.TimeScore)
		assert.Nil(t, d.Results[1].Guesses[0].GuessDate)
		assert.Nil(t, d.Results[1].Guesses[0].TimeScore)

		assert.Equal(t, 5, d.Members[1].Played)
		assert.InDelta(t, total, d.Members[1].Score, 1e-6)
	})

	t.Run("a player who has not finished sees the standings but no answer", func(t *testing.T) {
		d := f.get(t, f.alice, c.ID)
		assert.False(t, d.Finished)
		assert.Nil(t, d.Results)
		require.NotNil(t, d.Round)
		assert.Equal(t, 1, d.Round.Position)
		assert.Zero(t, d.Played)
		assert.Equal(t, 5, d.Members[1].Played, "Bob's progress shows")
		assert.Greater(t, d.Members[1].Score, 0.0)

		// One round left is still not finished.
		for _, pos := range []int{1, 2, 3, 4} {
			f.mustGuess(t, f.alice, c.ID, pos, rounds[pos-1].answer.lat, rounds[pos-1].answer.lng, "")
		}
		d = f.get(t, f.alice, c.ID)
		assert.False(t, d.Finished)
		assert.Nil(t, d.Results)
		require.NotNil(t, d.Round)
		assert.Equal(t, 5, d.Round.Position)
	})

	t.Run("each round lists every player's guess, best first", func(t *testing.T) {
		f.mustGuess(t, f.alice, c.ID, 5, rounds[4].answer.lat, rounds[4].answer.lng, "")
		for _, user := range []string{f.alice, f.bob} {
			d := f.get(t, user, c.ID)
			assert.True(t, d.Finished)
			require.Len(t, d.Results, 5)
			for _, res := range d.Results {
				require.Len(t, res.Guesses, 2)
				assert.Equal(t, f.alice, res.Guesses[0].UserID, "Alice guessed the exact place")
				assert.InDelta(t, 5000, res.Guesses[0].TotalScore, 1e-6)
				assert.Equal(t, f.bob, res.Guesses[1].UserID)
			}
			assert.InDelta(t, 25000, d.Members[0].Score, 1e-6)
		}
	})

	t.Run("the list is each player's own standing, newest challenge first", func(t *testing.T) {
		second := f.create(t, f.bob, "", fmt.Sprintf(`{"name":"Bob's","member_ids":[%q]}`, f.alice))
		// created_at is the transaction's start: make the order certain.
		f.exec(t, `UPDATE challenges SET created_at = created_at - INTERVAL '1 hour' WHERE id = $1`, c.ID)

		forBob := f.list(t, f.bob)
		require.Len(t, forBob, 2)
		assert.Equal(t, []string{second.ID, c.ID}, []string{forBob[0].ID, forBob[1].ID})
		assert.True(t, forBob[0].IsOwner)
		assert.False(t, forBob[0].Finished)
		assert.Zero(t, forBob[0].Played)
		assert.True(t, forBob[1].Finished)
		assert.Equal(t, 5, forBob[1].Played)
		assert.Less(t, forBob[1].Score, 25000.0)
		assert.Equal(t, ptr("alice"), forBob[1].OwnerName)

		forAlice := f.list(t, f.alice)
		require.Len(t, forAlice, 2)
		assert.False(t, forAlice[0].IsOwner)
		assert.InDelta(t, 25000, forAlice[1].Score, 1e-6)
		assert.Empty(t, f.list(t, f.carol))
	})
}

// The answer is the item's own when the challenge was made: a later correction by its owner
// changes neither what a guess is scored against nor what the results show.
func TestChallenge_AnswerIsFixedWhenMade(t *testing.T) {
	f := newChallengeFixture(t)
	c := f.create(t, f.alice, "?owner=mine", f.withBob("Fixed"))
	rounds := f.rounds(t, c.ID)

	f.exec(t, `
		UPDATE media SET location = ST_SetSRID(ST_MakePoint(-70, -33), 4326), taken_at = '2019-01-01T00:00:00Z'
		WHERE id = $1`, rounds[0].mediaID)

	res := f.mustGuess(t, f.bob, c.ID, 1, rounds[0].answer.lat, rounds[0].answer.lng, (*rounds[0].answer.takenAt)[:10])
	assert.InDelta(t, 0, res.DistanceKm, 1e-6)
	assert.InDelta(t, 5000, res.TotalScore, 1e-6)
	assert.InDelta(t, rounds[0].answer.lat, res.ActualLat, 1e-9)
	assert.Equal(t, rounds[0].answer.takenAt, res.ActualDate)

	for pos := 2; pos <= 5; pos++ {
		f.mustGuess(t, f.bob, c.ID, pos, 0, 0, "")
	}
	d := f.get(t, f.bob, c.ID)
	require.Len(t, d.Results, 5)
	require.NotNil(t, d.Results[0].ActualLat)
	assert.InDelta(t, rounds[0].answer.lat, *d.Results[0].ActualLat, 1e-9)
	assert.Equal(t, rounds[0].answer.takenAt, d.Results[0].ActualDate)
}

// A challenge is no second way to read media: a round whose item a player can no longer read
// through an album is neither shown nor guessable, and its answer and guesses are withheld.
func TestChallenge_NeverShowsMediaThePlayerCannotRead(t *testing.T) {
	f := newChallengeFixture(t)
	ctx := context.Background()
	c := f.create(t, f.alice, "?owner=mine", f.withBob("Access"))
	rounds := f.rounds(t, c.ID)
	hidden := rounds[0]
	var hiddenKey string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT storage_key FROM media WHERE id = $1`, hidden.mediaID).Scan(&hiddenKey))

	// Alice takes the first round's photo out of the album: Bob no longer reads it.
	f.exec(t, `DELETE FROM album_media WHERE album_id = $1 AND media_id = $2`, f.family, hidden.mediaID)

	t.Run("the round is skipped and cannot be guessed", func(t *testing.T) {
		d := f.get(t, f.bob, c.ID)
		require.NotNil(t, d.Round)
		assert.Equal(t, 2, d.Round.Position, "the first round Bob can read")
		assert.Equal(t, 2, d.Round.Number, "it is still the second of five")
		assert.Equal(t, 5, d.ItemCount)

		rec := f.guess(f.bob, c.ID, 1, hidden.answer.lat, hidden.answer.lng, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Zero(t, f.count(t, "challenge_scores"))
	})

	t.Run("its owner still plays it", func(t *testing.T) {
		d := f.get(t, f.alice, c.ID)
		require.NotNil(t, d.Round)
		assert.Equal(t, 1, d.Round.Position)
		f.mustGuess(t, f.alice, c.ID, 1, hidden.answer.lat, hidden.answer.lng, "")
	})

	t.Run("finished without it, the player gets no answer, guess or key of it", func(t *testing.T) {
		for pos := 2; pos <= 5; pos++ {
			f.mustGuess(t, f.bob, c.ID, pos, 48, 17, "")
		}
		rec := do(t, f.api, f.bob, http.MethodGet, "/api/challenges/"+c.ID, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), hidden.mediaID)
		assert.NotContains(t, rec.Body.String(), hiddenKey)

		var d challengeDetail
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
		assert.True(t, d.Finished, "no round is left that Bob can play")
		assert.Nil(t, d.Round)
		assert.Equal(t, 4, d.Played)
		require.Len(t, d.Results, 5)
		assert.Equal(t, challengeResult{Position: 1, Number: 1, Guesses: []challengeGuess{}}, d.Results[0],
			"Alice's guess on it would give the place away")
		for _, res := range d.Results[1:] {
			assert.True(t, res.Available)
			assert.Len(t, res.Guesses, 1)
		}
		assert.Equal(t, 1, d.Members[0].Played, "the standings still count Alice's guess")

		forBob := f.list(t, f.bob)
		require.Len(t, forBob, 1)
		assert.True(t, forBob[0].Finished)
		assert.Equal(t, 4, forBob[0].Played)
	})

	t.Run("shared again, the round is the player's to guess", func(t *testing.T) {
		insertAlbumMedia(t, f.pool, f.family, hidden.mediaID)
		d := f.get(t, f.bob, c.ID)
		assert.False(t, d.Finished)
		assert.Nil(t, d.Results)
		require.NotNil(t, d.Round)
		assert.Equal(t, 1, d.Round.Position)
		assert.False(t, f.list(t, f.bob)[0].Finished)
	})

	t.Run("out of the album, the player reads no round at all", func(t *testing.T) {
		f.exec(t, `DELETE FROM album_members WHERE album_id = $1 AND user_id = $2`, f.family, f.bob)
		rec := do(t, f.api, f.bob, http.MethodGet, "/api/challenges/"+c.ID, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		for _, r := range rounds {
			assert.NotContains(t, rec.Body.String(), r.mediaID)
		}
		assert.NotContains(t, rec.Body.String(), "s3.test", "no presigned URL")

		var d challengeDetail
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
		assert.True(t, d.Finished)
		assert.Nil(t, d.Round)
		require.Len(t, d.Results, 5)
		for i, res := range d.Results {
			assert.Equal(t, challengeResult{Position: i + 1, Number: i + 1, Guesses: []challengeGuess{}}, res)
		}
		assert.Equal(t, 4, d.Played, "Bob's own score stays")
		assert.Len(t, d.Members, 2)

		rec = f.guess(f.bob, c.ID, 1, 48, 17, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})
}

func TestChallenge_DeletedMediaTakesItsRoundAlong(t *testing.T) {
	f := newChallengeFixture(t)
	c := f.create(t, f.alice, "?owner=mine", f.withBob("Shrinks"))
	rounds := f.rounds(t, c.ID)
	first := f.mustGuess(t, f.bob, c.ID, 1, 48, 17, "")
	f.mustGuess(t, f.bob, c.ID, 2, 48, 17, "")

	f.exec(t, `DELETE FROM media WHERE id = $1`, rounds[1].mediaID)

	d := f.get(t, f.bob, c.ID)
	assert.Equal(t, 4, d.ItemCount)
	assert.Equal(t, 1, d.Played, "the guess on the deleted round is gone")
	assert.InDelta(t, first.TotalScore, d.Score, 1e-6)
	require.NotNil(t, d.Round)
	assert.Equal(t, 3, d.Round.Position, "positions keep their numbers")
	assert.Equal(t, 2, d.Round.Number, "the second of the four rounds left")
	assert.Equal(t, rounds[2].mediaID, d.Round.ID)

	rec := f.guess(f.bob, c.ID, 2, 48, 17, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "no such round any more: %s", rec.Body.String())

	for _, pos := range []int{3, 4, 5} {
		f.mustGuess(t, f.bob, c.ID, pos, 48, 17, "")
	}
	d = f.get(t, f.bob, c.ID)
	assert.True(t, d.Finished)
	require.Len(t, d.Results, 4)
	assert.Equal(t, []int{1, 3, 4, 5}, []int{d.Results[0].Position, d.Results[1].Position, d.Results[2].Position, d.Results[3].Position})
	assert.Equal(t, []int{1, 2, 3, 4}, []int{d.Results[0].Number, d.Results[1].Number, d.Results[2].Number, d.Results[3].Number})

	// Every item gone: an empty challenge is finished for everyone and still opens.
	f.exec(t, `DELETE FROM media WHERE owner_id = $1`, f.alice)
	d = f.get(t, f.alice, c.ID)
	assert.True(t, d.Finished)
	assert.Zero(t, d.ItemCount)
	assert.Empty(t, d.Results)
	assert.NotNil(t, d.Results, "an empty result list is [], which says finished")
	assert.Zero(t, f.count(t, "challenge_scores"))
}

func TestChallenge_DeleteAndLeave(t *testing.T) {
	f := newChallengeFixture(t)
	c := f.create(t, f.alice, "", f.withBob("Ends"))
	other := f.create(t, f.alice, "", f.withBob("Stays"))
	f.mustGuess(t, f.bob, c.ID, 1, 48, 17, "")
	f.mustGuess(t, f.bob, other.ID, 1, 48, 17, "")
	f.mustGuess(t, f.alice, c.ID, 1, 48, 17, "")
	media := f.count(t, "media")

	refusals := []struct {
		name, user, method, path string
		wantStatus               int
		wantBody                 string
	}{
		{"a player cannot delete", f.bob, http.MethodDelete, "/api/challenges/" + c.ID, http.StatusForbidden, "only the challenge owner can do this"},
		{"an outsider deletes nothing", f.carol, http.MethodDelete, "/api/challenges/" + c.ID, http.StatusNotFound, "not found"},
		{"no such challenge to delete", f.alice, http.MethodDelete, "/api/challenges/" + unknownUUID, http.StatusNotFound, "not found"},
		{"a malformed ID to delete", f.alice, http.MethodDelete, "/api/challenges/nope", http.StatusNotFound, "not found"},
		{"the owner cannot leave", f.alice, http.MethodPost, "/api/challenges/" + c.ID + "/leave", http.StatusBadRequest,
			"the challenge owner cannot leave; delete the challenge instead"},
		{"an outsider leaves nothing", f.carol, http.MethodPost, "/api/challenges/" + c.ID + "/leave", http.StatusNotFound, "not found"},
		{"no such challenge to leave", f.bob, http.MethodPost, "/api/challenges/" + unknownUUID + "/leave", http.StatusNotFound, "not found"},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.api, tt.user, tt.method, tt.path, "")
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantBody, strings.TrimSpace(rec.Body.String()))
			assert.Equal(t, 2, f.count(t, "challenges"))
			assert.Equal(t, 4, f.count(t, "challenge_members"))
			assert.Equal(t, 3, f.count(t, "challenge_scores"))
		})
	}

	t.Run("a player leaves, and their guesses go with them", func(t *testing.T) {
		rec := do(t, f.api, f.bob, http.MethodPost, "/api/challenges/"+c.ID+"/leave", "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

		rec = do(t, f.api, f.bob, http.MethodGet, "/api/challenges/"+c.ID, "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
		forBob := f.list(t, f.bob)
		require.Len(t, forBob, 1)
		assert.Equal(t, other.ID, forBob[0].ID, "the other challenge is untouched")
		assert.Equal(t, 1, forBob[0].Played)

		d := f.get(t, f.alice, c.ID)
		require.Len(t, d.Members, 1)
		assert.Equal(t, f.alice, d.Members[0].UserID)
		assert.Equal(t, 1, d.Members[0].Played, "Alice's own guess stays")
		assert.Equal(t, 1, d.MemberCount)
		assert.Equal(t, 2, f.count(t, "challenge_scores"))

		rec = do(t, f.api, f.bob, http.MethodPost, "/api/challenges/"+c.ID+"/leave", "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "already out")
		rec = f.guess(f.bob, c.ID, 2, 48, 17, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "no guess after leaving")
	})

	t.Run("the owner deletes it: rounds and guesses go, the media stay", func(t *testing.T) {
		rec := do(t, f.api, f.alice, http.MethodDelete, "/api/challenges/"+c.ID, "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

		rec = do(t, f.api, f.alice, http.MethodGet, "/api/challenges/"+c.ID, "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, 1, f.count(t, "challenges"))
		assert.Equal(t, 2, f.count(t, "challenge_members"))
		assert.Equal(t, 5, f.count(t, "challenge_items"))
		assert.Equal(t, 1, f.count(t, "challenge_scores"))
		assert.Equal(t, media, f.count(t, "media"))

		rec = do(t, f.api, f.alice, http.MethodDelete, "/api/challenges/"+c.ID, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "already deleted")
	})
}

// A disabled player keeps their place and their score, as a disabled album member keeps theirs.
func TestChallenge_DisabledPlayerStaysInTheStandings(t *testing.T) {
	f := newChallengeFixture(t)
	c := f.create(t, f.alice, "", f.withBob("Keeps"))
	f.mustGuess(t, f.bob, c.ID, 1, 48, 17, "")
	f.exec(t, `UPDATE users SET disabled_at = NOW() WHERE id = $1`, f.bob)

	d := f.get(t, f.alice, c.ID)
	require.Len(t, d.Members, 2)
	assert.Equal(t, f.bob, d.Members[1].UserID)
	assert.Equal(t, 1, d.Members[1].Played)
}
