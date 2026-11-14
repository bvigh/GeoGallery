package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/jackc/pgx/v5"
)

// clockRuleQuerier is what loadClockRules needs: a pool, a connection or a transaction.
type clockRuleQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadClockRules returns ownerID's camera clock rules, for ingest.CameraZone.Clock. They describe the
// owner's own cameras, so the literal owner_id filter is what keeps one user's rule off another
// user's files of the same model. pgx reads a DATE as midnight UTC, the form ingest.ClockRule
// holds, and NULL as nil. The order is the API's: camera without case, then the period's start.
func loadClockRules(ctx context.Context, db clockRuleQuerier, ownerID string) (ingest.ClockRules, error) {
	rows, err := db.Query(ctx, `
		SELECT id::text, make, model, utc_offset_min, starts_on, ends_on
		FROM camera_clock_rules
		WHERE owner_id = $1
		ORDER BY lower(make), lower(model), starts_on NULLS FIRST, id
	`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("load clock rules: %w", err)
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ingest.ClockRule, error) {
		var r ingest.ClockRule
		err := row.Scan(&r.ID, &r.Make, &r.Model, &r.OffsetMin, &r.StartsOn, &r.EndsOn)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("load clock rules: %w", err)
	}
	return rules, nil
}

// clockRuleDate is how a rule's day is written in JSON: "2006-01-02".
const clockRuleDate = time.DateOnly

// evidenceClockRule is captureEvidence.ClockRule: the owner's rule as it was when it applied. It is
// a snapshot, because the rule can later be changed or deleted, and the evidence must still say what
// produced taken_at.
type evidenceClockRule struct {
	ID        string  `json:"id"`
	Make      string  `json:"make"`
	Model     string  `json:"model"`
	OffsetMin int     `json:"offset_min"`
	StartsOn  *string `json:"starts_on"` // "2006-01-02", or null for no start
	EndsOn    *string `json:"ends_on"`   // "2006-01-02", or null for no end
}

// newEvidenceClockRule records r. nil gives nil.
func newEvidenceClockRule(r *ingest.ClockRule) *evidenceClockRule {
	if r == nil {
		return nil
	}
	return &evidenceClockRule{
		ID:        r.ID,
		Make:      r.Make,
		Model:     r.Model,
		OffsetMin: r.OffsetMin,
		StartsOn:  formatRuleDate(r.StartsOn),
		EndsOn:    formatRuleDate(r.EndsOn),
	}
}

// formatRuleDate writes a rule's day as its date in its own location, which is UTC for a day read
// from the database.
func formatRuleDate(d *time.Time) *string {
	if d == nil {
		return nil
	}
	s := d.Format(clockRuleDate)
	return &s
}
