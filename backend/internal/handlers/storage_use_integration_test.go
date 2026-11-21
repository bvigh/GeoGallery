package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sizedMedia stores an item of ownerID's in tripID (nil: none) with size bytes (nil: unknown, as on
// a row stored before sizes were recorded).
func sizedMedia(t *testing.T, pool *pgxpool.Pool, ownerID, filename string, tripID *string, size *int64) string {
	t.Helper()
	id := insertMedia(t, pool, mediaFixture{owner: ownerID, filename: filename, takenAt: "2024-05-01T12:00:00Z", tripID: tripID})
	_, err := pool.Exec(context.Background(), `UPDATE media SET size_bytes = $2 WHERE id = $1`, id, size)
	require.NoError(t, err)
	return id
}

func getMyStorage(t *testing.T, pool *pgxpool.Pool, userID string) (MyStorageResponse, []byte) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/me/storage", nil)
	r = r.WithContext(auth.WithUser(r.Context(), auth.User{ID: userID, Role: "user"}, auth.MethodToken))
	rec := httptest.NewRecorder()
	NewStorageUseHandler(pool).Mine(rec, r)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp MyStorageResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp, rec.Body.Bytes()
}

func TestMyStorage_Integration(t *testing.T) {
	pool := testdb.New(t)
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")
	carol := createUser(t, pool, "carol")

	lisbon := insertTrip(t, pool, alice, "Lisbon", true, "2024-05-01", "2024-05-03")
	june := insertTrip(t, pool, alice, "Trip 2024-06-01", false, "2024-06-01", "2024-06-01")
	planned := insertTrip(t, pool, alice, "Planned", true, "2025-01-01", "2025-01-02")
	bobTrip := insertTrip(t, pool, bob, "Vienna by bike", true, "2024-07-01", "2024-07-02")

	a1 := sizedMedia(t, pool, alice, "a1.jpg", &lisbon, ptr[int64](1000))
	sizedMedia(t, pool, alice, "a2.mp4", &lisbon, ptr[int64](3000))
	a3 := sizedMedia(t, pool, alice, "a3.jpg", &june, nil)
	sizedMedia(t, pool, alice, "a4.jpg", nil, ptr[int64](500))
	sizedMedia(t, pool, alice, "a5.jpg", nil, nil)
	b1 := sizedMedia(t, pool, bob, "b1.mp4", nil, ptr[int64](10_000))
	b2 := sizedMedia(t, pool, bob, "b2.mp4", &bobTrip, ptr[int64](20_000))

	// Alice's album holds two of her items and one of bob's; bob's album one of hers. Carol's album
	// shares bob's other item with alice, who put nothing in it.
	family := insertAlbum(t, pool, alice, "Family")
	insertAlbumMember(t, pool, family, bob)
	insertAlbumMedia(t, pool, family, a1)
	insertAlbumMedia(t, pool, family, a3)
	insertAlbumMedia(t, pool, family, b1)
	hikes := insertAlbum(t, pool, bob, "Hikes")
	insertAlbumMember(t, pool, hikes, alice)
	insertAlbumMedia(t, pool, hikes, a1)
	onlyBob := insertAlbum(t, pool, carol, "Carol's")
	insertAlbumMember(t, pool, onlyBob, alice)
	insertAlbumMember(t, pool, onlyBob, bob)
	insertAlbumMedia(t, pool, onlyBob, b2)
	empty := insertAlbum(t, pool, alice, "Empty")

	t.Run("alice's own items in all, per trip and in none", func(t *testing.T) {
		resp, _ := getMyStorage(t, pool, alice)
		assert.Equal(t, StorageUse{Items: 5, Bytes: 4500, UnknownItems: 2}, resp.StorageUse,
			"her items only: bob's in her album are his")
		assert.Equal(t, StorageUse{Items: 2, Bytes: 500, UnknownItems: 1}, resp.NoTrip)

		require.Len(t, resp.Trips, 3)
		assert.Equal(t, storageTrip{ID: lisbon, Name: "Lisbon", StartDate: ptr("2024-05-01"), EndDate: ptr("2024-05-03"),
			UserEdited: true, StorageUse: StorageUse{Items: 2, Bytes: 4000}}, resp.Trips[0])
		assert.Equal(t, june, resp.Trips[1].ID)
		assert.Equal(t, StorageUse{Items: 1, UnknownItems: 1}, resp.Trips[1].StorageUse, "an item whose size is unknown")
		assert.False(t, resp.Trips[1].UserEdited)
		assert.Equal(t, planned, resp.Trips[2].ID)
		assert.Equal(t, StorageUse{}, resp.Trips[2].StorageUse, "an empty named trip")

		var sum StorageUse
		for _, trip := range resp.Trips {
			sum.add(trip.StorageUse)
		}
		sum.add(resp.NoTrip)
		assert.Equal(t, resp.StorageUse, sum, "the trips and the items in none add up to her items")
	})

	t.Run("alice's albums: only those holding her items, counting only hers", func(t *testing.T) {
		resp, raw := getMyStorage(t, pool, alice)
		assert.Equal(t, []storageAlbum{
			{ID: family, Name: "Family", OwnerID: alice, OwnerName: ptr("alice"), IsOwner: true,
				StorageUse: StorageUse{Items: 2, Bytes: 1000, UnknownItems: 1}},
			{ID: hikes, Name: "Hikes", OwnerID: bob, OwnerName: ptr("bob"), IsOwner: false,
				StorageUse: StorageUse{Items: 1, Bytes: 1000}},
		}, resp.Albums, "largest first, then by items; an item in two albums counts in each")
		for _, absent := range []string{onlyBob, empty, "Vienna by bike", bobTrip, b1, b2} {
			assert.NotContains(t, string(raw), absent, "an album without her items, an empty one, or anything of bob's")
		}
	})

	t.Run("bob's own items and his share of alice's album", func(t *testing.T) {
		resp, raw := getMyStorage(t, pool, bob)
		assert.Equal(t, StorageUse{Items: 2, Bytes: 30_000}, resp.StorageUse)
		assert.Equal(t, StorageUse{Items: 1, Bytes: 10_000}, resp.NoTrip)
		require.Len(t, resp.Trips, 1)
		assert.Equal(t, bobTrip, resp.Trips[0].ID)
		assert.Equal(t, []storageAlbum{
			{ID: onlyBob, Name: "Carol's", OwnerID: carol, OwnerName: ptr("carol"), IsOwner: false,
				StorageUse: StorageUse{Items: 1, Bytes: 20_000}},
			{ID: family, Name: "Family", OwnerID: alice, OwnerName: ptr("alice"), IsOwner: false,
				StorageUse: StorageUse{Items: 1, Bytes: 10_000}},
		}, resp.Albums)
		for _, absent := range []string{"Lisbon", lisbon, june, planned, hikes, a1, a3} {
			assert.NotContains(t, string(raw), absent, "nothing of alice's but the album he is in")
		}
	})

	t.Run("an empty library, with lists never null", func(t *testing.T) {
		resp, raw := getMyStorage(t, pool, carol)
		assert.Equal(t, MyStorageResponse{Trips: []storageTrip{}, Albums: []storageAlbum{}}, resp)
		assert.ElementsMatch(t, []string{"items", "bytes", "unknown_items", "trips", "no_trip", "albums"}, jsonKeys(t, raw))
		var body map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &body))
		assert.JSONEq(t, `[]`, string(body["trips"]))
		assert.JSONEq(t, `[]`, string(body["albums"]))
	})
}
