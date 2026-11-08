package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingIngestDeps runs the real matcher and trip detection against pool, recording the owner
// each was called with. S3 is faked.
func recordingIngestDeps(pool *pgxpool.Pool, store *fakeObjectStore, matcherOwners, tripOwners *[]string) IngestDeps {
	return IngestDeps{
		registrar:    &MediaRegistrar{db: pool, storage: store},
		matcherPool:  pool,
		matchWindow:  time.Hour,
		tripGapHours: 24,
		runMatcher: func(ctx context.Context, db matcherDB, ownerID string, w time.Duration, reprocess bool) error {
			*matcherOwners = append(*matcherOwners, ownerID)
			return RunGPSMatcher(ctx, db, ownerID, w, reprocess)
		},
		runTripDetect: func(ctx context.Context, db tripDetectDB, ownerID string, gapHours int) error {
			*tripOwners = append(*tripOwners, ownerID)
			return RunTripDetect(ctx, db, ownerID, gapHours)
		},
	}
}

func countMedia(t *testing.T, pool *pgxpool.Pool, ownerID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM media WHERE owner_id = $1`, ownerID).Scan(&n))
	return n
}

func TestRunIngestScan_Integration_OwnershipAndDedup(t *testing.T) {
	pool := testdb.New(t)
	store := &fakeObjectStore{}
	userA := createUser(t, pool, "alice")
	userB := createUser(t, pool, "bob")

	dir := t.TempDir()
	writeFile(t, dir, "one.jpg", "first fake photo")
	writeFile(t, dir, "two.jpg", "second fake photo")

	var matcherOwners, tripOwners []string
	deps := recordingIngestDeps(pool, store, &matcherOwners, &tripOwners)
	ctx := context.Background()

	res, err := RunIngestScan(ctx, deps, dir, userA)
	require.NoError(t, err)
	assert.Equal(t, ScanResult{Ingested: 2}, res)
	assert.Equal(t, 2, countMedia(t, pool, userA), "both rows are owned by A")
	assert.Equal(t, 0, countMedia(t, pool, userB))
	assert.Equal(t, []string{userA}, matcherOwners)
	assert.Equal(t, []string{userA}, tripOwners)
	for _, key := range store.keys {
		assert.Regexp(t, `^media/`+userA+`/[0-9a-f]{32}\.jpg$`, key)
	}

	res, err = RunIngestScan(ctx, deps, dir, userA)
	require.NoError(t, err)
	assert.Equal(t, ScanResult{Skipped: 2}, res, "a second run for A deduplicates")
	assert.Equal(t, 2, countMedia(t, pool, userA))
	assert.Len(t, store.keys, 2, "the second run uploads nothing")
	assert.Empty(t, store.deleted)
	assertNoOrphans(t, pool, store)

	res, err = RunIngestScan(ctx, deps, dir, userB)
	require.NoError(t, err)
	assert.Equal(t, ScanResult{Ingested: 2}, res, "dedup is per owner, so B ingests the same files again")
	assert.Equal(t, 2, countMedia(t, pool, userB))
	assert.Equal(t, []string{userA, userA, userB}, matcherOwners)
	assert.Equal(t, []string{userA, userA, userB}, tripOwners)

	var foreignTripLinks int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM media m JOIN trips t ON t.id = m.trip_id WHERE t.owner_id <> m.owner_id`,
	).Scan(&foreignTripLinks))
	assert.Zero(t, foreignTripLinks)
	var tripsA, tripsB int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM trips WHERE owner_id = $1`, userA).Scan(&tripsA))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM trips WHERE owner_id = $1`, userB).Scan(&tripsB))
	assert.Equal(t, 1, tripsA, "B's trip detection left A's auto trip in place")
	assert.Equal(t, 1, tripsB)
}

func TestRunIngestScan_Integration_ZeroByteVideoCountsAsFailed(t *testing.T) {
	pool := testdb.New(t)
	userA := createUser(t, pool, "alice")

	dir := t.TempDir()
	writeFile(t, dir, "good.jpg", "a fake photo")
	writeFile(t, dir, "broken.mp4", "")

	var matcherOwners, tripOwners []string
	deps := recordingIngestDeps(pool, &fakeObjectStore{}, &matcherOwners, &tripOwners)

	res, err := RunIngestScan(context.Background(), deps, dir, userA)

	require.NoError(t, err, "per-file failures do not abort the scan")
	assert.Equal(t, ScanResult{Ingested: 1, Failed: 1}, res)
	assert.Equal(t, 1, countMedia(t, pool, userA))
	assert.Equal(t, []string{userA}, matcherOwners, "matcher still runs after a per-file failure")
}
