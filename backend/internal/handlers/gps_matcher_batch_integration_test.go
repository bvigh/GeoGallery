package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bracketingPointsMany sends bracketingPoints' own two queries, so on real rows it must return
// exactly what bracketingPoints does, lookup by lookup, across batch boundaries.
func TestBracketingPointsMany_EqualsBracketingPoints(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	owner := createUser(t, pool, "owner")
	stranger := createUser(t, pool, "stranger")

	start := provTime
	insertTrackSeries(t, pool, owner, "gpx:a.gpx", start, 7*time.Minute, 40)
	insertTrackSeries(t, pool, owner, "nmea:b.nmea", start.Add(3*time.Minute), 11*time.Minute, 20)
	// Ties: every other gpx instant has an esoko point too, so each side must break them by source.
	insertTrackSeries(t, pool, owner, "esoko:c.log", start, 14*time.Minute, 20)
	insertTrackSeries(t, pool, owner, "exif", start.Add(30*time.Second), 23*time.Minute, 10)
	insertTrackSeries(t, pool, owner, "manual", start.Add(90*time.Second), 41*time.Minute, 5)
	// A second stretch three hours later, so lookups between the two find an empty side or none.
	insertTrackSeries(t, pool, owner, "gpx:a.gpx", start.Add(8*time.Hour), 5*time.Minute, 10)
	// Another user's points everywhere: neither function may see them.
	insertTrackSeries(t, pool, stranger, "gpx:near.gpx", start.Add(-2*time.Hour), time.Minute, 720)

	options := []bracketOptions{
		{},
		{excludeFormats: []string{}},
		{excludeFormats: []string{formatExif, formatManual}},
		{excludeFormats: []string{"gpx"}},
		{excludeFormats: []string{"gpx", "nmea", "esoko"}},
	}
	var lookups []bracketLookup
	for i := range 2*bracketBatchLookups + 101 {
		// From two hours before the first point to well past the last, in steps that land on points too.
		at := start.Add(-2*time.Hour + time.Duration(i)*42*time.Second)
		lookups = append(lookups, bracketLookup{at: at, opts: options[i%len(options)]})
	}
	for i := range 20 {
		lookups = append(lookups, bracketLookup{at: start.Add(time.Duration(i) * 14 * time.Minute)}) // exactly on a tie
	}

	got, err := bracketingPointsMany(ctx, pool, owner, lookups, time.Hour)
	require.NoError(t, err)
	require.Len(t, got, len(lookups))

	var noBefore, noAfter, neither, ties int
	for i, l := range lookups {
		before, after, err := bracketingPoints(ctx, pool, owner, l.at, time.Hour, l.opts)
		require.NoError(t, err)
		assert.Equal(t, before, got[i].before, "lookup %d before", i)
		assert.Equal(t, after, got[i].after, "lookup %d after", i)

		switch {
		case before == nil && after == nil:
			neither++
		case before == nil:
			noBefore++
		case after == nil:
			noAfter++
		}
		if before != nil && before.At.Equal(l.at) && before.Source == "esoko:c.log" {
			ties++
		}
		for _, p := range []*geo.Point{got[i].before, got[i].after} {
			if p != nil {
				assert.NotEqual(t, "gpx:near.gpx", p.Source, "never another user's point")
			}
		}
	}
	// The fixture must really exercise every case it is meant to.
	assert.Positive(t, noBefore, "lookups with nothing before them")
	assert.Positive(t, noAfter, "lookups with nothing after them")
	assert.Positive(t, neither, "lookups with nothing in the window")
	assert.Positive(t, ties, "lookups on a tie, broken by source")
}
