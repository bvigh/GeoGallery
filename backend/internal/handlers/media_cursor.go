package handlers

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
)

// nextCursorHeader is the response header of GET /api/media holding the cursor of the next page.
// The last page has none.
const nextCursorHeader = "X-Next-Cursor"

// mediaCursor is where a page of GET /api/media starts: the sort key of the last item of the page
// before it. The list is ordered by taken_at DESC NULLS LAST, id, so a dated item is followed by
// the items taken earlier, the items taken at the same instant with a greater ID, then every
// undated item, and an undated item by the undated items with a greater ID.
//
// It is a position, not a count: an item added or deleted on an earlier page moves no item across
// a page boundary, where an OFFSET would list one twice or skip one.
type mediaCursor struct {
	takenAt *time.Time // nil for an undated item
	id      string     // canonical UUID
}

// String is the cursor as the header carries it, which clients pass back without reading: base64url
// of the time in RFC 3339 UTC with its fraction (empty for an undated item), a space and the ID.
// UTC, because an offset with seconds (a zone's local mean time before 1900) would be cut to minutes.
func (c mediaCursor) String() string {
	var at string
	if c.takenAt != nil {
		at = c.takenAt.UTC().Format(time.RFC3339Nano)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(at + " " + c.id))
}

var errInvalidCursor = errors.New("invalid cursor")

// parseMediaCursor reads a cursor that String wrote, so that a crafted one is a 400 rather than a
// query error. RFC 3339 has four-digit years, which Postgres can always compare with, and ingest
// stores no time outside the UTC years 1-9998, so every cursor String writes reads back.
func parseMediaCursor(s string) (mediaCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return mediaCursor{}, errInvalidCursor
	}
	at, rawID, ok := strings.Cut(string(raw), " ")
	if !ok {
		return mediaCursor{}, errInvalidCursor
	}
	id, ok := auth.CanonicalUUID(rawID)
	if !ok {
		return mediaCursor{}, errInvalidCursor
	}
	c := mediaCursor{id: id}
	if at != "" {
		t, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return mediaCursor{}, errInvalidCursor
		}
		c.takenAt = &t
	}
	return c, nil
}
