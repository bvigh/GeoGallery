package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"context"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func postRunMatcher(h *GPSMatcherHandler) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/gps-matcher/run", nil)
	w := httptest.NewRecorder()
	h.Run(w, r)
	return w
}

// --- Cycle 2: Run endpoint calls matcher with reprocess=true, returns 200 ---

func TestGPSMatcherHandler_Run_Returns200(t *testing.T) {
	var capturedReprocess bool
	h := &GPSMatcherHandler{
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, reprocess bool) error {
			capturedReprocess = reprocess
			return nil
		},
	}

	w := postRunMatcher(h)

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, capturedReprocess)

	var body map[string]bool
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.True(t, body["ok"])
}

// --- Cycle 3: matcher error → 500 ---

func TestGPSMatcherHandler_Run_MatcherError_Returns500(t *testing.T) {
	h := &GPSMatcherHandler{
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ time.Duration, _ bool) error {
			return errors.New("db unavailable")
		},
	}

	w := postRunMatcher(h)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
