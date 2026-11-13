package handlers

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	// fileRereadTimeout bounds one re-read, including its wait for a probe slot.
	fileRereadTimeout = 15 * time.Second
	// maxConcurrentProbes limits ffprobe runs: every existing row is unrecorded, so arrow-stepping
	// through old videos would otherwise start one process per item. The figure is a judgement.
	maxConcurrentProbes = 4
	// maxConcurrentPhotoReads limits photo decodes separately, so slow videos never delay a photo.
	// Decoding cannot be cancelled, so the request's deadline alone would not bound that work.
	maxConcurrentPhotoReads = 4
)

// What a re-read found, compared with the stored time.
const (
	rereadUnverifiableTranscoded = "unverifiable_transcoded"
	rereadNoFileTime             = "no_file_time"
	rereadStoredHasNoTime        = "stored_has_no_time"
	rereadSame                   = "same"
	rereadWholeOffset            = "whole_offset"
	rereadOther                  = "other"
)

// A difference of a whole number of quarter hours, up to the largest real UTC offset, is what an
// older rule or a changed zone setting produces.
const (
	rereadOffsetStep = 15 * time.Minute
	rereadOffsetMax  = 14 * time.Hour
)

const (
	checkTakenAfterUpload  = "taken_after_upload"
	checkIFD0MayBeEditTime = "ifd0_may_be_edit_time"
	checkOffsetIgnored     = "offset_ignored"
	checkGPSClockRejected  = "gps_clock_rejected"
	checkVideoTimeMayBeEnd = "video_time_may_be_end"

	checkLevelWarn = "warn"
	checkLevelInfo = "info"
)

// rereadStorage is what a re-read needs from object storage. A presigned URL must never reach a
// log, and that includes the one handed to ffprobe. ffprobe runs here, so its URL is signed for
// S3_ENDPOINT: PresignGet signs for S3_PUBLIC_ENDPOINT, which the backend may not reach.
type rereadStorage interface {
	PresignGetInternal(ctx context.Context, key string) (string, error)
	ReadHead(ctx context.Context, key string, n int64) ([]byte, error)
}

// FileReread is GET /api/media/{id}/provenance/file. Instants are always UTC on the wire.
type FileReread struct {
	Basis         string        `json:"basis"` // always "reread_now"
	StoredTakenAt *time.Time    `json:"stored_taken_at"`
	UploadedAt    time.Time     `json:"uploaded_at"`
	File          rereadFile    `json:"file"`
	RuleToday     *rereadRule   `json:"rule_today"` // nil when no capture time could be read
	DeltaS        *float64      `json:"delta_s"`    // RuleToday.TakenAt minus stored, in seconds
	Class         string        `json:"class"`
	Checks        []rereadCheck `json:"checks"`
	Tags          []ingest.Tag  `json:"tags"`
}

type rereadFile struct {
	WallClock     *string      `json:"wall_clock"`
	WallTag       *string      `json:"wall_tag"`
	OffsetTag     *string      `json:"offset_tag"`
	OffsetIgnored bool         `json:"offset_ignored"`
	GPSStamp      *time.Time   `json:"gps_stamp"`
	WallMinusGPSS *float64     `json:"wall_minus_gps_s"`
	Make          *string      `json:"make"`
	Model         *string      `json:"model"`
	Video         *rereadVideo `json:"video"`
}

type rereadVideo struct {
	CreationTime *time.Time `json:"creation_time"`
	DurationS    *float64   `json:"duration_s"`
	Encoder      *string    `json:"encoder"`
	Codec        *string    `json:"codec"`
}

// rereadRule is the capture time today's rules read from the file.
type rereadRule struct {
	Source    string    `json:"source"`
	ZoneRule  *string   `json:"zone_rule"`
	OffsetMin *int      `json:"offset_min"` // nil for a container time
	ZoneName  *string   `json:"zone_name"`  // set only when ZoneRule is "camera"
	TakenAt   time.Time `json:"taken_at"`
}

type rereadCheck struct {
	ID     string   `json:"id"`
	Level  string   `json:"level"`
	DeltaS *float64 `json:"delta_s"`
}

// rereadItem is the stored row. evidence is nil unless the row's capture evidence counts as
// recorded, which is exactly when the provenance response shows it.
type rereadItem struct {
	mediaType  string
	storageKey string
	takenAt    *time.Time
	uploadedAt time.Time
	evidence   json.RawMessage
}

// fileRead is what the stored file says today.
type fileRead struct {
	file rereadFile
	rule *rereadRule
	tags []ingest.Tag
}

// File serves GET /api/media/{id}/provenance/file: the stored file read again with today's rules.
// It only reports, and never moves taken_at. A foreign or shared item is 404.
func (h *ProvenanceHandler) File(w http.ResponseWriter, r *http.Request) {
	ownerID := auth.MustUser(r.Context()).ID
	id, ok := auth.CanonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cmp.Or(h.fileTimeout, fileRereadTimeout))
	defer cancel()

	item, err := h.loadRereadItem(ctx, id, ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		if !rereadStopped(ctx, w, "load item") {
			slog.Error("provenance file: load item failed", "err", err)
			http.Error(w, "db error", http.StatusInternalServerError)
		}
		return
	}

	read, err := h.readFile(ctx, item)
	if err != nil {
		if !rereadStopped(ctx, w, "read file") {
			slog.Error("provenance file: read failed", "err", err)
			http.Error(w, "could not read the stored file", http.StatusBadGateway)
		}
		return
	}

	body, err := json.Marshal(newFileReread(item, read))
	if err != nil {
		slog.Error("provenance file: encode response failed", "err", err)
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		slog.Warn("provenance file: write response failed", "err", err)
	}
}

// rereadStopped answers 504 when the context has ended, and reports whether it did. An abort is
// routine, because leaving an item cancels the request. Only ctx.Err is logged: the error that
// follows a timeout is not needed, and could name what was being fetched.
func rereadStopped(ctx context.Context, w http.ResponseWriter, step string) bool {
	cause := ctx.Err()
	if cause == nil {
		return false
	}
	level := slog.LevelWarn
	if errors.Is(cause, context.Canceled) {
		level = slog.LevelDebug
	}
	slog.Log(ctx, level, "provenance file: stopped", "step", step, "cause", cause)
	http.Error(w, "timed out reading the stored file", http.StatusGatewayTimeout)
	return true
}

// loadRereadItem reads one own item. The literal owner_id is what makes a foreign or shared item
// pgx.ErrNoRows.
func (h *ProvenanceHandler) loadRereadItem(ctx context.Context, id, ownerID string) (rereadItem, error) {
	var item rereadItem
	var evidence []byte
	err := h.db.QueryRow(ctx, `
		SELECT m.media_type, m.storage_key, m.taken_at, m.created_at, m.capture_evidence
		FROM media m
		WHERE m.id = $1 AND m.owner_id = $2
	`, id, ownerID).Scan(&item.mediaType, &item.storageKey, &item.takenAt, &item.uploadedAt, &evidence)
	if err != nil {
		return rereadItem{}, err
	}
	item.uploadedAt = item.uploadedAt.UTC()
	item.takenAt = utcTime(item.takenAt)
	item.evidence = h.timeFacts(evidence).Evidence
	return item, nil
}

func isVideoMedia(mediaType string) bool {
	return mediaType == "video" || mediaType == "video_360"
}

// utcTime returns t in UTC, or nil. pgx scans a timestamptz into the process's zone. A year RFC 3339
// cannot express also gives nil: time.Time refuses to marshal it, so one corrupt tag would otherwise
// lose the whole response.
func utcTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	if u.Year() < jsonFirstYear || u.Year() > jsonLastYear {
		return nil
	}
	return &u
}

// readFile waits for a re-read slot of the file's kind. The wait is inside ctx, so an exhausted wait
// is the ordinary 504.
func (h *ProvenanceHandler) readFile(ctx context.Context, item rereadItem) (fileRead, error) {
	slots, read := h.photoSlots, h.readPhoto
	if isVideoMedia(item.mediaType) {
		slots, read = h.probeSlots, h.readVideo
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return fileRead{}, ctx.Err()
	}
	return read(ctx, item.storageKey)
}

// readPhoto takes one ranged GET. Photos are never transcoded, so the object is the original. The
// decode waits for one of the exifDecodeSlots that uploads use, so re-reads add nothing to what
// decoding can take in this process; photoSlots only limits the heads fetched and held.
func (h *ProvenanceHandler) readPhoto(ctx context.Context, key string) (fileRead, error) {
	head, err := h.storage.ReadHead(ctx, key, mediaHeadSize)
	if err != nil {
		return fileRead{}, err
	}
	return inExifDecodeSlot(ctx, func() fileRead { return newPhotoRead(head, h.cameraZone) })
}

func newPhotoRead(head []byte, zone ingest.CameraZone) fileRead {
	file, rule := exifFileRead(ingest.ExtractExif(bytes.NewReader(head), zone), zone)
	return fileRead{file: file, rule: rule, tags: ingest.ReadTags(head)}
}

// exifFileRead is what a file's EXIF says about its capture time, and the rule today reads from it.
func exifFileRead(e ingest.ExifData, zone ingest.CameraZone) (rereadFile, *rereadRule) {
	file := rereadFile{
		WallClock:     nilIfEmpty(e.Raw.WallClock),
		WallTag:       nilIfEmpty(e.Raw.WallTag),
		OffsetTag:     nilIfEmpty(e.Raw.OffsetTag),
		OffsetIgnored: e.Raw.OffsetIgnored,
		GPSStamp:      utcTime(e.Raw.GPSStamp),
		Make:          nilIfEmpty(e.Raw.Make),
		Model:         nilIfEmpty(e.CameraModel),
	}
	if file.GPSStamp != nil && e.Raw.WallMinusGPS != nil {
		seconds := e.Raw.WallMinusGPS.Seconds()
		file.WallMinusGPSS = &seconds
	}
	takenAt := utcTime(e.TakenAt)
	if takenAt == nil {
		return file, nil
	}
	_, offsetSeconds := e.TakenAt.Zone()
	offsetMin := offsetSeconds / 60
	rule := &rereadRule{
		Source:    captureSource(e, ingest.VideoProbeResult{}, nil),
		ZoneRule:  nilIfEmpty(e.TakenAtZone),
		OffsetMin: &offsetMin,
		TakenAt:   *takenAt,
	}
	if e.TakenAtZone == ingest.ZoneCamera {
		name := zone.Name()
		rule.ZoneName = &name
	}
	return file, rule
}

// readVideo runs ffprobe on a presigned URL. A head-only read cannot do that part, because moov is
// often at the end. When a camera rule dates some videos from the EXIF inside them, it first takes
// one ranged GET of the head ingest read that EXIF from, and decodes it in one of the
// exifDecodeSlots, like a photo.
func (h *ProvenanceHandler) readVideo(ctx context.Context, key string) (fileRead, error) {
	var embedded videoExif
	if h.cameraZone.Rules.DatesVideosFromExif() {
		head, err := h.storage.ReadHead(ctx, key, mediaHeadSize)
		if err != nil {
			return fileRead{}, err
		}
		embedded, err = inExifDecodeSlot(ctx, func() videoExif {
			e, ok := ingest.VideoExif(head, h.cameraZone)
			return videoExif{e, ok}
		})
		if err != nil {
			return fileRead{}, err
		}
	}
	url, err := h.storage.PresignGetInternal(ctx, key)
	if err != nil {
		return fileRead{}, err
	}
	probe, err := h.probeURL(ctx, url)
	if err != nil {
		return fileRead{}, err
	}
	return newVideoRead(probe, embedded, h.cameraZone), nil
}

// newVideoRead reads the time from the video's EXIF when the camera rules date it from there, as
// ingest does, and from the container otherwise.
func newVideoRead(probe ingest.VideoProbeResult, embedded videoExif, zone ingest.CameraZone) fileRead {
	read := fileRead{tags: ingest.TagsFromMap(probe.RawTags)}
	if embedded.ok {
		read.file, read.rule = exifFileRead(embedded.exif, zone)
	}
	read.file.Video = &rereadVideo{
		CreationTime: utcTime(probe.CreatedAt),
		DurationS:    probe.DurationS,
		Encoder:      nilIfEmpty(probe.Encoder),
		Codec:        nilIfEmpty(ingest.CleanTag(probe.CodecName)),
	}
	if created := read.file.Video.CreationTime; !embedded.ok && created != nil {
		read.rule = &rereadRule{Source: captureSrcContainer, TakenAt: *created}
	}
	return read
}

// newFileReread compares what the file says today with the stored row.
func newFileReread(item rereadItem, read fileRead) FileReread {
	result := FileReread{
		Basis:         "reread_now",
		StoredTakenAt: item.takenAt,
		UploadedAt:    item.uploadedAt,
		File:          read.file,
		RuleToday:     read.rule,
		Checks:        rereadChecks(item, read),
		Tags:          read.tags,
	}
	var delta *time.Duration
	if read.rule != nil && item.takenAt != nil {
		d := read.rule.TakenAt.Sub(*item.takenAt)
		seconds := d.Seconds()
		delta, result.DeltaS = &d, &seconds
	}
	result.Class = rereadClass(item, read, delta)
	return result
}

// rereadClass is the first matching row of the class table in docs/gps-debugging.md. delta is nil
// exactly when the file gave no time or the row holds none, and the first three rows cover both.
func rereadClass(item rereadItem, read fileRead, delta *time.Duration) string {
	switch {
	case read.rule == nil && reEncodedVideo(item, read):
		return rereadUnverifiableTranscoded
	case read.rule == nil:
		return rereadNoFileTime
	case item.takenAt == nil:
		return rereadStoredHasNoTime
	case delta.Abs() < time.Second:
		return rereadSame
	case *delta%rereadOffsetStep == 0 && delta.Abs() <= rereadOffsetMax:
		return rereadWholeOffset
	default:
		return rereadOther
	}
}

// reEncodedVideo says whether the stored object is, or looks like, a transcode. With evidence only
// its transcoded flag counts. Without any, an H.264 file written by ffmpeg's muxer matches, as does
// an H.264 file exported by desktop software, so the frontend's wording says "may".
func reEncodedVideo(item rereadItem, read fileRead) bool {
	if !isVideoMedia(item.mediaType) {
		return false
	}
	if len(item.evidence) > 0 {
		var recorded struct {
			Transcoded bool `json:"transcoded"`
		}
		if err := json.Unmarshal(item.evidence, &recorded); err != nil {
			return false
		}
		return recorded.Transcoded
	}
	video := read.file.Video
	return video.Codec != nil && *video.Codec == "h264" &&
		video.Encoder != nil && strings.HasPrefix(*video.Encoder, "Lavf")
}

// rereadChecks lists what deserves a remark about this file, in the table's order.
func rereadChecks(item rereadItem, read fileRead) []rereadCheck {
	checks := []rereadCheck{}
	add := func(id, level string, deltaS *float64) {
		checks = append(checks, rereadCheck{ID: id, Level: level, DeltaS: deltaS})
	}

	if item.takenAt != nil && item.takenAt.After(item.uploadedAt) {
		seconds := item.takenAt.Sub(item.uploadedAt).Seconds()
		add(checkTakenAfterUpload, checkLevelWarn, &seconds)
	}
	if read.file.WallTag != nil && *read.file.WallTag == ingest.WallTagIFD0 {
		add(checkIFD0MayBeEditTime, checkLevelInfo, nil)
	}
	if read.file.OffsetIgnored {
		add(checkOffsetIgnored, checkLevelInfo, nil)
	}
	if read.file.GPSStamp != nil && (read.rule == nil || read.rule.ZoneRule == nil || *read.rule.ZoneRule != ingest.ZoneGPS) {
		add(checkGPSClockRejected, checkLevelInfo, read.file.WallMinusGPSS)
	}
	if read.rule != nil && read.rule.Source == captureSrcContainer {
		add(checkVideoTimeMayBeEnd, checkLevelInfo, nil)
	}
	return checks
}
