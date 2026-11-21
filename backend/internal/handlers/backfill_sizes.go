package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/bvigh/geogallery/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// objectSizer is what the backfill needs of the bucket (storage.Client.ObjectSize): the size of
// the object under a key, or an error wrapping storage.ErrNoObject when there is none.
type objectSizer interface {
	ObjectSize(ctx context.Context, key string) (int64, error)
}

type backfillDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// BackfillSizesDeps is what RunBackfillSizes needs.
type BackfillSizesDeps struct {
	db      backfillDB
	storage objectSizer
	page    int // rows listed at a time
	workers int // objects looked up at once
}

func NewBackfillSizesDeps(pool *pgxpool.Pool, s3 *storage.Client) BackfillSizesDeps {
	return BackfillSizesDeps{db: pool, storage: s3, page: 500, workers: 8}
}

// BackfillSizesResult counts what RunBackfillSizes did. A row deleted while it runs, or given a size
// by another run meanwhile, is checked and counted nowhere else.
type BackfillSizesResult struct {
	Checked int // rows without a size whose object was looked up
	Filled  int // rows given their object's size
	Missing int // rows the bucket holds no object for: their size stays unknown
	Failed  int // rows whose lookup or write failed: a re-run tries them again
}

// listUnsizedSQL lists the rows without a size after a given ID ($1, NULL for the first page), in
// ID order, so every row is listed once though failed rows keep no size.
const listUnsizedSQL = `
	SELECT id::text, owner_id::text, storage_key FROM media
	WHERE size_bytes IS NULL AND ($1::uuid IS NULL OR id > $1::uuid)
	ORDER BY id
	LIMIT $2`

// fillSizeSQL writes a size only where none is stored yet, and only to the row whose object was
// looked up (storage_key never changes), so it never overwrites the size ingest wrote.
const fillSizeSQL = `UPDATE media SET size_bytes = $3 WHERE id = $1 AND storage_key = $2 AND size_bytes IS NULL`

// rowExistsSQL tells a row deleted since it was listed, which takes its object along, from one
// the bucket has lost.
const rowExistsSQL = `SELECT EXISTS (SELECT 1 FROM media WHERE id = $1 AND storage_key = $2)`

type unsizedRow struct {
	id, ownerID, key string
}

type backfillOutcome int

const (
	backfillInterrupted backfillOutcome = iota // ctx ended first: not counted, a re-run looks again
	backfillFilled
	backfillGone // the row was deleted, or given a size, meanwhile
	backfillMissing
	backfillFailed
)

// RunBackfillSizes fills media.size_bytes from the bucket, with one HEAD request per object, for
// every user's rows stored before ingest recorded sizes (migration 17). It is `server
// backfill-sizes`; there is no HTTP route. A row whose object is gone keeps no size and is logged,
// a failed lookup or write is logged and left for a re-run, and nothing else is written, so a
// re-run only looks at what is still unknown. It stops when ctx ends, after the lookups in flight.
func RunBackfillSizes(ctx context.Context, deps BackfillSizesDeps) (BackfillSizesResult, error) {
	var res BackfillSizesResult
	var after *string
	for {
		rows, err := listUnsized(ctx, deps.db, after, deps.page)
		if err != nil {
			return res, fmt.Errorf("list rows without a size: %w", err)
		}
		if len(rows) == 0 {
			return res, nil
		}
		for _, o := range sizeObjects(ctx, deps, rows) {
			switch o {
			case backfillInterrupted:
				continue
			case backfillFilled:
				res.Filled++
			case backfillMissing:
				res.Missing++
			case backfillFailed:
				res.Failed++
			}
			res.Checked++
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
		slog.Info("backfill sizes: progress", "checked", res.Checked, "filled", res.Filled, "missing", res.Missing, "failed", res.Failed)
		after = &rows[len(rows)-1].id
	}
}

func listUnsized(ctx context.Context, db backfillDB, after *string, limit int) ([]unsizedRow, error) {
	rows, err := db.Query(ctx, listUnsizedSQL, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []unsizedRow
	for rows.Next() {
		var r unsizedRow
		if err := rows.Scan(&r.id, &r.ownerID, &r.key); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sizeObjects fills the size of each row, deps.workers at a time, and returns each row's outcome.
func sizeObjects(ctx context.Context, deps BackfillSizesDeps, rows []unsizedRow) []backfillOutcome {
	outcomes := make([]backfillOutcome, len(rows))
	slots := make(chan struct{}, max(deps.workers, 1))
	var wg sync.WaitGroup
	for i, row := range rows {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		// select picks either case when both are ready, so the context is checked after it.
		if ctx.Err() != nil {
			break // the rest stay backfillInterrupted
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			outcomes[i] = fillSize(ctx, deps, row)
		}()
	}
	wg.Wait()
	return outcomes
}

// fillSize looks up one row's object and writes its size.
func fillSize(ctx context.Context, deps BackfillSizesDeps, row unsizedRow) backfillOutcome {
	size, err := deps.storage.ObjectSize(ctx, row.key)
	switch {
	case ctx.Err() != nil:
		return backfillInterrupted
	case errors.Is(err, storage.ErrNoObject):
		var exists bool
		if err := deps.db.QueryRow(ctx, rowExistsSQL, row.id, row.key).Scan(&exists); err != nil {
			if ctx.Err() != nil {
				return backfillInterrupted
			}
			slog.Error("backfill sizes: check whether the row still exists failed", "media_id", row.id, "err", err)
			return backfillFailed
		}
		if !exists {
			return backfillGone
		}
		slog.Error("backfill sizes: the bucket holds no object for this row; its size stays unknown",
			"media_id", row.id, "owner_id", row.ownerID, "key", row.key)
		return backfillMissing
	case err != nil:
		slog.Error("backfill sizes: object lookup failed", "media_id", row.id, "key", row.key, "err", err)
		return backfillFailed
	}
	tag, err := deps.db.Exec(ctx, fillSizeSQL, row.id, row.key, size)
	switch {
	case err != nil && ctx.Err() != nil:
		return backfillInterrupted
	case err != nil:
		slog.Error("backfill sizes: write failed", "media_id", row.id, "err", err)
		return backfillFailed
	case tag.RowsAffected() == 0:
		return backfillGone
	}
	return backfillFilled
}
