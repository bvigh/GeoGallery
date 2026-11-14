package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertClockRule stores one camera clock rule and returns its ID. Dates are "2006-01-02" or nil.
func insertClockRule(t *testing.T, pool *pgxpool.Pool, ownerID, cameraMake, model string, offsetMin int, startsOn, endsOn *string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO camera_clock_rules (owner_id, make, model, utc_offset_min, starts_on, ends_on)
		VALUES ($1, $2, $3, $4, $5::date, $6::date) RETURNING id::text`,
		ownerID, cameraMake, model, offsetMin, startsOn, endsOn).Scan(&id))
	return id
}

func TestLoadClockRules(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	owner := createUser(t, pool, "clock-owner")
	other := createUser(t, pool, "clock-other")
	empty := createUser(t, pool, "clock-empty")

	// Inserted out of order: the result is ordered by camera without case, then by first day with
	// no start first.
	tz200From := insertClockRule(t, pool, owner, "Panasonic", "DC-TZ200", 60, ptr("2023-01-01"), nil)
	oneRS := insertClockRule(t, pool, owner, "Arashi Vision", "Insta360 OneRS", 60, ptr("2022-06-01"), ptr("2022-12-31"))
	tz200Until := insertClockRule(t, pool, owner, "panasonic", "DC-TZ200", 120, nil, ptr("2022-12-31"))
	insertClockRule(t, pool, other, "Panasonic", "DC-TZ200", -300, nil, nil)

	rules, err := loadClockRules(ctx, pool, owner)
	require.NoError(t, err)

	assert.Equal(t, ingest.ClockRules{
		{ID: oneRS, Make: "Arashi Vision", Model: "Insta360 OneRS", OffsetMin: 60, StartsOn: ruleDay(2022, 6, 1), EndsOn: ruleDay(2022, 12, 31)},
		{ID: tz200Until, Make: "panasonic", Model: "DC-TZ200", OffsetMin: 120, EndsOn: ruleDay(2022, 12, 31)},
		{ID: tz200From, Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 60, StartsOn: ruleDay(2023, 1, 1)},
	}, rules, "the owner's rules only, in order, case as entered")
	for _, r := range rules {
		for _, d := range []*time.Time{r.StartsOn, r.EndsOn} {
			if d != nil {
				assert.Equal(t, time.UTC, d.Location(), "a day is midnight UTC")
			}
		}
	}

	t.Run("they match the owner's camera on the camera's own date", func(t *testing.T) {
		got, ok := rules.Match("Panasonic", "DC-TZ200", time.Date(2022, 12, 31, 23, 59, 59, 0, time.UTC))
		require.True(t, ok)
		assert.Equal(t, tz200Until, got.ID)
		got, ok = rules.Match("PANASONIC", "dc-tz200", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
		require.True(t, ok)
		assert.Equal(t, tz200From, got.ID)
	})

	t.Run("another owner's rule is never returned", func(t *testing.T) {
		otherRules, err := loadClockRules(ctx, pool, other)
		require.NoError(t, err)
		require.Len(t, otherRules, 1)
		assert.Equal(t, -300, otherRules[0].OffsetMin)
		assert.Nil(t, otherRules[0].StartsOn)
		assert.Nil(t, otherRules[0].EndsOn)
	})

	t.Run("an owner without rules gets none", func(t *testing.T) {
		got, err := loadClockRules(ctx, pool, empty)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a transaction serves too", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		got, err := loadClockRules(ctx, tx, owner)
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})
}
