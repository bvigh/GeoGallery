package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingBatchDB keeps every batch it is sent and answers each queued query with rowFor.
type recordingBatchDB struct {
	batches  []*pgx.Batch
	rowFor   func(q *pgx.QueuedQuery) pgx.Row
	closeErr error
	closed   int
}

func (d *recordingBatchDB) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	d.batches = append(d.batches, b)
	return &recordedBatchResults{db: d, queued: b.QueuedQueries}
}

type recordedBatchResults struct {
	db     *recordingBatchDB
	queued []*pgx.QueuedQuery
	next   int
}

func (r *recordedBatchResults) QueryRow() pgx.Row {
	q := r.queued[r.next]
	r.next++
	return r.db.rowFor(q)
}

func (r *recordedBatchResults) Close() error {
	r.db.closed++
	return r.db.closeErr
}

func (r *recordedBatchResults) Exec() (pgconn.CommandTag, error) {
	panic("bracket lookups only read rows")
}
func (r *recordedBatchResults) Query() (pgx.Rows, error) { panic("bracket lookups only read rows") }

// sideRow answers a bracket query with a point at the lookup's instant named after its side, and
// the after side of every third lookup with no row.
func sideRow(q *pgx.QueuedQuery) pgx.Row {
	at := q.Arguments[0].(time.Time)
	if q.SQL == bracketAfterSQL {
		if at.Unix()%3 == 0 {
			return noRowsRow{}
		}
		return trackPointRow{recordedAt: at, source: "after"}
	}
	return trackPointRow{recordedAt: at, source: "before"}
}

func TestBracketingPointsMany_QueuesBracketingPointsQueries(t *testing.T) {
	const owner = "owner-id"
	exclude := []string{"exif", "manual"}
	lookups := make([]bracketLookup, 2*bracketBatchLookups+1)
	for i := range lookups {
		lookups[i] = bracketLookup{at: checksAt.Add(time.Duration(i) * time.Second)}
		if i%2 == 1 {
			lookups[i].opts = bracketOptions{excludeFormats: exclude}
		}
	}
	db := &recordingBatchDB{rowFor: sideRow}

	got, err := bracketingPointsMany(context.Background(), db, owner, lookups, time.Hour)

	require.NoError(t, err)
	require.Len(t, db.batches, 3, "two full batches and one of a single lookup")
	assert.Equal(t, 2*bracketBatchLookups, db.batches[0].Len())
	assert.Equal(t, 2, db.batches[2].Len())
	assert.Equal(t, 3, db.closed, "every batch is closed")

	var queued []*pgx.QueuedQuery
	for _, b := range db.batches {
		queued = append(queued, b.QueuedQueries...)
	}
	require.Len(t, got, len(lookups))
	for i, l := range lookups {
		wantExclude := []string{}
		if l.opts.excludeFormats != nil {
			wantExclude = exclude
		}
		before, after := queued[2*i], queued[2*i+1]
		assert.Equal(t, bracketBeforeSQL, before.SQL, i)
		assert.Equal(t, []any{l.at, l.at.Add(-time.Hour), owner, wantExclude}, before.Arguments, "lookup %d before", i)
		assert.Equal(t, bracketAfterSQL, after.SQL, i)
		assert.Equal(t, []any{l.at, l.at.Add(time.Hour), owner, wantExclude}, after.Arguments, "lookup %d after", i)

		require.NotNil(t, got[i].before, i)
		assert.Equal(t, "before", got[i].before.Source)
		assert.Equal(t, l.at, got[i].before.At, "results in the order of lookups")
		if l.at.Unix()%3 == 0 {
			assert.Nil(t, got[i].after, "no row is a nil point")
		} else {
			require.NotNil(t, got[i].after, i)
			assert.Equal(t, l.at, got[i].after.At)
		}
	}
}

func TestBracketingPointsMany_Errors(t *testing.T) {
	boom := errors.New("boom")
	lookups := []bracketLookup{{at: checksAt}, {at: checksAt.Add(time.Second)}}

	t.Run("a read error is returned, and the batch closed", func(t *testing.T) {
		reads := 0
		db := &recordingBatchDB{rowFor: func(q *pgx.QueuedQuery) pgx.Row {
			if reads++; reads == 3 {
				return errRow{boom}
			}
			return sideRow(q)
		}}

		got, err := bracketingPointsMany(context.Background(), db, "owner", lookups, time.Hour)

		assert.ErrorIs(t, err, boom)
		assert.Nil(t, got)
		assert.Equal(t, 1, db.closed)
	})

	t.Run("an error closing the batch is returned", func(t *testing.T) {
		db := &recordingBatchDB{rowFor: sideRow, closeErr: boom}

		_, err := bracketingPointsMany(context.Background(), db, "owner", lookups, time.Hour)

		assert.ErrorIs(t, err, boom)
	})

	t.Run("no lookups send nothing", func(t *testing.T) {
		db := &recordingBatchDB{rowFor: sideRow}

		got, err := bracketingPointsMany(context.Background(), db, "owner", nil, time.Hour)

		require.NoError(t, err)
		assert.Empty(t, got)
		assert.Empty(t, db.batches)
	})
}
