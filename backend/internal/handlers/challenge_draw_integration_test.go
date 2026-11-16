package handlers

import (
	"fmt"
	"slices"
	"testing"

	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDrawFixture is Alice and Bob with no media yet: Bob is a member of Alice's album, so what a
// test puts into it is the pool of a challenge between them.
func newDrawFixture(t *testing.T) *challengeFixture {
	t.Helper()
	pool := testdb.New(t)
	f := &challengeFixture{pool: pool, store: &fakeObjectStore{}}
	f.api = newChallengeRouter(pool, f.store)
	f.alice = createUser(t, pool, "alice")
	f.bob = createUser(t, pool, "bob")
	f.family = insertAlbum(t, pool, f.alice, "Family")
	insertAlbumMember(t, pool, f.family, f.bob)
	return f
}

// photo adds a located photo of Alice's to the family album.
func (f *challengeFixture) photo(t *testing.T, name string, lat, lng float64, takenAt string) string {
	t.Helper()
	id := insertMedia(t, f.pool, mediaFixture{owner: f.alice, filename: name, takenAt: takenAt, lat: &lat, lng: &lng})
	insertAlbumMedia(t, f.pool, f.family, id)
	return id
}

// drawn makes a challenge and returns the media of its rounds, sorted.
func (f *challengeFixture) drawn(t *testing.T, user, body string) (challengeID string, media []string) {
	t.Helper()
	c := f.create(t, user, "", body)
	for _, r := range f.rounds(t, c.ID) {
		media = append(media, r.mediaID)
	}
	slices.Sort(media)
	return c.ID, media
}

// without returns the items of all that are not in drawn, sorted.
func without(all, drawn []string) []string {
	var rest []string
	for _, id := range all {
		if !slices.Contains(drawn, id) {
			rest = append(rest, id)
		}
	}
	slices.Sort(rest)
	return rest
}

// A place with forty photos gives one round, like a place with one: the rounds come from
// different cells of the grid over the pool, whatever the number of items in them. A plain random
// draw would take four or five of the forty nearly every time.
func TestChallengeDraw_TakesTheRoundsFromDifferentPlaces(t *testing.T) {
	f := newDrawFixture(t)
	var crowd []string
	for i := range 40 {
		crowd = append(crowd, f.photo(t, fmt.Sprintf("crowd%02d.jpg", i),
			10+0.001*float64(i), 10+0.001*float64(i), fmt.Sprintf("2024-05-%02dT12:00:00Z", i%28+1)))
	}
	// Five places of one photo each. With the crowd they span 10..80 both ways, so the grid's cells
	// are 8.75 degrees a side and each of the six places has a cell of its own.
	lone := []string{
		f.photo(t, "south-east.jpg", 10, 80, "2024-06-01T12:00:00Z"),
		f.photo(t, "north-west.jpg", 80, 10, "2024-06-02T12:00:00Z"),
		f.photo(t, "north-east.jpg", 80, 80, "2024-06-03T12:00:00Z"),
		f.photo(t, "middle.jpg", 45, 45, "2024-06-04T12:00:00Z"),
		f.photo(t, "east.jpg", 45, 80, "2024-06-05T12:00:00Z"),
	}

	crowdRounds := 0
	const draws = 12
	for range draws {
		// Deleted again each time, so every draw starts from a pool nobody has played.
		id, media := f.drawn(t, f.alice, f.withBob("Spread"))
		require.Len(t, media, 5)
		fromCrowd := 0
		for _, m := range media {
			if slices.Contains(crowd, m) {
				fromCrowd++
			}
		}
		assert.LessOrEqual(t, fromCrowd, 1, "five of the six places, so the crowded one at most once")
		assert.Len(t, without(lone, media), fromCrowd, "and it takes the place of one of the others")
		crowdRounds += fromCrowd
		f.exec(t, `DELETE FROM challenges WHERE id = $1`, id)
	}
	// Each place is left out of a draw one time in six: the crowded one is neither always in nor
	// never (both would take odds of about one in a billion over twelve draws of a fair choice).
	assert.Greater(t, crowdRounds, 0, "the crowded place takes its turn")
}

// Inside one place the rounds come from different periods: forty photos of one day and one photo
// from each of four later years give one round to that day and one to each of the years.
func TestChallengeDraw_TakesAPlacesItemsFromDifferentPeriods(t *testing.T) {
	f := newDrawFixture(t)
	var oneDay []string
	for i := range 40 {
		oneDay = append(oneDay, f.photo(t, fmt.Sprintf("day%02d.jpg", i), 47, 19, "2020-01-01T12:00:00Z"))
	}
	// The span of four years is cut into eight periods of half a year: each of these is alone in one.
	years := []string{
		f.photo(t, "2021.jpg", 47, 19, "2021-01-01T12:00:00Z"),
		f.photo(t, "2022.jpg", 47, 19, "2022-01-01T12:00:00Z"),
		f.photo(t, "2023.jpg", 47, 19, "2023-01-01T12:00:00Z"),
		f.photo(t, "2024.jpg", 47, 19, "2024-01-01T12:00:00Z"),
	}

	for range 6 {
		id, media := f.drawn(t, f.alice, f.withBob("Years"))
		require.Len(t, media, 5)
		assert.Empty(t, without(years, media), "every later year has its round")
		fromDay := 0
		for _, m := range media {
			if slices.Contains(oneDay, m) {
				fromDay++
			}
		}
		assert.Equal(t, 1, fromDay, "and the day with forty photos has one")
		f.exec(t, `DELETE FROM challenges WHERE id = $1`, id)
	}
}

// An item comes up again only when the pool has nothing the players have had less often. What a
// player has had is read from the challenges they play in: another player's own challenge does not
// count for them, and a deleted challenge is forgotten.
func TestChallengeDraw_PrefersItemsThePlayersHaveNotHad(t *testing.T) {
	f := newDrawFixture(t)
	var all []string
	for i := range 10 {
		all = append(all, f.photo(t, fmt.Sprintf("p%d.jpg", i),
			40+float64(i), 10+2*float64(i), fmt.Sprintf("2024-0%d-10T12:00:00Z", i%9+1)))
	}
	slices.Sort(all)
	alone := `{"name":"Alone"}`

	// Alice plays five items alone. A challenge with Bob then takes the other five: Alice had the
	// first ones.
	_, first := f.drawn(t, f.alice, alone)
	require.Len(t, first, 5)
	_, second := f.drawn(t, f.alice, f.withBob("Together"))
	assert.Equal(t, without(all, first), second, "the five Alice has not had")

	// Bob has only had the second five: his own challenge takes the first five, which are new to
	// him though Alice played them.
	_, bobs := f.drawn(t, f.bob, alone)
	assert.Equal(t, first, bobs, "Alice's own challenge does not count for Bob")

	// Now each of them has had every item once, in different challenges: an item counts as often
	// as the player who has had it most. With nothing fresh left, any five come up, and the
	// challenge after that takes the five that were used once less.
	_, third := f.drawn(t, f.alice, f.withBob("Again"))
	require.Len(t, third, 5)
	_, fourth := f.drawn(t, f.alice, f.withBob("And again"))
	assert.Equal(t, without(all, third), fourth, "the least used come first")

	// Deleted challenges are forgotten: after two new challenges every item is used once again.
	f.exec(t, `DELETE FROM challenges`)
	_, fresh := f.drawn(t, f.alice, f.withBob("Fresh"))
	_, rest := f.drawn(t, f.alice, f.withBob("Rest"))
	assert.Equal(t, without(all, fresh), rest)
}

// A pool at one point, or along one line, or of one instant, has no extent to lay a grid over, and
// an undated item has no period: all of it is still drawn.
func TestChallengeDraw_PoolWithoutAnExtent(t *testing.T) {
	tests := []struct {
		name string
		at   func(i int) (lat, lng float64)
	}{
		{"one point", func(int) (float64, float64) { return 47, 19 }},
		{"one meridian", func(i int) (float64, float64) { return 40 + float64(i), 19 }},
		{"one parallel", func(i int) (float64, float64) { return 47, 10 + float64(i) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDrawFixture(t)
			var all []string
			for i := range 5 {
				lat, lng := tt.at(i)
				all = append(all, f.photo(t, fmt.Sprintf("p%d.jpg", i), lat, lng, "2024-05-01T12:00:00Z"))
			}
			// One of them has no capture time.
			f.exec(t, `UPDATE media SET taken_at = NULL WHERE id = $1`, all[4])
			slices.Sort(all)

			id, media := f.drawn(t, f.alice, f.withBob("Flat"))
			assert.Equal(t, all, media)
			positions := []int{}
			for _, r := range f.rounds(t, id) {
				positions = append(positions, r.position)
			}
			assert.Equal(t, []int{1, 2, 3, 4, 5}, positions)
		})
	}
}
