package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"codeberg.org/bvigh/geogallery/internal/geo"
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

// GET /api/media pages with OFFSET, so the order must be total: rows that tie on taken_at must
// not move between pages, or a client paging through everything sees some twice and some never.
func TestMediaList_OffsetPagesHoldEveryItemOnce(t *testing.T) {
	f := newSharingFixture(t)
	// B's own items and A's shared ones share their timestamps, as when two members upload the
	// same burst; neither tie group size divides the page size.
	own := insertBurst(t, f.pool, f.userB, 700, 7)
	shared := insertBurst(t, f.pool, f.userA, 700, 7)
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO album_media (album_id, media_id, owner_id)
		SELECT $1, id, owner_id FROM media WHERE id = ANY($2::uuid[])`, f.family, shared)
	require.NoError(t, err)

	want := sorted(append(append(append([]string{}, own...), shared...), f.mediaB, f.mediaA, f.mediaAUnlocated)...)
	var got []string
	for offset := 0; ; offset += 500 {
		rec := do(t, f.router, f.userB, http.MethodGet, fmt.Sprintf("/api/media?offset=%d", offset), "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		page := decodeIDs(t, rec)
		got = append(got, page...)
		if len(page) < 500 {
			break
		}
	}
	pages := map[string]int{}
	for _, id := range got {
		pages[id]++
	}
	var twice, never []string
	for _, id := range want {
		switch pages[id] {
		case 0:
			never = append(never, id)
		case 1:
		default:
			twice = append(twice, id)
		}
	}
	assert.Empty(t, twice, "items listed on more than one page")
	assert.Empty(t, never, "items listed on no page")
	assert.Len(t, got, len(want))

	// The map asks for large pages: one holds everything, in the same order as the small pages.
	rec := do(t, f.router, f.userB, http.MethodGet, "/api/media?limit=5000", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, got, decodeIDs(t, rec))
}
