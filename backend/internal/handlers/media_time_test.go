package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The IDs of these tests' items, in the form auth.CanonicalUUID returns.
const (
	timeItemA = "00000000-0000-4000-8000-0000000000a1"
	timeItemB = "00000000-0000-4000-8000-0000000000b2"
)

func takenAtRequest(id, body string) *http.Request {
	r := newChiRequest(http.MethodPatch, "/api/media/"+id+"/taken-at", "id", id)
	r.Body = io.NopCloser(strings.NewReader(body))
	return r
}

func shiftRequest(body string) *http.Request {
	return withTestUser(httptest.NewRequest(http.MethodPost, "/api/media/shift-time", strings.NewReader(body)))
}

// noDBPool fails the test when a handler begins a transaction.
type noDBPool struct{ t *testing.T }

func (p noDBPool) Begin(context.Context) (pgx.Tx, error) {
	p.t.Error("the database was reached")
	return nil, errors.New("no database here")
}

// timePool hands out tx, or fails to begin with err.
type timePool struct {
	tx  pgx.Tx
	err error
}

func (p *timePool) Begin(context.Context) (pgx.Tx, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.tx, nil
}

// timeTx is mockTx with Query, which the shift locks its rows with. recordingTimeTx records every
// QueryRow in queryRows.
type timeTx struct {
	*mockTx
	queryFn   func(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	queryRows []execCall
}

func (tx *timeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return tx.queryFn(ctx, sql, args...)
}

type execCall struct {
	sql  string
	args []any
}

// lockedRow answers the lookup of SetTakenAt with m.
func lockedRow(m timeEditRow) pgx.Row {
	return &mockRow{scanFn: func(dest ...any) error {
		*dest[0].(*string) = m.id
		*dest[1].(**time.Time) = m.takenAt
		*dest[2].(*string) = m.mediaType
		*dest[3].(*string) = m.gpsSource
		*dest[4].(**float64) = m.lat
		*dest[5].(**float64) = m.lng
		*dest[6].(*[]byte) = m.timeEdit
		*dest[7].(**string) = m.tripID
		return nil
	}}
}

// recordingTimeTx locks row (or fails the lookup with lookupErr), records every Exec and fails the
// first one whose SQL holds failOn with execErr.
func recordingTimeTx(row timeEditRow, lookupErr error, failOn string, execErr error) (*timeTx, *[]execCall) {
	execs := &[]execCall{}
	tx := &timeTx{}
	tx.mockTx = &mockTx{
		queryRowFn: func(_ context.Context, sql string, args ...any) pgx.Row {
			tx.queryRows = append(tx.queryRows, execCall{sql, args})
			if lookupErr != nil {
				return &mockRow{scanFn: func(...any) error { return lookupErr }}
			}
			return lockedRow(row)
		},
		execFn: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			*execs = append(*execs, execCall{sql, args})
			if failOn != "" && strings.Contains(sql, failOn) {
				return pgconn.CommandTag{}, execErr
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	tx.queryFn = func(context.Context, string, ...any) (pgx.Rows, error) {
		return &mockRows{rows: [][]any{{row.id, *row.takenAt, row.mediaType, row.gpsSource, nil, nil, nil, nil}}}, nil
	}
	return tx, execs
}

// afterOwnerLock checks that the first of execs takes the caller's owner lock (lockOwnerRow), so
// that one owner's capture time changes run one after the other, and returns the rest.
func afterOwnerLock(t *testing.T, execs []execCall) []execCall {
	t.Helper()
	require.NotEmpty(t, execs, "the owner lock was not taken")
	assert.Equal(t, "SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE", execs[0].sql)
	assert.Equal(t, []any{testOwnerID}, execs[0].args)
	return execs[1:]
}

// followup records what the follow-up after a change was asked to do.
type followup struct {
	matcherCalls, tripCalls int
	owner                   string
	window                  time.Duration
	around                  []time.Time
	ctxErr                  error
	deadline                time.Duration // how far off the context's deadline was
	gapHours                int
}

func (f *followup) matcher(err error) func(context.Context, matcherDB, string, time.Duration, []time.Time) error {
	return func(ctx context.Context, _ matcherDB, owner string, window time.Duration, around []time.Time) error {
		f.matcherCalls++
		f.owner, f.window, f.around, f.ctxErr = owner, window, around, ctx.Err()
		if d, ok := ctx.Deadline(); ok {
			f.deadline = time.Until(d)
		}
		return err
	}
}

func (f *followup) trips(err error) func(context.Context, tripDetectDB, string, int) error {
	return func(_ context.Context, _ tripDetectDB, owner string, gapHours int) error {
		f.tripCalls++
		f.gapHours = gapHours
		return err
	}
}

// tripRow answers the trip lookup after a change with tripID (nil: in no trip), or err.
func tripRow(tripID *string, err error) *mockDB {
	return &mockDB{queryRowFn: func(_ context.Context, sql string, args ...any) pgx.Row {
		return &mockRow{scanFn: func(dest ...any) error {
			if err != nil {
				return err
			}
			*dest[0].(**string) = tripID
			return nil
		}}
	}}
}

func utcAt(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return at
}

const validTakenAtBody = `{"wall":"2024-05-01T12:00:00","zone":"Europe/Warsaw"}`

func TestSetTakenAt_RefusedBodies(t *testing.T) {
	const invalid = "invalid body: want a JSON object with wall and either zone or utc_offset_min"
	const badWall = "wall must be a real date and time written YYYY-MM-DDTHH:MM:SS in the years 1-9998"
	const badOffset = "utc_offset_min must be a multiple of 15 from -720 to 840"
	tests := []struct {
		name, body string
		wantStatus int
		wantReason string
	}{
		{"empty body", "", http.StatusBadRequest, invalid},
		{"not an object", `["2024-05-01T12:00:00"]`, http.StatusBadRequest, invalid},
		{"unknown key", `{"wall":"2024-05-01T12:00:00","zone":"UTC","note":"x"}`, http.StatusBadRequest, invalid},
		{"trailing data", validTakenAtBody + `{}`, http.StatusBadRequest, invalid},
		{"wall not text", `{"wall":20240501,"zone":"UTC"}`, http.StatusBadRequest, invalid},
		{"zone not text", `{"wall":"2024-05-01T12:00:00","zone":1}`, http.StatusBadRequest, invalid},
		{"offset as text", `{"wall":"2024-05-01T12:00:00","utc_offset_min":"60"}`, http.StatusBadRequest, invalid},
		{"offset a fraction", `{"wall":"2024-05-01T12:00:00","utc_offset_min":60.5}`, http.StatusBadRequest, invalid},
		{"missing wall", `{"zone":"UTC"}`, http.StatusBadRequest, "wall is required"},
		{"null wall", `{"wall":null,"zone":"UTC"}`, http.StatusBadRequest, "wall is required"},
		{"neither zone nor offset", `{"wall":"2024-05-01T12:00:00"}`, http.StatusBadRequest, "give exactly one of zone and utc_offset_min"},
		{"both null", `{"wall":"2024-05-01T12:00:00","zone":null,"utc_offset_min":null}`, http.StatusBadRequest, "give exactly one of zone and utc_offset_min"},
		{"both", `{"wall":"2024-05-01T12:00:00","zone":"UTC","utc_offset_min":0}`, http.StatusBadRequest, "give exactly one of zone and utc_offset_min"},
		{"space for T", `{"wall":"2024-05-01 12:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"no seconds", `{"wall":"2024-05-01T12:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"a fraction", `{"wall":"2024-05-01T12:00:00.5","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"a zone in the wall", `{"wall":"2024-05-01T12:00:00Z","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"an offset in the wall", `{"wall":"2024-05-01T12:00:00+02:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"short fields", `{"wall":"2024-5-1T12:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"two-digit year", `{"wall":"24-05-01T12:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"30 February", `{"wall":"2024-02-30T12:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"29 February of a common year", `{"wall":"2023-02-29T12:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"hour 24", `{"wall":"2024-05-01T24:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"second 60", `{"wall":"2024-05-01T12:00:60","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"year 0", `{"wall":"0000-06-01T12:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"year 9999", `{"wall":"9999-06-01T12:00:00","zone":"UTC"}`, http.StatusBadRequest, badWall},
		{"empty zone", `{"wall":"2024-05-01T12:00:00","zone":""}`, http.StatusBadRequest, `zone: unknown time zone ""`},
		{"Local", `{"wall":"2024-05-01T12:00:00","zone":"Local"}`, http.StatusBadRequest, `zone: unknown time zone "Local"`},
		{"a path", `{"wall":"2024-05-01T12:00:00","zone":"/etc/localtime"}`, http.StatusBadRequest, `zone: unknown time zone "/etc/localtime"`},
		{"a relative path", `{"wall":"2024-05-01T12:00:00","zone":"../zoneinfo/UTC"}`, http.StatusBadRequest, `zone: unknown time zone "../zoneinfo/UTC"`},
		{"unknown zone", `{"wall":"2024-05-01T12:00:00","zone":"Europe/Nowhere"}`, http.StatusBadRequest, `zone: unknown time zone "Europe/Nowhere"`},
		{"zone over 64 bytes", `{"wall":"2024-05-01T12:00:00","zone":"` + strings.Repeat("A", 65) + `"}`, http.StatusBadRequest, "zone: time zone name longer than 64 bytes"},
		{"offset below -12:00", `{"wall":"2024-05-01T12:00:00","utc_offset_min":-735}`, http.StatusBadRequest, badOffset},
		{"offset above +14:00", `{"wall":"2024-05-01T12:00:00","utc_offset_min":855}`, http.StatusBadRequest, badOffset},
		{"offset not a quarter hour", `{"wall":"2024-05-01T12:00:00","utc_offset_min":61}`, http.StatusBadRequest, badOffset},
		{"spring-forward gap", `{"wall":"2024-03-31T02:30:00","zone":"Europe/Warsaw"}`, http.StatusBadRequest,
			"wall: 2024-03-31T02:30:00 does not exist in Europe/Warsaw: the clocks skipped it"},
		{"before year 1 in UTC", `{"wall":"0001-01-01T05:00:00","utc_offset_min":840}`, http.StatusBadRequest,
			"wall: 0001-01-01T05:00:00 is outside the years 1-9998 in UTC"},
		{"after year 9998 in UTC", `{"wall":"9998-12-31T20:00:00","utc_offset_min":-720}`, http.StatusBadRequest,
			"wall: 9998-12-31T20:00:00 is outside the years 1-9998 in UTC"},
		{"over 16 KiB", strings.Repeat(" ", maxTakenAtBodyBytes) + validTakenAtBody, http.StatusRequestEntityTooLarge, "request body too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &MediaHandler{pool: noDBPool{t}}
			w := httptest.NewRecorder()
			h.SetTakenAt(w, takenAtRequest(timeItemA, tt.body))
			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, tt.wantReason+"\n", w.Body.String())
		})
	}
}

func TestShiftTimes_RefusedBodies(t *testing.T) {
	const invalid = "invalid body: want a JSON object with media_ids and delta_seconds"
	const notWhole = "delta_seconds must be a whole number of seconds"
	const beyond = "delta_seconds must be at most 3155760000 (100 years) either way"
	ids := func(n int) string {
		quoted := make([]string, n)
		for i := range quoted {
			quoted[i] = fmt.Sprintf(`"00000000-0000-4000-8000-%012d"`, i)
		}
		return "[" + strings.Join(quoted, ",") + "]"
	}
	one := `["` + timeItemA + `"]`
	tests := []struct {
		name, body string
		wantStatus int
		wantReason string
	}{
		{"empty body", "", http.StatusBadRequest, invalid},
		{"not an object", `[1]`, http.StatusBadRequest, invalid},
		{"unknown key", `{"media_ids":` + one + `,"delta_seconds":60,"trip_id":"x"}`, http.StatusBadRequest, invalid},
		{"trailing data", `{"media_ids":` + one + `,"delta_seconds":60} x`, http.StatusBadRequest, invalid},
		{"media_ids not a list", `{"media_ids":"` + timeItemA + `","delta_seconds":60}`, http.StatusBadRequest, invalid},
		{"an ID not text", `{"media_ids":[1],"delta_seconds":60}`, http.StatusBadRequest, invalid},
		{"missing media_ids", `{"delta_seconds":60}`, http.StatusBadRequest, "media_ids must hold 1-5000 IDs"},
		{"empty media_ids", `{"media_ids":[],"delta_seconds":60}`, http.StatusBadRequest, "media_ids must hold 1-5000 IDs"},
		{"5001 IDs", `{"media_ids":` + ids(5001) + `,"delta_seconds":60}`, http.StatusBadRequest, "media_ids must hold 1-5000 IDs"},
		{"a malformed ID", `{"media_ids":["` + timeItemA + `","not-a-uuid"],"delta_seconds":60}`, http.StatusBadRequest, "invalid media_ids"},
		{"missing delta", `{"media_ids":` + one + `}`, http.StatusBadRequest, "delta_seconds is required"},
		{"null delta", `{"media_ids":` + one + `,"delta_seconds":null}`, http.StatusBadRequest, "delta_seconds is required"},
		{"delta as text", `{"media_ids":` + one + `,"delta_seconds":"60"}`, http.StatusBadRequest, notWhole},
		{"delta a fraction", `{"media_ids":` + one + `,"delta_seconds":1.5}`, http.StatusBadRequest, notWhole},
		{"delta with a decimal point", `{"media_ids":` + one + `,"delta_seconds":60.0}`, http.StatusBadRequest, notWhole},
		{"delta with an exponent", `{"media_ids":` + one + `,"delta_seconds":6e1}`, http.StatusBadRequest, notWhole},
		{"delta a boolean", `{"media_ids":` + one + `,"delta_seconds":true}`, http.StatusBadRequest, notWhole},
		{"delta beyond int64", `{"media_ids":` + one + `,"delta_seconds":99999999999999999999}`, http.StatusBadRequest, notWhole},
		{"delta 0", `{"media_ids":` + one + `,"delta_seconds":0}`, http.StatusBadRequest, "delta_seconds must not be 0"},
		{"delta -0", `{"media_ids":` + one + `,"delta_seconds":-0}`, http.StatusBadRequest, "delta_seconds must not be 0"},
		{"beyond the cap later", `{"media_ids":` + one + `,"delta_seconds":3155760001}`, http.StatusBadRequest, beyond},
		{"beyond the cap earlier", `{"media_ids":` + one + `,"delta_seconds":-3155760001}`, http.StatusBadRequest, beyond},
		{"over 1 MiB", strings.Repeat(" ", maxShiftTimeBodyBytes) + `{"media_ids":` + one + `,"delta_seconds":60}`,
			http.StatusRequestEntityTooLarge, "request body too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &MediaHandler{pool: noDBPool{t}}
			w := httptest.NewRecorder()
			h.ShiftTimes(w, shiftRequest(tt.body))
			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, tt.wantReason+"\n", w.Body.String())
		})
	}
}

func TestValidateShiftTime_Accepted(t *testing.T) {
	tests := []struct {
		name    string
		in      shiftTimeInput
		wantIDs []string
		want    time.Duration
	}{
		{"the cap later", shiftTimeInput{[]string{timeItemA}, json.RawMessage(`3155760000`)}, []string{timeItemA}, 3_155_760_000 * time.Second},
		{"the cap earlier", shiftTimeInput{[]string{timeItemA}, json.RawMessage(`-3155760000`)}, []string{timeItemA}, -3_155_760_000 * time.Second},
		{"one second", shiftTimeInput{[]string{timeItemA}, json.RawMessage(`1`)}, []string{timeItemA}, time.Second},
		{"duplicates collapse, in any spelling", shiftTimeInput{
			[]string{timeItemA, timeItemB, strings.ToUpper(timeItemA), strings.ReplaceAll(timeItemB, "-", "")}, json.RawMessage(`-3600`)},
			[]string{timeItemA, timeItemB}, -time.Hour},
		{"5000 IDs", shiftTimeInput{func() []string {
			ids := make([]string, maxShiftMediaIDs)
			for i := range ids {
				ids[i] = timeItemA
			}
			return ids
		}(), json.RawMessage(`60`)}, []string{timeItemA}, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := validateShiftTime(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.wantIDs, v.ids)
			assert.Equal(t, tt.want, v.delta)
		})
	}
}

// The instant a wall clock names: in a zone with its daylight saving time, the earlier of the two
// readings a zone showed twice, at a fixed offset, and never in the process's own zone.
func TestValidateTakenAt_ResolvesTheWallClock(t *testing.T) {
	zone := func(wall, name string) takenAtInput { return takenAtInput{Wall: &wall, Zone: &name} }
	offset := func(wall string, min int) takenAtInput { return takenAtInput{Wall: &wall, UTCOffsetMin: &min} }
	tests := []struct {
		name       string
		in         takenAtInput
		wantAt     string
		wantOffset int
	}{
		{"Warsaw in summer", zone("2024-07-01T12:00:00", "Europe/Warsaw"), "2024-07-01T10:00:00Z", 120},
		{"Warsaw in winter", zone("2024-01-15T12:00:00", "Europe/Warsaw"), "2024-01-15T11:00:00Z", 60},
		{"Warsaw shows 02:30 twice: the earlier", zone("2024-10-27T02:30:00", "Europe/Warsaw"), "2024-10-27T00:30:00Z", 120},
		{"Warsaw just after the clocks went back", zone("2024-10-27T03:00:00", "Europe/Warsaw"), "2024-10-27T02:00:00Z", 60},
		{"Warsaw just after the clocks went forward", zone("2024-03-31T03:00:00", "Europe/Warsaw"), "2024-03-31T01:00:00Z", 120},
		{"New York shows 01:30 twice: the earlier", zone("2024-11-03T01:30:00", "America/New_York"), "2024-11-03T05:30:00Z", -240},
		{"Lord Howe's half-hour fall back: the earlier", zone("2024-04-07T01:45:00", "Australia/Lord_Howe"), "2024-04-06T14:45:00Z", 660},
		{"Kathmandu", zone("2024-05-01T12:00:00", "Asia/Kathmandu"), "2024-05-01T06:15:00Z", 345},
		{"UTC", zone("2024-05-01T12:00:00", "UTC"), "2024-05-01T12:00:00Z", 0},
		{"Warsaw's mean time in year 1", zone("0001-06-01T12:00:00", "Europe/Warsaw"), "0001-06-01T10:36:00Z", 84},
		{"a fixed +05:45", offset("2024-05-01T12:00:00", 345), "2024-05-01T06:15:00Z", 345},
		{"a fixed -03:30", offset("2024-05-01T12:00:00", -210), "2024-05-01T15:30:00Z", -210},
		{"a fixed +14:00", offset("2024-05-01T12:00:00", 840), "2024-04-30T22:00:00Z", 840},
		{"a fixed -12:00 on the last day", offset("9998-12-31T11:00:00", -720), "9998-12-31T23:00:00Z", -720},
		{"a fixed offset ignores the zone's clocks", offset("2024-03-31T02:30:00", 60), "2024-03-31T01:30:00Z", 60},
	}
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	for _, local := range []string{"UTC", "Pacific/Chatham", "America/St_Johns", "Europe/Warsaw"} {
		loc, err := time.LoadLocation(local)
		require.NoError(t, err)
		time.Local = loc
		for _, tt := range tests {
			t.Run(local+"/"+tt.name, func(t *testing.T) {
				v, err := validateTakenAt(tt.in)
				require.NoError(t, err)
				assert.Equal(t, tt.wantAt, v.at.UTC().Format(time.RFC3339))
				assert.Equal(t, tt.wantOffset, v.offsetMin)
				assert.Equal(t, *tt.in.Wall, v.wall)
				assert.Equal(t, tt.in.Zone, v.zone)
			})
		}
	}
}

func TestZoneOffsetsNear(t *testing.T) {
	warsaw := mustLoadLocation(t, "Europe/Warsaw")
	tests := []struct {
		name string
		at   string
		loc  *time.Location
		want []int
	}{
		{"across a change", "2024-03-31T02:30:00Z", warsaw, []int{3600, 7200}},
		{"no change within two days", "2024-06-01T12:00:00Z", warsaw, []int{7200}},
		{"a zone without changes", "2024-06-01T12:00:00Z", time.UTC, []int{0}},
		{"a fixed zone", "2024-06-01T12:00:00Z", time.FixedZone("x", 345*60), []int{345 * 60}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, zoneOffsetsNear(utcAt(t, tt.at), tt.loc))
		})
	}
}

func TestNewTimeEdit(t *testing.T) {
	now := time.Date(2026, 11, 15, 19, 0, 0, 0, time.FixedZone("CET", 3600))
	from := utcAt(t, "2019-03-13T18:28:15Z")
	to := utcAt(t, "2019-03-13T17:28:15Z")
	yearOne := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	lastYear := time.Date(9998, 12, 31, 23, 59, 59, 500_000_000, time.UTC)
	tests := []struct {
		name         string
		from         *time.Time
		to           time.Time
		prev         []byte
		wantFrom     *string
		wantOriginal *string
		wantDelta    *float64
	}{
		{"a first change", &from, to, nil, ptr("2019-03-13T18:28:15Z"), ptr("2019-03-13T18:28:15Z"), ptr(-3600.0)},
		{"a later change keeps the original", &from, to, []byte(`{"v":1,"original":"2019-03-13T20:28:15Z"}`),
			ptr("2019-03-13T18:28:15Z"), ptr("2019-03-13T20:28:15Z"), ptr(-3600.0)},
		{"an original of null is kept", &from, to, []byte(`{"v":1,"original":null}`),
			ptr("2019-03-13T18:28:15Z"), nil, ptr(-3600.0)},
		{"an undated item", nil, to, nil, nil, nil, nil},
		{"an unreadable stored edit", &from, to, []byte(`{"original":5}`),
			ptr("2019-03-13T18:28:15Z"), ptr("2019-03-13T18:28:15Z"), ptr(-3600.0)},
		{"across the millennia", &yearOne, lastYear, nil, ptr("0001-01-01T00:00:00Z"), ptr("0001-01-01T00:00:00Z"),
			ptr(float64(lastYear.Unix()-yearOne.Unix()) + 0.5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTimeEdit(timeEditShift, now, tt.from, tt.to, tt.prev)
			assert.Equal(t, timeEdit{
				V: 1, Kind: "shift", At: "2026-11-15T18:00:00Z", From: tt.wantFrom, Original: tt.wantOriginal,
				DeltaS: tt.wantDelta,
			}, e)
		})
	}

	t.Run("every key is always present", func(t *testing.T) {
		b, err := json.Marshal(newTimeEdit(timeEditShift, now, nil, to, nil))
		require.NoError(t, err)
		assert.JSONEq(t, `{"v":1,"kind":"shift","at":"2026-11-15T18:00:00Z","from":null,"original":null,
			"delta_s":null,"wall":null,"zone":null,"offset_min":null}`, string(b))
	})
}

// A malformed ID is 404 with the body unread, and a body is checked before any database work.
func TestCaptureTimeRoutes_CheckOrder(t *testing.T) {
	t.Run("malformed ID before the body", func(t *testing.T) {
		body := &countingReader{r: strings.NewReader(validTakenAtBody)}
		r := takenAtRequest("not-a-uuid", "")
		r.Body = io.NopCloser(body)
		h := &MediaHandler{pool: noDBPool{t}}
		w := httptest.NewRecorder()
		h.SetTakenAt(w, r)
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Zero(t, body.reads, "the body was read")
	})
	t.Run("a refused body never reaches the database", func(t *testing.T) {
		h := &MediaHandler{pool: noDBPool{t}}
		w := httptest.NewRecorder()
		h.SetTakenAt(w, takenAtRequest(timeItemA, `{"wall":"2024-05-01T12:00:00"}`))
		assert.Equal(t, http.StatusBadRequest, w.Code)

		w = httptest.NewRecorder()
		h.ShiftTimes(w, shiftRequest(`{"media_ids":["`+timeItemA+`"],"delta_seconds":0}`))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestSetTakenAt_NotFound(t *testing.T) {
	tx, execs := recordingTimeTx(timeEditRow{}, pgx.ErrNoRows, "", nil)
	h := &MediaHandler{pool: &timePool{tx: tx}}
	w := httptest.NewRecorder()
	h.SetTakenAt(w, takenAtRequest(timeItemA, validTakenAtBody))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, afterOwnerLock(t, *execs), "nothing but the owner lock")
}

func TestCaptureTimeRoutes_DatabaseErrors(t *testing.T) {
	stored := utcAt(t, "2024-05-01T08:00:00Z")
	row := timeEditRow{id: timeItemA, takenAt: &stored, mediaType: "photo"}
	dbErr := errors.New("connection reset")
	shiftBody := `{"media_ids":["` + timeItemA + `"],"delta_seconds":3600}`
	tests := []struct {
		name    string
		shift   bool
		pool    func() poolBeginner
		wantLog string
	}{
		{"set: begin", false, func() poolBeginner { return &timePool{err: dbErr} }, "media: set taken_at begin tx failed"},
		{"set: owner lock", false, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "FROM users", dbErr)
			return &timePool{tx: tx}
		}, "media: set taken_at owner lock failed"},
		{"set: lookup", false, func() poolBeginner {
			tx, _ := recordingTimeTx(row, dbErr, "", nil)
			return &timePool{tx: tx}
		}, "media: set taken_at lookup failed"},
		{"set: update", false, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "UPDATE media", dbErr)
			return &timePool{tx: tx}
		}, "media: set taken_at failed"},
		{"set: commit", false, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "", nil)
			tx.commitErr = dbErr
			return &timePool{tx: tx}
		}, "media: set taken_at commit failed"},
		{"shift: begin", true, func() poolBeginner { return &timePool{err: dbErr} }, "media: shift times begin tx failed"},
		{"shift: owner lock", true, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "FROM users", dbErr)
			return &timePool{tx: tx}
		}, "media: shift times owner lock failed"},
		{"shift: lookup", true, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "", nil)
			tx.queryFn = func(context.Context, string, ...any) (pgx.Rows, error) { return nil, dbErr }
			return &timePool{tx: tx}
		}, "media: shift times lookup failed"},
		{"shift: rows", true, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "", nil)
			tx.queryFn = func(context.Context, string, ...any) (pgx.Rows, error) { return &mockRows{err: dbErr}, nil }
			return &timePool{tx: tx}
		}, "media: shift times rows failed"},
		{"shift: update", true, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "UPDATE media", dbErr)
			return &timePool{tx: tx}
		}, "media: shift times failed"},
		{"shift: commit", true, func() poolBeginner {
			tx, _ := recordingTimeTx(row, nil, "", nil)
			tx.commitErr = dbErr
			return &timePool{tx: tx}
		}, "media: shift times commit failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			var f followup
			h := &MediaHandler{pool: tt.pool(), runMatcherAround: f.matcher(nil), runTripDetect: f.trips(nil)}
			w := httptest.NewRecorder()
			if tt.shift {
				h.ShiftTimes(w, shiftRequest(shiftBody))
			} else {
				h.SetTakenAt(w, takenAtRequest(timeItemA, validTakenAtBody))
			}
			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.Contains(t, logs.String(), "level=ERROR msg=\""+tt.wantLog+"\"")
			assert.Contains(t, logs.String(), "connection reset")
			assert.Zero(t, f.matcherCalls+f.tripCalls, "nothing runs after a failed change")
		})
	}
}

// A set records what it did in time_edit, and the follow-up runs after the commit on a context the
// request's end does not cancel, around the old and the new time.
func TestSetTakenAt_RecordsTheEditAndRunsTheFollowup(t *testing.T) {
	stored := utcAt(t, "2024-05-01T08:00:00Z")
	row := timeEditRow{id: timeItemA, takenAt: &stored, mediaType: "photo", tripID: ptr("old-trip"),
		timeEdit: []byte(`{"v":1,"kind":"shift","original":"2024-05-01T09:00:00Z"}`)}
	tx, execs := recordingTimeTx(row, nil, "", nil)
	var f followup
	h := &MediaHandler{
		pool: &timePool{tx: tx}, db: tripRow(ptr("new-trip"), nil), matchWindow: time.Hour, tripGapHours: 24,
		runMatcherAround: f.matcher(nil), runTripDetect: f.trips(nil),
	}

	r := takenAtRequest(timeItemA, `{"wall":"2024-05-01T12:00:00","zone":"Europe/Warsaw"}`)
	ctx, cancel := context.WithCancel(r.Context())
	cancel() // the client went away: the follow-up must still run
	before := time.Now()
	w := httptest.NewRecorder()
	h.SetTakenAt(w, r.WithContext(ctx))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"taken_at":"2024-05-01T10:00:00Z","trip_id":"new-trip","followup_failed":false}`, w.Body.String())

	require.Len(t, tx.queryRows, 1, "an unlocated photo has no point to look up")
	lookup := tx.queryRows[0]
	assert.Contains(t, lookup.sql, "FROM media WHERE id = $1 AND owner_id = $2")
	assert.Contains(t, lookup.sql, "FOR UPDATE", "the row stays as read until the commit")
	assert.Equal(t, []any{timeItemA, testOwnerID}, lookup.args)

	writes := afterOwnerLock(t, *execs)
	require.NotEmpty(t, writes)
	update := writes[0]
	assert.Contains(t, update.sql, "UPDATE media")
	assert.Contains(t, update.sql, "WHERE id = $1 AND owner_id = $2")
	require.Len(t, update.args, 4)
	assert.Equal(t, timeItemA, update.args[0])
	assert.Equal(t, testOwnerID, update.args[1])
	assert.True(t, utcAt(t, "2024-05-01T10:00:00Z").Equal(update.args[2].(time.Time)))
	var edit map[string]any
	require.NoError(t, json.Unmarshal(update.args[3].([]byte), &edit))
	at := utcAt(t, edit["at"].(string))
	assert.False(t, at.Before(before.Truncate(time.Second)), "at is when the change was made")
	assert.False(t, at.After(time.Now()))
	delete(edit, "at")
	assert.Equal(t, map[string]any{
		"v": 1.0, "kind": "set", "from": "2024-05-01T08:00:00Z", "original": "2024-05-01T09:00:00Z",
		"delta_s": 7200.0, "wall": "2024-05-01T12:00:00", "zone": "Europe/Warsaw", "offset_min": 120.0,
	}, edit)
	assert.Len(t, writes, 1, "an unlocated photo has no point to move")

	assert.Equal(t, 1, f.matcherCalls)
	assert.Equal(t, testOwnerID, f.owner)
	assert.Equal(t, time.Hour, f.window)
	assert.Equal(t, []time.Time{utcAt(t, "2024-05-01T10:00:00Z"), stored}, f.around)
	assert.NoError(t, f.ctxErr, "the follow-up's context outlives the request")
	assert.InDelta(t, timeEditFollowupTimeout.Seconds(), f.deadline.Seconds(), 5)
	assert.Equal(t, 1, f.tripCalls)
	assert.Equal(t, 24, f.gapHours)
}

// The route takes whole seconds, and the dialog shows a stored fraction (a file date in
// milliseconds) without it: a time in the stored time's second is the stored time, and the answer
// gives the stored value.
func TestSetTakenAt_SameInstantWritesNothing(t *testing.T) {
	tests := []struct {
		name, stored string
	}{
		{"the same instant", "2024-05-01T10:00:00Z"},
		{"a stored fraction of that second", "2024-05-01T10:00:00.437Z"},
		{"the last microsecond of that second", "2024-05-01T10:00:00.999999Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stored, err := time.Parse(time.RFC3339Nano, tt.stored)
			require.NoError(t, err)
			row := timeEditRow{id: timeItemA, takenAt: &stored, mediaType: "photo", tripID: ptr("trip-1")}
			tx, execs := recordingTimeTx(row, nil, "", nil)
			var f followup
			h := &MediaHandler{pool: &timePool{tx: tx}, runMatcherAround: f.matcher(nil), runTripDetect: f.trips(nil)}

			w := httptest.NewRecorder()
			h.SetTakenAt(w, takenAtRequest(timeItemA, `{"wall":"2024-05-01T11:30:00","utc_offset_min":90}`))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.JSONEq(t, `{"taken_at":"`+tt.stored+`","trip_id":"trip-1","followup_failed":false}`, w.Body.String())
			assert.Empty(t, afterOwnerLock(t, *execs), "nothing written")
			assert.Zero(t, f.matcherCalls+f.tripCalls)
		})
	}

	t.Run("the next second is a change", func(t *testing.T) {
		stored := utcAt(t, "2024-05-01T09:59:59Z").Add(999 * time.Millisecond)
		row := timeEditRow{id: timeItemA, takenAt: &stored, mediaType: "photo"}
		tx, execs := recordingTimeTx(row, nil, "", nil)
		var f followup
		h := &MediaHandler{pool: &timePool{tx: tx}, db: tripRow(nil, nil),
			runMatcherAround: f.matcher(nil), runTripDetect: f.trips(nil)}

		w := httptest.NewRecorder()
		h.SetTakenAt(w, takenAtRequest(timeItemA, `{"wall":"2024-05-01T11:30:00","utc_offset_min":90}`))

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.JSONEq(t, `{"taken_at":"2024-05-01T10:00:00Z","trip_id":null,"followup_failed":false}`, w.Body.String())
		assert.Len(t, afterOwnerLock(t, *execs), 1, "the update")
		assert.Equal(t, 1, f.matcherCalls)
	})
}

// The change is committed before the follow-up runs, so its failure is a 200 that says so: a 500
// would invite a retry, and a retried shift moves the items again.
func TestCaptureTimeRoutes_FollowupFailure(t *testing.T) {
	stored := utcAt(t, "2024-05-01T08:00:00Z")
	row := timeEditRow{id: timeItemA, takenAt: &stored, mediaType: "photo"}
	followupErr := errors.New("matcher down")
	tests := []struct {
		name                string
		matcherErr, tripErr error
		lookupErr           error
		wantLog             string
		wantTripCalls       int
	}{
		{"matcher", followupErr, nil, nil, "media: GPS matcher failed after a capture time change", 0},
		{"trip detection", nil, followupErr, nil, "media: trip detect failed after a capture time change", 1},
		{"trip lookup", nil, nil, followupErr, "trip lookup failed after a capture time", 1},
	}
	for _, tt := range tests {
		t.Run("set: "+tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			tx, _ := recordingTimeTx(row, nil, "", nil)
			var f followup
			h := &MediaHandler{pool: &timePool{tx: tx}, db: tripRow(ptr("trip-1"), tt.lookupErr),
				runMatcherAround: f.matcher(tt.matcherErr), runTripDetect: f.trips(tt.tripErr)}
			w := httptest.NewRecorder()
			h.SetTakenAt(w, takenAtRequest(timeItemA, validTakenAtBody))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp setTakenAtResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.True(t, resp.FollowupFailed)
			assert.Equal(t, "2024-05-01T10:00:00Z", resp.TakenAt)
			assert.Contains(t, logs.String(), "level=ERROR")
			assert.Contains(t, logs.String(), tt.wantLog)
			assert.Equal(t, tt.wantTripCalls, f.tripCalls)
		})
		t.Run("shift: "+tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			tx, _ := recordingTimeTx(row, nil, "", nil)
			var f followup
			db := &mockDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) {
				if tt.lookupErr != nil {
					return nil, tt.lookupErr
				}
				return &mockRows{rows: [][]any{{"trip-1", 1}}}, nil
			}}
			h := &MediaHandler{pool: &timePool{tx: tx}, db: db,
				runMatcherAround: f.matcher(tt.matcherErr), runTripDetect: f.trips(tt.tripErr)}
			w := httptest.NewRecorder()
			h.ShiftTimes(w, shiftRequest(`{"media_ids":["`+timeItemA+`"],"delta_seconds":60}`))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			var resp shiftTimesResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.True(t, resp.FollowupFailed)
			assert.Equal(t, 1, resp.Shifted)
			assert.NotNil(t, resp.Trips, "trips is never null")
			assert.Contains(t, logs.String(), "level=ERROR")
			assert.Contains(t, logs.String(), tt.wantLog)
		})
	}
}

func TestShiftTimes_Unit(t *testing.T) {
	stored := utcAt(t, "2024-05-01T08:00:00Z")
	lastDay := utcAt(t, "9998-12-31T12:00:00Z")
	dated := func(id string, at time.Time) []any { return []any{id, at, "photo", "", nil, nil, nil, nil} }
	undated := func(id string) []any { return []any{id, nil, "photo", "", nil, nil, nil, nil} }
	tests := []struct {
		name       string
		body       string
		rows       [][]any
		wantStatus int
		wantBody   string
		wantExecs  int // after the owner lock
		wantAround []time.Time
	}{
		{"an ID that is not the caller's", `{"media_ids":["` + timeItemA + `","` + timeItemB + `"],"delta_seconds":60}`,
			[][]any{dated(timeItemA, stored)}, http.StatusNotFound, "not found\n", 0, nil},
		{"out of range", `{"media_ids":["` + timeItemA + `","` + timeItemB + `"],"delta_seconds":86400}`,
			[][]any{dated(timeItemA, stored), dated(timeItemB, lastDay)}, http.StatusBadRequest,
			"the shift would move a capture time out of the years 1-9998\n", 0, nil},
		{"only undated items", `{"media_ids":["` + timeItemA + `"],"delta_seconds":60}`,
			[][]any{undated(timeItemA)}, http.StatusOK,
			`{"shifted":0,"undated":1,"trips":[],"followup_failed":false}` + "\n", 0, nil},
		{"dated and undated", `{"media_ids":["` + timeItemA + `","` + timeItemB + `"],"delta_seconds":-60}`,
			[][]any{dated(timeItemA, stored), undated(timeItemB)}, http.StatusOK,
			`{"shifted":1,"undated":1,"trips":[{"id":"trip-1","items":1}],"followup_failed":false}` + "\n", 1,
			[]time.Time{stored, stored.Add(-time.Minute)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lockSQL string
			var lockArgs []any
			tx, execs := recordingTimeTx(timeEditRow{}, nil, "", nil)
			tx.queryFn = func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
				lockSQL, lockArgs = sql, args
				return &mockRows{rows: tt.rows}, nil
			}
			var f followup
			db := &mockDB{queryFn: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
				assert.Contains(t, sql, "WHERE owner_id = $1 AND id = ANY($2) AND trip_id IS NOT NULL")
				assert.Equal(t, []any{testOwnerID, []string{timeItemA}}, args, "the shifted items only")
				return &mockRows{rows: [][]any{{"trip-1", 1}}}, nil
			}}
			h := &MediaHandler{pool: &timePool{tx: tx}, db: db, matchWindow: time.Hour, tripGapHours: 24,
				runMatcherAround: f.matcher(nil), runTripDetect: f.trips(nil)}
			r := shiftRequest(tt.body)
			ctx, cancel := context.WithCancel(r.Context())
			cancel() // the client went away: the follow-up must still run
			w := httptest.NewRecorder()
			h.ShiftTimes(w, r.WithContext(ctx))

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, tt.wantBody, w.Body.String())
			assert.Len(t, afterOwnerLock(t, *execs), tt.wantExecs)
			assert.Contains(t, lockSQL, "FROM media WHERE owner_id = $1 AND id = ANY($2)")
			assert.Contains(t, lockSQL, "ORDER BY id")
			assert.Contains(t, lockSQL, "FOR UPDATE")
			assert.Equal(t, testOwnerID, lockArgs[0])

			if tt.wantAround == nil {
				assert.Zero(t, f.matcherCalls+f.tripCalls, "nothing moved, nothing runs")
				return
			}
			assert.Equal(t, 1, f.matcherCalls)
			assert.Equal(t, 1, f.tripCalls)
			assert.Equal(t, testOwnerID, f.owner)
			assert.Equal(t, time.Hour, f.window)
			assert.Equal(t, tt.wantAround, f.around, "every old and new instant is placed again")
			assert.NoError(t, f.ctxErr, "the follow-up's context outlives the request")
			assert.InDelta(t, timeEditFollowupTimeout.Seconds(), f.deadline.Seconds(), 5)
			assert.Equal(t, 24, f.gapHours)
		})
	}

	t.Run("several items: each old and new instant, in the order they move", func(t *testing.T) {
		later := stored.Add(90 * time.Minute)
		tx, _ := recordingTimeTx(timeEditRow{}, nil, "", nil)
		tx.queryFn = func(context.Context, string, ...any) (pgx.Rows, error) {
			return &mockRows{rows: [][]any{dated(timeItemA, stored), dated(timeItemB, later)}}, nil
		}
		var f followup
		db := &mockDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) { return &mockRows{}, nil }}
		h := &MediaHandler{pool: &timePool{tx: tx}, db: db, matchWindow: time.Hour,
			runMatcherAround: f.matcher(nil), runTripDetect: f.trips(nil)}
		w := httptest.NewRecorder()
		h.ShiftTimes(w, shiftRequest(`{"media_ids":["`+timeItemA+`","`+timeItemB+`"],"delta_seconds":3600}`))

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, []time.Time{later, later.Add(time.Hour), stored, stored.Add(time.Hour)}, f.around,
			"latest first for a shift later")
	})
}

// deadlockErr is what Postgres reports to the transaction it aborts to break a deadlock.
var deadlockErr = &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}

// sequencePool hands out txs in turn, one per Begin, and counts the Begins.
type sequencePool struct {
	txs    []pgx.Tx
	begins int
}

func (p *sequencePool) Begin(context.Context) (pgx.Tx, error) {
	p.begins++
	return p.txs[p.begins-1], nil
}

// A transaction Postgres aborted as a deadlock victim (40P01) committed nothing, so it runs again
// from Begin, up to three times in all. Any other error is a 500 at once.
func TestCaptureTimeRoutes_RetryADeadlockVictim(t *testing.T) {
	stored := utcAt(t, "2024-05-01T08:00:00Z")
	row := timeEditRow{id: timeItemA, takenAt: &stored, mediaType: "photo"}
	otherErr := &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
	failing := func(err error) pgx.Tx {
		tx, _ := recordingTimeTx(row, nil, "UPDATE media", err)
		return tx
	}
	succeeding := func() pgx.Tx {
		tx, _ := recordingTimeTx(row, nil, "", nil)
		return tx
	}
	tests := []struct {
		name       string
		txs        []pgx.Tx
		wantStatus int
		wantBegins int
	}{
		{"one deadlock, then success", []pgx.Tx{failing(deadlockErr), succeeding()}, http.StatusOK, 2},
		{"two deadlocks, then success", []pgx.Tx{failing(deadlockErr), failing(deadlockErr), succeeding()}, http.StatusOK, 3},
		{"a deadlock every time: three attempts", []pgx.Tx{failing(deadlockErr), failing(deadlockErr), failing(deadlockErr),
			succeeding()}, http.StatusInternalServerError, 3},
		{"another database error: no retry", []pgx.Tx{failing(otherErr), succeeding()}, http.StatusInternalServerError, 1},
		{"a deadlock, then another error: no more retries", []pgx.Tx{failing(deadlockErr), failing(otherErr), succeeding()},
			http.StatusInternalServerError, 2},
	}
	for _, route := range []string{"set", "shift"} {
		for _, tt := range tests {
			t.Run(route+": "+tt.name, func(t *testing.T) {
				logs := captureLogs(t)
				pool := &sequencePool{txs: tt.txs}
				var f followup
				db := &mockDB{
					queryRowFn: tripRow(nil, nil).queryRowFn,
					queryFn:    func(context.Context, string, ...any) (pgx.Rows, error) { return &mockRows{}, nil },
				}
				h := &MediaHandler{pool: pool, db: db, runMatcherAround: f.matcher(nil), runTripDetect: f.trips(nil)}
				w := httptest.NewRecorder()
				if route == "set" {
					h.SetTakenAt(w, takenAtRequest(timeItemA, validTakenAtBody))
				} else {
					h.ShiftTimes(w, shiftRequest(`{"media_ids":["`+timeItemA+`"],"delta_seconds":60}`))
				}

				assert.Equal(t, tt.wantStatus, w.Code, w.Body.String())
				assert.Equal(t, tt.wantBegins, pool.begins)
				if tt.wantStatus == http.StatusOK {
					assert.Equal(t, 1, f.matcherCalls, "the follow-up runs once, after the commit")
					assert.Contains(t, logs.String(), "level=WARN")
					assert.Contains(t, logs.String(), "deadlock victim")
					assert.NotContains(t, logs.String(), "level=ERROR")
				} else {
					assert.Zero(t, f.matcherCalls+f.tripCalls)
					assert.Contains(t, logs.String(), "level=ERROR")
				}
			})
		}
	}
}
