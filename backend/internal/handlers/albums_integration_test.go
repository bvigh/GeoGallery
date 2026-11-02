package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAlbumsRouter serves the album routes and hands every other path to f.router, so a test can
// change an album and then look at the media list. Requests authenticate by X-Test-User, as in
// newOwnershipRouter; the session-only wiring is tested in cmd/server.
func newAlbumsRouter(f *ownershipFixture) http.Handler {
	albums := NewAlbumHandler(f.pool)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.User{ID: r.Header.Get(testUserHeader), Role: "user"}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u, auth.MethodSession)))
		})
	})
	r.Get("/api/albums", albums.List)
	r.Post("/api/albums", albums.Create)
	r.Patch("/api/albums/{id}", albums.Update)
	r.Delete("/api/albums/{id}", albums.Delete)
	r.Get("/api/albums/{id}/members", albums.ListMembers)
	r.Post("/api/albums/{id}/members", albums.AddMember)
	r.Delete("/api/albums/{id}/members/{user_id}", albums.RemoveMember)
	r.Post("/api/albums/{id}/media", albums.AddMedia)
	r.Post("/api/albums/{id}/media/remove", albums.RemoveMedia)
	r.Get("/api/media/{id}/albums", albums.MediaAlbums)
	r.Get("/api/users", albums.ListUsers)
	r.NotFound(f.router.ServeHTTP)
	return r
}

// albumsFixture is the sharing fixture behind a router that also serves the album routes.
type albumsFixture struct {
	*sharingFixture
	api http.Handler
}

func newAlbumsFixture(t *testing.T) *albumsFixture {
	t.Helper()
	f := &albumsFixture{sharingFixture: newSharingFixture(t)}
	f.api = newAlbumsRouter(f.ownershipFixture)
	return f
}

const unknownUUID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

func decodeAlbums(t *testing.T, rec *httptest.ResponseRecorder) []albumItem {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var albums []albumItem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &albums), rec.Body.String())
	for i := range albums {
		albums[i].CreatedAt = time.Time{} // compared separately where it matters
	}
	return albums
}

func albumIDs(albums []albumItem) []string {
	ids := make([]string, 0, len(albums))
	for _, a := range albums {
		ids = append(ids, a.ID)
	}
	return ids
}

// readable returns the IDs userID gets from GET /api/media, sorted.
func readable(t *testing.T, f *albumsFixture, userID string) []string {
	t.Helper()
	rec := do(t, f.api, userID, http.MethodGet, "/api/media", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ids := decodeIDs(t, rec)
	sort.Strings(ids)
	return ids
}

func sorted(ids ...string) []string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return out
}

// queryIDs runs a query returning one text column, sorted.
func queryIDs(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, args...)
	require.NoError(t, err)
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	sort.Strings(ids)
	return ids
}

func albumMemberIDs(t *testing.T, pool *pgxpool.Pool, albumID string) []string {
	t.Helper()
	return queryIDs(t, pool, `SELECT user_id::text FROM album_members WHERE album_id = $1`, albumID)
}

func albumContents(t *testing.T, pool *pgxpool.Pool, albumID string) []string {
	t.Helper()
	return queryIDs(t, pool, `SELECT media_id::text FROM album_media WHERE album_id = $1`, albumID)
}

func storedAlbumName(t *testing.T, pool *pgxpool.Pool, albumID string) string {
	t.Helper()
	var name string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT name FROM albums WHERE id = $1`, albumID).Scan(&name))
	return name
}

func disableUser(t *testing.T, pool *pgxpool.Pool, userID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `UPDATE users SET disabled_at = NOW() WHERE id = $1`, userID)
	require.NoError(t, err)
}

// jsonKeys returns the sorted keys of a JSON object.
func jsonKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &obj), string(raw))
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestAlbums_CreateAndList(t *testing.T) {
	f := newAlbumsFixture(t)

	rec := do(t, f.api, f.userB, http.MethodPost, "/api/albums", `{"name":"  Iceland 2025 \n"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"created_at", "end_date", "id", "is_owner", "media_count", "member_count",
		"name", "owner_id", "owner_name", "start_date"}, jsonKeys(t, rec.Body.Bytes()))
	var created albumItem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.WithinDuration(t, time.Now(), created.CreatedAt, time.Minute)
	require.NotEmpty(t, created.ID)
	created.CreatedAt = time.Time{}
	assert.Equal(t, albumItem{ID: created.ID, Name: "Iceland 2025", OwnerID: f.userB, OwnerName: ptr("bob"),
		IsOwner: true, MemberCount: 1}, created, "trimmed name, no media and no dates yet")
	assert.Equal(t, []string{f.userB}, albumMemberIDs(t, f.pool, created.ID),
		"the owner's member row is created with the album")

	t.Run("invalid names are rejected", func(t *testing.T) {
		tests := []struct {
			name string
			body string
		}{
			{"empty", `{"name":""}`},
			{"whitespace only", `{"name":"  \t "}`},
			{"missing", `{}`},
			{"201 characters", `{"name":"` + strings.Repeat("x", 201) + `"}`},
			{"NUL", `{"name":"a\u0000b"}`},
			{"not JSON", `name=x`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := do(t, f.api, f.userA, http.MethodPost, "/api/albums", tt.body)
				assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			})
		}
		var n int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM albums WHERE owner_id = $1`, f.userA).Scan(&n))
		assert.Equal(t, 2, n, "only the fixture's family and private albums")
	})

	t.Run("200 characters are accepted", func(t *testing.T) {
		name := strings.Repeat("é", 200)
		rec := do(t, f.api, f.carol, http.MethodPost, "/api/albums", `{"name":"`+name+`"}`)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var a albumItem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &a))
		assert.Equal(t, name, storedAlbumName(t, f.pool, a.ID))
		_, err := f.pool.Exec(context.Background(), `DELETE FROM albums WHERE id = $1`, a.ID)
		require.NoError(t, err)
	})

	t.Run("each member lists their albums, newest first", func(t *testing.T) {
		familyAsOwner := albumItem{ID: f.family, Name: "Family", OwnerID: f.userA, OwnerName: ptr("alice"),
			IsOwner: true, MemberCount: 2, MediaCount: 2, StartDate: ptr("2024-05-02"), EndDate: ptr("2024-05-03")}
		privateAsOwner := albumItem{ID: f.private, Name: "A only", OwnerID: f.userA, OwnerName: ptr("alice"),
			IsOwner: true, MemberCount: 1, MediaCount: 1, StartDate: ptr("2024-05-02"), EndDate: ptr("2024-05-02")}
		familyAsMember := familyAsOwner
		familyAsMember.IsOwner = false

		assert.Equal(t, []albumItem{privateAsOwner, familyAsOwner},
			decodeAlbums(t, do(t, f.api, f.userA, http.MethodGet, "/api/albums", "")), "A does not see B's new album")
		assert.Equal(t, []albumItem{created, familyAsMember},
			decodeAlbums(t, do(t, f.api, f.userB, http.MethodGet, "/api/albums", "")), "the counts are the same for every member")

		rec := do(t, f.api, f.carol, http.MethodGet, "/api/albums", "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `[]`, rec.Body.String(), "a user in no album gets [], not null")
	})
}

func TestAlbums_Rename(t *testing.T) {
	f := newAlbumsFixture(t)

	// Run in order; each step's want is the name afterwards.
	tests := []struct {
		name       string
		user       string
		album      string
		body       string
		wantStatus int
		wantName   string
	}{
		{"member is forbidden", f.userB, f.family, `{"name":"Bob's"}`, http.StatusForbidden, "Family"},
		{"non-member gets 404", f.carol, f.family, `{"name":"Carol's"}`, http.StatusNotFound, "Family"},
		{"malformed id", f.userA, "nope", `{"name":"x"}`, http.StatusNotFound, "Family"},
		{"unknown album", f.userA, unknownUUID, `{"name":"x"}`, http.StatusNotFound, "Family"},
		{"invalid name", f.userA, f.family, `{"name":"   "}`, http.StatusBadRequest, "Family"},
		{"owner renames, trimmed", f.userA, f.family, `{"name":" Family 2024 "}`, http.StatusNoContent, "Family 2024"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.api, tt.user, http.MethodPatch, "/api/albums/"+tt.album, tt.body)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantName, storedAlbumName(t, f.pool, f.family))
		})
	}
}

func TestAlbums_DeleteKeepsMedia(t *testing.T) {
	f := newAlbumsFixture(t)
	insertAlbumMedia(t, f.pool, f.family, f.mediaB)
	require.Equal(t, sorted(f.mediaA, f.mediaAUnlocated, f.mediaB), readable(t, f, f.userB))

	tests := []struct {
		name       string
		user       string
		album      string
		wantStatus int
	}{
		{"member is forbidden", f.userB, f.family, http.StatusForbidden},
		{"non-member gets 404", f.carol, f.family, http.StatusNotFound},
		{"malformed id", f.userA, "nope", http.StatusNotFound},
		{"unknown album", f.userA, unknownUUID, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.api, tt.user, http.MethodDelete, "/api/albums/"+tt.album, "")
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Len(t, albumContents(t, f.pool, f.family), 3, "nothing was deleted")
		})
	}

	rec := do(t, f.api, f.userA, http.MethodDelete, "/api/albums/"+f.family, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assert.Empty(t, albumMemberIDs(t, f.pool, f.family), "members cascade")
	assert.Empty(t, albumContents(t, f.pool, f.family), "contributions cascade")
	assert.Equal(t, []string{f.mediaA}, albumContents(t, f.pool, f.private), "other albums are untouched")
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM media`).Scan(&n))
	assert.Equal(t, 3, n, "no media is deleted")
	assert.Empty(t, f.store.deleted, "no S3 object is deleted")

	assert.Equal(t, []string{f.mediaB}, readable(t, f, f.userB), "B lost access to A's media")
	assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), readable(t, f, f.userA), "A lost access to B's media")
	assert.Empty(t, decodeAlbums(t, do(t, f.api, f.userB, http.MethodGet, "/api/albums", "")))

	rec = do(t, f.api, f.userA, http.MethodDelete, "/api/albums/"+f.family, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "deleting twice")
}

func TestAlbums_Members(t *testing.T) {
	f := newAlbumsFixture(t)
	dave := createUser(t, f.pool, "dave")
	disableUser(t, f.pool, dave)

	t.Run("list", func(t *testing.T) {
		for _, user := range []string{f.userA, f.userB} {
			rec := do(t, f.api, user, http.MethodGet, "/api/albums/"+f.family+"/members", "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var raw []json.RawMessage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
			require.Len(t, raw, 2)
			assert.Equal(t, []string{"added_at", "display_name", "email", "is_owner", "user_id"}, jsonKeys(t, raw[0]))

			var members []albumMember
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &members))
			for i := range members {
				assert.WithinDuration(t, time.Now(), members[i].AddedAt, time.Minute)
				members[i].AddedAt = time.Time{}
			}
			assert.Equal(t, []albumMember{
				{UserID: f.userA, DisplayName: ptr("alice"), Email: ptr("alice@example.test"), IsOwner: true},
				{UserID: f.userB, DisplayName: ptr("bob"), Email: ptr("bob@example.test")},
			}, members, "the owner first, for every member")
		}

		for _, tt := range []struct {
			name, user, album string
		}{
			{"non-member", f.carol, f.family},
			{"malformed id", f.userA, "nope"},
			{"unknown album", f.userA, unknownUUID},
		} {
			rec := do(t, f.api, tt.user, http.MethodGet, "/api/albums/"+tt.album+"/members", "")
			assert.Equal(t, http.StatusNotFound, rec.Code, "%s: %s", tt.name, rec.Body.String())
		}
	})

	t.Run("add", func(t *testing.T) {
		// Run in order; each step's want is the member set afterwards.
		before := sorted(f.userA, f.userB)
		after := sorted(f.userA, f.userB, f.carol)
		tests := []struct {
			name        string
			user        string
			album       string
			body        string
			wantStatus  int
			wantMembers []string
		}{
			{"member is forbidden", f.userB, f.family, `{"user_id":"` + f.carol + `"}`, http.StatusForbidden, before},
			{"non-member gets 404", f.carol, f.family, `{"user_id":"` + f.carol + `"}`, http.StatusNotFound, before},
			{"malformed album id", f.userA, "nope", `{"user_id":"` + f.carol + `"}`, http.StatusNotFound, before},
			{"unknown album", f.userA, unknownUUID, `{"user_id":"` + f.carol + `"}`, http.StatusNotFound, before},
			{"unknown user", f.userA, f.family, `{"user_id":"` + unknownUUID + `"}`, http.StatusNotFound, before},
			{"unclaimed legacy owner", f.userA, f.family, `{"user_id":"` + auth.LegacyOwnerID + `"}`, http.StatusNotFound, before},
			{"disabled user", f.userA, f.family, `{"user_id":"` + dave + `"}`, http.StatusNotFound, before},
			{"malformed user id", f.userA, f.family, `{"user_id":"nope"}`, http.StatusBadRequest, before},
			{"missing user id", f.userA, f.family, `{}`, http.StatusBadRequest, before},
			{"not JSON", f.userA, f.family, `user_id=x`, http.StatusBadRequest, before},
			{"owner adds carol", f.userA, f.family, `{"user_id":"` + strings.ToUpper(f.carol) + `"}`, http.StatusNoContent, after},
			{"adding again is a no-op", f.userA, f.family, `{"user_id":"` + f.carol + `"}`, http.StatusNoContent, after},
			{"adding an existing member", f.userA, f.family, `{"user_id":"` + f.userB + `"}`, http.StatusNoContent, after},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := do(t, f.api, tt.user, http.MethodPost, "/api/albums/"+tt.album+"/members", tt.body)
				assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
				assert.Equal(t, tt.wantMembers, albumMemberIDs(t, f.pool, f.family))
			})
		}

		assert.Equal(t, []string{f.family}, albumIDs(decodeAlbums(t, do(t, f.api, f.carol, http.MethodGet, "/api/albums", ""))))
		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), readable(t, f, f.carol), "carol now reads the album")
	})
}

func TestAlbums_RemoveMemberAndLeave(t *testing.T) {
	f := newAlbumsFixture(t)
	dave := createUser(t, f.pool, "dave")
	insertAlbumMember(t, f.pool, f.family, f.carol)
	mediaC := insertMedia(t, f.pool, mediaFixture{owner: f.carol, filename: "c1.jpg", takenAt: "2024-05-04T09:00:00Z"})
	insertAlbumMedia(t, f.pool, f.family, f.mediaB)
	insertAlbumMedia(t, f.pool, f.family, mediaC)
	everyone := sorted(f.mediaA, f.mediaAUnlocated, f.mediaB, mediaC)
	require.Equal(t, everyone, readable(t, f, f.userB))

	members := sorted(f.userA, f.userB, f.carol)
	tests := []struct {
		name       string
		user       string
		album      string
		target     string
		wantStatus int
	}{
		{"member cannot remove another member", f.userB, f.family, f.carol, http.StatusForbidden},
		{"member cannot remove the owner", f.userB, f.family, f.userA, http.StatusForbidden},
		{"non-member gets 404", dave, f.family, f.userB, http.StatusNotFound},
		{"non-member cannot leave", dave, f.family, dave, http.StatusNotFound},
		{"owner cannot leave", f.userA, f.family, f.userA, http.StatusBadRequest},
		{"target is not a member", f.userA, f.family, dave, http.StatusNotFound},
		{"malformed user id", f.userA, f.family, "nope", http.StatusNotFound},
		{"malformed album id", f.userA, "nope", f.carol, http.StatusNotFound},
		{"unknown album", f.userA, unknownUUID, f.carol, http.StatusNotFound},
		{"leaving an album you are not in", f.userB, f.private, f.userB, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.api, tt.user, http.MethodDelete, "/api/albums/"+tt.album+"/members/"+tt.target, "")
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, members, albumMemberIDs(t, f.pool, f.family), "nobody was removed")
			assert.Len(t, albumContents(t, f.pool, f.family), 4, "no contribution was removed")
		})
	}

	t.Run("owner removes carol: her items leave and she loses access", func(t *testing.T) {
		rec := do(t, f.api, f.userA, http.MethodDelete, "/api/albums/"+f.family+"/members/"+f.carol, "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated, f.mediaB), albumContents(t, f.pool, f.family))
		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated, f.mediaB), readable(t, f, f.userB), "B no longer sees carol's item")
		assert.Equal(t, []string{mediaC}, readable(t, f, f.carol), "carol keeps only her own media")
		assert.Empty(t, decodeAlbums(t, do(t, f.api, f.carol, http.MethodGet, "/api/albums", "")))
	})

	t.Run("B leaves: his items leave and he loses access", func(t *testing.T) {
		rec := do(t, f.api, f.userB, http.MethodDelete, "/api/albums/"+f.family+"/members/"+f.userB, "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), albumContents(t, f.pool, f.family))
		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), readable(t, f, f.userA), "A no longer sees B's item")
		assert.Equal(t, []string{f.mediaB}, readable(t, f, f.userB), "B keeps only his own media")
		assert.Equal(t, []string{f.userA}, albumMemberIDs(t, f.pool, f.family), "the owner's row stays")

		rec = do(t, f.api, f.userB, http.MethodDelete, "/api/albums/"+f.family+"/members/"+f.userB, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "leaving twice")
	})

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM media`).Scan(&n))
	assert.Equal(t, 4, n, "removal never deletes media")
}

// A disabled user keeps their memberships and contributions, and albums they own stay readable.
// The users are disabled through auth.PGStore, as the admin route does, so a cleanup step added
// to SetDisabled that touches album_members (and cascades contributions away) fails here.
func TestAlbums_DisabledUserKeepsMembershipsAndContributions(t *testing.T) {
	f := newAlbumsFixture(t)
	ctx := context.Background()
	store := auth.NewPGStore(f.pool)
	insertAlbumMember(t, f.pool, f.family, f.carol)
	insertAlbumMedia(t, f.pool, f.family, f.mediaB)
	members := sorted(f.userA, f.userB, f.carol)
	contents := sorted(f.mediaA, f.mediaAUnlocated, f.mediaB)
	addedAt := func(userID string) time.Time {
		t.Helper()
		var at time.Time
		require.NoError(t, f.pool.QueryRow(ctx,
			`SELECT added_at FROM album_members WHERE album_id = $1 AND user_id = $2`, f.family, userID).Scan(&at))
		return at
	}
	bAddedAt := addedAt(f.userB)

	t.Run("a disabled member's photos stay in the album", func(t *testing.T) {
		require.NoError(t, store.SetDisabled(ctx, f.userB, true))

		assert.Equal(t, members, albumMemberIDs(t, f.pool, f.family))
		assert.Equal(t, contents, albumContents(t, f.pool, f.family))
		for _, user := range []string{f.userA, f.carol} {
			assert.Contains(t, readable(t, f, user), f.mediaB)
			rec := do(t, f.api, user, http.MethodGet, "/api/media/"+f.mediaB+"/url", "")
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		}

		// B left the directory, so the owner cannot add him again, and his row is untouched.
		rec := do(t, f.api, f.userA, http.MethodPost, "/api/albums/"+f.family+"/members", `{"user_id":"`+f.userB+`"}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Equal(t, members, albumMemberIDs(t, f.pool, f.family))
		assert.Equal(t, bAddedAt, addedAt(f.userB))
		assert.Equal(t, contents, albumContents(t, f.pool, f.family))
	})

	t.Run("a disabled owner's album stays readable but unmanaged", func(t *testing.T) {
		require.NoError(t, store.SetDisabled(ctx, f.userA, true))

		assert.Equal(t, "Family", storedAlbumName(t, f.pool, f.family))
		assert.Equal(t, members, albumMemberIDs(t, f.pool, f.family))
		assert.Equal(t, contents, albumContents(t, f.pool, f.family))
		assert.Equal(t, []string{f.mediaA}, albumContents(t, f.pool, f.private))
		assert.Equal(t, contents, readable(t, f, f.carol), "the remaining active member still reads everything")
		rec := do(t, f.api, f.carol, http.MethodGet, "/api/media/"+f.mediaA+"/url", "")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rec = do(t, f.api, f.carol, http.MethodPatch, "/api/albums/"+f.family, `{"name":"Mine now"}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, "there is no ownership transfer")
		assert.Equal(t, "Family", storedAlbumName(t, f.pool, f.family))
	})
}

// mediaIDsBody builds {"media_ids": [...]} from ids.
func mediaIDsBody(t *testing.T, ids ...string) string {
	t.Helper()
	b, err := json.Marshal(map[string][]string{"media_ids": ids})
	require.NoError(t, err)
	return string(b)
}

func manyIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
	}
	return ids
}

// Both media endpoints share the request checks.
func TestAlbums_MediaRequestErrors(t *testing.T) {
	f := newAlbumsFixture(t)
	tripBody := func(id string) string { return `{"trip_id":"` + id + `"}` }

	tests := []struct {
		name       string
		user       string
		album      string
		body       string
		wantStatus int
	}{
		{"neither field", f.userB, f.family, `{}`, http.StatusBadRequest},
		{"both fields", f.userB, f.family, `{"media_ids":["` + f.mediaB + `"],"trip_id":"` + f.tripB + `"}`, http.StatusBadRequest},
		{"empty media_ids", f.userB, f.family, `{"media_ids":[]}`, http.StatusBadRequest},
		{"null media_ids", f.userB, f.family, `{"media_ids":null}`, http.StatusBadRequest},
		{"5001 media_ids", f.userB, f.family, mediaIDsBody(t, manyIDs(5001)...), http.StatusBadRequest},
		{"malformed media id", f.userB, f.family, mediaIDsBody(t, f.mediaB, "nope"), http.StatusBadRequest},
		{"malformed trip_id", f.userB, f.family, tripBody("nope"), http.StatusBadRequest},
		{"not JSON", f.userB, f.family, `media_ids=x`, http.StatusBadRequest},
		{"non-member", f.carol, f.family, mediaIDsBody(t, f.mediaA), http.StatusNotFound},
		{"malformed album id", f.userB, "nope", mediaIDsBody(t, f.mediaB), http.StatusNotFound},
		{"unknown album", f.userB, unknownUUID, mediaIDsBody(t, f.mediaB), http.StatusNotFound},
		{"album the caller is not in", f.userB, f.private, mediaIDsBody(t, f.mediaB), http.StatusNotFound},
		{"someone else's trip", f.userB, f.family, tripBody(f.tripA), http.StatusNotFound},
		{"unknown trip", f.userB, f.family, tripBody(unknownUUID), http.StatusNotFound},
	}
	for _, path := range []string{"/media", "/media/remove"} {
		for _, tt := range tests {
			t.Run(strings.TrimPrefix(path, "/")+"/"+tt.name, func(t *testing.T) {
				rec := do(t, f.api, tt.user, http.MethodPost, "/api/albums/"+tt.album+path, tt.body)
				assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
				assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), albumContents(t, f.pool, f.family))
				assert.Equal(t, []string{f.mediaA}, albumContents(t, f.pool, f.private))
			})
		}
	}
}

func TestAlbums_AddMedia(t *testing.T) {
	f := newAlbumsFixture(t)
	album := insertAlbum(t, f.pool, f.userA, "Iceland")
	insertAlbumMember(t, f.pool, album, f.userB)
	add := func(t *testing.T, user, body string) int64 {
		t.Helper()
		rec := do(t, f.api, user, http.MethodPost, "/api/albums/"+album+"/media", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var res map[string]int64
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		require.Contains(t, res, "added")
		return res["added"]
	}

	t.Run("media the caller does not own are skipped", func(t *testing.T) {
		assert.EqualValues(t, 1, add(t, f.userA, mediaIDsBody(t, f.mediaA, f.mediaB, unknownUUID)))
		assert.Equal(t, []string{f.mediaA}, albumContents(t, f.pool, album))

		assert.EqualValues(t, 0, add(t, f.userB, mediaIDsBody(t, f.mediaA)), "B cannot re-share A's item he can read")
		assert.EqualValues(t, 0, add(t, f.userA, mediaIDsBody(t, strings.ToUpper(f.mediaA))), "already in the album")
		assert.Equal(t, []string{f.mediaA}, albumContents(t, f.pool, album))
	})

	t.Run("5000 IDs at once", func(t *testing.T) {
		ids := append(manyIDs(4999), f.mediaAUnlocated)
		assert.EqualValues(t, 1, add(t, f.userA, mediaIDsBody(t, ids...)))
	})

	t.Run("a trip is a snapshot; adding it again picks up only new items", func(t *testing.T) {
		assert.EqualValues(t, 1, add(t, f.userB, `{"trip_id":"`+f.tripB+`"}`))
		assert.EqualValues(t, 0, add(t, f.userB, `{"trip_id":"`+f.tripB+`"}`), "idempotent")

		mediaB2 := insertMedia(t, f.pool, mediaFixture{owner: f.userB, filename: "b2.jpg",
			takenAt: "2024-05-03T14:00:00Z", tripID: &f.tripB})
		assert.EqualValues(t, 1, add(t, f.userB, `{"trip_id":"`+strings.ToUpper(f.tripB)+`"}`))
		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated, f.mediaB, mediaB2), albumContents(t, f.pool, album))

		var tripMedia int
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM media WHERE trip_id = $1 AND owner_id = $2`, f.tripB, f.userB).Scan(&tripMedia))
		assert.Equal(t, 2, tripMedia, "the trip itself is unchanged")
	})

	t.Run("each contribution belongs to the media's owner", func(t *testing.T) {
		var foreign int
		require.NoError(t, f.pool.QueryRow(context.Background(), `
			SELECT COUNT(*) FROM album_media am JOIN media m ON m.id = am.media_id
			WHERE am.album_id = $1 AND am.owner_id <> m.owner_id`, album).Scan(&foreign))
		assert.Zero(t, foreign)

		rec := do(t, f.api, f.userA, http.MethodGet, "/api/media?album_id="+album, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Len(t, decodeIDs(t, rec), 4, "A reads B's contributions through the album filter")
	})
}

func TestAlbums_RemoveMedia(t *testing.T) {
	f := newAlbumsFixture(t)
	insertAlbumMedia(t, f.pool, f.family, f.mediaB)
	familyPath := "/api/albums/" + f.family + "/media"
	remove := func(t *testing.T, user, body string) int64 {
		t.Helper()
		rec := do(t, f.api, user, http.MethodPost, familyPath+"/remove", body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var res map[string]int64
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
		require.Contains(t, res, "removed")
		return res["removed"]
	}
	all := sorted(f.mediaA, f.mediaAUnlocated, f.mediaB)

	t.Run("a member cannot remove someone else's item", func(t *testing.T) {
		for _, body := range []string{
			mediaIDsBody(t, f.mediaA),
			mediaIDsBody(t, f.mediaB, f.mediaA), // refused whole: B's own item stays too
		} {
			rec := do(t, f.api, f.userB, http.MethodPost, familyPath+"/remove", body)
			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Equal(t, all, albumContents(t, f.pool, f.family), "nothing removed")
		}
	})

	t.Run("a member removes their own items", func(t *testing.T) {
		assert.EqualValues(t, 1, remove(t, f.userB, mediaIDsBody(t, f.mediaB, unknownUUID)))
		assert.EqualValues(t, 0, remove(t, f.userB, mediaIDsBody(t, f.mediaB)), "already removed")
		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), albumContents(t, f.pool, f.family))

		rec := do(t, f.api, f.userB, http.MethodPost, familyPath, `{"trip_id":"`+f.tripB+`"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.EqualValues(t, 1, remove(t, f.userB, `{"trip_id":"`+f.tripB+`"}`), "by trip")
		assert.Equal(t, sorted(f.mediaA, f.mediaAUnlocated), albumContents(t, f.pool, f.family))
	})

	t.Run("the owner removes anyone's items", func(t *testing.T) {
		rec := do(t, f.api, f.userB, http.MethodPost, familyPath, mediaIDsBody(t, f.mediaB))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, all, albumContents(t, f.pool, f.family))

		assert.EqualValues(t, 2, remove(t, f.userA, mediaIDsBody(t, f.mediaB, f.mediaAUnlocated)))
		assert.Equal(t, []string{f.mediaA}, albumContents(t, f.pool, f.family))
		assert.Equal(t, sorted(f.mediaA, f.mediaB), readable(t, f, f.userB), "B no longer reads the removed a2")
	})

	t.Run("a trip_id resolves to the caller's own trip", func(t *testing.T) {
		assert.EqualValues(t, 1, remove(t, f.userA, `{"trip_id":"`+f.tripA+`"}`))
		assert.EqualValues(t, 0, remove(t, f.userA, `{"trip_id":"`+f.tripA+`"}`))
		assert.Empty(t, albumContents(t, f.pool, f.family))
		assert.Equal(t, []string{f.mediaA}, albumContents(t, f.pool, f.private), "other albums keep the item")
	})

	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM media`).Scan(&n))
	assert.Equal(t, 3, n, "removing from an album never deletes media")
}

func TestAlbums_MediaAlbums(t *testing.T) {
	f := newAlbumsFixture(t)
	family := `{"id":"` + f.family + `","name":"Family"}`
	private := `{"id":"` + f.private + `","name":"A only"}`

	tests := []struct {
		name  string
		user  string
		media string
		want  string
	}{
		{"owner, newest album first", f.userA, f.mediaA, "[" + private + "," + family + "]"},
		{"owner, one album", f.userA, f.mediaAUnlocated, "[" + family + "]"},
		{"member sees only albums they are in", f.userB, f.mediaA, "[" + family + "]"},
		{"non-member", f.carol, f.mediaA, "[]"},
		{"own item in no album", f.userB, f.mediaB, "[]"},
		{"unknown id", f.userA, unknownUUID, "[]"},
		{"malformed id", f.userA, "nope", "[]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.api, tt.user, http.MethodGet, "/api/media/"+tt.media+"/albums", "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, tt.want, rec.Body.String())
		})
	}
}

func TestAlbums_UserDirectory(t *testing.T) {
	f := newAlbumsFixture(t)
	disableUser(t, f.pool, createUser(t, f.pool, "dave"))
	var abe, nameless string
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email) VALUES ('https://idp.test', 'abe', 'abe@example.test')
		RETURNING id::text`).Scan(&abe))
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject) VALUES ('https://idp.test', 'nameless') RETURNING id::text`).Scan(&nameless))

	entry := func(id, name string) string {
		return fmt.Sprintf(`{"id":%q,"display_name":%q,"email":"%s@example.test"}`, id, name, name)
	}
	// Ordered by display name, else email; the legacy Owner (unclaimed) and dave (disabled) are left out.
	want := "[" + strings.Join([]string{
		`{"id":"` + abe + `","display_name":null,"email":"abe@example.test"}`,
		entry(f.userA, "alice"), entry(f.userB, "bob"), entry(f.carol, "carol"),
		`{"id":"` + nameless + `","display_name":null,"email":null}`,
	}, ",") + "]"

	for _, user := range []string{f.userA, f.carol} {
		rec := do(t, f.api, user, http.MethodGet, "/api/users", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, want, rec.Body.String(), "as %s", user)
	}
}
