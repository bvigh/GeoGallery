package handlers

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
)

// validateMediaFilters rejects from_date/to_date and trip_id values that Postgres would fail to cast,
// so malformed input is a 400 rather than a query error.
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
	return nil
}
