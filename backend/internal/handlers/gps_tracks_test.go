package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock for gpsTrackDB ---

type mockGPSTrackDB struct {
	execFn func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (m *mockGPSTrackDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return m.execFn(ctx, sql, args...)
}

// makeTag returns a CommandTag reporting n rows affected.
func makeTag(n int) pgconn.CommandTag {
	return pgconn.NewCommandTag(fmt.Sprintf("INSERT 0 %d", n))
}

// postImport fires POST /api/gps-tracks/import with the given body.
func postImport(h *GPSTrackHandler, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/gps-tracks/import", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, r)
	return w
}

// --- Cycle 1: single valid point → inserted:1, skipped:0, errors:[] ---

func TestImport_SingleValidPoint_ReturnsInserted1(t *testing.T) {
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	require.Equal(t, http.StatusOK, w.Code)
	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 1, resp.Inserted)
	assert.Equal(t, 0, resp.Skipped)
	assert.Empty(t, resp.Errors)
}

// --- Cycle 2: invalid recorded_at → row error at index 0 ---

func TestImport_InvalidRecordedAt_ReturnsRowError(t *testing.T) {
	execCalled := false
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return makeTag(0), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "not-a-date", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	require.Equal(t, http.StatusOK, w.Code)
	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 0, resp.Inserted)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, 0, resp.Errors[0].Row)
	assert.Contains(t, resp.Errors[0].Reason, "recorded_at")
	assert.False(t, execCalled)
}

// --- Cycle 3: latitude out of range → row error ---

func TestImport_LatitudeOutOfRange_ReturnsRowError(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 91.0, "lng": 19.0, "source": "strava"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, 0, resp.Errors[0].Row)
	assert.Contains(t, resp.Errors[0].Reason, "lat")
}

// --- Cycle 4: longitude out of range → row error ---

func TestImport_LongitudeOutOfRange_ReturnsRowError(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": -181.0, "source": "strava"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Reason, "lng")
}

// --- Cycle 5: empty source → row error ---

func TestImport_EmptySource_ReturnsRowError(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": ""},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Reason, "source")
}

// --- Cycle 6: all-invalid rows → no DB Exec called ---

func TestImport_AllInvalidRows_NoDBCall(t *testing.T) {
	execCalled := false
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return makeTag(0), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "bad", "lat": 47.5, "lng": 19.0, "source": "s"},
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 99.0, "lng": 19.0, "source": "s"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 0, resp.Inserted)
	assert.Len(t, resp.Errors, 2)
	assert.False(t, execCalled)
}

// --- Cycle 7: ON CONFLICT DO NOTHING → skipped count via RowsAffected ---

func TestImport_DuplicateRows_CountedAsSkipped(t *testing.T) {
	// Two rows sent; DB reports only 1 inserted (the other conflicted).
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil // 1 inserted, 1 skipped by ON CONFLICT
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
		{"recorded_at": "2024-06-15T12:00:01Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 1, resp.Inserted)
	assert.Equal(t, 1, resp.Skipped)
	assert.Empty(t, resp.Errors)
}

// --- Cycle 8: GPS matcher is called after successful insert ---

func TestImport_ValidPoints_MatcherCalled(t *testing.T) {
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil
		},
	}
	matcherCalled := false
	h := &GPSTrackHandler{
		db:          db,
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
			matcherCalled = true
			return nil
		},
	}

	postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	assert.True(t, matcherCalled)
}

// --- Cycle 9: invalid JSON body → 400 ---

func TestImport_InvalidJSON_Returns400(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	r := httptest.NewRequest(http.MethodPost, "/api/gps-tracks/import", strings.NewReader("not json"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Cycle 10: 1001 valid rows → two DB Exec calls (batching) ---

func TestImport_1001Rows_TwoDBBatches(t *testing.T) {
	execCallCount := 0
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCallCount++
			return makeTag(1000), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error { return nil }}

	rows := make([]map[string]any, 1001)
	for i := range rows {
		rows[i] = map[string]any{
			"recorded_at": fmt.Sprintf("2024-06-15T%02d:%02d:%02dZ", i/3600, (i/60)%60, i%60),
			"lat":         47.5,
			"lng":         19.0,
			"source":      "strava",
		}
	}

	w := postImport(h, rows)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 2, execCallCount)
}
