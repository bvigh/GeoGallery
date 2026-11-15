package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Runs every buildMediaFilter branch against PostGIS through both handlers that use it, so the
// generated SQL (aliases, placeholders, casts) is exercised for real, not just as strings.
func TestMediaFilters_ListAndRound(t *testing.T) {
	f := newOwnershipFixture(t)
	// A's located, unassigned video, the day after a2.
	a3 := insertMedia(t, f.pool, mediaFixture{owner: f.userA, filename: "a3.mp4", takenAt: "2024-05-04T09:00:00Z",
		lat: ptr(viennaLat), lng: ptr(viennaLng)})
	_, err := f.pool.Exec(context.Background(), `UPDATE media SET media_type = 'video' WHERE id = $1`, a3)
	require.NoError(t, err)
	budapestViennaKm := geo.HaversineKm(budapestLat, budapestLng, viennaLat, viennaLng)

	tests := []struct {
		name      string
		user      string
		query     string
		wantList  []string // ordered by taken_at DESC
		wantRound []string // the located subset a round may pick; empty means 404
		wantMaxKm float64
	}{
		{"no filters", f.userA, "",
			[]string{a3, f.mediaAUnlocated, f.mediaA}, []string{a3, f.mediaA}, budapestViennaKm},
		{"from_date", f.userA, "from_date=2024-05-03",
			[]string{a3, f.mediaAUnlocated}, []string{a3}, 1},
		{"to_date includes the whole day", f.userA, "to_date=2024-05-02",
			[]string{f.mediaA}, []string{f.mediaA}, 1},
		{"has_location", f.userA, "has_location=true",
			[]string{a3, f.mediaA}, []string{a3, f.mediaA}, budapestViennaKm},
		{"media_type", f.userA, "media_type=video",
			[]string{a3}, []string{a3}, 1},
		{"trip_id", f.userA, "trip_id=" + f.tripA,
			[]string{f.mediaA}, []string{f.mediaA}, 1},
		{"unassigned", f.userA, "trip_id=unassigned",
			[]string{a3, f.mediaAUnlocated}, []string{a3}, 1},
		{"trip_id or unassigned", f.userA, "trip_id=unassigned&trip_id=" + f.tripA,
			[]string{a3, f.mediaAUnlocated, f.mediaA}, []string{a3, f.mediaA}, budapestViennaKm},
		{"foreign trip_id matches nothing", f.userA, "trip_id=" + f.tripB,
			[]string{}, nil, 0},
		{"all filters combine with AND", f.userA, "from_date=2024-05-03&to_date=2024-05-03&media_type=photo&trip_id=unassigned",
			[]string{f.mediaAUnlocated}, nil, 0},
		{"B's own trip", f.userB, "trip_id=" + f.tripB,
			[]string{f.mediaB}, []string{f.mediaB}, 1},
		{"unassigned never matches another owner's media", f.userB, "trip_id=unassigned",
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
				assert.InEpsilon(t, tt.wantMaxKm, round.MaxDistKm, 0.01, "max_dist_km spans the filtered set only")
			}
		})
	}
}

// auth.CanonicalUUID accepts any character in the four separator positions, Postgres only a dash.
// The handlers must bind the canonical spelling they validated, or such an ID fails the cast (500).
func TestMediaFilters_BindCanonicalIDs(t *testing.T) {
	f := newSharingFixture(t)
	xSeparated := func(id string) string { return strings.ReplaceAll(id, "-", "x") }

	tests := []struct {
		name      string
		user      string
		query     string
		wantList  []string
		wantRound []string // empty means 404
	}{
		{"album_id", f.userB, "album_id=" + xSeparated(f.family),
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}},
		{"trip_id", f.userA, "trip_id=" + xSeparated(f.tripA),
			[]string{f.mediaA}, []string{f.mediaA}},
		{"trip_id with unassigned", f.userA, "trip_id=unassigned&trip_id=" + xSeparated(f.tripA),
			[]string{f.mediaAUnlocated, f.mediaA}, []string{f.mediaA}},
		{"trip_id or album_id", f.userB, "trip_id=" + xSeparated(f.tripB) + "&album_id=" + xSeparated(f.family),
			[]string{f.mediaB, f.mediaAUnlocated, f.mediaA}, []string{f.mediaB, f.mediaA}},
		{"unknown album", f.userB, "album_id=" + xSeparated(unknownUUID), []string{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, f.router, tt.user, http.MethodGet, "/api/media?"+tt.query, "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantList, decodeIDs(t, rec))

			rec = do(t, f.router, tt.user, http.MethodGet, "/api/game/round?"+tt.query, "")
			if len(tt.wantRound) == 0 {
				assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
				return
			}
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var round roundMedia
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &round))
			assert.Contains(t, tt.wantRound, round.ID)
		})
	}
}

// insertBurst inserts n photos owned by ownerID in bursts of burst items sharing one taken_at,
// as a camera's EXIF seconds do, and returns their IDs.
func insertBurst(t *testing.T, pool *pgxpool.Pool, ownerID string, n, burst int) []string {
	t.Helper()
	return queryIDs(t, pool, `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, taken_at)
		SELECT $1::uuid, 'burst-' || i || '.jpg', 'photo', 'media/' || $1::text || '/burst-' || i,
		       'hash-' || $1::text || '-burst-' || i,
		       '2024-06-01T12:00:00Z'::timestamptz + (i / $3::int) * INTERVAL '1 second'
		FROM generate_series(0, $2::int - 1) AS i
		RETURNING id::text`, ownerID, n, burst)
}

// insertUndated inserts n photos owned by ownerID with no taken_at and returns their IDs.
func insertUndated(t *testing.T, pool *pgxpool.Pool, ownerID string, n int) []string {
	t.Helper()
	return queryIDs(t, pool, `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash)
		SELECT $1::uuid, 'undated-' || i || '.jpg', 'photo', 'media/' || $1::text || '/undated-' || i,
		       'hash-' || $1::text || '-undated-' || i
		FROM generate_series(0, $2::int - 1) AS i
		RETURNING id::text`, ownerID, n)
}

// listOrder returns ids in the list's order as Postgres itself sorts them.
func listOrder(t *testing.T, pool *pgxpool.Pool, ids []string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id::text FROM media WHERE id = ANY($1::uuid[]) ORDER BY taken_at DESC NULLS LAST, id`, ids)
	require.NoError(t, err)
	ordered, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Len(t, ordered, len(ids))
	return ordered
}

// listPage asks for one page of GET /api/media and returns its IDs and the next page's cursor.
func listPage(t *testing.T, router http.Handler, user, query, cursor string) (ids []string, next string) {
	t.Helper()
	path := "/api/media?" + query
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	rec := do(t, router, user, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeIDs(t, rec), rec.Header().Get(nextCursorHeader)
}

// pageThrough follows the cursor from the first page to the last, as the frontend does, and
// returns every page's IDs.
func pageThrough(t *testing.T, router http.Handler, user, query string) [][]string {
	t.Helper()
	var pages [][]string
	cursor := ""
	for {
		page, next := listPage(t, router, user, query, cursor)
		pages = append(pages, page)
		if next == "" {
			return pages
		}
		require.NotEqual(t, cursor, next, "the cursor moves on")
		cursor = next
	}
}

// The order is total (taken_at DESC NULLS LAST, then id) and each page starts after the last
// item of the one before. Items that tie on taken_at, and the undated items after every dated
// one, must neither repeat nor go missing at a page boundary, wherever it falls.
func TestMediaList_CursorPagesHoldEveryItemOnce(t *testing.T) {
	f := newSharingFixture(t)
	// B's own items and A's shared ones share their timestamps, as when two members upload the
	// same burst; the tie groups of 7 divide none of the page sizes.
	own := insertBurst(t, f.pool, f.userB, 700, 7)
	shared := insertBurst(t, f.pool, f.userA, 700, 7)
	ownUndated := insertUndated(t, f.pool, f.userB, 5)
	sharedUndated := insertUndated(t, f.pool, f.userA, 6)
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO album_media (album_id, media_id, owner_id)
		SELECT $1, id, owner_id FROM media WHERE id = ANY($2::uuid[])`, f.family, slices.Concat(shared, sharedUndated))
	require.NoError(t, err)

	want := listOrder(t, f.pool, slices.Concat(own, shared, ownUndated, sharedUndated,
		[]string{f.mediaB, f.mediaA, f.mediaAUnlocated}))
	dated := len(want) - len(ownUndated) - len(sharedUndated)
	wantShared := listOrder(t, f.pool, slices.Concat(shared, sharedUndated, []string{f.mediaA, f.mediaAUnlocated}))

	tests := []struct {
		name  string
		query string
		want  []string
		pages int
	}{
		{"the map's page holds everything, without a cursor", "limit=5000", want, 1},
		{"pages that end inside tie groups", "limit=500", want, 3},
		{"a page that ends at the last dated item", fmt.Sprintf("limit=%d", dated), want, 2},
		{"a page that ends inside the undated items", fmt.Sprintf("limit=%d", dated+2), want, 2},
		{"a last page of one", fmt.Sprintf("limit=%d", len(want)-1), want, 2},
		{"exactly one page: no cursor", fmt.Sprintf("limit=%d", len(want)), want, 1},
		// The filters' arguments come before the cursor's.
		{"with filters", "limit=300&media_type=photo&owner=shared", wantShared, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pages := pageThrough(t, f.router, f.userB, tt.query)
			assert.Equal(t, tt.want, slices.Concat(pages...), "every item once, in the list's order")
			assert.Len(t, pages, tt.pages)
		})
	}
}

// A cursor is a position, not a count: items added or deleted before it move nothing after it,
// where an OFFSET would list an item twice or skip one.
func TestMediaList_CursorKeepsItsPlace(t *testing.T) {
	f := newSharingFixture(t)
	ctx := context.Background()
	insertBurst(t, f.pool, f.userB, 30, 3)
	insertUndated(t, f.pool, f.userB, 4)
	all, next := listPage(t, f.router, f.userB, "limit=5000", "")
	require.Empty(t, next)
	require.Len(t, all, 37, "B's burst, undated items and photo, and A's two shared ones")

	first, cursor := listPage(t, f.router, f.userB, "limit=10", "")
	require.Equal(t, all[:10], first)

	// Between the requests: a new item sorts first, an item on the first page goes, and so does
	// the item the cursor names.
	insertMedia(t, f.pool, mediaFixture{owner: f.userB, filename: "newest.jpg", takenAt: "2030-01-01T00:00:00Z"})
	_, err := f.pool.Exec(ctx, `DELETE FROM media WHERE id = ANY($1::uuid[])`, []string{first[0], first[9]})
	require.NoError(t, err)

	var rest [][]string
	for cursor != "" {
		var page []string
		page, cursor = listPage(t, f.router, f.userB, "limit=10", cursor)
		rest = append(rest, page)
	}
	assert.Equal(t, all[10:], slices.Concat(rest...))

	t.Run("among the undated items", func(t *testing.T) {
		all, _ := listPage(t, f.router, f.userB, "limit=5000", "")
		dated := len(all) - 4
		limit := fmt.Sprintf("limit=%d", dated+2)
		page, cursor := listPage(t, f.router, f.userB, limit, "")
		require.Equal(t, all[:dated+2], page, "the page ends after two undated items")

		// Undated items sort by ID: one goes before the cursor's, one after the last.
		_, err := f.pool.Exec(ctx, `
			INSERT INTO media (id, owner_id, filename, media_type, storage_key, file_hash) VALUES
			('00000000-0000-4000-8000-000000000000', $1, 'first.jpg', 'photo', 'media/first', 'hash-first'),
			('ffffffff-ffff-4fff-bfff-ffffffffffff', $1, 'last.jpg', 'photo', 'media/last', 'hash-last')`, f.userB)
		require.NoError(t, err)

		page, cursor = listPage(t, f.router, f.userB, limit, cursor)
		assert.Empty(t, cursor)
		assert.Equal(t, slices.Concat(all[dated+2:], []string{"ffffffff-ffff-4fff-bfff-ffffffffffff"}), page)
	})
}
