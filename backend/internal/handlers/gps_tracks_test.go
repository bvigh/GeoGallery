package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mock for gpsTrackDB ---

type mockGPSTrackDB struct {
	execFn func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (m *mockGPSTrackDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return m.execFn(ctx, sql, args...)
}

// makeTag returns a CommandTag reporting n rows affected.
func makeTag(n int) pgconn.CommandTag {
	return pgconn.NewCommandTag(fmt.Sprintf("INSERT 0 %d", n))
}

// postImport fires POST /api/gps-tracks/import with the given body.
func postImport(h *GPSTrackHandler, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/gps-tracks/import", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, withTestUser(r))
	return w
}

// --- Cycle 1: single valid point → inserted:1, skipped:0, errors:[] ---

func TestImport_SingleValidPoint_ReturnsInserted1(t *testing.T) {
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	require.Equal(t, http.StatusOK, w.Code)
	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 1, resp.Inserted)
	assert.Equal(t, 0, resp.Skipped)
	assert.Empty(t, resp.Errors)
}

// --- Cycle 2: invalid recorded_at → row error at index 0 ---

func TestImport_InvalidRecordedAt_ReturnsRowError(t *testing.T) {
	execCalled := false
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return makeTag(0), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "not-a-date", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	require.Equal(t, http.StatusOK, w.Code)
	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 0, resp.Inserted)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, 0, resp.Errors[0].Row)
	assert.Contains(t, resp.Errors[0].Reason, "recorded_at")
	assert.False(t, execCalled)
}

// --- Cycle 3: latitude out of range → row error ---

func TestImport_LatitudeOutOfRange_ReturnsRowError(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 91.0, "lng": 19.0, "source": "strava"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, 0, resp.Errors[0].Row)
	assert.Contains(t, resp.Errors[0].Reason, "lat")
}

// --- Cycle 4: longitude out of range → row error ---

func TestImport_LongitudeOutOfRange_ReturnsRowError(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": -181.0, "source": "strava"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Reason, "lng")
}

// --- Cycle 5: empty source → row error ---

func TestImport_EmptySource_ReturnsRowError(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": ""},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Reason, "source")
}

// --- Cycle 6: all-invalid rows → no DB Exec called ---

func TestImport_AllInvalidRows_NoDBCall(t *testing.T) {
	execCalled := false
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled = true
			return makeTag(0), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "bad", "lat": 47.5, "lng": 19.0, "source": "s"},
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 99.0, "lng": 19.0, "source": "s"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 0, resp.Inserted)
	assert.Len(t, resp.Errors, 2)
	assert.False(t, execCalled)
}

// --- Cycle 7: ON CONFLICT DO NOTHING → skipped count via RowsAffected ---

func TestImport_DuplicateRows_CountedAsSkipped(t *testing.T) {
	// Two rows sent; DB reports only 1 inserted (the other conflicted).
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil // 1 inserted, 1 skipped by ON CONFLICT
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
		{"recorded_at": "2024-06-15T12:00:01Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	var resp importResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, 1, resp.Inserted)
	assert.Equal(t, 1, resp.Skipped)
	assert.Empty(t, resp.Errors)
}

// --- Cycle 8: GPS matcher is called after successful insert ---

func TestImport_ValidPoints_MatcherCalled(t *testing.T) {
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			return makeTag(1), nil
		},
	}
	matcherCalled := false
	h := &GPSTrackHandler{
		db:          db,
		matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error {
			matcherCalled = true
			return nil
		},
	}

	postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
	})

	assert.True(t, matcherCalled)
}

// --- Owner propagation: owner_id is in every row and the matcher runs for the caller ---

func TestImport_PropagatesOwner(t *testing.T) {
	var capturedSQL string
	var capturedArgs []any
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			capturedSQL, capturedArgs = sql, args
			return makeTag(2), nil
		},
	}
	var matcherOwner string
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, ownerID string, _ time.Duration, _ bool) error {
			matcherOwner = ownerID
			return nil
		}}

	w := postImport(h, []map[string]any{
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "strava"},
		{"recorded_at": "2024-06-15T12:00:01Z", "lat": 47.6, "lng": 19.1, "source": "strava"},
	})

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, capturedSQL, "INSERT INTO gps_tracks (owner_id, source,")
	require.Len(t, capturedArgs, 16, "8 bind params per row")
	assert.Equal(t, testOwnerID, capturedArgs[0])
	assert.Equal(t, testOwnerID, capturedArgs[8])
	assert.Equal(t, testOwnerID, matcherOwner)
}

// --- Body over the size limit → 413, nothing inserted ---

func TestImport_BodyTooLarge_Returns413(t *testing.T) {
	assert.Equal(t, int64(100<<20), maxImportBodyBytes)
	orig := maxImportBodyBytes
	maxImportBodyBytes = 1024
	t.Cleanup(func() { maxImportBodyBytes = orig })

	execCalled := false
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
		execCalled = true
		return makeTag(0), nil
	}}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	body := `[{"recorded_at":"2024-06-15T12:00:00Z","lat":47.5,"lng":19.0,"source":"` +
		strings.Repeat("x", int(maxImportBodyBytes)) + `"}]`
	r := httptest.NewRequest(http.MethodPost, "/api/gps-tracks/import", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Import(w, withTestUser(r))

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.False(t, execCalled)
}

// --- Cycle 9: invalid JSON body → 400 ---

func TestImport_InvalidJSON_Returns400(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) { return makeTag(0), nil }}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	r := httptest.NewRequest(http.MethodPost, "/api/gps-tracks/import", strings.NewReader("not json"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, withTestUser(r))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Cycle 10: 1001 valid rows → two DB Exec calls (batching) ---

func TestImport_1001Rows_TwoDBBatches(t *testing.T) {
	execCallCount := 0
	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCallCount++
			return makeTag(1000), nil
		},
	}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour, runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	rows := make([]map[string]any, 1001)
	for i := range rows {
		rows[i] = map[string]any{
			"recorded_at": fmt.Sprintf("2024-06-15T%02d:%02d:%02dZ", i/3600, (i/60)%60, i%60),
			"lat":         47.5,
			"lng":         19.0,
			"source":      "strava",
		}
	}

	w := postImport(h, rows)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 2, execCallCount)
}

// --- POST /api/gps-tracks/upload ---

type trackFile struct{ name, content string }

// postTrackFiles fires POST /api/gps-tracks/upload with one multipart "files" part per file.
func postTrackFiles(h *GPSTrackHandler, files ...trackFile) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		fw, _ := mw.CreateFormFile("files", f.name)
		fw.Write([]byte(f.content))
	}
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/gps-tracks/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.Upload(w, withTestUser(r))
	return w
}

const (
	walkGPX = `<gpx><trk><trkseg>
	  <trkpt lat="47.5" lon="19.0"><time>2024-06-15T12:00:00Z</time></trkpt>
	  <trkpt lat="47.6" lon="19.1"><time>2024-06-15T12:00:01Z</time></trkpt>
	</trkseg></trk></gpx>`
	driveNMEA = "$GPRMC,120000.000,A,4730.0000,N,01900.0000,E,0,0,150624,,,A*00\n"
)

// insertRecorder is a gpsTrackDB that records the source and owner of every inserted row and
// reports them all as new.
type insertRecorder struct {
	sources []string
	owners  []any
	execs   int
}

func (d *insertRecorder) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	d.execs++
	for i := 0; i < len(args); i += 8 {
		d.owners = append(d.owners, args[i])
		d.sources = append(d.sources, args[i+1].(string))
	}
	return makeTag(len(args) / 8), nil
}

func decodeTrackUpload(t *testing.T, w *httptest.ResponseRecorder) trackUploadResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp trackUploadResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

func TestTrackUpload_ParsesEachFileByExtension(t *testing.T) {
	db := &insertRecorder{}
	var matcherCalls int
	var matcherOwner string
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, ownerID string, _ time.Duration, reprocess bool) error {
			matcherCalls++
			matcherOwner = ownerID
			assert.True(t, reprocess)
			return nil
		}}

	resp := decodeTrackUpload(t, postTrackFiles(h,
		trackFile{"walk.gpx", walkGPX},
		trackFile{"FILE240615-120000.NMEA", driveNMEA},
	))

	assert.Equal(t, trackUploadResponse{Inserted: 3, Empty: []string{}, Failed: []fileError{}}, resp)
	assert.Equal(t, []string{"gpx:walk.gpx", "gpx:walk.gpx", "nmea:FILE240615-120000.NMEA"}, db.sources,
		"source is <format>:<file name>, so re-uploading the file skips its points")
	assert.Equal(t, []any{testOwnerID, testOwnerID, testOwnerID}, db.owners)
	assert.Equal(t, 2, db.execs, "one insert per file")
	assert.Equal(t, 1, matcherCalls, "the matcher runs once per request")
	assert.Equal(t, testOwnerID, matcherOwner)
}

func TestTrackUpload_ReportsFailedAndEmptyFiles(t *testing.T) {
	db := &insertRecorder{}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	resp := decodeTrackUpload(t, postTrackFiles(h,
		trackFile{"notes.txt", "hello"},
		trackFile{"broken.gpx", "<gpx><trk>"},
		trackFile{"planned-route.gpx", `<gpx><trkpt lat="47.5" lon="19.0"/></gpx>`},
		trackFile{"walk.gpx", walkGPX},
	))

	assert.Equal(t, 2, resp.Inserted, "the readable file still imports")
	assert.Equal(t, []string{"planned-route.gpx"}, resp.Empty)
	require.Len(t, resp.Failed, 2)
	assert.Equal(t, "notes.txt", resp.Failed[0].File)
	assert.Contains(t, resp.Failed[0].Reason, "unsupported file type")
	assert.Equal(t, "broken.gpx", resp.Failed[1].File)
	assert.True(t, strings.HasPrefix(resp.Failed[1].Reason, "gpx: "), resp.Failed[1].Reason)
}

func TestTrackUpload_NothingImportable_NoInsertNoMatcher(t *testing.T) {
	db := &insertRecorder{}
	matcherCalled := false
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error {
			matcherCalled = true
			return nil
		}}

	resp := decodeTrackUpload(t, postTrackFiles(h, trackFile{"notes.txt", "x"}, trackFile{"empty.nmea", ""}))

	assert.Equal(t, 0, resp.Inserted)
	assert.Equal(t, []string{"empty.nmea"}, resp.Empty)
	assert.Len(t, resp.Failed, 1)
	assert.Zero(t, db.execs)
	assert.False(t, matcherCalled)
}

func TestTrackUpload_GeoGalleryJSONKeepsRowSource(t *testing.T) {
	db := &insertRecorder{}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	decodeTrackUpload(t, postTrackFiles(h, trackFile{"points.json", `[
		{"recorded_at": "2024-06-15T12:00:00Z", "lat": 47.5, "lng": 19.0, "source": "gpx:tracks/Strava/1.gpx"},
		{"recorded_at": "2024-06-15T12:00:01Z", "lat": 47.6, "lng": 19.1}
	]`}))

	assert.Equal(t, []string{"gpx:tracks/Strava/1.gpx", "geogallery:points.json"}, db.sources)
}

func TestTrackUpload_DuplicatesCountedAsSkipped(t *testing.T) {
	db := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
		return makeTag(0), nil // both points were imported before
	}}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	resp := decodeTrackUpload(t, postTrackFiles(h, trackFile{"walk.gpx", walkGPX}))

	assert.Equal(t, 0, resp.Inserted)
	assert.Equal(t, 2, resp.Skipped)
}

func TestTrackUpload_BadRequests(t *testing.T) {
	h := &GPSTrackHandler{db: &insertRecorder{}, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postTrackFiles(h)
	assert.Equal(t, http.StatusBadRequest, w.Code, "no files")

	r := httptest.NewRequest(http.MethodPost, "/api/gps-tracks/upload", strings.NewReader(`[]`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Upload(w, withTestUser(r))
	assert.Equal(t, http.StatusBadRequest, w.Code, "not multipart")
}

func TestTrackUpload_BodyTooLarge_Returns413(t *testing.T) {
	assert.Equal(t, int64(256<<20), maxTrackUploadBytes)
	orig := maxTrackUploadBytes
	maxTrackUploadBytes = 1024
	t.Cleanup(func() { maxTrackUploadBytes = orig })

	db := &insertRecorder{}
	h := &GPSTrackHandler{db: db, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error { return nil }}

	w := postTrackFiles(h, trackFile{"walk.gpx", walkGPX + strings.Repeat(" ", 2048)})

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Zero(t, db.execs)
}

func TestTrackUpload_InsertOrMatcherFailure_Returns500(t *testing.T) {
	failingDB := &mockGPSTrackDB{execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, fmt.Errorf("connection reset")
	}}
	matcherCalled := false
	h := &GPSTrackHandler{db: failingDB, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error {
			matcherCalled = true
			return nil
		}}
	w := postTrackFiles(h, trackFile{"walk.gpx", walkGPX})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "connection reset", "DB errors stay in the log")
	assert.False(t, matcherCalled)

	h = &GPSTrackHandler{db: &insertRecorder{}, matchWindow: time.Hour,
		runMatcher: func(_ context.Context, _ matcherDB, _ string, _ time.Duration, _ bool) error {
			return fmt.Errorf("matcher down")
		}}
	w = postTrackFiles(h, trackFile{"walk.gpx", walkGPX})
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
