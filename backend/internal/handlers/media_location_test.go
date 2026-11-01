package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func locationReq(id, jsonBody string) *http.Request {
	r := newChiRequest("PATCH", "/api/media/"+id+"/location", "id", id)
	r.Body = io.NopCloser(strings.NewReader(jsonBody))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func buildLocationTx(takenAt *time.Time, execErr error) (*mockTx, *int) {
	return buildLocationTxCapture(takenAt, execErr, nil)
}

// buildLocationTxCapture records the args of the select (index 0) and each Exec into captured.
func buildLocationTxCapture(takenAt *time.Time, execErr error, captured *[][]any) (*mockTx, *int) {
	calls := 0
	record := func(args []any) {
		if captured != nil {
			*captured = append(*captured, args)
		}
	}
	tx := &mockTx{
		queryRowFn: func(_ context.Context, _ string, args ...any) pgx.Row {
			record(args)
			return &mockRow{scanFn: func(dest ...any) error {
				*dest[0].(**time.Time) = takenAt
				return nil
			}}
		},
		execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
			record(args)
			calls++
			if execErr != nil {
				return pgconn.CommandTag{}, execErr
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	return tx, &calls
}

func TestUpdateLocation_Success_Returns204(t *testing.T) {
	takenAt := time.Now()
	tx, execCalls := buildLocationTx(&takenAt, nil)

	matcherCalled := false
	h := &MediaHandler{
		pool: &mockPool{tx: tx},
		matcherPool: &mockDB{
			queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row { panic("unexpected") },
		},
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error {
			matcherCalled = true
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.UpdateLocation(w, locationReq("abc", `{"lat":48.8566,"lng":2.3522}`))

	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, 2, *execCalls, "expected UPDATE media + INSERT gps_tracks")
	assert.True(t, matcherCalled)
}

func TestUpdateLocation_PropagatesOwner(t *testing.T) {
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	var captured [][]any
	tx, _ := buildLocationTxCapture(&takenAt, nil, &captured)

	var matcherOwner string
	h := &MediaHandler{
		pool:        &mockPool{tx: tx},
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, ownerID string, _ time.Duration, _ bool) error {
			matcherOwner = ownerID
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.UpdateLocation(w, locationReq("abc", `{"lat":48.8566,"lng":2.3522}`))

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Len(t, captured, 3, "select, update media, insert gps_tracks")
	assert.Equal(t, []any{"abc", testOwnerID}, captured[0])
	assert.Equal(t, []any{2.3522, 48.8566, "abc", testOwnerID}, captured[1])
	assert.Equal(t, testOwnerID, captured[2][0], "manual gps_tracks row is owned by the caller")
	assert.Equal(t, testOwnerID, matcherOwner)
}

func TestUpdateLocation_WithoutTakenAt_SkipsGPSTrackAndMatcher(t *testing.T) {
	tx, execCalls := buildLocationTx(nil, nil)

	matcherCalled := false
	h := &MediaHandler{
		pool: &mockPool{tx: tx},
		matcherPool: &mockDB{
			queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row { panic("unexpected") },
		},
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error {
			matcherCalled = true
			return nil
		},
	}

	w := httptest.NewRecorder()
	h.UpdateLocation(w, locationReq("abc", `{"lat":48.8566,"lng":2.3522}`))

	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, 1, *execCalls, "expected only UPDATE media, no gps_tracks insert")
	assert.False(t, matcherCalled)
}

func TestUpdateLocation_NotFound_Returns404(t *testing.T) {
	tx := &mockTx{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return &mockRow{scanFn: func(_ ...any) error { return pgx.ErrNoRows }}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			panic("exec should not be called when media not found")
		},
	}

	h := &MediaHandler{pool: &mockPool{tx: tx}}
	w := httptest.NewRecorder()
	h.UpdateLocation(w, locationReq("missing", `{"lat":48.8566,"lng":2.3522}`))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpdateLocation_InvalidLat_Returns400(t *testing.T) {
	h := &MediaHandler{}
	w := httptest.NewRecorder()
	h.UpdateLocation(w, locationReq("abc", `{"lat":91.0,"lng":2.3522}`))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateLocation_InvalidLng_Returns400(t *testing.T) {
	h := &MediaHandler{}
	w := httptest.NewRecorder()
	h.UpdateLocation(w, locationReq("abc", `{"lat":48.8566,"lng":181.0}`))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateLocation_InvalidBody_Returns400(t *testing.T) {
	h := &MediaHandler{}
	w := httptest.NewRecorder()
	h.UpdateLocation(w, locationReq("abc", `not json`))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
