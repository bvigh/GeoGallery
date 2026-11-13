package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rereadID        = "33333333-3333-4333-8333-333333333333"
	rereadKey       = "media/owner/object.bin"
	rereadWallNoon  = "2001:02:03 12:00:00"
	recordedNoCopy  = `{"v":1,"by":"ingest","transcoded":false}`
	recordedCopy    = `{"v":1,"by":"ingest","transcoded":true}`
	rereadLavf      = "Lavf60.16.100"
	rereadPresigned = "https://s3.test/" + rereadKey + "?X-Amz-Signature=deadbeef"
	rereadBrowser   = "https://s3.public.test/" + rereadKey + "?X-Amz-Signature=browser"
)

var (
	rereadNoon     = time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)
	rereadUploaded = time.Date(2001, 3, 1, 0, 0, 0, 0, time.UTC)
	utcZone        = ingest.CameraZone{Loc: time.UTC, Set: true, Rules: panasonicOffsetRule}
	// panasonicOffsetRule is the shipped Panasonic rule without its video part, so no video re-read
	// here reads a head unless a test asks for it.
	panasonicOffsetRule = ingest.CameraRules{{Make: "Panasonic", IgnoreOffsetTag: true, Why: "test"}}
)

// noonPlus is a stored time, delta after the file's clock.
func noonPlus(delta time.Duration) *time.Time {
	t := rereadNoon.Add(delta)
	return &t
}

// rereadRow answers the item query, in the order the handler scans.
type rereadRow struct {
	mediaType string
	takenAt   *time.Time
	uploaded  time.Time
	evidence  string
}

func (r rereadRow) Scan(dest ...any) error {
	*dest[0].(*string) = r.mediaType
	*dest[1].(*string) = rereadKey
	*dest[2].(**time.Time) = r.takenAt
	*dest[3].(*time.Time) = r.uploaded
	if r.evidence != "" {
		*dest[4].(*[]byte) = []byte(r.evidence)
	}
	return nil
}

func photoRow(takenAt *time.Time) rereadRow {
	return rereadRow{mediaType: "photo", takenAt: takenAt, uploaded: rereadUploaded}
}

func videoRow(takenAt *time.Time, evidence string) rereadRow {
	return rereadRow{mediaType: "video", takenAt: takenAt, uploaded: rereadUploaded, evidence: evidence}
}

type stubRereadStorage struct {
	head       []byte
	headErr    error
	presignErr error
	blockHead  bool

	mu               sync.Mutex
	headKeys         []string
	headSizes        []int64
	presigned        []string
	browserPresigned []string
}

func (s *stubRereadStorage) PresignGetInternal(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presigned = append(s.presigned, key)
	return rereadPresigned, s.presignErr
}

// PresignGet is the browser's URL, which the backend may not reach. rereadStorage does not need it;
// the stub has it so a re-read switched to it fails the test below instead of compiling silently.
func (s *stubRereadStorage) PresignGet(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.browserPresigned = append(s.browserPresigned, key)
	return rereadBrowser, nil
}

func (s *stubRereadStorage) ReadHead(ctx context.Context, key string, n int64) ([]byte, error) {
	s.mu.Lock()
	s.headKeys = append(s.headKeys, key)
	s.headSizes = append(s.headSizes, n)
	s.mu.Unlock()
	if s.blockHead {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.head, s.headErr
}

type probeFunc = func(context.Context, string) (ingest.VideoProbeResult, error)

func probeReturns(result ingest.VideoProbeResult) probeFunc {
	return func(context.Context, string) (ingest.VideoProbeResult, error) { return result, nil }
}

func probeMustNotRun(t *testing.T) probeFunc {
	return func(context.Context, string) (ingest.VideoProbeResult, error) {
		t.Error("ffprobe must not run for this request")
		return ingest.VideoProbeResult{}, nil
	}
}

func newRereadHandler(row pgx.Row, store *stubRereadStorage, zone ingest.CameraZone, probe probeFunc) *ProvenanceHandler {
	return &ProvenanceHandler{
		db:         &mockMatcherDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return row }},
		storage:    store,
		cameraZone: zone,
		probeURL:   probe,
		probeSlots: make(chan struct{}, maxConcurrentProbes),
		photoSlots: make(chan struct{}, maxConcurrentPhotoReads),
	}
}

func callFile(t *testing.T, h *ProvenanceHandler) FileReread {
	t.Helper()
	rec := httptest.NewRecorder()
	h.File(rec, provenanceRequest(rereadID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var got FileReread
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
	return got
}

func photoReread(t *testing.T, photo exiftest.Photo, row rereadRow, zone ingest.CameraZone) FileReread {
	t.Helper()
	store := &stubRereadStorage{head: exiftest.JPEG(photo)}
	return callFile(t, newRereadHandler(row, store, zone, probeMustNotRun(t)))
}

func videoReread(t *testing.T, probe ingest.VideoProbeResult, row rereadRow) FileReread {
	t.Helper()
	return callFile(t, newRereadHandler(row, &stubRereadStorage{}, utcZone, probeReturns(probe)))
}

func checkIDs(got FileReread) []string {
	ids := []string{}
	for _, check := range got.Checks {
		ids = append(ids, check.ID)
	}
	return ids
}

func TestProvenanceFile_PhotoClasses(t *testing.T) {
	withTime := exiftest.Photo{DateTimeOriginal: rereadWallNoon}
	noTime := exiftest.Photo{Make: "TestMake"}

	tests := []struct {
		name      string
		photo     exiftest.Photo
		stored    *time.Time
		evidence  string
		want      string
		wantDelta *float64
	}{
		{"agrees", withTime, noonPlus(0), "", rereadSame, ptr(0.0)},
		{"under a second", withTime, noonPlus(999 * time.Millisecond), "", rereadSame, ptr(-0.999)},
		{"a second is not the same", withTime, noonPlus(time.Second), "", rereadOther, ptr(-1.0)},
		{"an hour later", withTime, noonPlus(-time.Hour), "", rereadWholeOffset, ptr(3600.0)},
		{"an hour earlier", withTime, noonPlus(time.Hour), "", rereadWholeOffset, ptr(-3600.0)},
		{"a quarter hour", withTime, noonPlus(-15 * time.Minute), "", rereadWholeOffset, ptr(900.0)},
		{"exactly 14 h", withTime, noonPlus(-14 * time.Hour), "", rereadWholeOffset, ptr(50400.0)},
		{"over 14 h is not an offset", withTime, noonPlus(-14*time.Hour - 15*time.Minute), "", rereadOther, ptr(51300.0)},
		{"not a multiple of 900", withTime, noonPlus(-time.Hour - time.Second), "", rereadOther, ptr(3601.0)},
		{"a file with no time", noTime, noonPlus(0), "", rereadNoFileTime, nil},
		{"no file time comes before no stored time", noTime, nil, "", rereadNoFileTime, nil},
		{"a time in the file and none stored", withTime, nil, "", rereadStoredHasNoTime, nil},
		{"a photo is never a transcode", noTime, noonPlus(0), recordedCopy, rereadNoFileTime, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := photoRow(tt.stored)
			row.evidence = tt.evidence

			got := photoReread(t, tt.photo, row, utcZone)

			assert.Equal(t, tt.want, got.Class)
			if tt.wantDelta == nil {
				assert.Nil(t, got.DeltaS)
			} else {
				require.NotNil(t, got.DeltaS)
				assert.InDelta(t, *tt.wantDelta, *got.DeltaS, 1e-9)
			}
		})
	}

	t.Run("bytes that are not EXIF", func(t *testing.T) {
		store := &stubRereadStorage{head: []byte("not a photo")}
		got := callFile(t, newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t)))
		assert.Equal(t, rereadNoFileTime, got.Class)
		assert.Nil(t, got.RuleToday)
	})
}

func TestProvenanceFile_VideoClasses(t *testing.T) {
	created := time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)
	withTime := ingest.VideoProbeResult{CodecName: "hevc", CreatedAt: &created}
	lavf := ingest.VideoProbeResult{CodecName: "h264", Encoder: rereadLavf}
	lavfWithTime := ingest.VideoProbeResult{CodecName: "h264", Encoder: rereadLavf, CreatedAt: &created}

	tests := []struct {
		name      string
		mediaType string
		probe     ingest.VideoProbeResult
		stored    *time.Time
		evidence  string
		want      string
	}{
		{"agrees", "video", withTime, noonPlus(0), "", rereadSame},
		{"an hour off", "video", withTime, noonPlus(-time.Hour), "", rereadWholeOffset},
		{"something else", "video", withTime, noonPlus(-17 * time.Second), "", rereadOther},
		{"a time in the file and none stored", "video", withTime, nil, "", rereadStoredHasNoTime},
		{"nothing to go on", "video", ingest.VideoProbeResult{CodecName: "hevc"}, noonPlus(0), "", rereadNoFileTime},
		{"no video stream", "video", ingest.VideoProbeResult{}, noonPlus(0), "", rereadNoFileTime},

		// With evidence, only its flag counts.
		{"evidence: transcoded", "video", ingest.VideoProbeResult{CodecName: "hevc"}, noonPlus(0), recordedCopy, rereadUnverifiableTranscoded},
		{"evidence: transcoded, for a 360 video", "video_360", ingest.VideoProbeResult{}, noonPlus(0), recordedCopy, rereadUnverifiableTranscoded},
		{"evidence: transcoded, and no stored time", "video", ingest.VideoProbeResult{}, nil, recordedCopy, rereadUnverifiableTranscoded},
		{"evidence: not transcoded, though it looks re-encoded", "video", lavf, noonPlus(0), recordedNoCopy, rereadNoFileTime},
		{"evidence: transcoded, with a time, is checkable", "video", lavfWithTime, noonPlus(0), recordedCopy, rereadSame},

		// Without evidence, the heuristic.
		{"heuristic: H.264 written by Lavf", "video", lavf, noonPlus(0), "", rereadUnverifiableTranscoded},
		{"heuristic: 360 video", "video_360", lavf, noonPlus(0), "", rereadUnverifiableTranscoded},
		{"heuristic: a different encoder", "video", ingest.VideoProbeResult{CodecName: "h264", Encoder: "HandBrake 1.0"}, noonPlus(0), "", rereadNoFileTime},
		{"heuristic: no encoder", "video", ingest.VideoProbeResult{CodecName: "h264"}, noonPlus(0), "", rereadNoFileTime},
		{"heuristic: another codec", "video", ingest.VideoProbeResult{CodecName: "hevc", Encoder: rereadLavf}, noonPlus(0), "", rereadNoFileTime},
		{"heuristic: with a time, is checkable", "video", lavfWithTime, noonPlus(0), "", rereadSame},

		// Evidence from an unknown writer is not shown as recorded, so it reads as absent here too.
		{"an unknown writer's evidence is no evidence", "video", lavf, noonPlus(0), `{"by":"someone","transcoded":false}`, rereadUnverifiableTranscoded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := videoRow(tt.stored, tt.evidence)
			row.mediaType = tt.mediaType

			got := videoReread(t, tt.probe, row)

			assert.Equal(t, tt.want, got.Class)
		})
	}
}

func TestProvenanceFile_RuleToday(t *testing.T) {
	zone := ingest.CameraZone{Loc: time.FixedZone("Test/Zone", 330*60), Set: true, Rules: panasonicOffsetRule}
	wall := exiftest.Photo{DateTimeOriginal: rereadWallNoon}
	ifd0 := exiftest.Photo{DateTime: rereadWallNoon}
	offset := exiftest.Photo{DateTimeOriginal: rereadWallNoon, OffsetTimeOriginal: "+02:00"}
	ignored := exiftest.Photo{DateTimeOriginal: rereadWallNoon, OffsetTimeOriginal: "+02:00", Make: "Panasonic"}
	gps := exiftest.Photo{DateTimeOriginal: rereadWallNoon, GPS: &exiftest.GPS{Lat: 10, Lng: 20, Time: rereadNoon.Add(-3 * time.Hour)}}

	const unset = "server zone (unset)"
	tests := []struct {
		name       string
		photo      exiftest.Photo
		zone       ingest.CameraZone
		wantSource string
		wantRule   string
		wantOffset int
		wantName   *string
		wantUTC    time.Time
	}{
		{"camera zone, set", wall, zone, "exif_original", "camera", 330, ptr("Test/Zone"), rereadNoon.Add(-330 * time.Minute)},
		{"camera zone, unset", wall, ingest.CameraZone{Loc: zone.Loc}, "exif_original", "camera", 330, ptr(unset), rereadNoon.Add(-330 * time.Minute)},
		{"a zero camera zone reads as UTC", wall, ingest.CameraZone{}, "exif_original", "camera", 0, ptr(unset), rereadNoon},
		{"IFD0 DateTime", ifd0, zone, "exif_ifd0", "camera", 330, ptr("Test/Zone"), rereadNoon.Add(-330 * time.Minute)},
		{"the offset tag", offset, zone, "exif_original", "offset", 120, nil, rereadNoon.Add(-2 * time.Hour)},
		{"an exempt make falls to the camera zone", ignored, zone, "exif_original", "camera", 330, ptr("Test/Zone"), rereadNoon.Add(-330 * time.Minute)},
		{"the GPS clock", gps, zone, "exif_original", "gps", 180, nil, rereadNoon.Add(-3 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := photoReread(t, tt.photo, photoRow(noonPlus(0)), tt.zone)

			require.NotNil(t, got.RuleToday)
			assert.Equal(t, tt.wantSource, got.RuleToday.Source)
			require.NotNil(t, got.RuleToday.ZoneRule)
			assert.Equal(t, tt.wantRule, *got.RuleToday.ZoneRule)
			require.NotNil(t, got.RuleToday.OffsetMin)
			assert.Equal(t, tt.wantOffset, *got.RuleToday.OffsetMin)
			assert.Equal(t, tt.wantName, got.RuleToday.ZoneName)
			assert.True(t, tt.wantUTC.Equal(got.RuleToday.TakenAt), "got %s", got.RuleToday.TakenAt)
		})
	}
}

func TestProvenanceFile_Checks(t *testing.T) {
	wall := exiftest.Photo{DateTimeOriginal: rereadWallNoon}
	staleGPS := &exiftest.GPS{Lat: 10, Lng: 20, Time: rereadNoon.Add(-10 * time.Minute)}
	created := rereadNoon

	t.Run("none when nothing is remarkable", func(t *testing.T) {
		assert.Equal(t, []string{}, checkIDs(photoReread(t, wall, photoRow(noonPlus(0)), utcZone)))
	})
	t.Run("taken after the upload", func(t *testing.T) {
		row := photoRow(noonPlus(0))
		row.uploaded = rereadNoon.Add(-2 * time.Hour)

		got := photoReread(t, wall, row, utcZone)

		require.Len(t, got.Checks, 1)
		assert.Equal(t, rereadCheck{ID: checkTakenAfterUpload, Level: checkLevelWarn, DeltaS: ptr(7200.0)}, got.Checks[0])
	})
	t.Run("taken at the upload instant is not after it", func(t *testing.T) {
		row := photoRow(noonPlus(0))
		row.uploaded = rereadNoon

		assert.Empty(t, photoReread(t, wall, row, utcZone).Checks)
	})
	t.Run("taken after upload needs a stored time", func(t *testing.T) {
		row := photoRow(nil)
		row.uploaded = rereadNoon.Add(-2 * time.Hour)

		assert.Empty(t, photoReread(t, wall, row, utcZone).Checks)
	})
	t.Run("IFD0 DateTime may be an edit time", func(t *testing.T) {
		got := photoReread(t, exiftest.Photo{DateTime: rereadWallNoon}, photoRow(noonPlus(0)), utcZone)

		assert.Equal(t, []rereadCheck{{ID: checkIFD0MayBeEditTime, Level: checkLevelInfo}}, got.Checks)
	})
	t.Run("offset ignored", func(t *testing.T) {
		got := photoReread(t, exiftest.Photo{DateTimeOriginal: rereadWallNoon, OffsetTimeOriginal: "+01:00", Make: "Panasonic"}, photoRow(noonPlus(0)), utcZone)

		assert.Equal(t, []rereadCheck{{ID: checkOffsetIgnored, Level: checkLevelInfo}}, got.Checks)
		assert.True(t, got.File.OffsetIgnored)
		assert.Equal(t, ptr("+01:00"), got.File.OffsetTag)
	})
	t.Run("a stale GPS clock is rejected, and says by how much", func(t *testing.T) {
		got := photoReread(t, exiftest.Photo{DateTimeOriginal: rereadWallNoon, GPS: staleGPS}, photoRow(noonPlus(0)), utcZone)

		assert.Equal(t, "camera", *got.RuleToday.ZoneRule)
		assert.Equal(t, []rereadCheck{{ID: checkGPSClockRejected, Level: checkLevelInfo, DeltaS: ptr(600.0)}}, got.Checks)
	})
	t.Run("an accepted GPS clock is not", func(t *testing.T) {
		accepted := &exiftest.GPS{Lat: 10, Lng: 20, Time: rereadNoon.Add(-time.Hour)}

		got := photoReread(t, exiftest.Photo{DateTimeOriginal: rereadWallNoon, GPS: accepted}, photoRow(noonPlus(-time.Hour)), utcZone)

		assert.Equal(t, "gps", *got.RuleToday.ZoneRule)
		assert.Empty(t, got.Checks)
	})
	t.Run("a GPS stamp with no wall clock has no difference to report", func(t *testing.T) {
		got := photoReread(t, exiftest.Photo{GPS: staleGPS}, photoRow(noonPlus(0)), utcZone)

		assert.Nil(t, got.RuleToday)
		assert.Nil(t, got.File.WallMinusGPSS)
		assert.Equal(t, []rereadCheck{{ID: checkGPSClockRejected, Level: checkLevelInfo}}, got.Checks)
	})
	t.Run("a video's time may be the end of the recording", func(t *testing.T) {
		got := videoReread(t, ingest.VideoProbeResult{CreatedAt: &created}, videoRow(noonPlus(0), ""))

		assert.Equal(t, []rereadCheck{{ID: checkVideoTimeMayBeEnd, Level: checkLevelInfo}}, got.Checks)
	})
	t.Run("a video with no time has nothing to remark on", func(t *testing.T) {
		assert.Empty(t, videoReread(t, ingest.VideoProbeResult{}, videoRow(noonPlus(0), "")).Checks)
	})
	t.Run("in the table's order", func(t *testing.T) {
		row := photoRow(noonPlus(0))
		row.uploaded = rereadNoon.Add(-time.Hour)

		got := photoReread(t, exiftest.Photo{DateTime: rereadWallNoon, OffsetTimeOriginal: "+01:00", Make: "Panasonic", GPS: staleGPS}, row, utcZone)

		assert.Equal(t, []string{checkTakenAfterUpload, checkIFD0MayBeEditTime, checkOffsetIgnored, checkGPSClockRejected}, checkIDs(got))
	})
}

// time.Time refuses to marshal a year outside 0 to 9999, so one corrupt or crafted value would
// otherwise answer 500 and lose the rest of what the file says.
func TestProvenanceFile_YearsJSONCannotCarry(t *testing.T) {
	const lastWall = "9999:12:31 23:59:59"
	lastDay := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	stampPastEnd := &exiftest.GPS{Lat: 10, Lng: 20, Time: lastDay, Hour: 24} // 10000-01-01T00:00:00Z

	beforeYearZero := time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("", 3600))
	pastYear9999 := time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -2*3600))
	storedPastEnd := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("photo", func(t *testing.T) {
		tests := []struct {
			name      string
			photo     exiftest.Photo
			stored    *time.Time
			wantClass string
			wantRule  bool
		}{
			{"a GPS stamp past the last year, and a good wall clock",
				exiftest.Photo{DateTimeOriginal: rereadWallNoon, GPS: stampPastEnd}, noonPlus(0), rereadSame, true},
			{"a GPS stamp past the last year, accepted against the wall clock beside it",
				exiftest.Photo{DateTimeOriginal: "9999:12:31 23:00:00", GPS: stampPastEnd}, noonPlus(0), rereadNoFileTime, false},
			{"a wall clock whose offset carries it past the last year",
				exiftest.Photo{DateTimeOriginal: lastWall, OffsetTimeOriginal: "-05:00"}, noonPlus(0), rereadNoFileTime, false},
			{"no file time wins over a stored time out of range",
				exiftest.Photo{DateTimeOriginal: lastWall, OffsetTimeOriginal: "-05:00"}, &storedPastEnd, rereadNoFileTime, false},
			{"a good file and a stored time out of range",
				exiftest.Photo{DateTimeOriginal: rereadWallNoon}, &storedPastEnd, rereadStoredHasNoTime, true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := photoReread(t, tt.photo, photoRow(tt.stored), utcZone)

				assert.Equal(t, tt.wantClass, got.Class)
				assert.Equal(t, tt.wantRule, got.RuleToday != nil)
				assert.Nil(t, got.File.GPSStamp)
				assert.Nil(t, got.File.WallMinusGPSS, "the difference needs the stamp")
				assert.NotContains(t, checkIDs(got), checkGPSClockRejected, "there is no stamp left to report")
				assert.Equal(t, ptr(tt.photo.DateTimeOriginal), got.File.WallClock, "the text as written is still reported")
			})
		}

		t.Run("a stored time out of range is reported as none", func(t *testing.T) {
			got := photoReread(t, exiftest.Photo{DateTimeOriginal: rereadWallNoon}, photoRow(&storedPastEnd), utcZone)
			assert.Nil(t, got.StoredTakenAt)
			assert.Nil(t, got.DeltaS)
			assert.NotContains(t, checkIDs(got), checkTakenAfterUpload)
		})
	})

	t.Run("video", func(t *testing.T) {
		tests := []struct {
			name      string
			created   time.Time
			evidence  string
			wantClass string
		}{
			{"before year 0 in UTC", beforeYearZero, "", rereadNoFileTime},
			{"past the last year in UTC", pastYear9999, "", rereadNoFileTime},
			{"still a transcode when recorded as one", beforeYearZero, recordedCopy, rereadUnverifiableTranscoded},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				probe := ingest.VideoProbeResult{CodecName: "hevc", CreatedAt: &tt.created}

				got := videoReread(t, probe, videoRow(noonPlus(0), tt.evidence))

				assert.Equal(t, tt.wantClass, got.Class)
				assert.Nil(t, got.RuleToday)
				assert.Nil(t, got.DeltaS)
				require.NotNil(t, got.File.Video)
				assert.Nil(t, got.File.Video.CreationTime)
				assert.Empty(t, got.Checks, "no container time was read")
			})
		}
	})
}

func TestUTCTime(t *testing.T) {
	tests := []struct {
		name string
		in   *time.Time
		want *time.Time
	}{
		{"nil", nil, nil},
		{"another zone comes back in UTC", ptr(time.Date(2001, 2, 3, 13, 0, 0, 0, time.FixedZone("", 3600))), ptr(time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC))},
		{"the first instant of year 0", ptr(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)), ptr(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC))},
		{"just before year 0", ptr(time.Date(-1, 12, 31, 23, 59, 59, 0, time.UTC)), nil},
		{"the last second of year 9999", ptr(time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)), ptr(time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC))},
		{"year 10000", ptr(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)), nil},
		{"the year is the one in UTC, not the one written", ptr(time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -3600))), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := utcTime(tt.in)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.True(t, tt.want.Equal(*got), "got %s", got)
			assert.Equal(t, time.UTC, got.Location())
			_, err := json.Marshal(got)
			assert.NoError(t, err)
		})
	}
}

// The field names and nulls are the contract with the frontend's FileReread.
func TestProvenanceFile_Wire(t *testing.T) {
	encodedTags := func(t *testing.T, tags []ingest.Tag) string {
		t.Helper()
		b, err := json.Marshal(tags)
		require.NoError(t, err)
		return string(b)
	}

	t.Run("photo", func(t *testing.T) {
		photo := exiftest.JPEG(exiftest.Photo{
			Make: "TestMake", Model: "TestModel", DateTimeOriginal: "2001:02:03 13:00:00", OffsetTimeOriginal: "+01:00",
			GPS: &exiftest.GPS{Lat: 10, Lng: 20, Time: rereadNoon},
		})
		h := newRereadHandler(photoRow(noonPlus(0)), &stubRereadStorage{head: photo}, utcZone, probeMustNotRun(t))
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, fmt.Sprintf(`{
			"basis": "reread_now",
			"stored_taken_at": "2001-02-03T12:00:00Z",
			"uploaded_at": "2001-03-01T00:00:00Z",
			"file": {
				"wall_clock": "2001:02:03 13:00:00",
				"wall_tag": "DateTimeOriginal",
				"offset_tag": "+01:00",
				"offset_ignored": false,
				"gps_stamp": "2001-02-03T12:00:00Z",
				"wall_minus_gps_s": 3600,
				"make": "TestMake",
				"model": "TestModel",
				"video": null
			},
			"rule_today": {"source": "exif_original", "zone_rule": "gps", "offset_min": 60, "zone_name": null, "taken_at": "2001-02-03T12:00:00Z"},
			"delta_s": 0,
			"class": "same",
			"checks": [],
			"tags": %s
		}`, encodedTags(t, ingest.ReadTags(photo))), rec.Body.String())
	})
	t.Run("video", func(t *testing.T) {
		created := rereadNoon
		probe := ingest.VideoProbeResult{
			CreatedAt: &created, DurationS: ptr(12.5), Encoder: rereadLavf, CodecName: "hevc",
			RawTags: map[string]string{"stream.handler_name": "VideoHandler", "format.encoder": rereadLavf},
		}
		h := newRereadHandler(videoRow(noonPlus(0), ""), &stubRereadStorage{}, utcZone, probeReturns(probe))
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{
			"basis": "reread_now",
			"stored_taken_at": "2001-02-03T12:00:00Z",
			"uploaded_at": "2001-03-01T00:00:00Z",
			"file": {
				"wall_clock": null, "wall_tag": null, "offset_tag": null, "offset_ignored": false,
				"gps_stamp": null, "wall_minus_gps_s": null, "make": null, "model": null,
				"video": {"creation_time": "2001-02-03T12:00:00Z", "duration_s": 12.5, "encoder": "Lavf60.16.100", "codec": "hevc"}
			},
			"rule_today": {"source": "container", "zone_rule": null, "offset_min": null, "zone_name": null, "taken_at": "2001-02-03T12:00:00Z"},
			"delta_s": 0,
			"class": "same",
			"checks": [{"id": "video_time_may_be_end", "level": "info", "delta_s": null}],
			"tags": [{"name": "format.encoder", "value": "Lavf60.16.100"}, {"name": "stream.handler_name", "value": "VideoHandler"}]
		}`, rec.Body.String())
	})
	t.Run("nothing read", func(t *testing.T) {
		h := newRereadHandler(photoRow(nil), &stubRereadStorage{head: []byte("not a photo")}, utcZone, probeMustNotRun(t))
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{
			"basis": "reread_now", "stored_taken_at": null, "uploaded_at": "2001-03-01T00:00:00Z",
			"file": {
				"wall_clock": null, "wall_tag": null, "offset_tag": null, "offset_ignored": false,
				"gps_stamp": null, "wall_minus_gps_s": null, "make": null, "model": null, "video": null
			},
			"rule_today": null, "delta_s": null, "class": "no_file_time", "checks": [], "tags": []
		}`, rec.Body.String())
	})
}

// Times a non-UTC process scanned, or a file wrote in another zone, still go out as UTC.
func TestProvenanceFile_SendsUTC(t *testing.T) {
	zone := time.FixedZone("TEST+9", 9*3600)
	row := photoRow(ptr(rereadNoon.In(zone)))
	row.uploaded = rereadUploaded.In(zone)
	photo := exiftest.Photo{DateTimeOriginal: rereadWallNoon, OffsetTimeOriginal: "+02:00", GPS: &exiftest.GPS{Lat: 10, Lng: 20, Time: rereadNoon}}
	store := &stubRereadStorage{head: exiftest.JPEG(photo)}
	rec := httptest.NewRecorder()

	newRereadHandler(row, store, utcZone, probeMustNotRun(t)).File(rec, provenanceRequest(rereadID))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "+09:00")
	var got FileReread
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, time.UTC, got.StoredTakenAt.Location())
	assert.Equal(t, time.UTC, got.UploadedAt.Location())
	assert.Equal(t, time.UTC, got.RuleToday.TakenAt.Location())
	assert.Equal(t, time.UTC, got.File.GPSStamp.Location())

	t.Run("a container time written with an offset", func(t *testing.T) {
		created := rereadNoon.In(zone)
		rec := httptest.NewRecorder()
		h := newRereadHandler(videoRow(noonPlus(0), ""), &stubRereadStorage{}, utcZone, probeReturns(ingest.VideoProbeResult{CreatedAt: &created}))

		h.File(rec, provenanceRequest(rereadID))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotContains(t, rec.Body.String(), "+09:00")
	})
}

func TestProvenanceFile_ReadsTheRightWay(t *testing.T) {
	t.Run("a photo takes one ranged read of 1 MiB and never runs ffprobe", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: rereadWallNoon})}

		callFile(t, newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t)))

		assert.Equal(t, []string{rereadKey}, store.headKeys)
		assert.Equal(t, []int64{1 << 20}, store.headSizes)
		assert.Empty(t, store.presigned)
	})
	t.Run("a 360 photo is a photo", func(t *testing.T) {
		row := photoRow(noonPlus(0))
		row.mediaType = "photo_360"
		store := &stubRereadStorage{head: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: rereadWallNoon})}

		assert.Equal(t, rereadSame, callFile(t, newRereadHandler(row, store, utcZone, probeMustNotRun(t))).Class)
	})
	t.Run("with a rule dating videos from their EXIF, a video also takes one ranged read of 1 MiB", func(t *testing.T) {
		store := &stubRereadStorage{}
		zone := utcZone
		zone.Rules = panasonicVideoRules

		callFile(t, newRereadHandler(videoRow(noonPlus(0), ""), store, zone, probeReturns(ingest.VideoProbeResult{})))

		assert.Equal(t, []string{rereadKey}, store.headKeys)
		assert.Equal(t, []int64{1 << 20}, store.headSizes)
		assert.Equal(t, []string{rereadKey}, store.presigned)
	})
	t.Run("without one, a video goes to ffprobe by a URL presigned for S3_ENDPOINT and is never read through S3", func(t *testing.T) {
		store := &stubRereadStorage{}
		var gotURL string
		probe := func(_ context.Context, url string) (ingest.VideoProbeResult, error) {
			gotURL = url
			return ingest.VideoProbeResult{}, nil
		}

		callFile(t, newRereadHandler(videoRow(noonPlus(0), ""), store, utcZone, probe))

		assert.Equal(t, rereadPresigned, gotURL)
		assert.Equal(t, []string{rereadKey}, store.presigned)
		assert.Empty(t, store.browserPresigned, "the backend may not reach S3_PUBLIC_ENDPOINT")
		assert.Empty(t, store.headKeys)
	})
	t.Run("the query is scoped to the owner, literally", func(t *testing.T) {
		var gotSQL string
		var gotArgs []any
		h := newRereadHandler(nil, &stubRereadStorage{}, utcZone, probeMustNotRun(t))
		h.db = &mockMatcherDB{queryRowFn: func(_ context.Context, sql string, args ...any) pgx.Row {
			gotSQL, gotArgs = sql, args
			return noRowsRow{}
		}}

		h.File(httptest.NewRecorder(), provenanceRequest(rereadID))

		assert.Contains(t, gotSQL, "m.owner_id = $2")
		assert.NotContains(t, gotSQL, "album")
		assert.Equal(t, []any{rereadID, testOwnerID}, gotArgs)
	})
	t.Run("the default limit is 15 s", func(t *testing.T) {
		var remaining time.Duration
		h := newRereadHandler(photoRow(noonPlus(0)), &stubRereadStorage{}, utcZone, probeMustNotRun(t))
		h.db = &mockMatcherDB{queryRowFn: func(ctx context.Context, _ string, _ ...any) pgx.Row {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			remaining = time.Until(deadline)
			return noRowsRow{}
		}}

		h.File(httptest.NewRecorder(), provenanceRequest(rereadID))

		assert.Greater(t, remaining, 10*time.Second)
		assert.LessOrEqual(t, remaining, 15*time.Second)
	})
}

func TestProvenanceFile_Errors(t *testing.T) {
	boom := errors.New("boom")
	failingProbe := func(context.Context, string) (ingest.VideoProbeResult, error) {
		return ingest.VideoProbeResult{}, errors.New("ffprobe failed: exit status 1: boom")
	}

	tests := []struct {
		name  string
		id    string
		row   pgx.Row
		store *stubRereadStorage
		probe probeFunc
		want  int
	}{
		{"a malformed id", "not-a-uuid", nil, &stubRereadStorage{}, probeMustNotRun(t), http.StatusNotFound},
		{"no such item for this owner", rereadID, noRowsRow{}, &stubRereadStorage{}, probeMustNotRun(t), http.StatusNotFound},
		{"the database fails", rereadID, errRow{boom}, &stubRereadStorage{}, probeMustNotRun(t), http.StatusInternalServerError},
		{"S3 fails", rereadID, photoRow(noonPlus(0)), &stubRereadStorage{headErr: boom}, probeMustNotRun(t), http.StatusBadGateway},
		{"presigning fails", rereadID, videoRow(noonPlus(0), ""), &stubRereadStorage{presignErr: boom}, probeMustNotRun(t), http.StatusBadGateway},
		{"ffprobe fails", rereadID, videoRow(noonPlus(0), ""), &stubRereadStorage{}, failingProbe, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newRereadHandler(tt.row, tt.store, utcZone, tt.probe)
			if tt.row == nil {
				h.db = &mockMatcherDB{} // a query would be a nil call
			}
			rec := httptest.NewRecorder()

			h.File(rec, provenanceRequest(tt.id))

			assert.Equal(t, tt.want, rec.Code)
			assert.NotContains(t, rec.Body.String(), "boom", "details stay in the log")
		})
	}
	t.Run("no user is a loud 500, never a query", func(t *testing.T) {
		h := newRereadHandler(nil, &stubRereadStorage{}, utcZone, probeMustNotRun(t))
		r := httptest.NewRequest(http.MethodGet, "/api/media/x/provenance/file", nil)

		assert.Panics(t, func() { h.File(httptest.NewRecorder(), r) })
	})
}

func TestProvenanceFile_Timeouts(t *testing.T) {
	blockingProbe := func(ctx context.Context, _ string) (ingest.VideoProbeResult, error) {
		<-ctx.Done()
		return ingest.VideoProbeResult{}, errors.New("ffprobe failed: signal: killed")
	}

	t.Run("S3 too slow", func(t *testing.T) {
		h := newRereadHandler(photoRow(noonPlus(0)), &stubRereadStorage{blockHead: true}, utcZone, probeMustNotRun(t))
		h.fileTimeout = 10 * time.Millisecond
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	})
	t.Run("ffprobe too slow", func(t *testing.T) {
		h := newRereadHandler(videoRow(noonPlus(0), ""), &stubRereadStorage{}, utcZone, blockingProbe)
		h.fileTimeout = 10 * time.Millisecond
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.Empty(t, h.probeSlots, "the slot is given back")
	})
	t.Run("the database too slow", func(t *testing.T) {
		h := newRereadHandler(nil, &stubRereadStorage{}, utcZone, probeMustNotRun(t))
		h.fileTimeout = 10 * time.Millisecond
		h.db = &mockMatcherDB{queryRowFn: func(ctx context.Context, _ string, _ ...any) pgx.Row {
			<-ctx.Done()
			return errRow{ctx.Err()}
		}}
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	})
	t.Run("leaving the item cancels ffprobe", func(t *testing.T) {
		started := make(chan struct{})
		probe := func(ctx context.Context, url string) (ingest.VideoProbeResult, error) {
			close(started)
			return blockingProbe(ctx, url)
		}
		h := newRereadHandler(videoRow(noonPlus(0), ""), &stubRereadStorage{}, utcZone, probe)
		h.fileTimeout = time.Minute
		req := provenanceRequest(rereadID)
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		go func() {
			<-started
			cancel()
		}()
		rec := httptest.NewRecorder()

		h.File(rec, req.WithContext(ctx))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.Empty(t, h.probeSlots)
	})
}

func TestProvenanceFile_ProbeSemaphore(t *testing.T) {
	created := rereadNoon
	result := ingest.VideoProbeResult{CreatedAt: &created}

	t.Run("an exhausted wait is the ordinary 504, and starts no process", func(t *testing.T) {
		h := newRereadHandler(videoRow(noonPlus(0), ""), &stubRereadStorage{}, utcZone, probeMustNotRun(t))
		h.fileTimeout = 10 * time.Millisecond
		for range maxConcurrentProbes {
			h.probeSlots <- struct{}{}
		}
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	})
	t.Run("a free slot is enough", func(t *testing.T) {
		h := newRereadHandler(videoRow(noonPlus(0), ""), &stubRereadStorage{}, utcZone, probeReturns(result))
		for range maxConcurrentProbes - 1 {
			h.probeSlots <- struct{}{}
		}

		assert.Equal(t, rereadSame, callFile(t, h).Class)
		assert.Len(t, h.probeSlots, maxConcurrentProbes-1, "the slot is given back")
	})
	t.Run("a photo waits for a photo slot, and an exhausted wait is the ordinary 504", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: rereadWallNoon})}
		h := newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t))
		h.fileTimeout = 10 * time.Millisecond
		for range maxConcurrentPhotoReads {
			h.photoSlots <- struct{}{}
		}
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	})
	t.Run("a photo decodes in an EXIF slot shared with uploads, and an exhausted wait is the ordinary 504", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: rereadWallNoon})}
		h := newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t))
		h.fileTimeout = 10 * time.Millisecond
		takeExifSlots(t)
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.Empty(t, h.photoSlots, "the photo slot is given back")
	})
	t.Run("a photo gives its EXIF slot back", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: rereadWallNoon})}
		h := newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t))

		assert.Equal(t, rereadSame, callFile(t, h).Class)
		assert.Empty(t, exifDecodeSlots)
	})
	t.Run("a photo gives its slot back", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: rereadWallNoon})}
		h := newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t))

		assert.Equal(t, rereadSame, callFile(t, h).Class)
		assert.Empty(t, h.photoSlots)
	})
	t.Run("photos do not wait for the video slots", func(t *testing.T) {
		store := &stubRereadStorage{head: exiftest.JPEG(exiftest.Photo{DateTimeOriginal: rereadWallNoon})}
		h := newRereadHandler(photoRow(noonPlus(0)), store, utcZone, probeMustNotRun(t))
		for range maxConcurrentProbes {
			h.probeSlots <- struct{}{}
		}

		assert.Equal(t, rereadSame, callFile(t, h).Class)
	})
	t.Run("at most four run at once and the rest wait", func(t *testing.T) {
		const requests = maxConcurrentProbes * 2
		started := make(chan struct{}, requests)
		gate := make(chan struct{})
		probe := func(context.Context, string) (ingest.VideoProbeResult, error) {
			started <- struct{}{}
			<-gate
			return result, nil
		}
		h := newRereadHandler(videoRow(noonPlus(0), ""), &stubRereadStorage{}, utcZone, probe)
		h.fileTimeout = time.Minute

		codes := make([]int, requests)
		var wg sync.WaitGroup
		for i := range requests {
			wg.Go(func() {
				rec := httptest.NewRecorder()
				h.File(rec, provenanceRequest(rereadID))
				codes[i] = rec.Code
			})
		}

		for range maxConcurrentProbes {
			<-started
		}
		select {
		case <-started:
			t.Error("a fifth ffprobe started while four were running")
		case <-time.After(50 * time.Millisecond):
		}
		close(gate)
		wg.Wait()

		for _, code := range codes {
			assert.Equal(t, http.StatusOK, code)
		}
		assert.Empty(t, h.probeSlots)
	})
}

func TestNewProvenanceHandler_FileDependencies(t *testing.T) {
	h := NewProvenanceHandler(nil, nil, time.Hour, ingest.CameraZone{})

	assert.Equal(t, 4, cap(h.probeSlots))
	assert.Equal(t, 4, cap(h.photoSlots))
	assert.NotNil(t, h.probeURL)
	assert.Equal(t, 15*time.Second, fileRereadTimeout)
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous, stdWriter, stdFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		// SetDefault also redirects the standard logger, and setting the default back does not undo it.
		log.SetOutput(stdWriter)
		log.SetFlags(stdFlags)
	})
	return &logs
}

// Invariant 10. The real ffprobe fails on a signed URL, and neither the log nor the answer holds it.
func TestProvenanceFile_PresignedURLIsNeverLogged(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not in PATH")
	}
	const signature = "deadbeefsignature"
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	store := &signedURLStorage{url: srv.URL + "/" + rereadKey + "?X-Amz-Signature=" + signature}
	h := newRereadHandler(videoRow(noonPlus(0), ""), nil, utcZone, ingest.ProbeURL)
	h.storage = store

	t.Run("ffprobe fails", func(t *testing.T) {
		logs := captureLogs(t)
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusBadGateway, rec.Code)
		assert.Contains(t, logs.String(), "provenance file: read failed", "so the check below looks at a real line")
		assert.NotContains(t, logs.String(), signature)
		assert.NotContains(t, logs.String(), srv.URL)
		assert.NotContains(t, rec.Body.String(), signature)
	})
	t.Run("nothing but the cause is logged after a timeout", func(t *testing.T) {
		logs := captureLogs(t)
		h := newRereadHandler(videoRow(noonPlus(0), ""), nil, utcZone, func(ctx context.Context, url string) (ingest.VideoProbeResult, error) {
			<-ctx.Done()
			return ingest.VideoProbeResult{}, fmt.Errorf("fetching %s: %w", url, ctx.Err())
		})
		h.storage = store
		h.fileTimeout = 10 * time.Millisecond
		rec := httptest.NewRecorder()

		h.File(rec, provenanceRequest(rereadID))

		assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
		assert.Contains(t, logs.String(), "provenance file: stopped")
		assert.NotContains(t, logs.String(), signature)
	})
}

// Real ffmpeg and ffprobe over HTTP: the container rule and the re-encode heuristic on what real
// tools actually write. A clip made by ffmpeg is what a transcode looks like.
func TestProvenanceFile_RealVideo(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	serve := func(t *testing.T, extraArgs ...string) *signedURLStorage {
		t.Helper()
		path := filepath.Join(t.TempDir(), "clip.mp4")
		args := append([]string{"-y", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.1:r=10", "-c:v", "libx264"}, extraArgs...)
		out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput()
		require.NoError(t, err, string(out))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, path) }))
		t.Cleanup(srv.Close)
		return &signedURLStorage{url: srv.URL + "/clip.mp4?X-Amz-Signature=sig"}
	}
	reread := func(t *testing.T, store *signedURLStorage, row rereadRow) FileReread {
		t.Helper()
		h := newRereadHandler(row, nil, utcZone, ingest.ProbeURL)
		h.storage = store
		return callFile(t, h)
	}

	t.Run("a container time", func(t *testing.T) {
		got := reread(t, serve(t, "-metadata", "creation_time=2001-02-03T12:00:00Z"), videoRow(noonPlus(0), ""))

		assert.Equal(t, rereadSame, got.Class)
		require.NotNil(t, got.RuleToday)
		assert.Equal(t, captureSrcContainer, got.RuleToday.Source)
		assert.Equal(t, ptr("h264"), got.File.Video.Codec)
		assert.NotNil(t, got.File.Video.DurationS)
		assert.Equal(t, []string{checkVideoTimeMayBeEnd}, checkIDs(got))
		names := []string{}
		for _, tag := range got.Tags {
			names = append(names, tag.Name)
		}
		assert.Contains(t, names, "format.creation_time")
		assert.Contains(t, names, "format.encoder")
		assert.Contains(t, names, "stream.handler_name")
		assert.True(t, slices.IsSorted(names))
	})
	t.Run("no container time, and the file says ffmpeg wrote it", func(t *testing.T) {
		store := serve(t)

		assert.Equal(t, rereadUnverifiableTranscoded, reread(t, store, videoRow(noonPlus(0), "")).Class, "by the heuristic")
		assert.Equal(t, rereadNoFileTime, reread(t, store, videoRow(noonPlus(0), recordedNoCopy)).Class, "evidence outranks it")
		assert.Equal(t, rereadUnverifiableTranscoded, reread(t, store, videoRow(noonPlus(0), recordedCopy)).Class, "by the evidence")
	})
}

type signedURLStorage struct{ url string }

func (s *signedURLStorage) PresignGetInternal(context.Context, string) (string, error) {
	return s.url, nil
}

func (s *signedURLStorage) ReadHead(context.Context, string, int64) ([]byte, error) {
	return nil, errors.New("a video is never read through S3")
}
