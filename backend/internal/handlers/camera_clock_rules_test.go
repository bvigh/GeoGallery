package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateClockRule(t *testing.T) {
	valid := func() clockRuleInput {
		return clockRuleInput{Make: "Panasonic", Model: "DC-TZ200", UTCOffsetMin: ptr(120)}
	}
	with := func(change func(*clockRuleInput)) clockRuleInput {
		in := valid()
		change(&in)
		return in
	}
	base := clockRuleValues{make: "Panasonic", model: "DC-TZ200", offsetMin: 120}
	want := func(change func(*clockRuleValues)) clockRuleValues {
		v := base
		change(&v)
		return v
	}
	long := strings.Repeat("é", maxClockRuleCameraRunes) // 200 runes, 400 bytes

	tests := []struct {
		name    string
		in      clockRuleInput
		want    clockRuleValues
		wantErr string
	}{
		{"minimal rule", valid(), base, ""},

		// Make and model are stored as ingest.CleanTag leaves them, the form Match compares in.
		{"make and model trimmed", with(func(in *clockRuleInput) { in.Make, in.Model = "  Panasonic\t", "\nDC-TZ200 " }), base, ""},
		{"no-break spaces trimmed", with(func(in *clockRuleInput) { in.Make = " Panasonic " }), base, ""},
		{"NUL and U+FFFD removed", with(func(in *clockRuleInput) { in.Make, in.Model = "Pana\x00sonic", "�DC-TZ200" }), base, ""},
		{"case kept as entered", with(func(in *clockRuleInput) { in.Make = "PANASONIC" }),
			want(func(v *clockRuleValues) { v.make = "PANASONIC" }), ""},
		{"inner space kept", with(func(in *clockRuleInput) { in.Make = "Arashi  Vision" }),
			want(func(v *clockRuleValues) { v.make = "Arashi  Vision" }), ""},
		{"200 runes", with(func(in *clockRuleInput) { in.Model = long }),
			want(func(v *clockRuleValues) { v.model = long }), ""},
		{"200 runes inside space", with(func(in *clockRuleInput) { in.Model = "  " + long + "  " }),
			want(func(v *clockRuleValues) { v.model = long }), ""},
		{"201 runes refused, not cut", with(func(in *clockRuleInput) { in.Model = long + "x" }), clockRuleValues{}, "model must be 1-200 characters"},
		{"empty make", with(func(in *clockRuleInput) { in.Make = "" }), clockRuleValues{}, "make must be 1-200 characters"},
		{"blank make", with(func(in *clockRuleInput) { in.Make = " \t " }), clockRuleValues{}, "make must be 1-200 characters"},
		{"make of NUL only", with(func(in *clockRuleInput) { in.Make = "\x00\x00" }), clockRuleValues{}, "make must be 1-200 characters"},
		{"empty model", with(func(in *clockRuleInput) { in.Model = "" }), clockRuleValues{}, "model must be 1-200 characters"},
		{"model of U+FFFD only", with(func(in *clockRuleInput) { in.Model = "�" }), clockRuleValues{}, "model must be 1-200 characters"},

		{"offset missing", with(func(in *clockRuleInput) { in.UTCOffsetMin = nil }), clockRuleValues{}, "utc_offset_min is required"},
		{"UTC", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(0) }),
			want(func(v *clockRuleValues) { v.offsetMin = 0 }), ""},
		{"UTC-12:00", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(-720) }),
			want(func(v *clockRuleValues) { v.offsetMin = -720 }), ""},
		{"UTC+14:00", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(840) }),
			want(func(v *clockRuleValues) { v.offsetMin = 840 }), ""},
		{"UTC+05:45", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(345) }),
			want(func(v *clockRuleValues) { v.offsetMin = 345 }), ""},
		{"UTC-03:30", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(-210) }),
			want(func(v *clockRuleValues) { v.offsetMin = -210 }), ""},
		{"below UTC-12:00", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(-735) }), clockRuleValues{},
			"utc_offset_min must be a multiple of 15 from -720 to 840"},
		{"above UTC+14:00", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(855) }), clockRuleValues{},
			"utc_offset_min must be a multiple of 15 from -720 to 840"},
		{"not a quarter hour", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(50) }), clockRuleValues{},
			"utc_offset_min must be a multiple of 15 from -720 to 840"},
		{"seconds, not minutes", with(func(in *clockRuleInput) { in.UTCOffsetMin = ptr(7200) }), clockRuleValues{},
			"utc_offset_min must be a multiple of 15 from -720 to 840"},

		{"both days", with(func(in *clockRuleInput) { in.StartsOn, in.EndsOn = ptr("2022-06-01"), ptr("2022-12-31") }),
			want(func(v *clockRuleValues) { v.startsOn, v.endsOn = ptr("2022-06-01"), ptr("2022-12-31") }), ""},
		{"one day", with(func(in *clockRuleInput) { in.StartsOn, in.EndsOn = ptr("2022-06-01"), ptr("2022-06-01") }),
			want(func(v *clockRuleValues) { v.startsOn, v.endsOn = ptr("2022-06-01"), ptr("2022-06-01") }), ""},
		{"start only", with(func(in *clockRuleInput) { in.StartsOn = ptr("2018-07-01") }),
			want(func(v *clockRuleValues) { v.startsOn = ptr("2018-07-01") }), ""},
		{"end only", with(func(in *clockRuleInput) { in.EndsOn = ptr("2018-06-30") }),
			want(func(v *clockRuleValues) { v.endsOn = ptr("2018-06-30") }), ""},
		{"first year", with(func(in *clockRuleInput) { in.StartsOn = ptr("1900-01-01") }),
			want(func(v *clockRuleValues) { v.startsOn = ptr("1900-01-01") }), ""},
		{"last year", with(func(in *clockRuleInput) { in.EndsOn = ptr("9998-12-31") }),
			want(func(v *clockRuleValues) { v.endsOn = ptr("9998-12-31") }), ""},
		{"start after end", with(func(in *clockRuleInput) { in.StartsOn, in.EndsOn = ptr("2023-01-01"), ptr("2022-12-31") }),
			clockRuleValues{}, "starts_on must not be after ends_on"},

		{"note trimmed", with(func(in *clockRuleInput) { in.Note = "  left on summer time \n" }),
			want(func(v *clockRuleValues) { v.note = "left on summer time" }), ""},
		{"500-rune note", with(func(in *clockRuleInput) { in.Note = strings.Repeat("ő", 500) }),
			want(func(v *clockRuleValues) { v.note = strings.Repeat("ő", 500) }), ""},
		{"501-rune note", with(func(in *clockRuleInput) { in.Note = strings.Repeat("ő", 501) }), clockRuleValues{},
			"note must be at most 500 characters"},
		{"note with NUL", with(func(in *clockRuleInput) { in.Note = "a\x00b" }), clockRuleValues{},
			"note must not contain NUL characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateClockRule(tt.in)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				assert.Equal(t, clockRuleValues{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	// A day is a real date written exactly YYYY-MM-DD in 1900-9998; anything else is refused, on
	// either end.
	for _, day := range []string{
		"", "2022-6-1", "2022-06-1", "22-06-01", "2022-02-30", "2022-13-01", "2022-06-01T00:00:00Z",
		" 2022-06-01", "2022-06-01 ", "2022/06/01", "1899-12-31", "9999-01-01", "+2022-06-01", "0000-01-01",
	} {
		t.Run("day "+fmt.Sprintf("%q", day), func(t *testing.T) {
			_, err := validateClockRule(with(func(in *clockRuleInput) { in.StartsOn = ptr(day) }))
			require.EqualError(t, err, "starts_on must be a date written YYYY-MM-DD in the years 1900-9998, or null")
			_, err = validateClockRule(with(func(in *clockRuleInput) { in.EndsOn = ptr(day) }))
			require.EqualError(t, err, "ends_on must be a date written YYYY-MM-DD in the years 1900-9998, or null")
		})
	}
}

func TestDecodeClockRule(t *testing.T) {
	const good = `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":120,"starts_on":"2018-07-01","ends_on":null,"note":"summer time"}`
	tests := []struct {
		name       string
		body       string
		wantStatus int // 0: decoded
		wantBody   string
	}{
		{"full body", good, 0, ""},
		{"note and days omitted", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":120}`, 0, ""},
		{"note null", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":120,"note":null}`, 0, ""},
		{"trailing space", good + " \n", 0, ""},
		{"unknown key", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":120,"owner_id":"x"}`, http.StatusBadRequest, "invalid body"},
		{"id in the body", `{"id":"6f1c1d1e-0000-4000-8000-000000000001","make":"Panasonic","model":"DC-TZ200","utc_offset_min":120}`,
			http.StatusBadRequest, "invalid body"},
		{"a second object", good + good, http.StatusBadRequest, "invalid body"},
		{"trailing text", good + " x", http.StatusBadRequest, "invalid body"},
		{"empty body", ``, http.StatusBadRequest, "invalid body"},
		{"an array", `[` + good + `]`, http.StatusBadRequest, "invalid body"},
		{"offset as text", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":"120"}`, http.StatusBadRequest, "invalid body"},
		{"fractional offset", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":60.5}`, http.StatusBadRequest, "invalid body"},
		{"day as a number", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":60,"starts_on":20180701}`, http.StatusBadRequest, "invalid body"},
		{"null body", `null`, http.StatusBadRequest, "make must be 1-200 characters"},
		{"invalid rule", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":61}`, http.StatusBadRequest,
			"utc_offset_min must be a multiple of 15 from -720 to 840"},
		{"over 16 KiB", `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":60,"note":"` + strings.Repeat("a", maxClockRuleBodyBytes) + `"}`,
			http.StatusRequestEntityTooLarge, "request body too large"},
		{"16 KiB of trailing space", good + strings.Repeat(" ", maxClockRuleBodyBytes), http.StatusRequestEntityTooLarge, "request body too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/camera-clock-rules", strings.NewReader(tt.body))
			_, ok := decodeClockRule(rec, req)
			if tt.wantStatus == 0 {
				assert.True(t, ok, rec.Body.String())
				assert.Equal(t, http.StatusOK, rec.Code, "nothing written")
				return
			}
			assert.False(t, ok)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}

	t.Run("the decoded values", func(t *testing.T) {
		rec := httptest.NewRecorder()
		v, ok := decodeClockRule(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(good)))
		require.True(t, ok)
		assert.Equal(t, clockRuleValues{make: "Panasonic", model: "DC-TZ200", offsetMin: 120,
			startsOn: ptr("2018-07-01"), note: "summer time"}, v)
	})
}

func TestClockRuleWriteError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		handled    bool
		wantStatus int
		wantBody   string
	}{
		{"overlap", &pgconn.PgError{Code: "23P01", ConstraintName: "camera_clock_rules_no_overlap"}, true,
			http.StatusConflict, "overlaps another rule for this camera"},
		{"wrapped overlap", fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23P01"}), true,
			http.StatusConflict, "overlaps another rule for this camera"},
		{"check", &pgconn.PgError{Code: "23514", ConstraintName: "camera_clock_rules_make_check"}, true,
			http.StatusBadRequest, "the rule is not valid (camera_clock_rules_make_check)"},
		{"period check", &pgconn.PgError{Code: "23514", ConstraintName: "camera_clock_rules_period_check"}, true,
			http.StatusBadRequest, "camera_clock_rules_period_check"},
		{"foreign key", &pgconn.PgError{Code: "23503"}, false, 0, ""},
		{"not a PostgreSQL error", errors.New("connection reset"), false, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			assert.Equal(t, tt.handled, clockRuleWriteError(rec, tt.err))
			if !tt.handled {
				assert.Empty(t, rec.Body.String(), "nothing written")
				return
			}
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

// Path and body checks answer before the handler touches the database: this handler has none, and
// would panic if it did.
func TestCameraClockRuleHandler_ChecksBeforeTheDatabase(t *testing.T) {
	h := NewCameraClockRuleHandler(nil)
	r := chi.NewRouter()
	r.Post("/api/camera-clock-rules", h.Create)
	r.Put("/api/camera-clock-rules/{id}", h.Update)
	r.Delete("/api/camera-clock-rules/{id}", h.Delete)

	const (
		ruleID = "6f1c1d1e-0000-4000-8000-000000000001"
		good   = `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":120}`
	)
	tests := []struct {
		name, method, path, body string
		wantStatus               int
	}{
		{"create, invalid body", http.MethodPost, "/api/camera-clock-rules", `{"make":""}`, http.StatusBadRequest},
		{"create, unknown key", http.MethodPost, "/api/camera-clock-rules", `{"owner_id":"x"}`, http.StatusBadRequest},
		{"update, malformed id", http.MethodPut, "/api/camera-clock-rules/not-a-uuid", good, http.StatusNotFound},
		{"update, malformed id and body", http.MethodPut, "/api/camera-clock-rules/not-a-uuid", `{}`, http.StatusNotFound},
		{"update, invalid body", http.MethodPut, "/api/camera-clock-rules/" + ruleID, `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":7}`, http.StatusBadRequest},
		{"delete, malformed id", http.MethodDelete, "/api/camera-clock-rules/not-a-uuid", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, withTestUser(httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))))
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}
}

func TestGroupCameras(t *testing.T) {
	at := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return &v
	}
	warsaw := mustLoadLocation(t, "Europe/Warsaw")
	inWarsaw := time.Date(2019, 3, 13, 19, 28, 15, 0, warsaw)
	beyond := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		spellings []cameraSpelling
		want      []libraryCamera
	}{
		{"none", nil, []libraryCamera{}},
		{"one camera", []cameraSpelling{
			{make: "Panasonic", model: "DC-TZ200", items: 3, first: at("2019-01-01T10:00:00Z"), last: at("2022-05-01T10:00:00Z")},
		}, []libraryCamera{
			{Make: "Panasonic", Model: "DC-TZ200", Items: 3, FirstTakenAt: at("2019-01-01T10:00:00Z"), LastTakenAt: at("2022-05-01T10:00:00Z")},
		}},
		{"spellings Match takes for one camera are one, shown as the most frequent", []cameraSpelling{
			{make: "PANASONIC", model: "dc-tz200", items: 2, first: at("2018-01-01T00:00:00Z"), last: at("2018-02-01T00:00:00Z")},
			{make: "Panasonic", model: "DC-TZ200", items: 5, first: at("2019-01-01T00:00:00Z"), last: at("2023-01-01T00:00:00Z")},
			{make: " Panasonic\x00", model: "DC-TZ200\t", items: 1, first: nil, last: nil},
		}, []libraryCamera{
			{Make: "Panasonic", Model: "DC-TZ200", Items: 8, FirstTakenAt: at("2018-01-01T00:00:00Z"), LastTakenAt: at("2023-01-01T00:00:00Z")},
		}},
		{"a tie goes to the first spelling in byte order", []cameraSpelling{
			{make: "Panasonic", model: "DC-TZ200", items: 2},
			{make: "PANASONIC", model: "DC-TZ200", items: 2},
		}, []libraryCamera{{Make: "PANASONIC", Model: "DC-TZ200", Items: 4}}},
		{"another model is another camera", []cameraSpelling{
			{make: "Panasonic", model: "DC-TZ200", items: 1},
			{make: "Panasonic", model: "DC-TZ202", items: 1},
		}, []libraryCamera{
			{Make: "Panasonic", Model: "DC-TZ200", Items: 1},
			{Make: "Panasonic", Model: "DC-TZ202", Items: 1},
		}},
		{"most items first, then make and model without case", []cameraSpelling{
			{make: "samsung", model: "SM-S921B", items: 2},
			{make: "Arashi Vision", model: "Insta360 OneRS", items: 2},
			{make: "Apple", model: "iPhone 12", items: 9},
			{make: "arashi vision", model: "Insta360 One X2", items: 2},
		}, []libraryCamera{
			{Make: "Apple", Model: "iPhone 12", Items: 9},
			{Make: "arashi vision", Model: "Insta360 One X2", Items: 2},
			{Make: "Arashi Vision", Model: "Insta360 OneRS", Items: 2},
			{Make: "samsung", Model: "SM-S921B", Items: 2},
		}},
		{"nothing left after CleanTag: no rule could name it", []cameraSpelling{
			{make: " ", model: "DC-TZ200", items: 4},
			{make: "Panasonic", model: "\x00", items: 4},
			{make: "Canon", model: "EOS R6", items: 1},
		}, []libraryCamera{{Make: "Canon", Model: "EOS R6", Items: 1}}},
		{"times in UTC", []cameraSpelling{
			{make: "Panasonic", model: "DC-TZ200", items: 1, first: &inWarsaw, last: &inWarsaw},
		}, []libraryCamera{
			{Make: "Panasonic", Model: "DC-TZ200", Items: 1, FirstTakenAt: at("2019-03-13T18:28:15Z"), LastTakenAt: at("2019-03-13T18:28:15Z")},
		}},
		{"a year JSON cannot carry is null", []cameraSpelling{
			{make: "Panasonic", model: "DC-TZ200", items: 2, first: &before, last: &beyond},
		}, []libraryCamera{{Make: "Panasonic", Model: "DC-TZ200", Items: 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := groupCameras(tt.spellings)
			assert.Equal(t, tt.want, got)
			for _, c := range got {
				for _, ts := range []*time.Time{c.FirstTakenAt, c.LastTakenAt} {
					if ts != nil {
						assert.Equal(t, time.UTC, ts.Location())
					}
				}
			}
			body, err := json.Marshal(got)
			require.NoError(t, err, "always encodable")
			if len(tt.spellings) == 0 {
				assert.JSONEq(t, `[]`, string(body))
			}
		})
	}
}
