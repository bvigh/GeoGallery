package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTx implements pgx.Tx for delete handler tests.
type mockTx struct {
	queryRowFn func(ctx context.Context, sql string, args ...any) pgx.Row
	execFn     func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	commitErr  error
}

func (m *mockTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return m.queryRowFn(ctx, sql, args...)
}
func (m *mockTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return m.execFn(ctx, sql, args...)
}
func (m *mockTx) Commit(ctx context.Context) error   { return m.commitErr }
func (m *mockTx) Rollback(ctx context.Context) error { return nil }

// Remaining pgx.Tx methods — not used by the delete handler.
func (m *mockTx) Begin(ctx context.Context) (pgx.Tx, error) { panic("unexpected") }
func (m *mockTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("unexpected")
}
func (m *mockTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults { panic("unexpected") }
func (m *mockTx) LargeObjects() pgx.LargeObjects                             { panic("unexpected") }
func (m *mockTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("unexpected")
}
func (m *mockTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) { panic("unexpected") }
func (m *mockTx) Exec2(_ context.Context, _ string, _ ...any) pgconn.CommandTag { panic("unexpected") }
func (m *mockTx) Conn() *pgx.Conn                                               { panic("unexpected") }

// mockPool returns the configured mockTx from Begin.
type mockPool struct {
	tx  *mockTx
	err error
}

func (m *mockPool) Begin(_ context.Context) (pgx.Tx, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.tx, nil
}

// mockDeleteStorage tracks whether Delete was called and with what key.
type mockDeleteStorage struct {
	deletedKey string
	deleteErr  error
}

func (m *mockDeleteStorage) PresignGet(_ context.Context, _ string) (string, error) {
	panic("not expected in delete tests")
}
func (m *mockDeleteStorage) Delete(_ context.Context, key string) error {
	m.deletedKey = key
	return m.deleteErr
}

func TestDeleteMedia_Success_Returns204(t *testing.T) {
	const mediaID = "media-uuid-1"
	const storageKey = "media/1234/photo.jpg"

	var selectArgs, deleteArgs []any
	tx := &mockTx{
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			selectArgs = args
			return &mockRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = storageKey
				return nil
			}}
		},
		execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
			deleteArgs = args
			return pgconn.NewCommandTag("DELETE 1"), nil
		},
	}
	store := &mockDeleteStorage{}

	h := &MediaHandler{pool: &mockPool{tx: tx}, storage: store}
	w := httptest.NewRecorder()
	r := newChiRequest("DELETE", "/api/media/"+mediaID, "id", mediaID)

	h.Delete(w, r)

	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, storageKey, store.deletedKey)
	assert.Equal(t, []any{mediaID, testOwnerID}, selectArgs)
	assert.Equal(t, []any{mediaID, testOwnerID}, deleteArgs)
}

func TestDeleteMedia_NotFound_Returns404(t *testing.T) {
	tx := &mockTx{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return &mockRow{scanFn: func(_ ...any) error { return pgx.ErrNoRows }}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			panic("exec should not be called when row not found")
		},
	}
	store := &mockDeleteStorage{}

	h := &MediaHandler{pool: &mockPool{tx: tx}, storage: store}
	w := httptest.NewRecorder()
	r := newChiRequest("DELETE", "/api/media/missing", "id", "missing")

	h.Delete(w, r)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, store.deletedKey)
}

func TestDeleteMedia_DBExecError_Returns500(t *testing.T) {
	const storageKey = "media/1234/photo.jpg"

	tx := &mockTx{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return &mockRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = storageKey
				return nil
			}}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, errors.New("db write error")
		},
	}
	store := &mockDeleteStorage{}

	h := &MediaHandler{pool: &mockPool{tx: tx}, storage: store}
	w := httptest.NewRecorder()
	r := newChiRequest("DELETE", "/api/media/abc", "id", "abc")

	h.Delete(w, r)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Empty(t, store.deletedKey)
}
