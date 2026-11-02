package handlers

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateMediaFilters(t *testing.T) {
	const tripID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const albumID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	tests := []struct {
		name    string
		query   string
		wantErr string // empty: valid
	}{
		{"no filters", "", ""},
		{"dates", "from_date=2024-03-01&to_date=2024-03-31", ""},
		{"empty dates are ignored", "from_date=&to_date=", ""},
		{"trip uuid and unassigned", "trip_id=unassigned&trip_id=" + tripID, ""},
		{"media_type is not validated", "media_type=anything", ""},
		{"bad from_date", "from_date=garbage", "invalid from_date: want YYYY-MM-DD"},
		{"impossible to_date", "to_date=2024-13-01", "invalid to_date: want YYYY-MM-DD"},
		{"datetime instead of date", "from_date=2024-03-01T00:00:00Z", "invalid from_date: want YYYY-MM-DD"},
		{"non-uuid trip_id", "trip_id=unassigned&trip_id=nope", "invalid trip_id"},
		{"x-separated trip_id is accepted and bound canonically", "trip_id=aaaaaaaaxaaaax4aaax8aaaxaaaaaaaaaaaa", ""},
		{"empty trip_id", "trip_id=", "invalid trip_id"},
		{"album uuids", "album_id=" + albumID + "&album_id=" + tripID, ""},
		{"albums with trips", "album_id=" + albumID + "&trip_id=unassigned", ""},
		{"non-uuid album_id", "album_id=" + albumID + "&album_id=nope", "invalid album_id"},
		{"unassigned is not an album", "album_id=unassigned", "invalid album_id"},
		{"empty album_id", "album_id=", "invalid album_id"},
		{"x-separated album_id is accepted and bound canonically", "album_id=ccccccccxccccx4cccx8cccxcccccccccccc", ""},
		{"owner mine", "owner=mine", ""},
		{"owner shared", "owner=shared", ""},
		{"empty owner means all", "owner=", ""},
		{"owner all is not a value", "owner=all", "invalid owner: want mine or shared"},
		{"owner is case-sensitive", "owner=Mine", "invalid owner: want mine or shared"},
		{"owner user id", "owner=" + testOwnerID, "invalid owner: want mine or shared"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			require.NoError(t, err)

			err = validateMediaFilters(q)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
		})
	}
}

func TestBuildMediaFilter(t *testing.T) {
	const tripA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const tripB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	const albumA = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	const albumB = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	readable := readableMediaCond("m", 1)
	inAlbums := func(n string) string {
		return "EXISTS (SELECT 1 FROM album_media am" +
			" JOIN album_members mem ON mem.album_id = am.album_id AND mem.user_id = $1" +
			" WHERE am.media_id = m.id AND am.album_id = ANY(" + n + "))"
	}

	tests := []struct {
		name      string
		query     string
		wantConds []string
		wantArgs  []any
	}{
		{"no filters", "", []string{readable}, []any{testOwnerID}},
		{"empty dates are ignored", "from_date=&to_date=", []string{readable}, []any{testOwnerID}},
		{"from_date", "from_date=2024-03-01",
			[]string{readable, "m.taken_at >= $2::date"},
			[]any{testOwnerID, "2024-03-01"}},
		{"to_date includes the whole day", "to_date=2024-03-31",
			[]string{readable, "m.taken_at < ($2::date + INTERVAL '1 day')"},
			[]any{testOwnerID, "2024-03-31"}},
		{"media types", "media_type=photo&media_type=video_360",
			[]string{readable, "m.media_type = ANY($2)"},
			[]any{testOwnerID, []string{"photo", "video_360"}}},
		{"trip ids are owner-scoped", "trip_id=" + tripA + "&trip_id=" + tripB,
			[]string{readable, "(m.owner_id = $1 AND m.trip_id = ANY($2))"},
			[]any{testOwnerID, []string{tripA, tripB}}},
		{"unassigned is owner-scoped and takes no arg", "trip_id=unassigned",
			[]string{readable, "(m.owner_id = $1 AND m.trip_id IS NULL)"},
			[]any{testOwnerID}},
		{"trip ids plus unassigned", "trip_id=unassigned&trip_id=" + tripA,
			[]string{readable, "(m.owner_id = $1 AND (m.trip_id = ANY($2) OR m.trip_id IS NULL))"},
			[]any{testOwnerID, []string{tripA}}},
		{"album ids need the caller's membership", "album_id=" + albumA + "&album_id=" + albumB,
			[]string{readable, inAlbums("$2")},
			[]any{testOwnerID, []string{albumA, albumB}}},
		{"trips and albums are a union", "album_id=" + albumA + "&trip_id=" + tripA,
			[]string{readable, "((m.owner_id = $1 AND m.trip_id = ANY($2)) OR " + inAlbums("$3") + ")"},
			[]any{testOwnerID, []string{tripA}, []string{albumA}}},
		{"unassigned or an album", "trip_id=unassigned&album_id=" + albumA,
			[]string{readable, "((m.owner_id = $1 AND m.trip_id IS NULL) OR " + inAlbums("$2") + ")"},
			[]any{testOwnerID, []string{albumA}}},
		// CanonicalUUID accepts these spellings and Postgres rejects the first: only the canonical
		// form may reach the query.
		{"trip and album ids are bound canonically",
			"trip_id=AAAAAAAAxAAAAx4AAAx8AAAxAAAAAAAAAAAA&trip_id=unassigned&album_id=ccccccccxccccx4cccx8cccxcccccccccccc" +
				"&album_id=DDDDDDDDDDDD4DDD8DDDDDDDDDDDDDDD",
			[]string{readable, "((m.owner_id = $1 AND (m.trip_id = ANY($2) OR m.trip_id IS NULL)) OR " + inAlbums("$3") + ")"},
			[]any{testOwnerID, []string{tripA}, []string{albumA, albumB}}},
		{"owner mine", "owner=mine",
			[]string{readable, "m.owner_id = $1"},
			[]any{testOwnerID}},
		{"owner shared", "owner=shared",
			[]string{readable, "m.owner_id <> $1"},
			[]any{testOwnerID}},
		{"empty owner means all", "owner=", []string{readable}, []any{testOwnerID}},
		{"owner narrows the collections with AND", "owner=shared&album_id=" + albumA,
			[]string{readable, inAlbums("$2"), "m.owner_id <> $1"},
			[]any{testOwnerID, []string{albumA}}},
		{"all filters number their args in a fixed order",
			"owner=mine&album_id=" + albumA + "&trip_id=" + tripA + "&media_type=photo&to_date=2024-03-31&from_date=2024-03-01",
			[]string{
				readable,
				"m.taken_at >= $2::date",
				"m.taken_at < ($3::date + INTERVAL '1 day')",
				"m.media_type = ANY($4)",
				"((m.owner_id = $1 AND m.trip_id = ANY($5)) OR " + inAlbums("$6") + ")",
				"m.owner_id = $1",
			},
			[]any{testOwnerID, "2024-03-01", "2024-03-31", []string{"photo"}, []string{tripA}, []string{albumA}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			require.NoError(t, err)

			conds, args := buildMediaFilter(q, testOwnerID)
			assert.Equal(t, tt.wantConds, conds)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}
