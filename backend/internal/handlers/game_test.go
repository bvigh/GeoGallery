package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func errRowDB(err error, calls *int) *mockDB {
	return &mockDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
		*calls++
		return &mockRow{scanFn: func(...any) error { return err }}
	}}
}

func TestNewRound_QueryErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"no rows", pgx.ErrNoRows, http.StatusNotFound, "no media available"},
		{"wrapped no rows", errors.Join(errors.New("ctx"), pgx.ErrNoRows), http.StatusNotFound, "no media available"},
		{"db failure", errors.New("connection refused"), http.StatusInternalServerError, "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			h := &GameHandler{db: errRowDB(tt.err, &calls)}
			rec := httptest.NewRecorder()
			h.NewRound(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/game/round", nil)))
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestNewRound_RoundAndMeasureShareFilter(t *testing.T) {
	const query = "from_date=2024-01-01&media_type=photo&trip_id=unassigned&trip_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" +
		"&album_id=cccccccc-cccc-4ccc-8ccc-cccccccccccc&owner=shared"
	type call struct {
		sql  string
		args []any
	}
	var calls []call
	db := &mockDB{queryRowFn: func(_ context.Context, sql string, args ...any) pgx.Row {
		calls = append(calls, call{sql, args})
		if len(calls) == 1 {
			return &mockRow{scanFn: func(dest ...any) error {
				*dest[0].(*string) = "m1"
				*dest[1].(*string) = "photo"
				*dest[2].(*string) = "media/k.jpg"
				return nil
			}}
		}
		return &mockRow{scanFn: func(dest ...any) error {
			*dest[0].(*float64) = 42
			for i, deg := range []float64{47.4979, 16.3738, 48.2082, 19.0402} {
				*dest[i+1].(**float64) = ptr(deg)
			}
			return nil
		}}
	}}
	store := &mockStorage{presignFn: func(context.Context, string) (string, error) { return "https://s3.test/k", nil }}

	rec := httptest.NewRecorder()
	(&GameHandler{db: db, storage: store}).NewRound(rec,
		withTestUser(httptest.NewRequest(http.MethodGet, "/api/game/round?"+query, nil)))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var round roundMedia
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &round))
	assert.InDelta(t, 42, round.MaxDistKm, 1e-9)
	assert.Equal(t, &[4]float64{47, 16, 48.5, 19.5}, round.Bounds)

	q, err := url.ParseQuery(query)
	require.NoError(t, err)
	wantConds, wantArgs := buildMediaFilter(q, testOwnerID)
	require.Len(t, wantConds, 5, "read access, from_date, media_type, collections, owner")
	wantWhere := strings.Join(strings.Fields(
		"FROM media m WHERE "+strings.Join(append(wantConds, "m.location IS NOT NULL"), " AND ")), " ")
	require.Len(t, calls, 2, "round query, then the set's distance and bounds")
	for i, c := range calls {
		assert.Contains(t, strings.Join(strings.Fields(c.sql), " "), wantWhere, "call %d", i)
		assert.Equal(t, wantArgs, c.args, "call %d", i)
	}
}

// The round is playable without a measurement: the scale falls back to 1 km and the map stays on
// its default view.
func TestNewRound_MeasureFallbacks(t *testing.T) {
	tests := []struct {
		name string
		scan func(dest ...any) error
	}{
		{"query fails", func(...any) error { return errors.New("connection refused") }},
		{"set emptied since the round was picked", func(dest ...any) error {
			*dest[0].(*float64) = 0
			return nil // the bounds stay NULL
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			db := &mockDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
				calls++
				if calls == 2 {
					return &mockRow{scanFn: tt.scan}
				}
				return &mockRow{scanFn: func(dest ...any) error {
					*dest[0].(*string) = "m1"
					*dest[1].(*string) = "photo"
					*dest[2].(*string) = "media/k.jpg"
					return nil
				}}
			}}
			store := &mockStorage{presignFn: func(context.Context, string) (string, error) { return "https://s3.test/k", nil }}

			rec := httptest.NewRecorder()
			(&GameHandler{db: db, storage: store}).NewRound(rec,
				withTestUser(httptest.NewRequest(http.MethodGet, "/api/game/round", nil)))

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"bounds":null`)
			assert.Contains(t, rec.Body.String(), `"max_dist_km":1`)
		})
	}
}

func TestGuessMapBounds(t *testing.T) {
	tests := []struct {
		name                     string
		south, west, north, east float64
		want                     [4]float64
	}{
		{"one place gets a whole cell", 47.4979, 19.0402, 47.4979, 19.0402, [4]float64{47, 19, 47.5, 19.5}},
		{"the cell does not depend on where in it the place is", 47.01, 19.49, 47.01, 19.49, [4]float64{47, 19, 47.5, 19.5}},
		{"a point on a grid line still gets an area", 47.5, 19, 47.5, 19, [4]float64{47.5, 19, 48, 19.5}},
		{"a tiny set across a grid line gets both cells", 47.49, 18.99, 47.51, 19.01, [4]float64{47, 18.5, 48, 19.5}},
		{"south and west round away from zero too", -33.87, -70.66, -33.87, -70.66, [4]float64{-34, -71, -33.5, -70.5}},
		{"a wide set grows by less than a cell on each side", 35.2, -9.1, 60.3, 24.9, [4]float64{35, -9.5, 60.5, 25}},
		{"north-east limits", 90, 180, 90, 180, [4]float64{89.5, 179.5, 90, 180}},
		{"south-west limits", -90, -180, -90, -180, [4]float64{-90, -180, -89.5, -179.5}},
		{"a wide set ending on the limit keeps its low side", 10, 100, 90, 180, [4]float64{10, 100, 90, 180}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, &tt.want, guessMapBounds(tt.south, tt.west, tt.north, tt.east))
		})
	}
}

func TestSubmitGuess_LookupErrors(t *testing.T) {
	const validID = "22222222-2222-2222-2222-222222222222"
	tests := []struct {
		name       string
		mediaID    string
		err        error
		wantStatus int
		wantCalls  int
	}{
		{"not a uuid", "not-a-uuid", nil, http.StatusNotFound, 0},
		{"no rows", validID, pgx.ErrNoRows, http.StatusNotFound, 1},
		{"db failure", validID, errors.New("connection refused"), http.StatusInternalServerError, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			h := &GameHandler{db: errRowDB(tt.err, &calls)}
			rec := httptest.NewRecorder()
			body := strings.NewReader(`{"media_id":"` + tt.mediaID + `","guess_lat":1,"guess_lng":2}`)
			h.SubmitGuess(rec, withTestUser(httptest.NewRequest(http.MethodPost, "/api/game/guess", body)))
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func TestMalformedFilters_Return400(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"bad from_date", "from_date=garbage"},
		{"bad to_date", "to_date=2024-13-01"},
		{"datetime instead of date", "from_date=2024-03-01T00:00:00Z"},
		{"non-uuid trip_id", "trip_id=unassigned&trip_id=nope"},
		{"non-uuid album_id", "album_id=nope"},
		{"unknown owner", "owner=everyone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			db := &mockDB{
				queryRowFn: func(context.Context, string, ...any) pgx.Row { calls++; return &mockRow{} },
				queryFn:    func(context.Context, string, ...any) (pgx.Rows, error) { calls++; return emptyRows(), nil },
			}

			rec := httptest.NewRecorder()
			(&GameHandler{db: db}).NewRound(rec, withTestUser(httptest.NewRequest(http.MethodGet, "/api/game/round?"+tt.query, nil)))
			assert.Equal(t, http.StatusBadRequest, rec.Code, "game round")

			rec = httptest.NewRecorder()
			(&MediaHandler{db: db}).List(rec, listRequest(tt.query))
			assert.Equal(t, http.StatusBadRequest, rec.Code, "media list")

			assert.Zero(t, calls, "no query issued")
		})
	}
}

// scoreGuess is the one scoring rule of a free round and of a challenge's round.
func TestScoreGuess(t *testing.T) {
	// 1° of latitude is about 111.2 km.
	budapest := guessAnswer{lat: 47.5, lng: 19.0, takenAt: ptr("2024-05-02 12:00:00+00"), confidence: 1}
	undated := guessAnswer{lat: 47.5, lng: 19.0, confidence: 1}

	t.Run("an exact guess without a date is the full location score", func(t *testing.T) {
		got := scoreGuess(budapest, 47.5, 19.0, "", 500)
		assert.InDelta(t, 0, got.DistanceKm, 1e-9)
		assert.InDelta(t, 5000, got.LocationScore, 1e-9)
		assert.InDelta(t, 5000, got.TotalScore, 1e-9)
		assert.Nil(t, got.TimeScore)
		assert.Nil(t, got.DateDiffDays)
		assert.Equal(t, 47.5, got.ActualLat)
		assert.Equal(t, 19.0, got.ActualLng)
		assert.Equal(t, budapest.takenAt, got.ActualDate)
	})

	t.Run("the location score falls with the distance, relative to the scale", func(t *testing.T) {
		near := scoreGuess(budapest, 48.5, 19.0, "", 500)
		assert.InDelta(t, 111.2, near.DistanceKm, 0.1)
		assert.InDelta(t, 5000*(1-near.DistanceKm/500), near.LocationScore, 1e-6)
		wide := scoreGuess(budapest, 48.5, 19.0, "", 5000)
		assert.Greater(t, wide.LocationScore, near.LocationScore, "the same miss costs less on a wider scale")
		assert.Zero(t, scoreGuess(budapest, 57.5, 19.0, "", 500).LocationScore, "past the scale scores nothing, never below 0")
	})

	t.Run("a scale under 1 km counts as 1 km", func(t *testing.T) {
		got := scoreGuess(budapest, 47.5, 19.0, "", 0)
		assert.InDelta(t, 5000, got.LocationScore, 1e-9)
		assert.Equal(t, scoreGuess(budapest, 47.501, 19.0, "", 1), scoreGuess(budapest, 47.501, 19.0, "", -3))
	})

	t.Run("a low confidence forgives part of the distance", func(t *testing.T) {
		unsure := budapest
		unsure.confidence = 0.5 // 5 km of tolerance
		assert.InDelta(t, 5000, scoreGuess(unsure, 47.54, 19.0, "", 500).LocationScore, 1e-9, "4.4 km off")
		assert.Less(t, scoreGuess(budapest, 47.54, 19.0, "", 500).LocationScore, 5000.0)
	})

	t.Run("a date is scored against the item's day, and the total is the mean", func(t *testing.T) {
		got := scoreGuess(budapest, 47.5, 19.0, "2024-05-12", 500)
		require.NotNil(t, got.DateDiffDays)
		require.NotNil(t, got.TimeScore)
		assert.InDelta(t, 10, *got.DateDiffDays, 1e-9)
		assert.InDelta(t, 5000*(1-10.0/365), *got.TimeScore, 1e-9)
		assert.InDelta(t, (5000+*got.TimeScore)/2, got.TotalScore, 1e-9)

		before := scoreGuess(budapest, 47.5, 19.0, "2024-04-22", 500)
		assert.InDelta(t, 10, *before.DateDiffDays, 1e-9, "ten days early costs what ten days late does")
		assert.Zero(t, *scoreGuess(budapest, 47.5, 19.0, "2020-01-01", 500).TimeScore, "over a year off scores no time points")
	})

	t.Run("no time score without a date on either side", func(t *testing.T) {
		for name, got := range map[string]guessResult{
			"the item has no date":       scoreGuess(undated, 47.5, 19.0, "2024-05-02", 500),
			"the date cannot be read":    scoreGuess(budapest, 47.5, 19.0, "02-05-2024", 500),
			"the item's date is cut off": scoreGuess(guessAnswer{lat: 47.5, lng: 19.0, takenAt: ptr("2024"), confidence: 1}, 47.5, 19.0, "2024-05-02", 500),
			"the item's date is not one": scoreGuess(guessAnswer{lat: 47.5, lng: 19.0, takenAt: ptr("infinity-and-on"), confidence: 1}, 47.5, 19.0, "2024-05-02", 500),
		} {
			assert.Nil(t, got.TimeScore, name)
			assert.Nil(t, got.DateDiffDays, name)
			assert.InDelta(t, 5000, got.TotalScore, 1e-9, name)
		}
	})
}
