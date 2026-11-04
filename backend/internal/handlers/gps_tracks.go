package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"codeberg.org/bvigh/geogallery/internal/auth"
	"codeberg.org/bvigh/geogallery/internal/gpstrack"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type gpsTrackDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type GPSTrackHandler struct {
	db          gpsTrackDB
	matcherPool matcherDB // same *pgxpool.Pool in production; nil in tests (runMatcher is mocked)
	matchWindow time.Duration
	runMatcher  func(ctx context.Context, db matcherDB, ownerID string, maxWindow time.Duration, reprocess bool) error
}

func NewGPSTrackHandler(pool *pgxpool.Pool, matchWindow time.Duration) *GPSTrackHandler {
	return &GPSTrackHandler{
		db:          pool,
		matcherPool: pool,
		matchWindow: matchWindow,
		runMatcher:  RunGPSMatcher,
	}
}

type gpsPointInput struct {
	RecordedAt string   `json:"recorded_at"`
	Lat        float64  `json:"lat"`
	Lng        float64  `json:"lng"`
	Source     string   `json:"source"`
	AltitudeM  *float64 `json:"altitude_m"`
	AccuracyM  *float64 `json:"accuracy_m"`
	Activity   *string  `json:"activity"`
}

type rowError struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
}

type importResponse struct {
	Inserted int        `json:"inserted"`
	Skipped  int        `json:"skipped"`
	Errors   []rowError `json:"errors"`
}

const importBatchSize = 1000

var maxImportBodyBytes int64 = 100 << 20 // var so tests can lower it

func (h *GPSTrackHandler) Import(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBodyBytes)
	var points []gpsPointInput
	if err := json.NewDecoder(r.Body).Decode(&points); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	var valid []trackRow
	var errs []rowError

	for i, p := range points {
		t, err := time.Parse(time.RFC3339, p.RecordedAt)
		if err != nil {
			errs = append(errs, rowError{Row: i, Reason: "invalid recorded_at: must be RFC3339"})
			continue
		}
		if p.Lat < -90 || p.Lat > 90 {
			errs = append(errs, rowError{Row: i, Reason: "invalid lat: must be between -90 and 90"})
			continue
		}
		if p.Lng < -180 || p.Lng > 180 {
			errs = append(errs, rowError{Row: i, Reason: "invalid lng: must be between -180 and 180"})
			continue
		}
		if strings.TrimSpace(p.Source) == "" {
			errs = append(errs, rowError{Row: i, Reason: "invalid source: must not be empty"})
			continue
		}
		valid = append(valid, trackRow{
			recordedAt: t,
			lat:        p.Lat,
			lng:        p.Lng,
			source:     p.Source,
			altitudeM:  p.AltitudeM,
			accuracyM:  p.AccuracyM,
			activity:   p.Activity,
		})
	}

	resp := importResponse{Errors: errs}
	if resp.Errors == nil {
		resp.Errors = []rowError{}
	}

	ctx := r.Context()

	inserted, err := h.insertRows(ctx, ownerID, valid)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	resp.Inserted = inserted
	resp.Skipped = len(valid) - inserted

	if len(valid) > 0 {
		if err := h.runMatcher(ctx, h.matcherPool, ownerID, h.matchWindow, true); err != nil {
			slog.Error("gps-tracks: GPS matcher failed after import", "err", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// trackRow is one validated gps_tracks row.
type trackRow struct {
	recordedAt time.Time
	lat        float64
	lng        float64
	source     string
	altitudeM  *float64
	accuracyM  *float64
	activity   *string
}

// insertRows inserts rows for ownerID in batches of importBatchSize and returns how many were
// new; the rest already existed (same owner, source and recorded_at). It logs a failure.
func (h *GPSTrackHandler) insertRows(ctx context.Context, ownerID string, rows []trackRow) (int, error) {
	inserted := 0
	for start := 0; start < len(rows); start += importBatchSize {
		end := min(start+importBatchSize, len(rows))
		batch := rows[start:end]

		var sb strings.Builder
		sb.WriteString(`INSERT INTO gps_tracks (owner_id, source, recorded_at, location, altitude_m, accuracy_m, activity) VALUES `)
		const paramsPerRow = 8
		args := make([]any, 0, len(batch)*paramsPerRow)
		for j, vp := range batch {
			if j > 0 {
				sb.WriteString(", ")
			}
			base := j*paramsPerRow + 1
			sb.WriteString(fmt.Sprintf("($%d, $%d, $%d, ST_SetSRID(ST_MakePoint($%d, $%d), 4326), $%d, $%d, $%d)",
				base, base+1, base+2, base+3, base+4, base+5, base+6, base+7))
			args = append(args, ownerID, vp.source, vp.recordedAt, vp.lng, vp.lat, vp.altitudeM, vp.accuracyM, vp.activity)
		}
		sb.WriteString(` ON CONFLICT DO NOTHING`)

		tag, err := h.db.Exec(ctx, sb.String(), args...)
		if err != nil {
			slog.Error("gps-tracks: batch insert failed", "batch_start", start, "err", err)
			return inserted, err
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

var maxTrackUploadBytes int64 = 256 << 20 // var so tests can lower it

type fileError struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type trackUploadResponse struct {
	Inserted int         `json:"inserted"`
	Skipped  int         `json:"skipped"`
	Empty    []string    `json:"empty"`
	Failed   []fileError `json:"failed"`
}

// Upload imports track files (multipart field "files") in any format gpstrack.Parse reads. Each
// point's source is "<format>:<file name>", so re-uploading a file skips its points; a
// GeoGallery JSON row keeps its own source. A file that cannot be read is reported in failed and
// the rest still import; a file without a usable point is reported in empty. The matcher runs
// once at the end.
func (h *GPSTrackHandler) Upload(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	r.Body = http.MaxBytesReader(w, r.Body, maxTrackUploadBytes)
	if err := r.ParseMultipartForm(uploadFormMaxMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		slog.Warn("gps-tracks: parse multipart form failed", "owner", ownerID, "err", err)
		http.Error(w, `{"error":"invalid multipart form"}`, http.StatusBadRequest)
		return
	}
	// RequireUser hands us a copy of the request, so net/http would not remove spooled parts.
	defer func() {
		if err := r.MultipartForm.RemoveAll(); err != nil {
			slog.Error("gps-tracks: remove multipart temp files failed", "err", err)
		}
	}()

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, `{"error":"no files"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	resp := trackUploadResponse{Empty: []string{}, Failed: []fileError{}}
	anyPoints := false
	for _, fh := range files {
		format, points, err := parseTrackFile(fh)
		if err != nil {
			slog.Error("gps-tracks: track file not imported", "owner", ownerID, "file", fh.Filename, "err", err)
			resp.Failed = append(resp.Failed, fileError{File: fh.Filename, Reason: err.Error()})
			continue
		}
		if len(points) == 0 {
			resp.Empty = append(resp.Empty, fh.Filename)
			continue
		}
		anyPoints = true

		source := format + ":" + fh.Filename
		rows := make([]trackRow, len(points))
		for i, p := range points {
			rows[i] = trackRow{recordedAt: p.Time, lat: p.Lat, lng: p.Lng, source: source,
				altitudeM: p.AltitudeM, accuracyM: p.AccuracyM, activity: p.Activity}
			if p.Source != "" {
				rows[i].source = p.Source
			}
		}
		inserted, err := h.insertRows(ctx, ownerID, rows)
		if err != nil {
			http.Error(w, `{"error":"insert failed"}`, http.StatusInternalServerError)
			return
		}
		resp.Inserted += inserted
		resp.Skipped += len(rows) - inserted
	}

	if anyPoints {
		if err := h.runMatcher(ctx, h.matcherPool, ownerID, h.matchWindow, true); err != nil {
			slog.Error("gps-tracks: GPS matcher failed after upload", "err", err)
			http.Error(w, `{"error":"GPS matcher failed"}`, http.StatusInternalServerError)
			return
		}
	}
	slog.Info("gps-tracks: upload done", "owner", ownerID, "files", len(files),
		"inserted", resp.Inserted, "skipped", resp.Skipped, "empty", len(resp.Empty), "failed", len(resp.Failed))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func parseTrackFile(fh *multipart.FileHeader) (string, []gpstrack.Point, error) {
	f, err := fh.Open()
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	return gpstrack.Parse(fh.Filename, f)
}
