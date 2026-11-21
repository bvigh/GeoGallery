package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageUse_Add(t *testing.T) {
	u := StorageUse{Items: 2, Bytes: 300, UnknownItems: 1}
	u.add(StorageUse{Items: 3, Bytes: 4000})
	u.add(StorageUse{Items: 1, UnknownItems: 1})
	assert.Equal(t, StorageUse{Items: 6, Bytes: 4300, UnknownItems: 2}, u)
}

// Every query of GET /api/me/storage reads the caller's own rows by a literal owner_id, never
// through album membership: a shared item is its owner's storage.
func TestMyStorage_OwnRowsOnly(t *testing.T) {
	for name, sql := range map[string]string{
		"totals": myStorageTotalsSQL, "trips": myStorageTripsSQL, "albums": myStorageAlbumsSQL,
	} {
		assert.Contains(t, sql, "owner_id = $1", name)
		assert.NotContains(t, sql, "album_members", name)
	}
	assert.Contains(t, myStorageTripsSQL, "WHERE t.owner_id = $1")
	assert.Contains(t, myStorageTripsSQL, "m.owner_id = $1")
	assert.Contains(t, myStorageAlbumsSQL, "WHERE am.owner_id = $1")
	assert.Contains(t, myStorageAlbumsSQL, "m.owner_id = $1")
}

func TestMyStorage_OneReadOnlySnapshot(t *testing.T) {
	db := &recordingChecksDB{rows: map[string][][]any{
		myStorageTotalsSQL: {{int64(3), int64(4500), int64(1), int64(1), int64(500), int64(0)}},
		myStorageTripsSQL:  {{"t-1", "Lisbon", "2024-05-01", "2024-05-03", true, int64(2), int64(4000), int64(1)}},
		myStorageAlbumsSQL: {{"a-1", "Family", "u-bob", "Bob", false, int64(1), int64(1000), int64(0)}},
	}}
	h := &StorageUseHandler{db: db}

	rec := httptest.NewRecorder()
	h.Mine(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/me/storage", nil)))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []pgx.TxOptions{{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}}, db.opts,
		"one snapshot, and nothing written")
	assert.Equal(t, []string{"begin", myStorageTotalsSQL, myStorageTripsSQL, myStorageAlbumsSQL, "rollback"}, db.log)
	assert.JSONEq(t, `{
		"items": 3, "bytes": 4500, "unknown_items": 1,
		"trips": [{"id": "t-1", "name": "Lisbon", "start_date": "2024-05-01", "end_date": "2024-05-03",
		           "user_edited": true, "items": 2, "bytes": 4000, "unknown_items": 1}],
		"no_trip": {"items": 1, "bytes": 500, "unknown_items": 0},
		"albums": [{"id": "a-1", "name": "Family", "owner_id": "u-bob", "owner_name": "Bob", "is_owner": false,
		            "items": 1, "bytes": 1000, "unknown_items": 0}]
	}`, rec.Body.String())
}

func TestMyStorage_EmptyLibrary(t *testing.T) {
	db := &recordingChecksDB{rows: map[string][][]any{
		myStorageTotalsSQL: {{int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)}},
	}}
	h := &StorageUseHandler{db: db}

	rec := httptest.NewRecorder()
	h.Mine(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/me/storage", nil)))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"items":0,"bytes":0,"unknown_items":0,"trips":[],"no_trip":{"items":0,"bytes":0,"unknown_items":0},"albums":[]}`,
		rec.Body.String(), "empty lists, never null")
}

func TestMyStorage_Failures(t *testing.T) {
	t.Run("a transaction that cannot begin is a 500", func(t *testing.T) {
		db := &recordingChecksDB{beginErr: errors.New("too many clients already")}
		h := &StorageUseHandler{db: db}

		rec := httptest.NewRecorder()
		h.Mine(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/me/storage", nil)))

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "too many clients", "the error stays in the log")
	})

	t.Run("a request that has ended gets no answer", func(t *testing.T) {
		db := &recordingChecksDB{}
		h := &StorageUseHandler{db: db}
		ended, cancel := context.WithCancel(context.Background())
		cancel()

		rec := httptest.NewRecorder()
		h.Mine(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/me/storage", nil).WithContext(ended)))

		assert.Empty(t, rec.Body.String())
		assert.Equal(t, []string{"begin"}, db.log)
	})
}
