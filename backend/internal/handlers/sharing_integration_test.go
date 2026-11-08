package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertAlbum creates an album owned by ownerID together with the owner's member row, as the
// albums API does in one transaction.
func insertAlbum(t *testing.T, pool *pgxpool.Pool, ownerID, name string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO albums (owner_id, name) VALUES ($1, $2) RETURNING id::text`, ownerID, name).Scan(&id))
	insertAlbumMember(t, pool, id, ownerID)
	return id
}

func insertAlbumMember(t *testing.T, pool *pgxpool.Pool, albumID, userID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO album_members (album_id, user_id) VALUES ($1, $2)`, albumID, userID)
	require.NoError(t, err)
}

// insertAlbumMedia adds mediaID to albumID as a contribution of the media's owner.
func insertAlbumMedia(t *testing.T, pool *pgxpool.Pool, albumID, mediaID string) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), `
		INSERT INTO album_media (album_id, media_id, owner_id)
		SELECT $1, id, owner_id FROM media WHERE id = $2`, albumID, mediaID)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected(), "media %s exists", mediaID)
}

// sharingFixture extends ownershipFixture: A shares both of her media with B through the family
// album, and keeps mediaA in a private album too. Carol is a member of no album.
type sharingFixture struct {
	*ownershipFixture
	carol   string
	family  string // owner A, member B: mediaA (in A's trip) and mediaAUnlocated (unassigned)
	private string // owner A, no other members: mediaA
}

func newSharingFixture(t *testing.T) *sharingFixture {
	t.Helper()
	f := &sharingFixture{ownershipFixture: newOwnershipFixture(t)}
	f.carol = createUser(t, f.pool, "carol")

	f.family = insertAlbum(t, f.pool, f.userA, "Family")
	insertAlbumMember(t, f.pool, f.family, f.userB)
	insertAlbumMedia(t, f.pool, f.family, f.mediaA)
	insertAlbumMedia(t, f.pool, f.family, f.mediaAUnlocated)

	f.private = insertAlbum(t, f.pool, f.userA, "A only")
	insertAlbumMedia(t, f.pool, f.private, f.mediaA)
	return f
}

func TestSharing_ReadAccessFollowsMembership(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	// B contributes too, so removing B must take mediaB out of A's view.
	insertAlbumMedia(t, f.pool, f.family, f.mediaB)

	const keyframes = `[{"time":0,"yaw":10,"pitch":0,"fov":90},{"time":5,"yaw":20,"pitch":0,"fov":90}]`
	guess := `{"media_id":"` + f.mediaA + `","guess_lat":47.5,"guess_lng":19.0,"guess_date":"2024-05-02","max_dist_km":100}`

	rec := do(t, f.router, f.userA, http.MethodGet, "/api/media", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []string{f.mediaB, f.mediaAUnlocated, f.mediaA}, decodeIDs(t, rec), "A reads B's contribution")

	// Run in order: "former member" deletes B's member row, as leaving or removal does.
	tests := []struct {
		name     string
		user     string
		setup    func(t *testing.T)
		wantList []string // ordered by taken_at DESC
		canRead  bool     // whether user may read A's shared mediaA
	}{
		{"member", f.userB, nil, []string{f.mediaB, f.mediaAUnlocated, f.mediaA}, true},
		{"non-member", f.carol, nil, []string{}, false},
		{"former member", f.userB, func(t *testing.T) {
			_, err := f.pool.Exec(ctx, `DELETE FROM album_members WHERE album_id = $1 AND user_id = $2`, f.family, f.userB)
			require.NoError(t, err)
		}, []string{f.mediaB}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}

			rec := do(t, f.router, tt.user, http.MethodGet, "/api/media", "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantList, decodeIDs(t, rec), "media list")

			url := do(t, f.router, tt.user, http.MethodGet, "/api/media/"+f.mediaA+"/url", "")
			kf := do(t, f.router, tt.user, http.MethodGet, "/api/media/"+f.mediaA+"/keyframes", "")
			g := do(t, f.router, tt.user, http.MethodPost, "/api/game/guess", guess)
			if !tt.canRead {
				assert.Equal(t, http.StatusNotFound, url.Code, "url: %s", url.Body.String())
				require.Equal(t, http.StatusOK, kf.Code, kf.Body.String())
				assert.JSONEq(t, "[]", kf.Body.String(), "keyframes")
				assert.Equal(t, http.StatusNotFound, g.Code, "guess: %s", g.Body.String())
				round := do(t, f.router, tt.user, http.MethodGet, "/api/game/round?owner=shared", "")
				assert.Equal(t, http.StatusNotFound, round.Code, "round: %s", round.Body.String())
				return
			}

			require.Equal(t, http.StatusOK, url.Code, url.Body.String())
			assert.Contains(t, url.Body.String(), "media/"+f.userA+"/a1.jpg")
			require.Equal(t, http.StatusOK, kf.Code, kf.Body.String())
			assert.JSONEq(t, keyframes, kf.Body.String(), "keyframes")
			require.Equal(t, http.StatusOK, g.Code, g.Body.String())
			var result guessResult
			require.NoError(t, json.Unmarshal(g.Body.Bytes(), &result))
			assert.InDelta(t, budapestLat, result.ActualLat, 1e-6)
			for range 5 {
				round := do(t, f.router, tt.user, http.MethodGet, "/api/game/round?owner=shared", "")
				require.Equal(t, http.StatusOK, round.Code, round.Body.String())
				var m roundMedia
				require.NoError(t, json.Unmarshal(round.Body.Bytes(), &m))
				assert.Equal(t, f.mediaA, m.ID, "mediaA is the only located shared item")
			}
		})
	}

	t.Run("the former member's contributions left with them", func(t *testing.T) {
		rec := do(t, f.router, f.userA, http.MethodGet, "/api/media", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{f.mediaAUnlocated, f.mediaA}, decodeIDs(t, rec))
	})
}

func TestSharing_MemberCannotWriteSharedMedia(t *testing.T) {
	f := newSharingFixture(t)
	before := snapshotMedia(t, f.pool, f.mediaA)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"update location", http.MethodPatch, "/api/media/" + f.mediaA + "/location", `{"lat":1,"lng":2}`},
		{"update view settings", http.MethodPatch, "/api/media/" + f.mediaA + "/view-settings", `{"initial_yaw":45}`},
		{"replace keyframes", http.MethodPut, "/api/media/" + f.mediaA + "/keyframes", `[{"time":1,"yaw":0,"pitch":0,"fov":90}]`},
		{"delete media", http.MethodDelete, "/api/media/" + f.mediaA, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.router, f.userB, tt.method, tt.path, tt.body)
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			assert.Equal(t, before, snapshotMedia(t, f.pool, f.mediaA))
		})
	}

	t.Run("nothing changed and B can still read it", func(t *testing.T) {
		assert.Equal(t, 2, before.keyframes)
		assert.Empty(t, f.store.deleted)
		var manual int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM gps_tracks WHERE source = 'manual'`).Scan(&manual))
		assert.Zero(t, manual, "the location write left no manual track point for anyone")
		rec := do(t, f.router, f.userB, http.MethodGet, "/api/media/"+f.mediaA+"/url", "")
		assert.Equal(t, http.StatusOK, rec.Code, "the 404s come from the owner check, not from lost read access")
	})

	t.Run("the owner's delete removes it for every member", func(t *testing.T) {
		rec := do(t, f.router, f.userA, http.MethodDelete, "/api/media/"+f.mediaA, "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

		rec = do(t, f.router, f.userB, http.MethodGet, "/api/media", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{f.mediaB, f.mediaAUnlocated}, decodeIDs(t, rec))
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM album_media WHERE media_id = $1`, f.mediaA).Scan(&n))
		assert.Zero(t, n, "contributions cascade with the media")
	})
}

// Every collection and owner filter against real albums, through both handlers that use the builder.
func TestSharing_Filters(t *testing.T) {
	f := newSharingFixture(t)
	budapestViennaKm := geo.HaversineKm(budapestLat, budapestLng, viennaLat, viennaLng)
	const unknownAlbum = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"

	tests := []struct {
		name      string
		user      string
		query     string
		wantList  []string // ordered by taken_at DESC
		wantRound []string // the located subset a round may pick; empty means 404
		wantMaxKm float64
	}{
		{"everything readable", f.userB, "",
			[]string{f.mediaB, f.mediaAUnlocated, f.mediaA}, []string{f.mediaB, f.mediaA}, budapestViennaKm},
		{"owner=mine", f.userB, "owner=mine",
			[]string{f.mediaB}, []string{f.mediaB}, 1},
		{"owner=shared", f.userB, "owner=shared",
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}, 1},
		{"owner=mine as the sharer", f.userA, "owner=mine",
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}, 1},
		{"owner=shared with nothing shared", f.userA, "owner=shared",
			[]string{}, nil, 0},
		{"unassigned excludes shared media", f.userB, "trip_id=unassigned",
			[]string{}, nil, 0},
		{"unassigned as the sharer", f.userA, "trip_id=unassigned",
			[]string{f.mediaAUnlocated}, nil, 0},
		{"another owner's trip excludes its shared media", f.userB, "trip_id=" + f.tripA,
			[]string{}, nil, 0},
		{"own trip excludes shared media", f.userB, "trip_id=" + f.tripB,
			[]string{f.mediaB}, []string{f.mediaB}, 1},
		{"album", f.userB, "album_id=" + f.family,
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}, 1},
		{"album as its owner", f.userA, "album_id=" + f.family,
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}, 1},
		{"trip or album", f.userB, "trip_id=" + f.tripB + "&album_id=" + f.family,
			[]string{f.mediaB, f.mediaAUnlocated, f.mediaA}, []string{f.mediaB, f.mediaA}, budapestViennaKm},
		{"unassigned or album", f.userA, "trip_id=unassigned&album_id=" + f.private,
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}, 1},
		{"owner narrows the union", f.userB, "trip_id=" + f.tripB + "&album_id=" + f.family + "&owner=shared",
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}, 1},
		{"dates narrow the album", f.userB, "album_id=" + f.family + "&from_date=2024-05-03",
			[]string{f.mediaAUnlocated}, nil, 0},
		{"foreign album matches nothing though its media are readable", f.userB, "album_id=" + f.private,
			[]string{}, nil, 0},
		{"foreign album adds nothing to a member album", f.userB, "album_id=" + f.private + "&album_id=" + f.family,
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}, 1},
		{"unknown album", f.userB, "album_id=" + unknownAlbum,
			[]string{}, nil, 0},
		{"non-member", f.carol, "album_id=" + f.family,
			[]string{}, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.router, tt.user, http.MethodGet, "/api/media?"+tt.query, "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantList, decodeIDs(t, rec))

			if len(tt.wantRound) == 0 {
				rec := do(t, f.router, tt.user, http.MethodGet, "/api/game/round?"+tt.query, "")
				assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
				return
			}
			for range 10 {
				rec := do(t, f.router, tt.user, http.MethodGet, "/api/game/round?"+tt.query, "")
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				var round roundMedia
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &round))
				assert.Contains(t, tt.wantRound, round.ID)
				assert.InEpsilon(t, tt.wantMaxKm, round.MaxDistKm, 0.01, "max_dist_km spans the filtered set, own and shared")
			}
		})
	}
}

func TestSharing_ListOwnerFields(t *testing.T) {
	f := newSharingFixture(t)
	list := func(t *testing.T, user string) map[string]mediaItem {
		t.Helper()
		rec := do(t, f.router, user, http.MethodGet, "/api/media", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var items []mediaItem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
		byID := map[string]mediaItem{}
		for _, it := range items {
			byID[it.ID] = it
		}
		return byID
	}

	t.Run("shared rows carry their owner and no trip", func(t *testing.T) {
		items := list(t, f.userB)
		require.Len(t, items, 3)
		for _, id := range []string{f.mediaA, f.mediaAUnlocated} {
			assert.Equal(t, f.userA, items[id].OwnerID)
			assert.Equal(t, ptr("alice"), items[id].OwnerName)
			assert.Nil(t, items[id].TripID, "trips are personal")
		}
		require.NotNil(t, snapshotMedia(t, f.pool, f.mediaA).tripID, "mediaA is in A's trip")
		assert.Equal(t, f.userB, items[f.mediaB].OwnerID)
		assert.Equal(t, ptr("bob"), items[f.mediaB].OwnerName)
		assert.Equal(t, ptr(f.tripB), items[f.mediaB].TripID)
	})

	t.Run("the owner sees their own trip", func(t *testing.T) {
		items := list(t, f.userA)
		require.Len(t, items, 2)
		assert.Equal(t, ptr(f.tripA), items[f.mediaA].TripID)
		assert.Equal(t, f.userA, items[f.mediaA].OwnerID)
		assert.Equal(t, ptr("alice"), items[f.mediaA].OwnerName)
	})

	// Run in order: each step clears one more of A's name fields.
	steps := []struct {
		name   string
		update string
		want   *string
	}{
		{"display name first", "", ptr("alice")},
		{"email without a display name", `UPDATE users SET display_name = NULL WHERE id = $1`, ptr("alice@example.test")},
		{"null without either", `UPDATE users SET email = NULL WHERE id = $1`, nil},
	}
	for _, s := range steps {
		t.Run("owner_name: "+s.name, func(t *testing.T) {
			if s.update != "" {
				_, err := f.pool.Exec(context.Background(), s.update, f.userA)
				require.NoError(t, err)
			}
			assert.Equal(t, s.want, list(t, f.userB)[f.mediaA].OwnerName)
		})
	}
}

// GPS stays per owner: a member's matcher neither writes shared media nor reads their location,
// and the sharer's matcher never uses the member's tracks.
func TestSharing_GPSIsolation(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	// B shares located media as well, so either side could borrow the other's positions.
	insertAlbumMedia(t, f.pool, f.family, f.mediaB)
	// Ingest records A's EXIF location as one of A's track points; B's matcher must not read it.
	require.NoError(t, insertExifGPSTrack(ctx, f.pool, f.userA, budapestLat, budapestLng,
		time.Date(2024, 5, 2, 12, 0, 0, 0, time.UTC)))
	// B's unlocated photo taken at the same instant as A's shared EXIF-located mediaA.
	bSameInstant := insertMedia(t, f.pool, mediaFixture{owner: f.userB, filename: "b-same-instant.jpg", takenAt: "2024-05-02T12:00:00Z"})
	beforeA := snapshotMedia(t, f.pool, f.mediaA)
	beforeB := snapshotMedia(t, f.pool, f.mediaB)

	// B's tracks bracket A's shared, unlocated a2 (12:30) and are a day away from bSameInstant.
	// The import runs B's matcher.
	rec := do(t, f.router, f.userB, http.MethodPost, "/api/gps-tracks/import", `[
		{"recorded_at":"2024-05-03T12:25:00Z","lat":48.2,"lng":16.37,"source":"strava"},
		{"recorded_at":"2024-05-03T12:35:00Z","lat":48.21,"lng":16.38,"source":"strava"}]`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = do(t, f.router, f.userB, http.MethodPost, "/api/gps-matcher/run", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	t.Run("B's matcher leaves A's shared media alone", func(t *testing.T) {
		assert.Equal(t, beforeA, snapshotMedia(t, f.pool, f.mediaA), "EXIF-located shared item unchanged")
		a2 := snapshotMedia(t, f.pool, f.mediaAUnlocated)
		assert.Nil(t, a2.lat, "B's tracks bracket a2, but a2 is not B's row")
		assert.Nil(t, a2.gpsSource)
	})

	t.Run("B's media borrow nothing from A's shared location", func(t *testing.T) {
		got := snapshotMedia(t, f.pool, bSameInstant)
		assert.Nil(t, got.lat, "no B track within the window; A's EXIF point is A's")
		assert.Nil(t, got.gpsSource)
		assert.Equal(t, beforeB, snapshotMedia(t, f.pool, f.mediaB))
	})

	t.Run("A's matcher ignores B's tracks", func(t *testing.T) {
		rec := do(t, f.router, f.userA, http.MethodPost, "/api/gps-matcher/run", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		a2 := snapshotMedia(t, f.pool, f.mediaAUnlocated)
		require.NotNil(t, a2.lat, "A's 12:20 point is within the window")
		assert.InDelta(t, budapestLat, *a2.lat, 1e-6, "from A's track only, never B's closer points or B's shared mediaB")
		assert.InDelta(t, budapestLng, *a2.lng, 1e-6)
		assert.Equal(t, "interpolated", *a2.gpsSource)

		assert.Nil(t, snapshotMedia(t, f.pool, bSameInstant).lat, "A's EXIF track point never places B's media")
		assert.Equal(t, beforeB, snapshotMedia(t, f.pool, f.mediaB))
	})

	t.Run("B sees A's improved position", func(t *testing.T) {
		rec := do(t, f.router, f.userB, http.MethodGet, "/api/media?owner=shared", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var items []mediaItem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
		require.Len(t, items, 2)
		assert.Equal(t, f.mediaAUnlocated, items[0].ID)
		require.NotNil(t, items[0].Lat)
		assert.InDelta(t, budapestLat, *items[0].Lat, 1e-6)
	})
}

func TestSharing_TripDetectIgnoresSharedMedia(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	// B's own unassigned photo, 15 minutes after A's shared, unassigned a2.
	b2 := insertMedia(t, f.pool, mediaFixture{owner: f.userB, filename: "b2.jpg", takenAt: "2024-05-03T12:45:00Z"})

	require.NoError(t, RunTripDetect(ctx, f.pool, f.userB, 24))

	assert.Nil(t, snapshotMedia(t, f.pool, f.mediaAUnlocated).tripID, "A's shared media never joins B's trips")
	bTrip := snapshotMedia(t, f.pool, b2).tripID
	require.NotNil(t, bTrip, "B's own unassigned media is grouped")

	var foreignInBTrips int
	require.NoError(t, f.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM media m JOIN trips t ON t.id = m.trip_id
		WHERE t.owner_id = $1 AND m.owner_id <> $1`, f.userB).Scan(&foreignInBTrips))
	assert.Zero(t, foreignInBTrips, "no B trip contains A's media")

	rec := do(t, f.router, f.userB, http.MethodGet, "/api/media?trip_id="+*bTrip, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{f.mediaB, b2}, decodeIDs(t, rec), "B's auto trip is regrouped from B's media only")
	assert.Equal(t, f.tripA, *snapshotMedia(t, f.pool, f.mediaA).tripID, "A's trip assignment is untouched")
}
