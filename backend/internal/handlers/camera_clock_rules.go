package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// What a camera clock rule may hold. The database checks the same bounds (migration 11); these are
// checked first so that a bad request gets a reason and never reaches an INSERT.
const (
	maxClockRuleBodyBytes   = 16 << 10
	maxClockRulesPerOwner   = 100
	maxClockRuleCameraRunes = 200
	maxClockRuleNoteRunes   = 500
	minClockRuleOffsetMin   = -12 * 60
	maxClockRuleOffsetMin   = 14 * 60
	clockRuleOffsetStepMin  = 15
	minClockRuleYear        = 1900
	maxClockRuleYear        = 9998
)

// SQLSTATEs the clock rule writes turn into a client error.
const (
	pgExclusionViolation = "23P01" // camera_clock_rules_no_overlap
	pgCheckViolation     = "23514"
)

const clockRuleOverlapMessage = "overlaps another rule for this camera"

// CameraClockRuleHandler serves the caller's camera clock rules (camera_clock_rules) and the cameras
// in their own library that a rule can name. A rule describes one user's camera, so every query
// filters on the literal owner_id: another user's rule, or a camera seen only in media shared
// through an album, is never listed, and a foreign, missing or malformed rule ID is 404.
//
// The routes accept API tokens: a rule changes how future uploads are dated, and neither a rule nor
// the camera list is location history.
type CameraClockRuleHandler struct {
	db *pgxpool.Pool
}

func NewCameraClockRuleHandler(db *pgxpool.Pool) *CameraClockRuleHandler {
	return &CameraClockRuleHandler{db: db}
}

// clockRuleInput is the body of POST and PUT, a full rule.
type clockRuleInput struct {
	Make         string  `json:"make"`
	Model        string  `json:"model"`
	UTCOffsetMin *int    `json:"utc_offset_min"` // required: a missing offset must not read as UTC
	StartsOn     *string `json:"starts_on"`      // "2006-01-02" or null
	EndsOn       *string `json:"ends_on"`        // "2006-01-02" or null
	Note         string  `json:"note"`
}

// clockRuleValues is a validated clockRuleInput, as it is stored.
type clockRuleValues struct {
	make, model string
	offsetMin   int
	startsOn    *string // "2006-01-02", nil for no start
	endsOn      *string // "2006-01-02", nil for no end
	note        string
}

// clockRuleItem is one rule on the wire. Days are "2006-01-02" or null, times RFC 3339 in UTC.
type clockRuleItem struct {
	ID           string    `json:"id"`
	Make         string    `json:"make"`
	Model        string    `json:"model"`
	UTCOffsetMin int       `json:"utc_offset_min"`
	StartsOn     *string   `json:"starts_on"`
	EndsOn       *string   `json:"ends_on"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// clockRuleColumns is what every statement returns, in scanClockRule's order.
const clockRuleColumns = `id::text, make, model, utc_offset_min, starts_on, ends_on, note, created_at, updated_at`

func scanClockRule(row pgx.Row) (clockRuleItem, error) {
	var c clockRuleItem
	var startsOn, endsOn *time.Time
	if err := row.Scan(&c.ID, &c.Make, &c.Model, &c.UTCOffsetMin, &startsOn, &endsOn, &c.Note,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return clockRuleItem{}, err
	}
	c.StartsOn, c.EndsOn = formatRuleDate(startsOn), formatRuleDate(endsOn)
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

// libraryCamera is one camera in the caller's own library, for the rule dialog.
type libraryCamera struct {
	Make         string     `json:"make"`
	Model        string     `json:"model"`
	Items        int        `json:"items"`
	FirstTakenAt *time.Time `json:"first_taken_at"` // RFC 3339 UTC; null without a time JSON can carry
	LastTakenAt  *time.Time `json:"last_taken_at"`
}

func writeClockRuleJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("clock rules: encode response failed", "err", err)
	}
}

// clockRuleCamera checks a make or a model and returns it as ingest.CleanTag leaves it, the form
// ClockRules.Match compares a file's EXIF in. CleanTag cuts at 200 runes, so a longer value is
// refused here instead of being cut: JSON decoding already turned invalid UTF-8 into U+FFFD, and
// CleanTag only removes from a value, so one of at most 200 runes after trimming is never cut.
func clockRuleCamera(field, raw string) (string, error) {
	cleaned := ingest.CleanTag(raw)
	if cleaned == "" || utf8.RuneCountInString(strings.TrimSpace(raw)) > maxClockRuleCameraRunes {
		return "", fmt.Errorf("%s must be 1-%d characters", field, maxClockRuleCameraRunes)
	}
	return cleaned, nil
}

// clockRuleDay checks a rule's day: null, or a real date written exactly "2006-01-02" in the years
// 1900-9998.
func clockRuleDay(field string, raw *string) (*string, *time.Time, error) {
	if raw == nil {
		return nil, nil, nil
	}
	day, err := time.Parse(clockRuleDate, *raw)
	if err != nil || day.Format(clockRuleDate) != *raw || day.Year() < minClockRuleYear || day.Year() > maxClockRuleYear {
		return nil, nil, fmt.Errorf("%s must be a date written YYYY-MM-DD in the years %d-%d, or null",
			field, minClockRuleYear, maxClockRuleYear)
	}
	return raw, &day, nil
}

// validateClockRule checks in against camera_clock_rules' constraints, except the overlap with the
// owner's other rules, which only the database can see.
func validateClockRule(in clockRuleInput) (clockRuleValues, error) {
	var v clockRuleValues
	var err error
	if v.make, err = clockRuleCamera("make", in.Make); err != nil {
		return clockRuleValues{}, err
	}
	if v.model, err = clockRuleCamera("model", in.Model); err != nil {
		return clockRuleValues{}, err
	}
	if in.UTCOffsetMin == nil {
		return clockRuleValues{}, errors.New("utc_offset_min is required")
	}
	v.offsetMin = *in.UTCOffsetMin
	if v.offsetMin < minClockRuleOffsetMin || v.offsetMin > maxClockRuleOffsetMin || v.offsetMin%clockRuleOffsetStepMin != 0 {
		return clockRuleValues{}, fmt.Errorf("utc_offset_min must be a multiple of %d from %d to %d",
			clockRuleOffsetStepMin, minClockRuleOffsetMin, maxClockRuleOffsetMin)
	}
	var startsOn, endsOn *time.Time
	if v.startsOn, startsOn, err = clockRuleDay("starts_on", in.StartsOn); err != nil {
		return clockRuleValues{}, err
	}
	if v.endsOn, endsOn, err = clockRuleDay("ends_on", in.EndsOn); err != nil {
		return clockRuleValues{}, err
	}
	if startsOn != nil && endsOn != nil && startsOn.After(*endsOn) {
		return clockRuleValues{}, errors.New("starts_on must not be after ends_on")
	}
	// PostgreSQL TEXT cannot hold NUL. A note is the user's own text, so it is refused rather than
	// silently changed.
	v.note = strings.TrimSpace(in.Note)
	if strings.ContainsRune(v.note, 0) {
		return clockRuleValues{}, errors.New("note must not contain NUL characters")
	}
	if utf8.RuneCountInString(v.note) > maxClockRuleNoteRunes {
		return clockRuleValues{}, fmt.Errorf("note must be at most %d characters", maxClockRuleNoteRunes)
	}
	return v, nil
}

// decodeClockRule reads and validates the body of POST and PUT, or writes 400 (413 for a body over
// 16 KiB). Unknown keys and anything after the object are refused.
func decodeClockRule(w http.ResponseWriter, r *http.Request) (clockRuleValues, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClockRuleBodyBytes))
	dec.DisallowUnknownFields()
	var in clockRuleInput
	err := dec.Decode(&in)
	if err == nil {
		if _, next := dec.Token(); next != io.EOF {
			err = errors.Join(errors.New("data after the JSON object"), next)
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return clockRuleValues{}, false
		}
		http.Error(w, "invalid body: want a JSON object with make, model, utc_offset_min, starts_on, ends_on and note",
			http.StatusBadRequest)
		return clockRuleValues{}, false
	}
	v, err := validateClockRule(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return clockRuleValues{}, false
	}
	return v, true
}

// clockRuleWriteError answers a failed INSERT or UPDATE. The overlap with another of the owner's
// rules is a conflict; a CHECK violation means validateClockRule let through something the table
// refuses, which is still the request's fault and never a 500. It reports false for anything else.
func clockRuleWriteError(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case pgExclusionViolation:
		http.Error(w, clockRuleOverlapMessage, http.StatusConflict)
		return true
	case pgCheckViolation:
		slog.Warn("clock rules: a check the handler should have made first", "constraint", pgErr.ConstraintName)
		http.Error(w, "the rule is not valid ("+pgErr.ConstraintName+")", http.StatusBadRequest)
		return true
	}
	return false
}

// clockRuleIDParam returns the canonical {id}, or writes 404 for a malformed one.
func clockRuleIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
	}
	return id, ok
}

// List returns the caller's rules in loadClockRules' order (camera without case, then the period's
// start), [] when there are none.
func (h *CameraClockRuleHandler) List(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	rows, err := h.db.Query(r.Context(), `
		SELECT `+clockRuleColumns+`
		FROM camera_clock_rules
		WHERE owner_id = $1
		ORDER BY lower(make), lower(model), starts_on NULLS FIRST, id`, me)
	if err != nil {
		slog.Error("clock rules: list query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (clockRuleItem, error) {
		return scanClockRule(row)
	})
	if err != nil {
		slog.Error("clock rules: list rows failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if rules == nil {
		rules = []clockRuleItem{}
	}
	writeClockRuleJSON(w, http.StatusOK, rules)
}

// Create stores a new rule: 201 with the rule, 409 when it overlaps another rule for the same
// camera or the caller already has maxClockRulesPerOwner rules.
//
// The count and the insert share a transaction that first locks the caller's users row, so two
// POSTs of one owner run one after the other and the second counts the first's rule: without the
// lock both could count 99 and insert. FOR NO KEY UPDATE does not block the FOR KEY SHARE locks that
// inserts referencing the user take, so uploads go on meanwhile.
func (h *CameraClockRuleHandler) Create(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	v, ok := decodeClockRule(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	tx, err := h.db.Begin(ctx)
	if err != nil {
		slog.Error("clock rules: create begin tx failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE`, me); err != nil {
		slog.Error("clock rules: create owner lock failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM camera_clock_rules WHERE owner_id = $1`, me).Scan(&count); err != nil {
		slog.Error("clock rules: create count failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if count >= maxClockRulesPerOwner {
		http.Error(w, fmt.Sprintf("at most %d camera clock rules", maxClockRulesPerOwner), http.StatusConflict)
		return
	}
	rule, err := scanClockRule(tx.QueryRow(ctx, `
		INSERT INTO camera_clock_rules (owner_id, make, model, utc_offset_min, starts_on, ends_on, note)
		VALUES ($1, $2, $3, $4, $5::date, $6::date, $7)
		RETURNING `+clockRuleColumns,
		me, v.make, v.model, v.offsetMin, v.startsOn, v.endsOn, v.note))
	if err != nil {
		if clockRuleWriteError(w, err) {
			return
		}
		slog.Error("clock rules: insert failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		if clockRuleWriteError(w, err) {
			return
		}
		slog.Error("clock rules: create commit failed", "rule_id", rule.ID, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Info("clock rules: created", "rule_id", rule.ID, "owner", me)
	writeClockRuleJSON(w, http.StatusCreated, rule)
}

// Update replaces a rule with the body: 200 with the rule.
//
// As in the album routes, a malformed ID is 404 first, then the body is checked (400), and only
// then is the rule looked up, by the UPDATE itself: a foreign or missing ID with a bad body is 400
// exactly as for one's own rule, and with a good body 404. The overlap check compares with the
// caller's own rules only, so a foreign ID can never answer 409 either.
func (h *CameraClockRuleHandler) Update(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := clockRuleIDParam(w, r)
	if !ok {
		return
	}
	v, ok := decodeClockRule(w, r)
	if !ok {
		return
	}

	rule, err := scanClockRule(h.db.QueryRow(r.Context(), `
		UPDATE camera_clock_rules
		SET make = $3, model = $4, utc_offset_min = $5, starts_on = $6::date, ends_on = $7::date,
		    note = $8, updated_at = now()
		WHERE id = $1 AND owner_id = $2
		RETURNING `+clockRuleColumns,
		id, me, v.make, v.model, v.offsetMin, v.startsOn, v.endsOn, v.note))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		if clockRuleWriteError(w, err) {
			return
		}
		slog.Error("clock rules: update failed", "rule_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Info("clock rules: updated", "rule_id", id, "owner", me)
	writeClockRuleJSON(w, http.StatusOK, rule)
}

// Delete removes a rule: 204. Items already dated with it keep their times and their evidence's
// snapshot of it.
func (h *CameraClockRuleHandler) Delete(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	id, ok := clockRuleIDParam(w, r)
	if !ok {
		return
	}
	tag, err := h.db.Exec(r.Context(), `DELETE FROM camera_clock_rules WHERE id = $1 AND owner_id = $2`, id, me)
	if err != nil {
		slog.Error("clock rules: delete failed", "rule_id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("clock rules: deleted", "rule_id", id, "owner", me)
	w.WriteHeader(http.StatusNoContent)
}

// cameraSpelling is one spelling of a camera in the library, as the cameras query groups it.
type cameraSpelling struct {
	make, model string
	items       int
	first, last *time.Time
}

// Cameras lists the cameras in the caller's own library, the ones a rule can name: GET /api/cameras.
// The make is read from capture_evidence and the model from camera_model, so only items ingested
// since migration 10 count. Spellings that ingest.ClockRules.Match would take for one camera (the
// same after ingest.CleanTag, without case) are one camera, shown as the spelling most items carry.
// The order is by items, most first, then by camera.
//
// The literal owner_id is deliberate, not readableMediaCond: a rule applies to the owner's own
// uploads, so a camera seen only in media shared through an album is not offered.
func (h *CameraClockRuleHandler) Cameras(w http.ResponseWriter, r *http.Request) {
	me := auth.MustUser(r.Context()).ID
	rows, err := h.db.Query(r.Context(), `
		SELECT m.capture_evidence->>'make', m.camera_model, count(*), min(m.taken_at), max(m.taken_at)
		FROM media m
		WHERE m.owner_id = $1 AND m.capture_evidence->>'make' IS NOT NULL AND m.camera_model IS NOT NULL
		GROUP BY 1, 2`, me)
	if err != nil {
		slog.Error("clock rules: cameras query failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	spellings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (cameraSpelling, error) {
		var s cameraSpelling
		err := row.Scan(&s.make, &s.model, &s.items, &s.first, &s.last)
		return s, err
	})
	if err != nil {
		slog.Error("clock rules: cameras rows failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeClockRuleJSON(w, http.StatusOK, groupCameras(spellings))
}

// groupCameras merges the spellings of one camera and orders the result. Spellings with nothing
// left after ingest.CleanTag are dropped: no rule could name them.
func groupCameras(spellings []cameraSpelling) []libraryCamera {
	type group struct {
		makeKey, modelKey string
		cameraSpelling        // the totals; make and model are the shown spelling
		best              int // items of the shown spelling
	}
	groups := map[string]*group{}
	for _, s := range spellings {
		cameraMake, model := ingest.CleanTag(s.make), ingest.CleanTag(s.model)
		if cameraMake == "" || model == "" {
			continue
		}
		makeKey, modelKey := strings.ToLower(cameraMake), strings.ToLower(model)
		key := makeKey + "\x00" + modelKey
		g, ok := groups[key]
		if !ok {
			g = &group{makeKey: makeKey, modelKey: modelKey}
			groups[key] = g
		}
		// Byte order breaks a tie, so the shown spelling does not depend on the query's row order.
		if s.items > g.best || (s.items == g.best && cameraMake+"\x00"+model < g.make+"\x00"+g.model) {
			g.make, g.model, g.best = cameraMake, model, s.items
		}
		g.items += s.items
		g.first = earlier(g.first, s.first)
		g.last = later(g.last, s.last)
	}

	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.items != b.items {
			return a.items > b.items
		}
		if a.makeKey != b.makeKey {
			return a.makeKey < b.makeKey
		}
		return a.modelKey < b.modelKey
	})
	cameras := make([]libraryCamera, 0, len(ordered))
	for _, g := range ordered {
		cameras = append(cameras, libraryCamera{
			Make: g.make, Model: g.model, Items: g.items,
			FirstTakenAt: utcTime(g.first), LastTakenAt: utcTime(g.last),
		})
	}
	return cameras
}

func earlier(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.Before(*a)) {
		return b
	}
	return a
}

func later(a, b *time.Time) *time.Time {
	if a == nil || (b != nil && b.After(*a)) {
		return b
	}
	return a
}
