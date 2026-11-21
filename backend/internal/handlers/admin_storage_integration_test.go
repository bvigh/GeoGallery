package handlers

import (
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

func getAdminStorage(t *testing.T, pool *pgxpool.Pool) (AdminStorageResponse, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	NewAdminStorageHandler(pool).Get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/storage", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp AdminStorageResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp, rec.Body.Bytes()
}

func storageUserByID(t *testing.T, resp AdminStorageResponse, id string) adminStorageUser {
	t.Helper()
	for _, u := range resp.Users {
		if u.ID == id {
			return u
		}
	}
	require.Failf(t, "user not in the summary", "%s", id)
	return adminStorageUser{}
}

func TestAdminStorage_Integration(t *testing.T) {
	pool := testdb.New(t)
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")
	carol := createUser(t, pool, "carol")
	disableUser(t, pool, carol)

	lisbon := insertTrip(t, pool, alice, "Lisbon", true, "2024-05-01", "2024-05-03")
	bobTrip := insertTrip(t, pool, bob, "Vienna by bike", true, "2024-07-01", "2024-07-02")
	a1 := sizedMedia(t, pool, alice, "a1.jpg", &lisbon, ptr[int64](1000))
	sizedMedia(t, pool, alice, "a2.mp4", &lisbon, ptr[int64](3000))
	sizedMedia(t, pool, alice, "a3.jpg", nil, nil)
	b1 := sizedMedia(t, pool, bob, "b1.mp4", nil, ptr[int64](10_000))
	sizedMedia(t, pool, bob, "b2.mp4", &bobTrip, ptr[int64](20_000))
	sizedMedia(t, pool, carol, "c1.jpg", nil, ptr[int64](0))

	for _, at := range []string{"2024-05-01T11:00:00Z", "2024-05-01T11:01:00Z", "2024-05-01T11:02:00Z"} {
		insertTrack(t, pool, alice, "gpx:walk.gpx", at, budapestLat, budapestLng)
	}
	insertTrack(t, pool, bob, "gpx:ride.gpx", "2024-07-01T10:00:00Z", viennaLat, viennaLng)

	family := insertAlbum(t, pool, alice, "Family secrets")
	insertAlbumMember(t, pool, family, bob)
	insertAlbumMedia(t, pool, family, a1)
	insertAlbumMedia(t, pool, family, b1)

	resp, raw := getAdminStorage(t, pool)

	t.Run("totals: every user's media and points", func(t *testing.T) {
		assert.Equal(t, StorageUse{Items: 6, Bytes: 34_000, UnknownItems: 1}, resp.StorageUse)
		assert.Equal(t, int64(4), resp.TrackPoints)
	})

	t.Run("users, largest first, the unclaimed owner of pre-auth data too", func(t *testing.T) {
		var order []string
		for _, u := range resp.Users {
			order = append(order, u.ID)
		}
		assert.Equal(t, []string{bob, alice, carol, auth.LegacyOwnerID}, order,
			"by bytes, then by items: carol's empty file still counts as an item")

		a := storageUserByID(t, resp, alice)
		assert.Equal(t, StorageUse{Items: 3, Bytes: 4000, UnknownItems: 1}, a.StorageUse,
			"her own items, once each, though one is in an album")
		assert.Equal(t, int64(3), a.TrackPoints)
		assert.Equal(t, "alice", *a.DisplayName)
		assert.Equal(t, "alice@example.test", *a.Email)
		assert.True(t, a.Claimed)
		assert.False(t, a.Disabled)

		b := storageUserByID(t, resp, bob)
		assert.Equal(t, StorageUse{Items: 2, Bytes: 30_000}, b.StorageUse, "his item in alice's album is his")
		assert.Equal(t, int64(1), b.TrackPoints)

		c := storageUserByID(t, resp, carol)
		assert.Equal(t, StorageUse{Items: 1}, c.StorageUse, "a size of 0 bytes is known")
		assert.True(t, c.Disabled)
		assert.Zero(t, c.TrackPoints)

		owner := storageUserByID(t, resp, auth.LegacyOwnerID)
		assert.False(t, owner.Claimed)
		assert.Equal(t, StorageUse{}, owner.StorageUse)
	})

	t.Run("nothing inside a library reaches the admin", func(t *testing.T) {
		for _, private := range []string{"Lisbon", "Vienna by bike", lisbon, bobTrip, "Family secrets", family,
			"a1.jpg", "b1.mp4", a1, "media/", "gpx:"} {
			assert.NotContains(t, string(raw), private)
		}
	})

	t.Run("the wire format", func(t *testing.T) {
		assert.ElementsMatch(t, []string{"items", "bytes", "unknown_items", "track_points", "users"}, jsonKeys(t, raw))
		var body struct {
			Users []json.RawMessage `json:"users"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		assert.ElementsMatch(t, []string{"id", "display_name", "email", "disabled", "claimed",
			"items", "bytes", "unknown_items", "track_points"}, jsonKeys(t, body.Users[0]))
	})
}
