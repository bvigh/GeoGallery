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
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// provenanceContextMaxPoints is the log-point budget of one item's context track. A context can
// hold over ten thousand points and is loaded on every open and every arrow step.
const provenanceContextMaxPoints = 2000

// originMaxItems caps the items listed behind one exif or manual track point.
const originMaxItems = 6

// Placement states: why an item is where it is, or why it is nowhere.
const (
	placementCurrent     = "current"
	placementStale       = "stale"
	placementNoSupport   = "no_support"
	placementPending     = "pending"
	placementNoBracket   = "no_bracket"
	placementNoTime      = "no_time"
	placementOwnPosition = "own_position"
	placementManual      = "manual"
)

// The tolerances at which a recompute reproduced every stored estimate of one real library.
const (
	placementMaxDeltaM          = 1.0
	placementMaxConfidenceDelta = 0.001
)

const (
	bracketKindLog    = "log"
	bracketKindExif   = "exif"
	bracketKindManual = "manual"

	originLinked = "linked"
	originOrphan = "orphan"
	originBurst  = "burst"
)

// Warning thresholds. The floors of 0.1 km and 120 s are judgements: the first removes jitter
// between near-simultaneous points, the second keeps a 1 Hz log during a short stop from qualifying.
const (
	warnImpossibleKmh = 150.0
	warnImpossibleKm  = 0.1
	warnFarKm         = 1.0
	warnParkedMinGapS = 120.0
	warnParkedMaxKmh  = 2.0
)

const (
	warningPairImpossible = "pair_impossible"
	warningPairFar        = "pair_far"
	warningLoggerParked   = "logger_parked"
	warningConfidenceZero = "confidence_zero"
	warningPointOrphan    = "point_orphan"

	warningLevelWarn = "warn"
	warningLevelInfo = "info"
)

// ProvenanceHandler explains one own item's capture time and position. It reads the owner's
// location history, so it is owner-only and session-only: every query filters on a literal owner_id
// and none may ever use readableMediaCond. A shared item is the caller's to look at, but the tracks
// that placed it are its owner's.
type ProvenanceHandler struct {
	db          matcherDB
	storage     rereadStorage
	matchWindow time.Duration
	cameraZone  ingest.CameraZone
	probeURL    func(ctx context.Context, url string) (ingest.VideoProbeResult, error)
	probeSlots  chan struct{} // the semaphore of concurrent ffprobe runs
	photoSlots  chan struct{} // the semaphore of concurrent photo decodes
	fileTimeout time.Duration // zero means fileRereadTimeout
}

func NewProvenanceHandler(pool *pgxpool.Pool, s *storage.Client, matchWindow time.Duration, cameraZone ingest.CameraZone) *ProvenanceHandler {
	return &ProvenanceHandler{
		db: pool, storage: s, matchWindow: matchWindow, cameraZone: cameraZone,
		probeURL: ingest.ProbeURL, probeSlots: make(chan struct{}, maxConcurrentProbes),
		photoSlots: make(chan struct{}, maxConcurrentPhotoReads),
	}
}

// ProvenanceResponse is GET /api/media/{id}/provenance. Instants are always UTC on the wire, because
// pgx scans a timestamptz into the process's local zone.
type ProvenanceResponse struct {
	Item      provenanceItem      `json:"item"`
	Time      provenanceTime      `json:"time"`
	Placement provenancePlacement `json:"placement"`
	Context   TrackResponse       `json:"context"`
}

type provenanceItem struct {
	ID            string     `json:"id"`
	Filename      string     `json:"filename"`
	MediaType     string     `json:"media_type"`
	TakenAt       *time.Time `json:"taken_at"`
	CreatedAt     time.Time  `json:"created_at"`
	Lat           *float64   `json:"lat"`
	Lng           *float64   `json:"lng"`
	GPSSource     *string    `json:"gps_source"`
	GPSConfidence *float64   `json:"gps_confidence"`
	CameraModel   *string    `json:"camera_model"`
	WidthPx       *int       `json:"width_px"`
	HeightPx      *int       `json:"height_px"`
	DurationSec   *float64   `json:"duration_sec"`
}

type provenanceTime struct {
	Recorded         string           `json:"recorded"` // "ingest", "backfill" or "not_recorded"
	Evidence         json.RawMessage  `json:"evidence"` // null when not_recorded
	ServerCameraZone serverCameraZone `json:"server_camera_zone"`
}

// serverCameraZone is the zone this server applies today.
type serverCameraZone struct {
	Name         string `json:"name"`
	Set          bool   `json:"set"`
	UTCFallback  bool   `json:"utc_fallback"`
	OffsetNowMin int    `json:"offset_now_min"`
}

type provenancePlacement struct {
	State          string                    `json:"state"`
	WindowS        int64                     `json:"window_s"`
	Now            *provenanceEstimate       `json:"now"`
	Independent    *provenanceIndependent    `json:"independent"`
	NearestOutside *provenanceNearestOutside `json:"nearest_outside"`
	Others         []provenanceOtherFormat   `json:"others"`
	Warnings       []provenanceWarning       `json:"warnings"`
}

// provenanceEstimate is what the matcher would compute for the item now.
type provenanceEstimate struct {
	Lat        float64              `json:"lat"`
	Lng        float64              `json:"lng"`
	Confidence float64              `json:"confidence"`
	DeltaM     *float64             `json:"delta_m"` // from the stored position; nil when there is none
	Before     *provenanceBracket   `json:"before"`
	After      *provenanceBracket   `json:"after"`
	Arithmetic provenanceArithmetic `json:"arithmetic"`
}

type provenanceArithmetic struct {
	Shape          string   `json:"shape"`
	PairGapS       *float64 `json:"pair_gap_s"` // nil for one_sided
	PairKm         *float64 `json:"pair_km"`    // nil for one_sided
	T              *float64 `json:"t"`          // two_sided only: 0 at before, 1 at after
	TimeFactor     float64  `json:"time_factor"`
	DistanceFactor *float64 `json:"distance_factor"` // nil for one_sided
	Confidence     float64  `json:"confidence"`
	ImpliedKmh     *float64 `json:"implied_kmh"` // two_sided only
}

// provenanceIndependent is the estimate from log points alone, without photos and pins.
type provenanceIndependent struct {
	Lat        float64            `json:"lat"`
	Lng        float64            `json:"lng"`
	Confidence float64            `json:"confidence"`
	MovedM     *float64           `json:"moved_m"`
	Before     *provenanceBracket `json:"before"`
	After      *provenanceBracket `json:"after"`
}

type provenanceBracket struct {
	Source    string            `json:"source"`
	Format    string            `json:"format"`
	Kind      string            `json:"kind"` // "log", "exif" or "manual"
	T         time.Time         `json:"t"`
	Lat       float64           `json:"lat"`
	Lng       float64           `json:"lng"`
	AccuracyM *float64          `json:"accuracy_m"`
	AltitudeM *float64          `json:"altitude_m"`
	GapS      float64           `json:"gap_s"` // absolute distance in time from the item
	Origin    *provenanceOrigin `json:"origin"`
}

// provenanceOrigin is the item that wrote a derived track point. There is no foreign key, so
// "orphan" is an inference: no item with a position exists at the point's time.
type provenanceOrigin struct {
	State string                 `json:"state"`
	Media []provenanceOriginItem `json:"media"`
}

type provenanceOriginItem struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
}

type provenanceNearestOutside struct {
	Before *provenanceOutsidePoint `json:"before"`
	After  *provenanceOutsidePoint `json:"after"`
}

type provenanceOutsidePoint struct {
	Source string    `json:"source"`
	Format string    `json:"format"`
	T      time.Time `json:"t"`
	Lat    float64   `json:"lat"`
	Lng    float64   `json:"lng"`
	GapS   float64   `json:"gap_s"`
}

type provenanceOtherFormat struct {
	Format    string    `json:"format"` // the raw prefix, so an unknown one is shown as written
	Source    string    `json:"source"`
	T         time.Time `json:"t"`
	Lat       float64   `json:"lat"`
	Lng       float64   `json:"lng"`
	GapS      float64   `json:"gap_s"`
	AccuracyM *float64  `json:"accuracy_m"`
	DistanceM *float64  `json:"distance_m"` // from the position on the map
	Used      bool      `json:"used"`       // the row is this estimate's before or after point
}

type provenanceWarning struct {
	Code  string `json:"code"`
	Level string `json:"level"`
}

// Get serves GET /api/media/{id}/provenance. A foreign or shared item is 404.
func (h *ProvenanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	item, evidence, err := h.loadItem(r.Context(), id, ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("provenance: load item failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	placement, err := h.placement(r.Context(), ownerID, item)
	if err != nil {
		slog.Error("provenance: compute placement failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	track, err := h.contextTrack(r.Context(), ownerID, item)
	if err != nil {
		slog.Error("provenance: load context track failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(ProvenanceResponse{Item: item, Time: h.timeFacts(evidence), Placement: placement, Context: track})
	if err != nil {
		slog.Error("provenance: encode response failed", "err", err)
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		slog.Warn("provenance: write response failed", "err", err)
	}
}

// loadItem reads one own item and its capture evidence. The literal owner_id is what makes a
// foreign or shared item pgx.ErrNoRows.
func (h *ProvenanceHandler) loadItem(ctx context.Context, id, ownerID string) (provenanceItem, []byte, error) {
	var item provenanceItem
	var evidence []byte
	err := h.db.QueryRow(ctx, `
		SELECT m.id::text, m.filename, m.media_type, m.taken_at, m.created_at,
		       ST_Y(m.location::geometry), ST_X(m.location::geometry),
		       m.gps_source, m.gps_confidence, m.camera_model, m.width_px, m.height_px, m.duration_sec,
		       m.capture_evidence
		FROM media m
		WHERE m.id = $1 AND m.owner_id = $2
	`, id, ownerID).Scan(
		&item.ID, &item.Filename, &item.MediaType, &item.TakenAt, &item.CreatedAt,
		&item.Lat, &item.Lng,
		&item.GPSSource, &item.GPSConfidence, &item.CameraModel, &item.WidthPx, &item.HeightPx, &item.DurationSec,
		&evidence,
	)
	if err != nil {
		return provenanceItem{}, nil, err
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.TakenAt = utcTime(item.TakenAt)
	if item.TakenAt != nil && !h.matchWindowFits(*item.TakenAt) {
		item.TakenAt = nil
	}
	return item, evidence, nil
}

// matchWindowFits reports whether both ends of the match window around t can be sent as JSON. Every
// point in the window then can too, so the placement and the context need no check of their own.
func (h *ProvenanceHandler) matchWindowFits(t time.Time) bool {
	from, to := t.Add(-h.matchWindow), t.Add(h.matchWindow)
	return utcTime(&from) != nil && utcTime(&to) != nil
}

// timeFacts reports how taken_at was recorded and the zone this server applies today.
func (h *ProvenanceHandler) timeFacts(evidence []byte) provenanceTime {
	loc := h.cameraZone.Loc
	if loc == nil {
		loc = time.UTC // a zero CameraZone reads wall clocks as UTC
	}
	_, offsetSeconds := time.Now().In(loc).Zone()
	facts := provenanceTime{
		Recorded: "not_recorded",
		ServerCameraZone: serverCameraZone{
			Name:         h.cameraZone.Name(),
			Set:          h.cameraZone.Set,
			UTCFallback:  h.cameraZone.UTCFallback,
			OffsetNowMin: offsetSeconds / 60,
		},
	}
	var header struct {
		By string `json:"by"`
	}
	if len(evidence) == 0 || json.Unmarshal(evidence, &header) != nil {
		return facts
	}
	if header.By == "ingest" || header.By == "backfill" {
		facts.Recorded = header.By
		facts.Evidence = evidence
	}
	return facts
}

// placementFacts are the inputs of placementState. DeltaM and NowConfidence describe the recompute
// with no exclusions, and matter only when HasBracket is true.
type placementFacts struct {
	GPSSource        *string
	HasTime          bool
	HasLocation      bool
	HasBracket       bool
	DeltaM           *float64
	StoredConfidence *float64
	NowConfidence    float64
}

// placementState is the first matching row of the placement table in docs/gps-debugging.md. A
// gps_source of "none" matches neither of the first two rows, so it reads as null.
func placementState(f placementFacts) string {
	source := ""
	if f.GPSSource != nil {
		source = *f.GPSSource
	}
	switch {
	case source == "manual":
		return placementManual
	case source == "exif":
		return placementOwnPosition
	case !f.HasTime:
		return placementNoTime
	case !f.HasLocation && f.HasBracket:
		return placementPending
	case !f.HasLocation:
		return placementNoBracket
	case !f.HasBracket:
		return placementNoSupport
	case f.DeltaM != nil && *f.DeltaM <= placementMaxDeltaM && f.StoredConfidence != nil &&
		math.Abs(*f.StoredConfidence-f.NowConfidence) <= placementMaxConfidenceDelta:
		return placementCurrent
	default:
		return placementStale
	}
}

// placementWarnings is computed from the recompute alone, so a nil estimate has no warnings.
func placementWarnings(now *provenanceEstimate) []provenanceWarning {
	warnings := []provenanceWarning{}
	if now == nil {
		return warnings
	}
	add := func(code, level string) {
		warnings = append(warnings, provenanceWarning{Code: code, Level: level})
	}

	a := now.Arithmetic
	if a.Shape == string(geo.ShapeTwoSided) && now.Before != nil && now.After != nil {
		// Raw prefixes, so two different unknown formats still count as two.
		formatsDiffer := sourcePrefix(now.Before.Source) != sourcePrefix(now.After.Source)
		impossible := formatsDiffer && *a.ImpliedKmh > warnImpossibleKmh && *a.PairKm > warnImpossibleKm
		if impossible {
			add(warningPairImpossible, warningLevelWarn)
		}
		if formatsDiffer && !impossible && *a.PairKm > warnFarKm {
			add(warningPairFar, warningLevelWarn)
		}
		if now.Before.Kind == bracketKindLog && now.After.Kind == bracketKindLog &&
			now.Before.Source == now.After.Source &&
			*a.PairGapS >= warnParkedMinGapS && *a.ImpliedKmh < warnParkedMaxKmh {
			add(warningLoggerParked, warningLevelInfo)
		}
	}
	if now.Confidence == 0 {
		add(warningConfidenceZero, warningLevelInfo)
	}
	if isOrphanPoint(now.Before) || isOrphanPoint(now.After) {
		add(warningPointOrphan, warningLevelWarn)
	}
	return warnings
}

func isOrphanPoint(b *provenanceBracket) bool {
	return b != nil && b.Origin != nil && b.Origin.State == originOrphan
}

// placement recomputes the item's position without writing anything and compares it with the row.
// bracketingPoints and geo.Explain are the matcher's own query and arithmetic, so the two cannot
// drift apart. Those brackets, like every query here, are ownerID's tracks only.
func (h *ProvenanceHandler) placement(ctx context.Context, ownerID string, item provenanceItem) (provenancePlacement, error) {
	p := provenancePlacement{
		WindowS:  int64(h.matchWindow / time.Second),
		Others:   []provenanceOtherFormat{},
		Warnings: []provenanceWarning{},
	}
	facts := placementFacts{GPSSource: item.GPSSource, HasLocation: item.Lat != nil && item.Lng != nil, StoredConfidence: item.GPSConfidence}
	if item.TakenAt == nil {
		p.State = placementState(facts)
		return p, nil
	}
	takenAt := *item.TakenAt
	facts.HasTime = true

	before, after, err := bracketingPoints(ctx, h.db, ownerID, takenAt, h.matchWindow, bracketOptions{})
	if err != nil {
		return provenancePlacement{}, fmt.Errorf("bracket: %w", err)
	}
	explanation := geo.Explain(before, after, takenAt, h.matchWindow)

	stored := storedPosition(item)
	var deltaM *float64
	if explanation.OK {
		deltaM = distanceFrom(stored, explanation.Lat, explanation.Lng)
	}
	facts.HasBracket = explanation.OK
	facts.DeltaM = deltaM
	facts.NowConfidence = explanation.Confidence
	p.State = placementState(facts)

	if p.State == placementCurrent || p.State == placementStale || p.State == placementPending {
		beforeBracket, err := h.bracket(ctx, ownerID, before, takenAt)
		if err != nil {
			return provenancePlacement{}, err
		}
		afterBracket, err := h.bracket(ctx, ownerID, after, takenAt)
		if err != nil {
			return provenancePlacement{}, err
		}
		p.Now = &provenanceEstimate{
			Lat: explanation.Lat, Lng: explanation.Lng, Confidence: explanation.Confidence,
			DeltaM: deltaM, Before: beforeBracket, After: afterBracket, Arithmetic: newArithmetic(explanation),
		}
	}

	reference := stored
	if reference == nil && p.Now != nil {
		reference = &position{lat: p.Now.Lat, lng: p.Now.Lng}
	}

	if p.Independent, err = h.independent(ctx, ownerID, takenAt, reference); err != nil {
		return provenancePlacement{}, err
	}
	if p.State == placementNoBracket || p.State == placementNoSupport {
		if p.NearestOutside, err = h.nearestOutside(ctx, ownerID, takenAt); err != nil {
			return provenancePlacement{}, err
		}
	}
	if p.Others, err = h.otherFormats(ctx, ownerID, takenAt, reference, p.Now); err != nil {
		return provenancePlacement{}, err
	}
	p.Warnings = placementWarnings(p.Now)
	return p, nil
}

func newArithmetic(e geo.Explanation) provenanceArithmetic {
	a := provenanceArithmetic{
		Shape:          string(e.Shape),
		T:              e.T,
		TimeFactor:     e.TimeFactor,
		DistanceFactor: e.DistanceFactor,
		Confidence:     e.Confidence,
		ImpliedKmh:     e.ImpliedKmh,
	}
	if e.Shape != geo.ShapeOneSided {
		pairGapS, pairKm := e.PairGapS, e.PairKm
		a.PairGapS, a.PairKm = &pairGapS, &pairKm
	}
	return a
}

type position struct {
	lat, lng float64
}

func storedPosition(item provenanceItem) *position {
	if item.Lat == nil || item.Lng == nil {
		return nil
	}
	return &position{lat: *item.Lat, lng: *item.Lng}
}

// distanceFrom is the distance in metres from ref, or nil when there is no ref.
func distanceFrom(ref *position, lat, lng float64) *float64 {
	if ref == nil {
		return nil
	}
	metres := geo.HaversineKm(ref.lat, ref.lng, lat, lng) * 1000
	return &metres
}

func gapSeconds(a, b time.Time) float64 {
	return a.Sub(b).Abs().Seconds()
}

// newBracket describes a point. Derived points also need an origin (see origin).
func newBracket(p *geo.Point, takenAt time.Time) *provenanceBracket {
	kind := bracketKindLog
	switch sourcePrefix(p.Source) {
	case formatExif:
		kind = bracketKindExif
	case formatManual:
		kind = bracketKindManual
	}
	return &provenanceBracket{
		Source: p.Source, Format: trackFormat(p.Source), Kind: kind, T: p.At.UTC(),
		Lat: p.Lat, Lng: p.Lng, AccuracyM: p.AccuracyM, AltitudeM: p.AltitudeM, GapS: gapSeconds(p.At, takenAt),
	}
}

// bracket describes one bracket point, nil when that side has none, with its origin when it is a
// derived point.
func (h *ProvenanceHandler) bracket(ctx context.Context, ownerID string, p *geo.Point, takenAt time.Time) (*provenanceBracket, error) {
	if p == nil {
		return nil, nil
	}
	b := newBracket(p, takenAt)
	if b.Kind != bracketKindLog {
		origin, err := h.origin(ctx, ownerID, p.At)
		if err != nil {
			return nil, err
		}
		b.Origin = &origin
	}
	return b, nil
}

// origin finds the own items a derived track point can have been written from.
func (h *ProvenanceHandler) origin(ctx context.Context, ownerID string, recordedAt time.Time) (provenanceOrigin, error) {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, filename
		FROM media
		WHERE owner_id = $1 AND taken_at = $2 AND gps_source IN ('exif', 'manual')
		ORDER BY filename, id
		LIMIT $3
	`, ownerID, recordedAt, originMaxItems)
	if err != nil {
		return provenanceOrigin{}, fmt.Errorf("origin: %w", err)
	}
	defer rows.Close()

	media := []provenanceOriginItem{}
	for rows.Next() {
		var item provenanceOriginItem
		if err := rows.Scan(&item.ID, &item.Filename); err != nil {
			return provenanceOrigin{}, fmt.Errorf("scan origin: %w", err)
		}
		media = append(media, item)
	}
	if err := rows.Err(); err != nil {
		return provenanceOrigin{}, fmt.Errorf("read origin: %w", err)
	}

	state := originBurst
	switch len(media) {
	case 0:
		state = originOrphan
	case 1:
		state = originLinked
	}
	return provenanceOrigin{State: state, Media: media}, nil
}

// independent re-runs the bracket without photos and pins, and is nil when no log is that close.
func (h *ProvenanceHandler) independent(ctx context.Context, ownerID string, takenAt time.Time, reference *position) (*provenanceIndependent, error) {
	before, after, err := bracketingPoints(ctx, h.db, ownerID, takenAt, h.matchWindow,
		bracketOptions{excludeFormats: []string{formatExif, formatManual}})
	if err != nil {
		return nil, fmt.Errorf("independent bracket: %w", err)
	}
	e := geo.Explain(before, after, takenAt, h.matchWindow)
	if !e.OK {
		return nil, nil
	}
	independent := &provenanceIndependent{
		Lat: e.Lat, Lng: e.Lng, Confidence: e.Confidence, MovedM: distanceFrom(reference, e.Lat, e.Lng),
	}
	if before != nil {
		independent.Before = newBracket(before, takenAt)
	}
	if after != nil {
		independent.After = newBracket(after, takenAt)
	}
	return independent, nil
}

// The nearest point earlier than $2 and the nearest later than $2, with the matcher's tie-break.
const (
	nearestEarlierSQL = `
		SELECT source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry)
		FROM gps_tracks
		WHERE owner_id = $1 AND recorded_at < $2
		ORDER BY recorded_at DESC, source ASC
		LIMIT 1
	`
	nearestLaterSQL = `
		SELECT source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry)
		FROM gps_tracks
		WHERE owner_id = $1 AND recorded_at > $2
		ORDER BY recorded_at ASC, source ASC
		LIMIT 1
	`
)

func (h *ProvenanceHandler) nearestOutside(ctx context.Context, ownerID string, takenAt time.Time) (*provenanceNearestOutside, error) {
	earlier, err := h.outsidePoint(ctx, nearestEarlierSQL, ownerID, takenAt.Add(-h.matchWindow), takenAt)
	if err != nil {
		return nil, err
	}
	later, err := h.outsidePoint(ctx, nearestLaterSQL, ownerID, takenAt.Add(h.matchWindow), takenAt)
	if err != nil {
		return nil, err
	}
	return &provenanceNearestOutside{Before: earlier, After: later}, nil
}

func (h *ProvenanceHandler) outsidePoint(ctx context.Context, sql, ownerID string, edge, takenAt time.Time) (*provenanceOutsidePoint, error) {
	var p provenanceOutsidePoint
	var at time.Time
	err := h.db.QueryRow(ctx, sql, ownerID, edge).Scan(&p.Source, &at, &p.Lat, &p.Lng)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("nearest outside: %w", err)
	}
	// These rows have no time limit, unlike the window's. A row JSON cannot carry means none on this
	// side can be sent: every row further out is beyond the limit too.
	t := utcTime(&at)
	if t == nil {
		return nil, nil
	}
	p.Format = trackFormat(p.Source)
	p.T = *t
	p.GapS = gapSeconds(at, takenAt)
	return &p, nil
}

// One row per raw format prefix, the nearest to the item's time, from one scan of the window. The
// format is computed after the time-range scan (see the note in track.go).
const provenanceOthersSQL = `
	SELECT DISTINCT ON (split_part(source, ':', 1))
	       source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry), accuracy_m
	FROM gps_tracks
	WHERE owner_id = $1 AND recorded_at BETWEEN $2 AND $3
	ORDER BY split_part(source, ':', 1), abs(extract(epoch FROM recorded_at - $4::timestamptz)), recorded_at, source
`

func (h *ProvenanceHandler) otherFormats(ctx context.Context, ownerID string, takenAt time.Time, reference *position, now *provenanceEstimate) ([]provenanceOtherFormat, error) {
	rows, err := h.db.Query(ctx, provenanceOthersSQL, ownerID, takenAt.Add(-h.matchWindow), takenAt.Add(h.matchWindow), takenAt)
	if err != nil {
		return nil, fmt.Errorf("other formats: %w", err)
	}
	defer rows.Close()

	others := []provenanceOtherFormat{}
	for rows.Next() {
		var o provenanceOtherFormat
		var at time.Time
		if err := rows.Scan(&o.Source, &at, &o.Lat, &o.Lng, &o.AccuracyM); err != nil {
			return nil, fmt.Errorf("scan other format: %w", err)
		}
		o.Format = sourcePrefix(o.Source)
		o.T = at.UTC()
		o.GapS = gapSeconds(at, takenAt)
		o.DistanceM = distanceFrom(reference, o.Lat, o.Lng)
		o.Used = now != nil && (isBracketRow(now.Before, o.Source, o.T) || isBracketRow(now.After, o.Source, o.T))
		others = append(others, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read other formats: %w", err)
	}
	slices.SortFunc(others, func(a, b provenanceOtherFormat) int {
		return cmp.Or(cmp.Compare(a.GapS, b.GapS), strings.Compare(a.Format, b.Format))
	})
	return others, nil
}

func isBracketRow(b *provenanceBracket, source string, at time.Time) bool {
	return b != nil && b.Source == source && b.T.Equal(at)
}

// contextTrack is the owner's track within one match window either side of the item's time, or
// empty arrays when the item has no time. Bracket points are not guaranteed to survive its
// downsampling: the evidence view draws them from the placement.
func (h *ProvenanceHandler) contextTrack(ctx context.Context, ownerID string, item provenanceItem) (TrackResponse, error) {
	track := emptyTrackResponse()
	if item.TakenAt != nil {
		window := timeWindow{from: item.TakenAt.Add(-h.matchWindow), to: item.TakenAt.Add(h.matchWindow)}
		var err error
		if track, err = loadTrack(ctx, h.db, ownerID, []timeWindow{window}, provenanceContextMaxPoints); err != nil {
			return TrackResponse{}, err
		}
	}
	track.MatchWindowS = int64(h.matchWindow / time.Second)
	return track, nil
}
