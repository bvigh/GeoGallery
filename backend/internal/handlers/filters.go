package handlers

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
)

// validateMediaFilters rejects from_date/to_date, trip_id and album_id values that Postgres would
// fail to cast, so malformed input is a 400 rather than a query error. It also rejects an owner
// value other than mine or shared, and a has_location value other than true.
func validateMediaFilters(q url.Values) error {
	for _, key := range []string{"from_date", "to_date"} {
		if v := q.Get(key); v != "" {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return fmt.Errorf("invalid %s: want YYYY-MM-DD", key)
			}
		}
	}
	for _, id := range q["trip_id"] {
		if id == "unassigned" {
			continue
		}
		if _, ok := auth.CanonicalUUID(id); !ok {
			return errors.New("invalid trip_id")
		}
	}
	for _, id := range q["album_id"] {
		if _, ok := auth.CanonicalUUID(id); !ok {
			return errors.New("invalid album_id")
		}
	}
	switch q.Get("owner") {
	case "", "mine", "shared":
	default:
		return errors.New("invalid owner: want mine or shared")
	}
	switch q.Get("has_location") {
	case "", "true":
	default:
		return errors.New("invalid has_location: want true")
	}
	return nil
}

// buildMediaFilter turns the filters shared by /api/media and /api/game/round into WHERE
// conditions on media aliased as m, for the caller to AND together. conds[0] is the read-access
// check and $1 is userID. Trips and albums are one list of collections: when both are given, an
// item matches if it is in any of them. Run validateMediaFilters first: values are passed as args
// unchecked, except that trip and album IDs are bound in canonical form.
func buildMediaFilter(q url.Values, userID string) (conds []string, args []any) {
	conds = []string{readableMediaCond("m", 1)}
	args = []any{userID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if v := q.Get("from_date"); v != "" {
		conds = append(conds, "m.taken_at >= "+arg(v)+"::date")
	}
	if v := q.Get("to_date"); v != "" {
		conds = append(conds, "m.taken_at < ("+arg(v)+"::date + INTERVAL '1 day')")
	}
	if mediaTypes := q["media_type"]; len(mediaTypes) > 0 {
		conds = append(conds, "m.media_type = ANY("+arg(mediaTypes)+")")
	}
	var collections []string
	if tripIDs := q["trip_id"]; len(tripIDs) > 0 {
		var hasUnassigned bool
		var uuids []string
		for _, id := range tripIDs {
			if id == "unassigned" {
				hasUnassigned = true
			} else {
				uuids = append(uuids, canonicalID(id))
			}
		}
		var trip string
		switch {
		case hasUnassigned && len(uuids) > 0:
			trip = "(m.trip_id = ANY(" + arg(uuids) + ") OR m.trip_id IS NULL)"
		case hasUnassigned:
			trip = "m.trip_id IS NULL"
		default:
			trip = "m.trip_id = ANY(" + arg(uuids) + ")"
		}
		// Trips are personal: without the owner check, "unassigned" would match every readable
		// item someone else owns. Parenthesised so it stays one condition when ORed with albums.
		collections = append(collections, "(m.owner_id = $1 AND "+trip+")")
	}
	if albumIDs := q["album_id"]; len(albumIDs) > 0 {
		uuids := make([]string, len(albumIDs))
		for i, id := range albumIDs {
			uuids[i] = canonicalID(id)
		}
		// The membership join makes an album the caller does not belong to match nothing, even
		// when its media are readable through another album.
		collections = append(collections, "EXISTS (SELECT 1 FROM album_media am"+
			" JOIN album_members mem ON mem.album_id = am.album_id AND mem.user_id = $1"+
			" WHERE am.media_id = m.id AND am.album_id = ANY("+arg(uuids)+"))")
	}
	switch len(collections) {
	case 1:
		conds = append(conds, collections[0])
	case 2:
		conds = append(conds, "("+strings.Join(collections, " OR ")+")")
	}
	switch q.Get("owner") {
	case "mine":
		conds = append(conds, "m.owner_id = $1")
	case "shared":
		conds = append(conds, "m.owner_id <> $1")
	}
	if q.Get("has_location") == "true" {
		conds = append(conds, "m.location IS NOT NULL")
	}
	return conds, args
}

// canonicalID returns the canonical spelling of an ID that validateMediaFilters accepted.
// auth.CanonicalUUID takes any character as a separator, but Postgres only takes a dash, so
// binding the raw value would turn an accepted ID into a query error.
func canonicalID(id string) string {
	c, _ := auth.CanonicalUUID(id)
	return c
}
