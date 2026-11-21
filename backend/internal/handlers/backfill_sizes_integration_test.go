package handlers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/bvigh/geogallery/internal/storage"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSizes is a bucket for the backfill: the size of each key in sizes, ErrNoObject for a key it
// lacks, and errs[key] for a lookup that fails. before runs as a key is looked up, before the
// answer, so a test can change the database meanwhile.
type fakeSizes struct {
	mu     sync.Mutex
	sizes  map[string]int64
	errs   map[string]error
	before func(key string)
	asked  []string
}

func (f *fakeSizes) ObjectSize(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	f.asked = append(f.asked, key)
	before := f.before
	f.mu.Unlock()
	if before != nil {
		before(key)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[key]; err != nil {
		return 0, err
	}
	size, ok := f.sizes[key]
	if !ok {
		return 0, fmt.Errorf("head %s: %w", key, storage.ErrNoObject)
	}
	return size, nil
}

func (f *fakeSizes) askedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := slices.Clone(f.asked)
	slices.Sort(keys)
	return keys
}

// unsized stores an item of ownerID's without a size, as ingest did before migration 17, and
// returns its ID and storage key.
func unsized(t *testing.T, pool *pgxpool.Pool, ownerID, filename string) (id, key string) {
	t.Helper()
	id = insertMedia(t, pool, mediaFixture{owner: ownerID, filename: filename, takenAt: "2024-05-01T12:00:00Z"})
	return id, "media/" + ownerID + "/" + filename
}

// sizeOf is the row's size_bytes, nil when it is unknown, and false when the row is gone.
func sizeOf(t *testing.T, pool *pgxpool.Pool, id string) (*int64, bool) {
	t.Helper()
	var size *int64
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*), max(size_bytes) FROM media WHERE id = $1`, id).Scan(&n, &size))
	return size, n == 1
}

func deleteMediaRow(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `DELETE FROM media WHERE storage_key = $1`, key)
	require.NoError(t, err)
}

func TestRunBackfillSizes_Integration(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	alice := createUser(t, pool, "alice")
	bob := createUser(t, pool, "bob")

	photo, photoKey := unsized(t, pool, alice, "photo.jpg")
	emptyFile, emptyKey := unsized(t, pool, alice, "empty.jpg")
	lost, lostKey := unsized(t, pool, alice, "lost.jpg")
	flaky, flakyKey := unsized(t, pool, alice, "flaky.jpg")
	deleted, deletedKey := unsized(t, pool, alice, "deleted.jpg")
	video, videoKey := unsized(t, pool, bob, "video.mp4")
	deletedLate, deletedLateKey := unsized(t, pool, bob, "deleted-late.mp4")
	raced, racedKey := unsized(t, pool, bob, "raced.mp4")
	ingested := sizedMedia(t, pool, alice, "ingested.jpg", nil, ptr[int64](55))

	bucket := &fakeSizes{
		sizes: map[string]int64{
			photoKey: 1000, emptyKey: 0, videoKey: 5 << 30, deletedLateKey: 777, racedKey: 888,
			"media/" + alice + "/ingested.jpg": 66,
		},
		errs: map[string]error{flakyKey: errors.New("connection reset by peer")},
	}
	bucket.before = func(key string) {
		switch key {
		case deletedKey: // deleted with its object, as DELETE /api/media/{id} does
			deleteMediaRow(t, pool, key)
		case deletedLateKey: // deleted after the lookup found the object
			deleteMediaRow(t, pool, key)
		case racedKey: // given a size by another run meanwhile
			_, err := pool.Exec(ctx, `UPDATE media SET size_bytes = 999 WHERE storage_key = $1`, key)
			require.NoError(t, err)
		}
	}
	// Two rows per page and three lookups at once, so the run crosses pages and workers.
	deps := BackfillSizesDeps{db: pool, storage: bucket, page: 2, workers: 3}

	res, err := RunBackfillSizes(ctx, deps)
	require.NoError(t, err)
	assert.Equal(t, BackfillSizesResult{Checked: 8, Filled: 3, Missing: 1, Failed: 1}, res)

	want := []string{photoKey, emptyKey, lostKey, flakyKey, deletedKey, videoKey, deletedLateKey, racedKey}
	slices.Sort(want)
	assert.Equal(t, want, bucket.askedKeys(), "every row without a size once, every owner's, never a sized one")

	for id, wantSize := range map[string]*int64{
		photo: ptr[int64](1000), emptyFile: ptr[int64](0), video: ptr[int64](5 << 30),
		lost: nil, flaky: nil, ingested: ptr[int64](55), raced: ptr[int64](999),
	} {
		size, ok := sizeOf(t, pool, id)
		require.True(t, ok, id)
		assert.Equal(t, wantSize, size, id)
	}
	for _, id := range []string{deleted, deletedLate} {
		_, ok := sizeOf(t, pool, id)
		assert.False(t, ok, "%s stays deleted", id)
	}

	t.Run("a re-run looks only at the rows still without a size", func(t *testing.T) {
		bucket.mu.Lock()
		bucket.asked = nil
		delete(bucket.errs, flakyKey)
		bucket.sizes[flakyKey] = 4321
		bucket.mu.Unlock()

		res, err := RunBackfillSizes(ctx, deps)
		require.NoError(t, err)
		assert.Equal(t, BackfillSizesResult{Checked: 2, Filled: 1, Missing: 1}, res)
		want := []string{lostKey, flakyKey}
		slices.Sort(want)
		assert.Equal(t, want, bucket.askedKeys())
		size, _ := sizeOf(t, pool, flaky)
		assert.Equal(t, ptr[int64](4321), size)
	})

	t.Run("then only the row whose object is gone is left", func(t *testing.T) {
		bucket.mu.Lock()
		bucket.asked = nil
		bucket.mu.Unlock()

		res, err := RunBackfillSizes(ctx, deps)
		require.NoError(t, err)
		assert.Equal(t, BackfillSizesResult{Checked: 1, Missing: 1}, res)
		assert.Equal(t, []string{lostKey}, bucket.askedKeys())
	})
}

func TestRunBackfillSizes_Integration_Interrupted(t *testing.T) {
	pool := testdb.New(t)
	alice := createUser(t, pool, "alice")
	sizes := map[string]int64{}
	var ids []string
	for i := range 5 {
		id, key := unsized(t, pool, alice, fmt.Sprintf("p%d.jpg", i))
		ids = append(ids, id)
		sizes[key] = int64(100 + i)
	}
	// IDs are random: the third key in ID order is the one whose lookup the interrupt cuts short.
	var keys []string
	rows, err := pool.Query(context.Background(), `SELECT storage_key FROM media ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	require.NoError(t, rows.Err())
	require.Len(t, keys, 5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bucket := &fakeSizes{sizes: sizes, before: func(key string) {
		if key == keys[2] {
			cancel()
		}
	}}
	deps := BackfillSizesDeps{db: pool, storage: bucket, page: 2, workers: 1}

	res, err := RunBackfillSizes(ctx, deps)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, BackfillSizesResult{Checked: 2, Filled: 2}, res, "the lookup the interrupt cut short is not counted")
	assert.Equal(t, keys[:3], bucket.asked, "nothing is looked up after the interrupt")

	var filled int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(size_bytes) FROM media`).Scan(&filled))
	assert.Equal(t, 2, filled)

	bucket.before = nil
	res, err = RunBackfillSizes(context.Background(), deps)
	require.NoError(t, err)
	assert.Equal(t, BackfillSizesResult{Checked: 3, Filled: 3}, res, "a re-run finishes")
	for _, id := range ids {
		size, _ := sizeOf(t, pool, id)
		assert.NotNil(t, size, id)
	}
}
