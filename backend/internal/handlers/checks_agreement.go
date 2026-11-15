package handlers

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/geo"
	"github.com/jackc/pgx/v5"
)

const (
	// agreementTimeout bounds GET /api/checks/agreement. A chunk the deadline cuts short is dropped.
	agreementTimeout = 60 * time.Second
	// agreementWorkBudget: no chunk starts after this much of the request, so the last one started
	// has the rest of agreementTimeout to finish.
	agreementWorkBudget = 45 * time.Second
	// A chunk's statements cover at most agreementChunkFiles track files and agreementChunkPoints
	// points, unless one file alone has more, so that the budget can stop the route between chunks
	// and no chunk runs long: the gap counts read every point. On one real library the 150-file
	// chunks took 0.2–2.6 s, the heaviest holding 1.7 million points, and under 250,000 points none
	// took more than 1.4 s.
	agreementChunkFiles  = 150
	agreementChunkPoints = 250_000
	// agreementMaxRuns bounds the runs one process serves at once: each keeps a pool connection busy
	// for up to a minute.
	agreementMaxRuns = 2
	// agreementMaxFrom is the largest index ?from= takes.
	agreementMaxFrom = 1_000_000_000
	// A file is sampled at agreementSamples instants spread over its span, the samples at least
	// agreementSampleSpacing apart, and each sample is compared with the nearest point of another
	// format within agreementWindow on either side.
	agreementSamples       = 20
	agreementSampleSpacing = 30 * time.Second
	agreementWindow        = 5 * time.Minute
	// agreementMinMatches is the fewest matched samples a median is given for.
	agreementMinMatches = 5
	// modalGapMinGaps is the fewest gaps a modal gap share is given for.
	modalGapMinGaps = 10
)

// AgreementResponse is GET /api/checks/agreement: each log file against the other formats at the
// same moments, and how regular its spacing is. Files the work budget did not reach are absent,
// and NextFrom says where the next request resumes.
type AgreementResponse struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Files       []agreementFile `json:"files"`     // log files only, in label order, from index from
	Truncated   bool            `json:"truncated"` // exactly when NextFrom is set
	TotalFiles  int             `json:"total_files"`
	// NextFrom is the index, in label order, of the first log file this request did not compare;
	// nil once it compared the last.
	NextFrom *int `json:"next_from"`
}

type agreementFile struct {
	Source  string `json:"source"`
	Samples int    `json:"samples"`
	Matched int    `json:"matched"`
	// MedianM is the median distance of the matched samples in metres, nil with fewer than
	// agreementMinMatches of them.
	MedianM *float64 `json:"median_m"`
	// ModalGapShare is the share of the file's consecutive gaps that have its most common length,
	// nil with fewer than modalGapMinGaps gaps. Times spread evenly by a program, rather than
	// recorded, come out near 1.
	ModalGapShare *float64 `json:"modal_gap_share"`
}

// Agreement serves GET /api/checks/agreement?from=<n>. It works through the owner's log files in
// label order from index n, a chunk at a time, and starts no chunk once the work budget is spent; a
// chunk the deadline cuts short is dropped. A partial answer is still an answer, so a timeout is
// never a 504: next_from says where to resume. A malformed from is a 400, and a request that finds
// every run slot taken a 503, both before any database work.
func (h *ChecksHandler) Agreement(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	from, ok := agreementFrom(r)
	if !ok {
		writeChecksJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_from"})
		return
	}
	if !h.startAgreementRun() {
		slog.Info("checks agreement: every run slot taken", "slots", cap(h.agreementRuns))
		writeChecksJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "checks_busy"})
		return
	}
	defer h.endAgreementRun()

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), agreementTimeout)
	defer cancel()

	resp, err := h.agreement(ctx, ownerID, from, start)
	if err != nil {
		slog.Error("checks agreement: compute failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	writeChecksJSON(w, http.StatusOK, resp)
}

// agreementFrom reads ?from=, a decimal index into the owner's log files in label order: a number,
// never a label, so that no file name reaches a URL. Absent is 0. Repeated, empty, signed, not
// digits only or above agreementMaxFrom is refused, and so is a query string that does not parse.
func agreementFrom(r *http.Request) (int, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, false
	}
	values, ok := query["from"]
	if !ok {
		return 0, true
	}
	if len(values) != 1 || values[0] == "" || strings.Trim(values[0], "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n > agreementMaxFrom {
		return 0, false
	}
	return n, true
}

// startAgreementRun takes a run slot if one is free, without waiting: a run holds its slot for up
// to a minute, and queueing behind it would only use up the next request's deadline.
func (h *ChecksHandler) startAgreementRun() bool {
	if h.agreementRuns == nil {
		return true
	}
	select {
	case h.agreementRuns <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *ChecksHandler) endAgreementRun() {
	if h.agreementRuns != nil {
		<-h.agreementRuns
	}
}

func (h *ChecksHandler) agreement(ctx context.Context, ownerID string, from int, start time.Time) (AgreementResponse, error) {
	resp := AgreementResponse{GeneratedAt: start.UTC(), Files: []agreementFile{}}
	files, err := loadTrackFileRows(ctx, h.db, trackFilesSQL, ownerID)
	if err != nil {
		if ctx.Err() != nil {
			// Nothing was counted, so the next request starts where this one would have.
			resp.stopAt(from)
			logAgreementStop(ctx, from, from, 0, start)
			return resp, nil
		}
		return AgreementResponse{}, fmt.Errorf("track files: %w", err)
	}
	files = slices.DeleteFunc(files, func(f trackFileRow) bool { return isDerivedPrefix(sourcePrefix(f.source)) })
	resp.TotalFiles = len(files)

	budget := cmp.Or(h.agreementBudget, agreementWorkBudget)
	maxFiles := cmp.Or(h.agreementChunkSize, agreementChunkFiles)
	maxPoints := cmp.Or(h.agreementChunkPointCap, agreementChunkPoints)
	next := from
	for next < len(files) {
		// The first chunk always starts, so every request makes progress unless the deadline cuts
		// that chunk short.
		if next > from && time.Since(start) >= budget {
			break
		}
		chunk := files[next : next+agreementChunkLen(files[next:], maxFiles, maxPoints)]
		done, err := h.agreementChunk(ctx, ownerID, chunk)
		if err != nil {
			if ctx.Err() == nil {
				return AgreementResponse{}, err
			}
			break
		}
		resp.Files = append(resp.Files, done...)
		next += len(chunk)
	}
	if next < len(files) {
		resp.stopAt(next)
		logAgreementStop(ctx, from, next, len(files), start)
	}
	return resp, nil
}

// stopAt marks the answer as ending before the log file at index next, where the next request
// resumes.
func (r *AgreementResponse) stopAt(next int) {
	r.Truncated = true
	r.NextFrom = &next
}

// agreementChunkLen is how many of files, from the first, the next chunk compares: at least one, at
// most maxFiles, and no more than maxPoints points unless the first file alone has more.
func agreementChunkLen(files []trackFileRow, maxFiles, maxPoints int) int {
	n, points := 0, 0
	for n < len(files) && n < maxFiles {
		if n > 0 && points+files[n].points > maxPoints {
			break
		}
		points += files[n].points
		n++
	}
	return n
}

// logAgreementStop says that the route answered before the last file. A cancelled request is
// routine, because closing the view cancels it; the budget or the deadline is not.
func logAgreementStop(ctx context.Context, from, next, files int, start time.Time) {
	cause, level := "work budget", slog.LevelWarn
	if err := ctx.Err(); err != nil {
		cause = err.Error()
		if errors.Is(err, context.Canceled) {
			level = slog.LevelDebug
		}
	}
	slog.Log(ctx, level, "checks agreement: stopped before the last file",
		"from", from, "next_from", next, "files", files, "cause", cause, "elapsed", time.Since(start))
}

// agreementChunk compares each file of one chunk with the other formats and measures its spacing:
// one statement for the samples, batches of bracket lookups for their nearest points, and one
// statement for the gaps, whatever the chunk's size.
func (h *ChecksHandler) agreementChunk(ctx context.Context, ownerID string, files []trackFileRow) ([]agreementFile, error) {
	samples, err := loadAgreementSamples(ctx, h.db, ownerID, files)
	if err != nil {
		return nil, err
	}
	distances, err := matchAgreementSamples(ctx, h.db, ownerID, files, samples)
	if err != nil {
		return nil, err
	}
	gaps, err := loadGapCounts(ctx, h.db, ownerID, files)
	if err != nil {
		return nil, err
	}
	done := make([]agreementFile, len(files))
	for i, f := range files {
		done[i] = newAgreementFile(f.source, len(samples[f.source]), distances[f.source], gaps[f.source])
	}
	return done, nil
}

// samplePoint is one point of a file that the file is sampled at.
type samplePoint struct {
	at       time.Time
	lat, lng float64
}

// fileSample is a sample with the label of its file.
type fileSample struct {
	source string
	point  samplePoint
}

// Each target's first point of its file at or after it, through the (owner_id, source,
// recorded_at) index.
const agreementSamplesSQL = `
	SELECT t.source, p.recorded_at, ST_Y(p.location::geometry), ST_X(p.location::geometry)
	FROM unnest($2::text[], $3::timestamptz[]) AS t(source, target)
	CROSS JOIN LATERAL (
		SELECT g.recorded_at, g.location
		FROM gps_tracks g
		WHERE g.owner_id = $1 AND g.source = t.source AND g.recorded_at >= t.target
		ORDER BY g.recorded_at
		LIMIT 1
	) p
`

// loadAgreementSamples maps every file's targets to its points and spaces them, by label.
func loadAgreementSamples(ctx context.Context, db matcherDB, ownerID string, files []trackFileRow) (map[string][]samplePoint, error) {
	var sources []string
	var targets []time.Time
	for _, f := range files {
		for _, t := range sampleTargets(f.first, f.last) {
			sources = append(sources, f.source)
			targets = append(targets, t)
		}
	}
	rows, err := db.Query(ctx, agreementSamplesSQL, ownerID, sources, targets)
	if err != nil {
		return nil, fmt.Errorf("agreement samples: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (fileSample, error) {
		var s fileSample
		err := row.Scan(&s.source, &s.point.at, &s.point.lat, &s.point.lng)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("read agreement samples: %w", err)
	}
	samples := map[string][]samplePoint{}
	for _, s := range found {
		samples[s.source] = append(samples[s.source], s.point)
	}
	for source, points := range samples {
		samples[source] = spaceSamples(points)
	}
	return samples, nil
}

// sampleTargets is where a file is sampled: agreementSamples instants spread evenly from its first
// point to its last, both included, or its first alone when it spans no time. A time.Duration ends
// at 292 years, so a corrupt file spanning more is sampled over its first 292 years and its end.
func sampleTargets(first, last time.Time) []time.Time {
	if !last.After(first) {
		return []time.Time{first}
	}
	step := last.Sub(first) / (agreementSamples - 1)
	targets := make([]time.Time, agreementSamples)
	for k := range targets {
		targets[k] = first.Add(step * time.Duration(k))
	}
	targets[len(targets)-1] = last // exactly, whatever the division dropped
	return targets
}

// spaceSamples keeps, in time order, each sample at least agreementSampleSpacing after the one kept
// before it. Several targets can map to one point; such a repeat is 0 s after it and goes too.
func spaceSamples(points []samplePoint) []samplePoint {
	slices.SortFunc(points, func(a, b samplePoint) int { return a.at.Compare(b.at) })
	var kept []samplePoint
	for _, p := range points {
		if len(kept) == 0 || p.at.Sub(kept[len(kept)-1].at) >= agreementSampleSpacing {
			kept = append(kept, p)
		}
	}
	return kept
}

// matchAgreementSamples finds each sample's nearest point in time of another format within
// agreementWindow, and returns the distances in metres by file. Another format is any other raw
// prefix but exif and manual: a second file of the same format is no independent witness, and
// derived points come from the owner's own photos and pins. The lookups are the matcher's bracket
// queries, nearest point on each side, of which the nearer counts; they measure a distance and
// never place anything.
func matchAgreementSamples(ctx context.Context, db batchDB, ownerID string, files []trackFileRow,
	samples map[string][]samplePoint) (map[string][]float64, error) {
	var lookups []bracketLookup
	var order []fileSample
	for _, f := range files {
		exclude := []string{formatExif, formatManual, sourcePrefix(f.source)}
		for _, p := range samples[f.source] {
			lookups = append(lookups, bracketLookup{at: p.at, opts: bracketOptions{excludeFormats: exclude}})
			order = append(order, fileSample{source: f.source, point: p})
		}
	}
	pairs, err := bracketingPointsMany(ctx, db, ownerID, lookups, agreementWindow)
	if err != nil {
		return nil, fmt.Errorf("agreement lookups: %w", err)
	}
	distances := map[string][]float64{}
	for i, pair := range pairs {
		s := order[i]
		if p := nearerPoint(s.point.at, pair.before, pair.after); p != nil {
			distances[s.source] = append(distances[s.source], geo.HaversineKm(s.point.lat, s.point.lng, p.Lat, p.Lng)*1000)
		}
	}
	return distances, nil
}

// nearerPoint is whichever of before and after is nearer in time to at, the earlier on a tie, or
// nil when there is neither. Each side already broke its own ties by source, and a point at at
// itself is the same row on both sides.
func nearerPoint(at time.Time, before, after *geo.Point) *geo.Point {
	switch {
	case before == nil:
		return after
	case after == nil:
		return before
	case after.At.Sub(at) < at.Sub(before.At):
		return after
	default:
		return before
	}
}

// gapCounts are a file's gaps between consecutive points: how many there are, and how many have
// its most common length.
type gapCounts struct {
	gaps, modal int
}

// share is the modal gap share, nil with fewer than modalGapMinGaps gaps.
func (g gapCounts) share() *float64 {
	if g.gaps < modalGapMinGaps {
		return nil
	}
	s := float64(g.modal) / float64(g.gaps)
	return &s
}

// Each file's gaps between consecutive points, rounded to whole milliseconds, counted by length.
// One file at a time, so its points come in time order straight from the (owner_id, source,
// recorded_at) index. Over source = ANY($2) instead, PostgreSQL 16 sorted a chunk's rows on disk
// twice and took more than twice as long on one real library. date_part is float8 arithmetic, a
// third faster there than extract's numeric.
const gapCountsSQL = `
	SELECT f.source, sum(lengths.n)::bigint, max(lengths.n)
	FROM unnest($2::text[]) AS f(source)
	CROSS JOIN LATERAL (
		SELECT gap_ms, count(*) AS n
		FROM (
			SELECT round(date_part('epoch', recorded_at - lag(recorded_at) OVER (ORDER BY recorded_at)) * 1000) AS gap_ms
			FROM gps_tracks
			WHERE owner_id = $1 AND source = f.source
		) gaps
		WHERE gap_ms IS NOT NULL
		GROUP BY gap_ms
	) lengths
	GROUP BY f.source
`

func loadGapCounts(ctx context.Context, db matcherDB, ownerID string, files []trackFileRow) (map[string]gapCounts, error) {
	labels := make([]string, len(files))
	for i, f := range files {
		labels[i] = f.source
	}
	rows, err := db.Query(ctx, gapCountsSQL, ownerID, labels)
	if err != nil {
		return nil, fmt.Errorf("gap counts: %w", err)
	}
	defer rows.Close()
	counts := map[string]gapCounts{}
	for rows.Next() {
		var source string
		var c gapCounts
		if err := rows.Scan(&source, &c.gaps, &c.modal); err != nil {
			return nil, fmt.Errorf("scan gap counts: %w", err)
		}
		counts[source] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read gap counts: %w", err)
	}
	return counts, nil
}

// newAgreementFile is one file's line. distances is reordered.
func newAgreementFile(source string, samples int, distances []float64, gaps gapCounts) agreementFile {
	f := agreementFile{Source: source, Samples: samples, Matched: len(distances), ModalGapShare: gaps.share()}
	if len(distances) >= agreementMinMatches {
		slices.Sort(distances)
		median := percentile(distances, 0.5)
		f.MedianM = &median
	}
	return f
}
