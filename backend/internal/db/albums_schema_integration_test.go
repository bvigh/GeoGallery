package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unknownID matches no row in any table.
const unknownID = "00000000-0000-0000-0000-0000000000ff"

func insertAlbumUser(t *testing.T, pool *pgxpool.Pool, subject string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO users (issuer, subject, email, display_name)
		VALUES ('https://idp.test', $1, $1 || '@example.test', $1) RETURNING id::text`, subject).Scan(&id))
	return id
}

func insertAlbumMedia(t *testing.T, pool *pgxpool.Pool, ownerID, filename string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash)
		VALUES ($1, $2, 'photo', $3, $4) RETURNING id::text`,
		ownerID, filename, "media/"+ownerID+"/"+filename, "hash-"+ownerID+"-"+filename).Scan(&id))
	return id
}

// createAlbum inserts an album together with its owner's member row, as the create transaction does.
func createAlbum(t *testing.T, pool *pgxpool.Pool, ownerID, name string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO albums (owner_id, name) VALUES ($1, $2) RETURNING id::text`, ownerID, name).Scan(&id))
	addAlbumMember(t, pool, id, ownerID)
	return id
}

func addAlbumMember(t *testing.T, pool *pgxpool.Pool, albumID, userID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO album_members (album_id, user_id) VALUES ($1, $2)`, albumID, userID)
	require.NoError(t, err)
}

func contribute(t *testing.T, pool *pgxpool.Pool, albumID, mediaID, ownerID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO album_media (album_id, media_id, owner_id) VALUES ($1, $2, $3)`, albumID, mediaID, ownerID)
	require.NoError(t, err)
}

func albumMediaIDs(t *testing.T, pool *pgxpool.Pool, albumID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT media_id::text FROM album_media WHERE album_id = $1`, albumID)
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return ids
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

// albumFixture: Alice owns the album and Bob is a member. Carol is not a member.
// Alice owns two photos, Bob and Carol one each. Alice's first photo is already in the album.
type albumFixture struct {
	pool                                          *pgxpool.Pool
	alice, bob, carol                             string
	album                                         string
	mediaAlice, mediaAlice2, mediaBob, mediaCarol string
}

func newAlbumFixture(t *testing.T) albumFixture {
	t.Helper()
	pool := testdb.New(t)
	f := albumFixture{
		pool:  pool,
		alice: insertAlbumUser(t, pool, "alice"),
		bob:   insertAlbumUser(t, pool, "bob"),
		carol: insertAlbumUser(t, pool, "carol"),
	}
	f.mediaAlice = insertAlbumMedia(t, pool, f.alice, "a.jpg")
	f.mediaAlice2 = insertAlbumMedia(t, pool, f.alice, "a2.jpg")
	f.mediaBob = insertAlbumMedia(t, pool, f.bob, "b.jpg")
	f.mediaCarol = insertAlbumMedia(t, pool, f.carol, "c.jpg")

	f.album = createAlbum(t, pool, f.alice, "Iceland 2025")
	addAlbumMember(t, pool, f.album, f.bob)
	contribute(t, pool, f.album, f.mediaAlice, f.alice)
	return f
}

func TestAlbumsSchema_Constraints(t *testing.T) {
	f := newAlbumFixture(t)
	const insertContribution = `INSERT INTO album_media (album_id, media_id, owner_id) VALUES ($1, $2, $3)`
	const insertMember = `INSERT INTO album_members (album_id, user_id) VALUES ($1, $2)`
	const insertAlbum = `INSERT INTO albums (owner_id, name) VALUES ($1, $2)`
	dave := insertAlbumUser(t, f.pool, "dave") // a member without media
	addAlbumMember(t, f.pool, f.album, dave)
	// The success cases use their own items, so no case depends on another one's insert.
	mediaBob2 := insertAlbumMedia(t, f.pool, f.bob, "b2.jpg")

	// wantName is the violated constraint, or the column for not-null violations (23502).
	tests := []struct {
		name     string
		sql      string
		args     []any
		wantCode string
		wantName string
	}{
		{"owner contributes own media", insertContribution, []any{f.album, f.mediaAlice2, f.alice}, "", ""},
		{"member contributes own media", insertContribution, []any{f.album, mediaBob2, f.bob}, "", ""},
		{"owner_id is not the media's owner", insertContribution, []any{f.album, f.mediaBob, f.alice},
			"23503", "album_media_media_id_owner_id_fkey"},
		{"member re-shares someone else's media", insertContribution, []any{f.album, f.mediaCarol, f.bob},
			"23503", "album_media_media_id_owner_id_fkey"},
		{"contributor is not a member", insertContribution, []any{f.album, f.mediaCarol, f.carol},
			"23503", "album_media_album_id_owner_id_fkey"},
		{"unknown album", insertContribution, []any{unknownID, f.mediaAlice2, f.alice},
			"23503", "album_media_album_id_owner_id_fkey"},
		{"unknown media", insertContribution, []any{f.album, unknownID, f.alice},
			"23503", "album_media_media_id_owner_id_fkey"},
		{"duplicate album_id and media_id", insertContribution, []any{f.album, f.mediaAlice, f.alice},
			"23505", "album_media_pkey"},
		{"duplicate member", insertMember, []any{f.album, f.bob}, "23505", "album_members_pkey"},
		{"member of unknown album", insertMember, []any{unknownID, f.carol}, "23503", "album_members_album_id_fkey"},
		{"unknown member user", insertMember, []any{f.album, unknownID}, "23503", "album_members_user_id_fkey"},
		{"album without owner", insertAlbum, []any{nil, "x"}, "23502", "owner_id"},
		{"album with unknown owner", insertAlbum, []any{unknownID, "x"}, "23503", "albums_owner_id_fkey"},
		{"album empty name", insertAlbum, []any{f.alice, ""}, "23514", "albums_name_check"},
		{"album name over 200 chars", insertAlbum, []any{f.alice, strings.Repeat("a", 201)}, "23514", "albums_name_check"},
		{"album name of 200 chars", insertAlbum, []any{f.alice, strings.Repeat("a", 200)}, "", ""},
		{"deleting a member user is restricted", `DELETE FROM users WHERE id = $1`, []any{dave},
			"23503", "album_members_user_id_fkey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.pool.Exec(context.Background(), tc.sql, tc.args...)
			if tc.wantCode == "" {
				assert.NoError(t, err)
				return
			}
			var pgErr *pgconn.PgError
			require.True(t, errors.As(err, &pgErr), "want PgError, got %v", err)
			assert.Equal(t, tc.wantCode, pgErr.Code)
			gotName := pgErr.ConstraintName
			if tc.wantCode == "23502" {
				gotName = pgErr.ColumnName
			}
			assert.Equal(t, tc.wantName, gotName)
		})
	}
}

func TestAlbumsSchema_RemovingMemberCascadesOnlyTheirContributions(t *testing.T) {
	f := newAlbumFixture(t)
	addAlbumMember(t, f.pool, f.album, f.carol)
	contribute(t, f.pool, f.album, f.mediaBob, f.bob)
	contribute(t, f.pool, f.album, f.mediaCarol, f.carol)
	// Bob also contributes the same photo to Carol's album; leaving Alice's album must not touch it.
	other := createAlbum(t, f.pool, f.carol, "Carol's")
	addAlbumMember(t, f.pool, other, f.bob)
	contribute(t, f.pool, other, f.mediaBob, f.bob)

	tag, err := f.pool.Exec(context.Background(),
		`DELETE FROM album_members WHERE album_id = $1 AND user_id = $2`, f.album, f.bob)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())

	assert.ElementsMatch(t, []string{f.mediaAlice, f.mediaCarol}, albumMediaIDs(t, f.pool, f.album),
		"only Bob's contribution leaves the album")
	assert.Equal(t, []string{f.mediaBob}, albumMediaIDs(t, f.pool, other), "Bob's contribution elsewhere stays")
	assert.Equal(t, 1, countRows(t, f.pool, `SELECT COUNT(*) FROM media WHERE id = $1`, f.mediaBob),
		"the media row itself stays")
	assert.Equal(t, 2, countRows(t, f.pool, `SELECT COUNT(*) FROM album_members WHERE album_id = $1`, f.album),
		"other members stay")
}

func TestAlbumsSchema_DeletingAlbumCascadesMembersAndContributions(t *testing.T) {
	f := newAlbumFixture(t)
	contribute(t, f.pool, f.album, f.mediaBob, f.bob)
	other := createAlbum(t, f.pool, f.bob, "Bob's")
	contribute(t, f.pool, other, f.mediaBob, f.bob)

	tag, err := f.pool.Exec(context.Background(), `DELETE FROM albums WHERE id = $1`, f.album)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())

	assert.Zero(t, countRows(t, f.pool, `SELECT COUNT(*) FROM album_members WHERE album_id = $1`, f.album))
	assert.Empty(t, albumMediaIDs(t, f.pool, f.album))
	assert.Equal(t, 4, countRows(t, f.pool, `SELECT COUNT(*) FROM media`), "no media is deleted")
	assert.Equal(t, []string{f.mediaBob}, albumMediaIDs(t, f.pool, other), "other albums are untouched")
	assert.Equal(t, 1, countRows(t, f.pool, `SELECT COUNT(*) FROM album_members WHERE album_id = $1`, other))
}

func TestAlbumsSchema_DeletingMediaCascadesItsContributions(t *testing.T) {
	f := newAlbumFixture(t)
	contribute(t, f.pool, f.album, f.mediaAlice2, f.alice)
	other := createAlbum(t, f.pool, f.alice, "Alice's other")
	contribute(t, f.pool, other, f.mediaAlice, f.alice)

	tag, err := f.pool.Exec(context.Background(), `DELETE FROM media WHERE id = $1`, f.mediaAlice)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())

	assert.Equal(t, []string{f.mediaAlice2}, albumMediaIDs(t, f.pool, f.album))
	assert.Empty(t, albumMediaIDs(t, f.pool, other), "removed from every album")
	assert.Equal(t, 2, countRows(t, f.pool, `SELECT COUNT(*) FROM album_members WHERE album_id = $1`, f.album),
		"memberships stay")
}
