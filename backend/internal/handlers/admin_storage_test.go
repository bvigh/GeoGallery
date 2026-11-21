package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminStorage_ReadOnlyWithoutJIT(t *testing.T) {
	db := &recordingChecksDB{}
	h := &AdminStorageHandler{db: db}

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/storage", nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []pgx.TxOptions{{AccessMode: pgx.ReadOnly}}, db.opts, "nothing written")
	assert.Equal(t, []string{"begin", "SET LOCAL jit = off", adminStorageUsersSQL, "rollback"}, db.log,
		"jit off before the query, which runs in the transaction")
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"items":0,"bytes":0,"unknown_items":0,"track_points":0,"users":[]}`, rec.Body.String(),
		"an empty list, never null")
}

// The summary is per user: it reads no trip, album or file, which only their owner sees.
func TestAdminStorage_NamesNothingInsideALibrary(t *testing.T) {
	for _, table := range []string{"trips", "album", "filename", "storage_key"} {
		assert.NotContains(t, adminStorageUsersSQL, table)
	}
}

func TestAdminStorage_SumsTheUsers(t *testing.T) {
	db := &recordingChecksDB{rows: map[string][][]any{adminStorageUsersSQL: {
		{"u-bob", nil, "bob@example.test", true, true, int64(2), int64(30_000), int64(0), int64(1)},
		{"u-alice", "Alice", nil, false, true, int64(5), int64(4500), int64(2), int64(6_246_825)},
	}}}
	h := &AdminStorageHandler{db: db}

	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/storage", nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{
		"items": 7, "bytes": 34500, "unknown_items": 2, "track_points": 6246826,
		"users": [
			{"id": "u-bob", "display_name": null, "email": "bob@example.test", "disabled": true, "claimed": true,
			 "items": 2, "bytes": 30000, "unknown_items": 0, "track_points": 1},
			{"id": "u-alice", "display_name": "Alice", "email": null, "disabled": false, "claimed": true,
			 "items": 5, "bytes": 4500, "unknown_items": 2, "track_points": 6246825}
		]}`, rec.Body.String())
}

func TestAdminStorage_TransactionFailures(t *testing.T) {
	t.Run("a transaction that cannot begin is a 500", func(t *testing.T) {
		db := &recordingChecksDB{beginErr: errors.New("too many clients already")}
		h := &AdminStorageHandler{db: db}

		rec := httptest.NewRecorder()
		h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/storage", nil))

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "too many clients", "the error stays in the log")
		assert.Equal(t, []string{"begin"}, db.log)
	})

	t.Run("a deadline before it begins is a 504", func(t *testing.T) {
		db := &recordingChecksDB{}
		h := &AdminStorageHandler{db: db}
		ended, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		rec := httptest.NewRecorder()
		h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/storage", nil).WithContext(ended))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.JSONEq(t, `{"error":"storage_timeout"}`, rec.Body.String())
		assert.Equal(t, []string{"begin"}, db.log)
	})

	t.Run("a cancelled request is a 504 too", func(t *testing.T) {
		db := &recordingChecksDB{}
		h := &AdminStorageHandler{db: db}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()

		rec := httptest.NewRecorder()
		h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/admin/storage", nil).WithContext(cancelled))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.Equal(t, []string{"begin"}, db.log)
	})
}
