package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlbumName(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"plain", "Iceland 2025", "Iceland 2025", false},
		{"trimmed", " \tIceland 2025\n ", "Iceland 2025", false},
		{"200 multi-byte characters", strings.Repeat("é", 200), strings.Repeat("é", 200), false},
		{"inner spaces kept", "a  b", "a  b", false},
		{"empty", "", "", true},
		{"only whitespace", "  \n\t", "", true},
		{"201 characters", strings.Repeat("x", 201), "", true},
		{"201 after trimming is still too long", " " + strings.Repeat("x", 201) + " ", "", true},
		{"NUL", "a\x00b", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := albumName(tt.raw)
			if tt.wantErr {
				assert.EqualError(t, err, "name must be 1-200 characters")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseAlbumMediaSelection(t *testing.T) {
	const (
		upper = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		lower = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	)
	ids := func(n int) *[]string {
		s := make([]string, n)
		for i := range s {
			s[i] = fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		}
		return &s
	}
	tests := []struct {
		name    string
		body    albumMediaBody
		want    albumMediaSelection
		wantErr string
	}{
		{"media ids canonicalised", albumMediaBody{MediaIDs: &[]string{upper}},
			albumMediaSelection{mediaIDs: []string{lower}}, ""},
		{"trip id canonicalised", albumMediaBody{TripID: ptr(upper)}, albumMediaSelection{tripID: lower}, ""},
		{"5000 media ids", albumMediaBody{MediaIDs: ids(5000)}, albumMediaSelection{mediaIDs: *ids(5000)}, ""},
		{"neither", albumMediaBody{}, albumMediaSelection{}, "body must have exactly one of media_ids and trip_id"},
		{"both", albumMediaBody{MediaIDs: &[]string{lower}, TripID: ptr(lower)}, albumMediaSelection{},
			"body must have exactly one of media_ids and trip_id"},
		{"empty media ids next to a trip id", albumMediaBody{MediaIDs: &[]string{}, TripID: ptr(lower)}, albumMediaSelection{},
			"body must have exactly one of media_ids and trip_id"},
		{"empty media ids", albumMediaBody{MediaIDs: &[]string{}}, albumMediaSelection{}, "media_ids must hold 1-5000 IDs"},
		{"5001 media ids", albumMediaBody{MediaIDs: ids(5001)}, albumMediaSelection{}, "media_ids must hold 1-5000 IDs"},
		{"malformed media id", albumMediaBody{MediaIDs: &[]string{lower, "nope"}}, albumMediaSelection{}, "invalid media_ids"},
		{"empty trip id", albumMediaBody{TripID: ptr("")}, albumMediaSelection{}, "invalid trip_id"},
		{"unassigned is not a trip here", albumMediaBody{TripID: ptr("unassigned")}, albumMediaSelection{}, "invalid trip_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAlbumMediaSelection(tt.body)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAlbumMediaSelection_CondAndArg(t *testing.T) {
	tests := []struct {
		name     string
		sel      albumMediaSelection
		n        int
		wantCond string
		wantArg  any
	}{
		{"media ids", albumMediaSelection{mediaIDs: []string{"a", "b"}}, 2, "m.id = ANY($2)", []string{"a", "b"}},
		{"trip", albumMediaSelection{tripID: "t"}, 3, "m.trip_id = $3", "t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantCond, tt.sel.cond(tt.n))
			assert.Equal(t, tt.wantArg, tt.sel.arg())
		})
	}
}

func TestParseAlbumMemberFilter(t *testing.T) {
	const (
		upper = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		lower = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		other = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	nineteen, twenty := challengeTestIDs(19), challengeTestIDs(20)
	tests := []struct {
		name    string
		values  []string // the member_id values, in order; nil leaves the key out
		want    []string
		wantErr string
	}{
		{"no filter", nil, []string{}, ""},
		{"canonicalised, in order", []string{other, upper}, []string{other, lower}, ""},
		{"a user named twice, in any spelling, counts once", []string{upper, lower, other, upper}, []string{lower, other}, ""},
		{"the caller's own ID is dropped", []string{strings.ToUpper(testOwnerID), other}, []string{other}, ""},
		{"only the caller: no filter", []string{testOwnerID}, []string{}, ""},
		{"19 other users", nineteen, nineteen, ""},
		{"19 other users and the caller", append([]string{testOwnerID}, nineteen...), nineteen, ""},
		{"19 other users, one named twice", append(append([]string{}, nineteen...), strings.ToUpper(nineteen[0])), nineteen, ""},

		{"malformed", []string{other, "nope"}, nil, "invalid member_id"},
		{"empty", []string{""}, nil, "invalid member_id"},
		{"unassigned is not a user", []string{"unassigned"}, nil, "invalid member_id"},
		{"20 other users", twenty, nil, "member_id may name at most 19 other users"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := url.Values{"owner": {"mine"}} // other keys are not this filter's
			if tt.values != nil {
				q["member_id"] = tt.values
			}
			got, err := parseAlbumMemberFilter(q, testOwnerID)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The handler has no database here: reaching it would panic, so each status comes from a check
// made before any query.
func TestAlbumHandler_ListRefusesBeforeTheDatabase(t *testing.T) {
	const valid = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	twenty := "member_id=" + strings.Join(challengeTestIDs(20), "&member_id=")
	tests := []struct {
		name     string
		target   string
		wantBody string
	}{
		{"a malformed member", "/api/albums?member_id=nope", "invalid member_id"},
		{"an empty member", "/api/albums?member_id=", "invalid member_id"},
		{"a malformed member after a valid one", "/api/albums?member_id=" + valid + "&member_id=nope", "invalid member_id"},
		{"20 other users", "/api/albums?" + twenty, "member_id may name at most 19 other users"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			(&AlbumHandler{}).List(rec, withTestUser(httptest.NewRequest(http.MethodGet, tt.target, nil)))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, tt.wantBody, strings.TrimSpace(rec.Body.String()))
		})
	}
}
