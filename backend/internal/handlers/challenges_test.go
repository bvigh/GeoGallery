package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func challengeTestIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%08x-0000-4000-8000-000000000000", i+1)
	}
	return ids
}

func TestValidateChallenge(t *testing.T) {
	const (
		upper = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		lower = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		other = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	ids := func(v ...string) *[]string { return &v }
	nineteen, twenty := challengeTestIDs(19), challengeTestIDs(20)
	tests := []struct {
		name    string
		in      challengeInput
		want    newChallenge
		wantErr string
	}{
		{"name alone: the owner plays alone", challengeInput{Name: ptr("Iceland")}, newChallenge{name: "Iceland", others: []string{}}, ""},
		{"name trimmed", challengeInput{Name: ptr("  Iceland \n")}, newChallenge{name: "Iceland", others: []string{}}, ""},
		{"empty member list", challengeInput{Name: ptr("x"), MemberIDs: ids()}, newChallenge{name: "x", others: []string{}}, ""},
		{"members canonicalised, in order", challengeInput{Name: ptr("x"), MemberIDs: ids(other, upper)},
			newChallenge{name: "x", others: []string{other, lower}}, ""},
		{"a member named twice, in any spelling, plays once", challengeInput{Name: ptr("x"), MemberIDs: ids(upper, lower, other, upper)},
			newChallenge{name: "x", others: []string{lower, other}}, ""},
		{"the owner's own ID is dropped", challengeInput{Name: ptr("x"), MemberIDs: ids(strings.ToUpper(testOwnerID), other)},
			newChallenge{name: "x", others: []string{other}}, ""},
		{"19 others with the owner are 20 players", challengeInput{Name: ptr("x"), MemberIDs: &nineteen},
			newChallenge{name: "x", others: nineteen}, ""},

		{"no name", challengeInput{MemberIDs: ids(other)}, newChallenge{}, "name is required"},
		{"empty name", challengeInput{Name: ptr(" ")}, newChallenge{}, "name must be 1-200 characters"},
		{"name too long", challengeInput{Name: ptr(strings.Repeat("x", 201))}, newChallenge{}, "name must be 1-200 characters"},
		{"malformed member", challengeInput{Name: ptr("x"), MemberIDs: ids(other, "nope")}, newChallenge{}, "invalid member_ids"},
		{"empty member", challengeInput{Name: ptr("x"), MemberIDs: ids("")}, newChallenge{}, "invalid member_ids"},
		{"20 others are 21 players", challengeInput{Name: ptr("x"), MemberIDs: &twenty}, newChallenge{}, "a challenge has at most 20 players"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateChallenge(tt.in, testOwnerID)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The pool is the Game's own filtered set, located, with one read-access condition per other
// player: readableMediaCond, bound to that player's ID.
func TestChallengePoolConds(t *testing.T) {
	const bob, carol = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

	t.Run("no other player: the owner's located media", func(t *testing.T) {
		conds, args := challengePoolConds(url.Values{}, testOwnerID, nil)
		assert.Equal(t, []string{readableMediaCond("m", 1), "m.location IS NOT NULL"}, conds)
		assert.Equal(t, []any{testOwnerID}, args)
	})

	t.Run("each other player must read the item too", func(t *testing.T) {
		conds, args := challengePoolConds(url.Values{}, testOwnerID, []string{bob, carol})
		assert.Equal(t, []string{
			readableMediaCond("m", 1), "m.location IS NOT NULL",
			readableMediaCond("m", 2), readableMediaCond("m", 3),
		}, conds)
		assert.Equal(t, []any{testOwnerID, bob, carol}, args)
	})

	t.Run("the players come after the filters' own arguments", func(t *testing.T) {
		q := url.Values{"from_date": {"2024-05-01"}, "owner": {"mine"}, "media_type": {"photo"}}
		filterConds, filterArgs := buildMediaFilter(q, testOwnerID)
		conds, args := challengePoolConds(q, testOwnerID, []string{bob})

		want := append(append([]string{}, filterConds...), "m.location IS NOT NULL", readableMediaCond("m", len(filterArgs)+1))
		assert.Equal(t, want, conds)
		assert.Equal(t, append(append([]any{}, filterArgs...), bob), args)
		assert.Contains(t, conds, "m.owner_id = $1", "the owner filter is the Game's")
	})
}

func TestTooFewItemsMessage(t *testing.T) {
	const tail = " the filters and can be seen by every player. A challenge needs 5."
	assert.Equal(t, "No located item matches"+tail, tooFewItemsMessage(0))
	assert.Equal(t, "Only 1 located item matches"+tail, tooFewItemsMessage(1))
	assert.Equal(t, "Only 4 located items match"+tail, tooFewItemsMessage(4))
}

func TestValidateChallengeGuess(t *testing.T) {
	in := func(position int, lat, lng float64, date string) challengeGuessInput {
		return challengeGuessInput{Position: &position, GuessLat: &lat, GuessLng: &lng, GuessDate: date}
	}
	tests := []struct {
		name    string
		in      challengeGuessInput
		want    challengeGuessValue
		wantErr string
	}{
		{"no date", in(1, 47.5, 19.04, ""), challengeGuessValue{position: 1, lat: 47.5, lng: 19.04}, ""},
		{"with a date", in(5, -33.9, 151.2, "2024-02-29"), challengeGuessValue{position: 5, lat: -33.9, lng: 151.2, date: ptr("2024-02-29")}, ""},
		{"the poles and the antimeridian", in(2, 90, -180, ""), challengeGuessValue{position: 2, lat: 90, lng: -180}, ""},
		{"a longitude from the next copy of the world", in(2, 10, 379.5, ""), challengeGuessValue{position: 2, lat: 10, lng: 19.5}, ""},
		{"a longitude from the copy before", in(2, 10, -200, ""), challengeGuessValue{position: 2, lat: 10, lng: 160}, ""},
		{"year 1", in(1, 0, 0, "0001-01-01"), challengeGuessValue{position: 1, date: ptr("0001-01-01")}, ""},

		{"no position", challengeGuessInput{GuessLat: ptr(1.0), GuessLng: ptr(2.0)}, challengeGuessValue{}, "position must be 1-5"},
		{"position 0", in(0, 1, 2, ""), challengeGuessValue{}, "position must be 1-5"},
		{"position 6", in(6, 1, 2, ""), challengeGuessValue{}, "position must be 1-5"},
		{"no latitude", challengeGuessInput{Position: ptr(1), GuessLng: ptr(2.0)}, challengeGuessValue{}, "guess_lat and guess_lng are required"},
		{"no longitude", challengeGuessInput{Position: ptr(1), GuessLat: ptr(2.0)}, challengeGuessValue{}, "guess_lat and guess_lng are required"},
		{"latitude past the pole", in(1, 90.01, 2, ""), challengeGuessValue{}, "guess_lat must be between -90 and 90"},
		{"latitude not a number", in(1, math.NaN(), 2, ""), challengeGuessValue{}, "guess_lat must be between -90 and 90"},
		{"longitude not a number", in(1, 1, math.NaN(), ""), challengeGuessValue{}, "guess_lng must be a number"},
		{"longitude infinite", in(1, 1, math.Inf(-1), ""), challengeGuessValue{}, "guess_lng must be a number"},
		{"date in another layout", in(1, 1, 2, "29-02-2024"), challengeGuessValue{}, "guess_date must be YYYY-MM-DD, or left out"},
		{"a day that does not exist", in(1, 1, 2, "2023-02-29"), challengeGuessValue{}, "guess_date must be YYYY-MM-DD, or left out"},
		{"a date with a time", in(1, 1, 2, "2024-02-29T10:00:00Z"), challengeGuessValue{}, "guess_date must be YYYY-MM-DD, or left out"},
		{"year 0, which Postgres has not", in(1, 1, 2, "0000-01-01"), challengeGuessValue{}, "guess_date must be YYYY-MM-DD, or left out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateChallengeGuess(tt.in)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want.position, got.position)
			assert.InDelta(t, tt.want.lat, got.lat, 1e-9)
			assert.InDelta(t, tt.want.lng, got.lng, 1e-9)
			assert.Equal(t, tt.want.date, got.date)
		})
	}
}

func TestWrapLongitude(t *testing.T) {
	tests := []struct{ in, want float64 }{
		{0, 0}, {19.04, 19.04}, {180, 180}, {-180, -180},
		{180.5, -179.5}, {-180.5, 179.5}, {360, 0}, {540, -180}, {-540, -180}, {725, 5}, {-725, -5},
	}
	for _, tt := range tests {
		got := wrapLongitude(tt.in)
		assert.InDelta(t, tt.want, got, 1e-9, "wrapLongitude(%v)", tt.in)
		assert.True(t, got >= -180 && got <= 180, "wrapLongitude(%v) = %v is in range", tt.in, got)
	}
	huge := wrapLongitude(1e300)
	assert.True(t, huge >= -180 && huge <= 180, "a huge longitude still lands in range, got %v", huge)
}

// challengeRequest is a request to a challenge route as the test user, with {id} set.
func challengeRequest(method, target, id, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return withTestUser(r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
}

// The handler has no database here: reaching it would panic, so each status comes from a check
// made before any query.
func TestChallengeHandler_RefusesBeforeTheDatabase(t *testing.T) {
	const validID = "22222222-2222-4222-8222-222222222222"
	h := &ChallengeHandler{}
	big := `{"name":"` + strings.Repeat("x", maxChallengeBodyBytes) + `"}`
	tests := []struct {
		name       string
		call       func(http.ResponseWriter, *http.Request)
		req        *http.Request
		wantStatus int
		wantBody   string
	}{
		{"create: a malformed filter", h.Create, challengeRequest(http.MethodPost, "/api/challenges?album_id=nope", "", `{"name":"x"}`),
			http.StatusBadRequest, "invalid album_id"},
		{"create: a malformed date filter", h.Create, challengeRequest(http.MethodPost, "/api/challenges?from_date=1-2-3", "", `{"name":"x"}`),
			http.StatusBadRequest, "invalid from_date: want YYYY-MM-DD"},
		{"create: no body", h.Create, challengeRequest(http.MethodPost, "/api/challenges", "", ``),
			http.StatusBadRequest, "invalid body: want a JSON object with name and member_ids"},
		{"create: an unknown key", h.Create, challengeRequest(http.MethodPost, "/api/challenges", "", `{"name":"x","items":9}`),
			http.StatusBadRequest, "invalid body: want a JSON object with name and member_ids"},
		{"create: data after the object", h.Create, challengeRequest(http.MethodPost, "/api/challenges", "", `{"name":"x"} {}`),
			http.StatusBadRequest, "invalid body: want a JSON object with name and member_ids"},
		{"create: members not a list", h.Create, challengeRequest(http.MethodPost, "/api/challenges", "", `{"name":"x","member_ids":"all"}`),
			http.StatusBadRequest, "invalid body: want a JSON object with name and member_ids"},
		{"create: no name", h.Create, challengeRequest(http.MethodPost, "/api/challenges", "", `{"member_ids":[]}`),
			http.StatusBadRequest, "name is required"},
		{"create: a malformed member", h.Create, challengeRequest(http.MethodPost, "/api/challenges", "", `{"name":"x","member_ids":["nope"]}`),
			http.StatusBadRequest, "invalid member_ids"},
		{"create: a body over 16 KiB", h.Create, challengeRequest(http.MethodPost, "/api/challenges", "", big),
			http.StatusRequestEntityTooLarge, "request body too large"},

		{"get: a malformed ID", h.Get, challengeRequest(http.MethodGet, "/api/challenges/nope", "nope", ``),
			http.StatusNotFound, "not found"},
		{"delete: a malformed ID", h.Delete, challengeRequest(http.MethodDelete, "/api/challenges/nope", "nope", ``),
			http.StatusNotFound, "not found"},
		{"leave: a malformed ID", h.Leave, challengeRequest(http.MethodPost, "/api/challenges/nope/leave", "nope", ``),
			http.StatusNotFound, "not found"},

		{"guess: a malformed ID, the body unread", h.Guess, challengeRequest(http.MethodPost, "/api/challenges/nope/guess", "nope", `not json`),
			http.StatusNotFound, "not found"},
		{"guess: no body", h.Guess, challengeRequest(http.MethodPost, "/api/challenges/"+validID+"/guess", validID, ``),
			http.StatusBadRequest, "invalid body: want a JSON object with position, guess_lat, guess_lng and guess_date"},
		{"guess: an unknown key", h.Guess, challengeRequest(http.MethodPost, "/api/challenges/"+validID+"/guess", validID,
			`{"position":1,"guess_lat":1,"guess_lng":2,"max_dist_km":1}`),
			http.StatusBadRequest, "invalid body: want a JSON object with position, guess_lat, guess_lng and guess_date"},
		{"guess: a position that is not a whole number", h.Guess, challengeRequest(http.MethodPost, "/api/challenges/"+validID+"/guess", validID,
			`{"position":1.5,"guess_lat":1,"guess_lng":2}`),
			http.StatusBadRequest, "invalid body: want a JSON object with position, guess_lat, guess_lng and guess_date"},
		{"guess: a position out of range", h.Guess, challengeRequest(http.MethodPost, "/api/challenges/"+validID+"/guess", validID,
			`{"position":6,"guess_lat":1,"guess_lng":2}`),
			http.StatusBadRequest, "position must be 1-5"},
		{"guess: no coordinates", h.Guess, challengeRequest(http.MethodPost, "/api/challenges/"+validID+"/guess", validID, `{"position":1}`),
			http.StatusBadRequest, "guess_lat and guess_lng are required"},
		{"guess: a date that cannot be read", h.Guess, challengeRequest(http.MethodPost, "/api/challenges/"+validID+"/guess", validID,
			`{"position":1,"guess_lat":1,"guess_lng":2,"guess_date":"yesterday"}`),
			http.StatusBadRequest, "guess_date must be YYYY-MM-DD, or left out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.call(rec, tt.req)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantBody, strings.TrimSpace(rec.Body.String()))
		})
	}
}
