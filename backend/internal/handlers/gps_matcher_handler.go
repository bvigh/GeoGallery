package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type GPSMatcherHandler struct {
	matcherPool matcherDB
	matchWindow time.Duration
	runMatcher  func(ctx context.Context, db matcherDB, maxWindow time.Duration, reprocess bool) error
}

func NewGPSMatcherHandler(pool *pgxpool.Pool, matchWindow time.Duration) *GPSMatcherHandler {
	return &GPSMatcherHandler{
		matcherPool: pool,
		matchWindow: matchWindow,
		runMatcher:  RunGPSMatcher,
	}
}

func (h *GPSMatcherHandler) Run(w http.ResponseWriter, r *http.Request) {
	if err := h.runMatcher(r.Context(), h.matcherPool, h.matchWindow, true); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
