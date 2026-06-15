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
	calls := 0
	tx := &mockTx{
		queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row {
			return &mockRow{scanFn: func(dest ...any) error {
				*dest[0].(**time.Time) = takenAt
				return nil
			}}
		},
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
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
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
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

func TestUpdateLocation_WithoutTakenAt_SkipsGPSTrackAndMatcher(t *testing.T) {
	tx, execCalls := buildLocationTx(nil, nil)

	matcherCalled := false
	h := &MediaHandler{
		pool: &mockPool{tx: tx},
		matcherPool: &mockDB{
			queryRowFn: func(_ context.Context, _ string, _ ...any) pgx.Row { panic("unexpected") },
		},
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
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
