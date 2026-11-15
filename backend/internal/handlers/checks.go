package handlers

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/gpstrack"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// checksTimeout bounds GET /api/checks. On one real library of 6.2 million track points it took
	// about two seconds.
	checksTimeout = 30 * time.Second
	// checksMaxMediaIDs caps a group's media_ids, the items the evidence view steps through.
	checksMaxMediaIDs = 200
	// futurePointsMargin: a track point dated more than this after the report is in the future. A
	// day keeps a clock or zone slip of a few hours from counting.
	futurePointsMargin = 24 * time.Hour
	// residualWindow: a log point this close to a photo's time, on either side, measures its format
	// against the photo's own position.
	residualWindow = 5 * time.Second
)

// The checks of GET /api/checks. pair_impossible and pair_far are Step 1's warnings of the same
// name, applied to every estimate.
const (
	checksPairImpossible         = warningPairImpossible
	checksPairFar                = warningPairFar
	checksFutureTrackPoints      = "future_track_points"
	checksTakenAfterUpload       = "taken_after_upload"
	checksStaleEstimates         = "stale_estimates"
	checksOrphanPoints           = "orphan_points"
	checksTimeMayBeEstimatedUsed = "time_may_be_estimated_used"

	checksSeverityHigh = "high"
	checksSeverityLow  = "low"
)

const gpsSourceInterpolated = "interpolated"

// What a recompute leaves out to test an estimate that rests on a point whose time may be
// estimated: KML and GeoJSON go together, so that neither stands in for the other. KMZ's time
// spacing was never checked, so its group is unproven and leaves out kmz as well.
var (
	excludeKMLGeoJSON    = []string{gpstrack.FormatKML, gpstrack.FormatGeoJSON}
	excludeKMLGeoJSONKMZ = []string{gpstrack.FormatKML, gpstrack.FormatGeoJSON, gpstrack.FormatKMZ}
)

// timeMayBeEstimatedFormats are the groups of time_may_be_estimated_used, in a fixed order.
var timeMayBeEstimatedFormats = []string{gpstrack.FormatKML, gpstrack.FormatGeoJSON, gpstrack.FormatKMZ}

// ChecksHandler serves the Checks page, which finds suspect track files, formats and items without
// the owner having to suspect one: GET /api/checks, and GET /api/checks/agreement for the slower
// comparison of each file with the others. Both return the owner's track files and findings drawn
// from their location history, so they are session-only, and every query filters on a literal
// owner_id and never uses readableMediaCond: media shared with the owner and other users' tracks
// never enter a result.
type ChecksHandler struct {
	db                     checksDB
	matchWindow            time.Duration
	agreementBudget        time.Duration // zero means agreementWorkBudget
	agreementChunkSize     int           // track files per chunk; zero means agreementChunkFiles
	agreementChunkPointCap int           // points per chunk, unless one file has more; zero means agreementChunkPoints
	// agreementRuns holds a token per agreement run in progress, at most agreementMaxRuns. Nil, as
	// in tests that build the handler literally, sets no bound.
	agreementRuns chan struct{}
}

// checksQuerier is what the checks' statements run on: the report's transaction, or the pool for
// the agreement. *pgxpool.Pool and pgx.Tx satisfy it.
type checksQuerier interface {
	matcherDB
	batchDB
}

// checksDB is what the checks query through. *pgxpool.Pool satisfies it.
type checksDB interface {
	checksQuerier
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

func NewChecksHandler(pool *pgxpool.Pool, matchWindow time.Duration) *ChecksHandler {
	return &ChecksHandler{db: pool, matchWindow: matchWindow, agreementRuns: make(chan struct{}, agreementMaxRuns)}
}

// ChecksResponse is GET /api/checks. Instants are always UTC on the wire, because pgx scans a
// timestamptz into the process's local zone.
type ChecksResponse struct {
	GeneratedAt time.Time        `json:"generated_at"`
	WindowS     int64            `json:"window_s"` // the match window, so a mismatch between processes shows
	Files       []checksFile     `json:"files"`
	Residuals   []checksResidual `json:"residuals"`
	Checks      []checksCheck    `json:"checks"`
}

// checksFile is one track file: every point of the owner with one source label, the derived exif
// and manual labels included.
type checksFile struct {
	Source string     `json:"source"`
	Format string     `json:"format"` // the raw prefix, so an unknown one is shown as written
	Kind   string     `json:"kind"`
	Points int        `json:"points"`
	First  *time.Time `json:"first"` // nil when JSON cannot carry the year
	Last   *time.Time `json:"last"`
	// ItemsPlaced counts the own estimates whose recomputed before or after point has this label.
	ItemsPlaced int `json:"items_placed"`
}

// checksResidual is how far one log format's points lie from photos with their own position at the
// same moment. It measures disagreement and does not say which side is right.
type checksResidual struct {
	Format string  `json:"format"` // the raw prefix
	N      int     `json:"n"`
	P50M   float64 `json:"p50_m"`
	P90M   float64 `json:"p90_m"`
}

type checksCheck struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Total counts distinct items across the groups, or points for the two point checks.
	Total  int           `json:"total"`
	Groups []checksGroup `json:"groups"`
}

type checksGroup struct {
	Key      string         `json:"key"`
	Label    string         `json:"label"`
	Count    int            `json:"count"`
	Evidence map[string]any `json:"evidence"`
	MediaIDs []string       `json:"media_ids"` // the first checksMaxMediaIDs by taken_at, then id
}

// Get serves GET /api/checks. A deadline or a cancelled request is a 504.
func (h *ChecksHandler) Get(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	ctx, cancel := context.WithTimeout(r.Context(), checksTimeout)
	defer cancel()

	start := time.Now()
	resp, err := h.report(ctx, ownerID, start.UTC())
	if err != nil {
		if cause := ctx.Err(); cause != nil {
			// Closing the view cancels the request, which is routine; a deadline is not.
			level := slog.LevelWarn
			if errors.Is(cause, context.Canceled) {
				level = slog.LevelDebug
			}
			slog.Log(ctx, level, "checks: stopped", "cause", cause, "elapsed", time.Since(start))
			writeChecksJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "checks_timeout"})
			return
		}
		slog.Error("checks: compute failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	slog.Debug("checks: done", "files", len(resp.Files), "elapsed", time.Since(start))
	writeChecksJSON(w, http.StatusOK, resp)
}

func writeChecksJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("checks: encode response failed", "err", err)
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.Warn("checks: write response failed", "err", err)
	}
}

// The report reads one snapshot, so a track imported meanwhile cannot make its sections disagree,
// and writes nothing.
var checksTxOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// checksJITOff is the report's first statement. The planner expects the residuals join to return
// hundreds of thousands of points per photo instead of a handful, and on one real library JIT then
// spent 150–360 ms compiling the statement for 30–60 ms of work, on every run.
const checksJITOff = "SET LOCAL jit = off"

// report runs every query of GET /api/checks, in one read-only transaction. generatedAt is the
// moment it describes: future points are counted from it.
func (h *ChecksHandler) report(ctx context.Context, ownerID string, generatedAt time.Time) (ChecksResponse, error) {
	tx, err := h.db.BeginTx(ctx, checksTxOptions)
	if err != nil {
		return ChecksResponse{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, checksJITOff); err != nil {
		return ChecksResponse{}, fmt.Errorf("jit off: %w", err)
	}

	files, err := loadTrackFileRows(ctx, tx, trackFilesSQL, ownerID)
	if err != nil {
		return ChecksResponse{}, fmt.Errorf("track files: %w", err)
	}
	estimates, err := h.estimates(ctx, tx, ownerID)
	if err != nil {
		return ChecksResponse{}, err
	}
	residuals, err := loadResidualPairs(ctx, tx, ownerID)
	if err != nil {
		return ChecksResponse{}, err
	}
	future, err := loadTrackFileRows(ctx, tx, futurePointsSQL, ownerID, generatedAt.Add(futurePointsMargin))
	if err != nil {
		return ChecksResponse{}, fmt.Errorf("future points: %w", err)
	}
	afterUpload, err := loadTakenAfterUpload(ctx, tx, ownerID)
	if err != nil {
		return ChecksResponse{}, err
	}
	orphans, err := loadOrphanPoints(ctx, tx, ownerID, derivedLabels(files))
	if err != nil {
		return ChecksResponse{}, err
	}

	return ChecksResponse{
		GeneratedAt: generatedAt,
		WindowS:     int64(h.matchWindow / time.Second),
		Files:       newChecksFiles(files, estimates),
		Residuals:   newChecksResiduals(residuals),
		// High severity first.
		Checks: []checksCheck{
			pairCheck(checksPairImpossible, checksSeverityHigh, estimates),
			futurePointsCheck(future),
			takenAfterUploadCheck(afterUpload),
			staleEstimatesCheck(estimates),
			pairCheck(checksPairFar, checksSeverityLow, estimates),
			orphanPointsCheck(orphans, estimates),
			timeMayBeEstimatedCheck(estimates),
		},
	}, nil
}

// trackFileRow is one track file: a source label, its points and their time span.
type trackFileRow struct {
	source      string
	points      int
	first, last time.Time
}

const (
	// Every track file of the owner, derived labels included.
	trackFilesSQL = `
		SELECT source, count(*), min(recorded_at), max(recorded_at)
		FROM gps_tracks
		WHERE owner_id = $1
		GROUP BY source
	`
	// The points of each file dated after $2.
	futurePointsSQL = `
		SELECT source, count(*), min(recorded_at), max(recorded_at)
		FROM gps_tracks
		WHERE owner_id = $1 AND recorded_at > $2
		GROUP BY source
	`
)

// loadTrackFileRows runs one of the two statements above and orders the files by label.
func loadTrackFileRows(ctx context.Context, db matcherDB, sql string, args ...any) ([]trackFileRow, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackFileRow, error) {
		var f trackFileRow
		err := row.Scan(&f.source, &f.points, &f.first, &f.last)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(a, b trackFileRow) int { return strings.Compare(a.source, b.source) })
	return files, nil
}

// derivedLabels are the files written from the owner's own photos and pins.
func derivedLabels(files []trackFileRow) []string {
	labels := []string{}
	for _, f := range files {
		if isDerivedPrefix(sourcePrefix(f.source)) {
			labels = append(labels, f.source)
		}
	}
	return labels
}

// checkedEstimate is one own estimate as GET /api/checks recomputes it: the matcher's bracket now,
// with no exclusions, and what the evidence view would say about the result.
type checkedEstimate struct {
	id            string
	takenAt       time.Time
	stored        *position  // nil when the row has no location
	before, after *geo.Point // the recomputed bracket
	state         string     // placementState
	now           *provenanceEstimate
	warnings      []provenanceWarning // placementWarnings(now)
	// The recompute without the formats whose times may be estimated: withoutKMLGeoJSON for an
	// estimate that rests on a kml or geojson point, withoutKMZ for one on a kmz point. Each
	// is nil when the estimate does not rest on such a point.
	withoutKMLGeoJSON, withoutKMZ *geo.Explanation
}

// estimateRow is one own estimate as stored.
type estimateRow struct {
	id         string
	takenAt    time.Time
	stored     *position
	confidence *float64
}

// Own estimates, in the order media_ids lists them.
const checksEstimatesSQL = `
	SELECT m.id::text, m.taken_at, ST_Y(m.location::geometry), ST_X(m.location::geometry), m.gps_confidence
	FROM media m
	WHERE m.owner_id = $1 AND m.gps_source = 'interpolated' AND m.taken_at IS NOT NULL
	ORDER BY m.taken_at, m.id
`

// estimates recomputes every own estimate once, through the matcher's own bracket query and
// arithmetic, so a finding can never disagree with the evidence view of the same item. A taken_at
// whose match window JSON cannot carry is left out, as the evidence view reads it as no time.
func (h *ChecksHandler) estimates(ctx context.Context, q checksQuerier, ownerID string) ([]checkedEstimate, error) {
	rows, err := q.Query(ctx, checksEstimatesSQL, ownerID)
	if err != nil {
		return nil, fmt.Errorf("estimates: %w", err)
	}
	stored, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (estimateRow, error) {
		var e estimateRow
		var lat, lng *float64
		if err := row.Scan(&e.id, &e.takenAt, &lat, &lng, &e.confidence); err != nil {
			return e, err
		}
		if lat != nil && lng != nil {
			e.stored = &position{lat: *lat, lng: *lng}
		}
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read estimates: %w", err)
	}
	stored = slices.DeleteFunc(stored, func(e estimateRow) bool { return !windowFits(e.takenAt, h.matchWindow) })

	lookups := make([]bracketLookup, len(stored))
	for i, e := range stored {
		lookups[i] = bracketLookup{at: e.takenAt}
	}
	pairs, err := bracketingPointsMany(ctx, q, ownerID, lookups, h.matchWindow)
	if err != nil {
		return nil, fmt.Errorf("estimate brackets: %w", err)
	}
	estimates := make([]checkedEstimate, len(stored))
	for i, e := range stored {
		estimates[i] = newCheckedEstimate(e, pairs[i], h.matchWindow)
	}
	if err := h.recomputeWithoutEstimatedTimes(ctx, q, ownerID, estimates); err != nil {
		return nil, err
	}
	return estimates, nil
}

// windowFits reports whether both ends of the match window around t can be sent as JSON, the test
// ProvenanceHandler.matchWindowFits applies before it explains an item.
func windowFits(t time.Time, window time.Duration) bool {
	from, to := t.Add(-window), t.Add(window)
	return utcTime(&from) != nil && utcTime(&to) != nil
}

// newCheckedEstimate explains one estimate's recomputed bracket as the evidence view does: the
// shared placementState and placementWarnings on the recompute described as placement.now. Derived
// points get no origin, because no check here reads the warning that needs one.
func newCheckedEstimate(row estimateRow, pair bracketPair, window time.Duration) checkedEstimate {
	e := geo.Explain(pair.before, pair.after, row.takenAt, window)
	c := checkedEstimate{id: row.id, takenAt: row.takenAt, stored: row.stored, before: pair.before, after: pair.after}
	source := gpsSourceInterpolated
	facts := placementFacts{
		GPSSource: &source, HasTime: true, HasLocation: row.stored != nil, HasBracket: e.OK,
		StoredConfidence: row.confidence, NowConfidence: e.Confidence,
	}
	if e.OK {
		facts.DeltaM = distanceFrom(row.stored, e.Lat, e.Lng)
		c.now = &provenanceEstimate{
			Lat: e.Lat, Lng: e.Lng, Confidence: e.Confidence, DeltaM: facts.DeltaM,
			Before: bracketOf(pair.before, row.takenAt), After: bracketOf(pair.after, row.takenAt),
			Arithmetic: newArithmetic(e),
		}
	}
	c.state = placementState(facts)
	c.warnings = placementWarnings(c.now)
	return c
}

func bracketOf(p *geo.Point, takenAt time.Time) *provenanceBracket {
	if p == nil {
		return nil
	}
	return newBracket(p, takenAt)
}

// uses reports whether one of the estimate's recomputed bracket points has the raw prefix format.
func (e checkedEstimate) uses(format string) bool {
	return (e.before != nil && sourcePrefix(e.before.Source) == format) ||
		(e.after != nil && sourcePrefix(e.after.Source) == format)
}

// sources are the labels of the estimate's recomputed bracket points, each once.
func (e checkedEstimate) sources() []string {
	var labels []string
	for _, p := range []*geo.Point{e.before, e.after} {
		if p != nil && !slices.Contains(labels, p.Source) {
			labels = append(labels, p.Source)
		}
	}
	return labels
}

func (e checkedEstimate) hasWarning(code string) bool {
	return slices.ContainsFunc(e.warnings, func(w provenanceWarning) bool { return w.Code == code })
}

// reference is the position a move is measured from: the stored one, or the recompute's when the
// row has none, as the evidence view measures moved_m.
func (e checkedEstimate) reference() *position {
	if e.stored == nil && e.now != nil {
		return &position{lat: e.now.Lat, lng: e.now.Lng}
	}
	return e.stored
}

// recomputeWithoutEstimatedTimes fills withoutKMLGeoJSON and withoutKMZ for the estimates that
// rest on a kml, geojson or kmz point, in one more batch of bracket lookups.
func (h *ChecksHandler) recomputeWithoutEstimatedTimes(ctx context.Context, q batchDB, ownerID string, estimates []checkedEstimate) error {
	type target struct {
		estimate int
		unproven bool
	}
	var lookups []bracketLookup
	var targets []target
	for i, e := range estimates {
		if e.uses(gpstrack.FormatKML) || e.uses(gpstrack.FormatGeoJSON) {
			lookups = append(lookups, bracketLookup{at: e.takenAt, opts: bracketOptions{excludeFormats: excludeKMLGeoJSON}})
			targets = append(targets, target{estimate: i})
		}
		if e.uses(gpstrack.FormatKMZ) {
			lookups = append(lookups, bracketLookup{at: e.takenAt, opts: bracketOptions{excludeFormats: excludeKMLGeoJSONKMZ}})
			targets = append(targets, target{estimate: i, unproven: true})
		}
	}
	pairs, err := bracketingPointsMany(ctx, q, ownerID, lookups, h.matchWindow)
	if err != nil {
		return fmt.Errorf("brackets without estimated times: %w", err)
	}
	for j, t := range targets {
		e := &estimates[t.estimate]
		alt := geo.Explain(pairs[j].before, pairs[j].after, e.takenAt, h.matchWindow)
		if t.unproven {
			e.withoutKMZ = &alt
		} else {
			e.withoutKMLGeoJSON = &alt
		}
	}
	return nil
}

// residualPair is one own-position photo and the nearest point of one log format to its time.
type residualPair struct {
	format             string
	photoLat, photoLng float64
	lat, lng           float64
}

// For each photo with its own position, the nearest point in time of each log format within $2 on
// either side: the earlier on a tie, then the first source. Each photo costs one index range scan
// of the window.
const residualPairsSQL = `
	SELECT DISTINCT ON (m.id, split_part(g.source, ':', 1))
	       split_part(g.source, ':', 1),
	       ST_Y(m.location::geometry), ST_X(m.location::geometry),
	       ST_Y(g.location::geometry), ST_X(g.location::geometry)
	FROM media m
	JOIN gps_tracks g
	  ON g.owner_id = $1 AND g.recorded_at BETWEEN m.taken_at - $2::interval AND m.taken_at + $2::interval
	WHERE m.owner_id = $1 AND m.gps_source = 'exif' AND m.media_type IN ('photo', 'photo_360')
	  AND m.taken_at IS NOT NULL AND m.location IS NOT NULL
	  AND split_part(g.source, ':', 1) NOT IN ('exif', 'manual')
	ORDER BY m.id, split_part(g.source, ':', 1), abs(extract(epoch FROM g.recorded_at - m.taken_at)), g.recorded_at, g.source
`

func loadResidualPairs(ctx context.Context, db matcherDB, ownerID string) ([]residualPair, error) {
	rows, err := db.Query(ctx, residualPairsSQL, ownerID, residualWindow)
	if err != nil {
		return nil, fmt.Errorf("residuals: %w", err)
	}
	pairs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (residualPair, error) {
		var p residualPair
		err := row.Scan(&p.format, &p.photoLat, &p.photoLng, &p.lat, &p.lng)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read residuals: %w", err)
	}
	return pairs, nil
}

// afterUploadItem is one own item dated after its upload.
type afterUploadItem struct {
	id      string
	camera  string // "" when unknown
	located bool
	aheadS  float64 // taken_at minus created_at
}

// Computed in SQL: a time.Duration saturates at 292 years, and such a time can be decades ahead.
const takenAfterUploadSQL = `
	SELECT m.id::text, coalesce(m.camera_model, ''), m.location IS NOT NULL,
	       extract(epoch FROM m.taken_at - m.created_at)::float8
	FROM media m
	WHERE m.owner_id = $1 AND m.taken_at > m.created_at
	ORDER BY m.taken_at, m.id
`

func loadTakenAfterUpload(ctx context.Context, db matcherDB, ownerID string) ([]afterUploadItem, error) {
	rows, err := db.Query(ctx, takenAfterUploadSQL, ownerID)
	if err != nil {
		return nil, fmt.Errorf("taken after upload: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (afterUploadItem, error) {
		var it afterUploadItem
		err := row.Scan(&it.id, &it.camera, &it.located, &it.aheadS)
		return it, err
	})
	if err != nil {
		return nil, fmt.Errorf("read taken after upload: %w", err)
	}
	return items, nil
}

// orphanPoint is a derived track point that no own item explains any more.
type orphanPoint struct {
	source string
	at     time.Time
}

// Derived points with no own photo or pin at their instant, as Step 1's origin finds them. $2 holds
// the owner's derived labels, so the scan takes the (owner_id, source, recorded_at) index instead of
// reading every point of the owner for its prefix.
const orphanPointsSQL = `
	SELECT g.source, g.recorded_at
	FROM gps_tracks g
	WHERE g.owner_id = $1 AND g.source = ANY($2)
	  AND NOT EXISTS (
		SELECT 1 FROM media m
		WHERE m.owner_id = $1 AND m.taken_at = g.recorded_at AND m.gps_source IN ('exif', 'manual'))
`

func loadOrphanPoints(ctx context.Context, db matcherDB, ownerID string, labels []string) ([]orphanPoint, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, orphanPointsSQL, ownerID, labels)
	if err != nil {
		return nil, fmt.Errorf("orphan points: %w", err)
	}
	points, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (orphanPoint, error) {
		var p orphanPoint
		err := row.Scan(&p.source, &p.at)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read orphan points: %w", err)
	}
	return points, nil
}

// newChecksFiles lists every file of the inventory in label order, with the estimates each places.
func newChecksFiles(rows []trackFileRow, estimates []checkedEstimate) []checksFile {
	placed := map[string]int{}
	for _, e := range estimates {
		for _, label := range e.sources() {
			placed[label]++
		}
	}
	files := make([]checksFile, 0, len(rows))
	for _, r := range rows {
		files = append(files, checksFile{
			Source: r.source, Format: sourcePrefix(r.source), Kind: trackKind(r.source), Points: r.points,
			First: utcTime(&r.first), Last: utcTime(&r.last), ItemsPlaced: placed[r.source],
		})
	}
	return files
}

// newChecksResiduals measures each format's distance from the photos, lowest median first.
func newChecksResiduals(pairs []residualPair) []checksResidual {
	byFormat := map[string][]float64{}
	for _, p := range pairs {
		byFormat[p.format] = append(byFormat[p.format], geo.HaversineKm(p.photoLat, p.photoLng, p.lat, p.lng)*1000)
	}
	residuals := make([]checksResidual, 0, len(byFormat))
	for format, metres := range byFormat {
		slices.Sort(metres)
		residuals = append(residuals, checksResidual{
			Format: format, N: len(metres), P50M: percentile(metres, 0.5), P90M: percentile(metres, 0.9),
		})
	}
	slices.SortFunc(residuals, func(a, b checksResidual) int {
		return cmp.Or(cmp.Compare(a.P50M, b.P50M), strings.Compare(a.Format, b.Format))
	})
	return residuals
}

// percentile is the p-quantile of sorted values by linear interpolation between the closest ranks,
// as PostgreSQL's percentile_cont computes it. One value is its own every quantile. sorted must
// not be empty.
func percentile(sorted []float64, p float64) float64 {
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := min(int(math.Ceil(pos)), len(sorted)-1)
	return sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
}

// checkGroups collects one check's groups by key. Every check labels a group with its key.
type checkGroups map[string]*checksGroup

func (gs checkGroups) get(key string) *checksGroup {
	g, ok := gs[key]
	if !ok {
		g = &checksGroup{Key: key, Label: key, Evidence: map[string]any{}, MediaIDs: []string{}}
		gs[key] = g
	}
	return g
}

// check lists the groups by count, largest first, then by key. A group that counted nothing is
// never listed, and a check that found nothing has an empty list.
func (gs checkGroups) check(code, severity string, total int) checksCheck {
	groups := []checksGroup{}
	for _, g := range gs {
		if g.Count > 0 {
			groups = append(groups, *g)
		}
	}
	slices.SortFunc(groups, func(a, b checksGroup) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Key, b.Key))
	})
	return checksCheck{Code: code, Severity: severity, Total: total, Groups: groups}
}

// list adds id to media_ids while there is room. Items are walked in taken_at, id order, so the
// list keeps the earliest.
func (g *checksGroup) list(id string) {
	if len(g.MediaIDs) < checksMaxMediaIDs {
		g.MediaIDs = append(g.MediaIDs, id)
	}
}

// add counts one item and lists it.
func (g *checksGroup) add(id string) {
	g.Count++
	g.list(id)
}

// raise sets evidence key to v when v is larger than what it holds, or when it holds nothing.
func (g *checksGroup) raise(key string, v float64) {
	if cur, ok := g.Evidence[key].(float64); !ok || v > cur {
		g.Evidence[key] = v
	}
}

// pairKey is a format pair's group key: the two raw prefixes in order, so a pair reads the same
// whichever side each format is on.
func pairKey(a, b string) string {
	if b < a {
		a, b = b, a
	}
	return a + "+" + b
}

// topSource is the label the most estimates of a group rest on, and how many estimates that is. ok
// is false when another label is as frequent: in the common case of one suspect file against one
// log, every estimate of the group rests on both, and naming either would point at it alone.
func topSource(estimates map[string]int) (label string, n int, ok bool) {
	for l, c := range estimates {
		switch {
		case c > n:
			label, n, ok = l, c, true
		case c == n:
			ok = false
		}
	}
	return label, n, ok
}

// pairCheck groups the estimates whose recompute carries the Step 1 warning code by the pair of
// formats they interpolate across, so that one bad file shows as one row.
func pairCheck(code, severity string, estimates []checkedEstimate) checksCheck {
	groups := checkGroups{}
	labels := map[string]map[string]int{} // by group: estimates per bracket label
	total := 0
	for _, e := range estimates {
		if !e.hasWarning(code) {
			continue
		}
		key := pairKey(sourcePrefix(e.before.Source), sourcePrefix(e.after.Source))
		g := groups.get(key)
		g.add(e.id)
		total++
		g.raise("max_km", *e.now.Arithmetic.PairKm)
		if code == checksPairImpossible {
			g.raise("max_kmh", *e.now.Arithmetic.ImpliedKmh)
		}
		if labels[key] == nil {
			labels[key] = map[string]int{}
		}
		for _, label := range e.sources() {
			labels[key][label]++
		}
	}
	for key, g := range groups {
		g.Evidence["top_source"], g.Evidence["top_source_estimates"] = nil, nil
		if label, n, ok := topSource(labels[key]); ok {
			g.Evidence["top_source"], g.Evidence["top_source_estimates"] = label, n
		}
	}
	return groups.check(code, severity, total)
}

// futurePointsCheck lists the files with points dated in the future, with the span of those points.
func futurePointsCheck(files []trackFileRow) checksCheck {
	groups := checkGroups{}
	total := 0
	for _, f := range files {
		g := groups.get(f.source)
		g.Count += f.points
		total += f.points
		g.Evidence["first"] = utcTime(&f.first)
		g.Evidence["last"] = utcTime(&f.last)
	}
	return groups.check(checksFutureTrackPoints, checksSeverityHigh, total)
}

// takenAfterUploadCheck groups the items dated after their upload by camera model.
func takenAfterUploadCheck(items []afterUploadItem) checksCheck {
	groups := checkGroups{}
	located := map[string]int{}
	for _, it := range items {
		g := groups.get(it.camera)
		g.add(it.id)
		g.raise("max_ahead_s", it.aheadS)
		if it.located {
			located[it.camera]++
		}
	}
	for camera, g := range groups {
		g.Evidence["located"] = located[camera]
	}
	return groups.check(checksTakenAfterUpload, checksSeverityHigh, len(items))
}

// staleEstimatesCheck groups the estimates the matcher would now place elsewhere (stale) or
// could not place at all (no_support, whose old location stays until it is placed by hand).
func staleEstimatesCheck(estimates []checkedEstimate) checksCheck {
	groups := checkGroups{}
	total := 0
	for _, e := range estimates {
		if e.state != placementStale && e.state != placementNoSupport {
			continue
		}
		g := groups.get(e.state)
		g.add(e.id)
		total++
		if e.state == placementStale && e.now != nil && e.now.DeltaM != nil {
			g.raise("max_delta_m", *e.now.DeltaM)
		}
	}
	return groups.check(checksStaleEstimates, checksSeverityHigh, total)
}

// pointKey identifies a track point: UNIQUE (owner_id, source, recorded_at). The instant is kept
// as seconds and nanoseconds, which every year PostgreSQL stores fits.
type pointKey struct {
	source string
	sec    int64
	nsec   int
}

func newPointKey(source string, at time.Time) pointKey {
	return pointKey{source: source, sec: at.Unix(), nsec: at.Nanosecond()}
}

// orphanPointsCheck groups the orphan points by prefix, exif or manual, with the estimates whose
// recomputed bracket rests on one of them.
func orphanPointsCheck(orphans []orphanPoint, estimates []checkedEstimate) checksCheck {
	groups := checkGroups{}
	orphan := map[pointKey]bool{}
	for _, p := range orphans {
		groups.get(sourcePrefix(p.source)).Count++
		orphan[newPointKey(p.source, p.at)] = true
	}
	using := map[string]int{}
	for _, e := range estimates {
		var prefixes []string
		for _, p := range []*geo.Point{e.before, e.after} {
			if p != nil && orphan[newPointKey(p.Source, p.At)] && !slices.Contains(prefixes, sourcePrefix(p.Source)) {
				prefixes = append(prefixes, sourcePrefix(p.Source))
			}
		}
		for _, prefix := range prefixes {
			groups.get(prefix).list(e.id)
			using[prefix]++
		}
	}
	for prefix, g := range groups {
		g.Evidence["estimates"] = using[prefix]
	}
	return groups.check(checksOrphanPoints, checksSeverityLow, len(orphans))
}

// timeMayBeEstimatedCheck finds the estimates that rest on a kml, geojson or kmz point although a
// recompute without those formats still finds a bracket, and how far each would move. Only these
// are counted: an estimate with no other point would only lose its position. An estimate can be in
// two groups, but the total counts it once.
func timeMayBeEstimatedCheck(estimates []checkedEstimate) checksCheck {
	groups := checkGroups{}
	using := map[string]int{}
	over100, over1k := map[string]int{}, map[string]int{}
	counted := map[string]bool{}
	for _, e := range estimates {
		for _, format := range timeMayBeEstimatedFormats {
			if !e.uses(format) {
				continue
			}
			using[format]++
			alt := e.withoutKMLGeoJSON
			if format == gpstrack.FormatKMZ {
				alt = e.withoutKMZ
			}
			if alt == nil || !alt.OK {
				continue
			}
			g := groups.get(format)
			g.add(e.id)
			counted[e.id] = true
			// A recompute exists, so the reference does too.
			move := *distanceFrom(e.reference(), alt.Lat, alt.Lng)
			g.raise("max_move_m", move)
			if move > 100 {
				over100[format]++
			}
			if move > 1000 {
				over1k[format]++
			}
		}
	}
	for format, g := range groups {
		g.Evidence["using"] = using[format]
		g.Evidence["over_100m"] = over100[format]
		g.Evidence["over_1km"] = over1k[format]
	}
	return groups.check(checksTimeMayBeEstimatedUsed, checksSeverityLow, len(counted))
}
