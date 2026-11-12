package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/ingest"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlacementState(t *testing.T) {
	source := func(s string) *string { return &s }
	closeBy, farAway := ptr(0.5), ptr(250.0)
	onEdge := ptr(1.0)
	const conf = 0.8

	tests := []struct {
		name  string
		facts placementFacts
		want  string
	}{
		{"manual wins over everything", placementFacts{GPSSource: source("manual"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: farAway}, placementManual},
		{"manual with no time", placementFacts{GPSSource: source("manual"), HasLocation: true}, placementManual},
		{"own position with a bracket", placementFacts{GPSSource: source("exif"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: farAway}, placementOwnPosition},
		{"own position with no time", placementFacts{GPSSource: source("exif"), HasLocation: true}, placementOwnPosition},
		{"no time, null source", placementFacts{}, placementNoTime},
		{"no time keeps an estimate unexplained", placementFacts{GPSSource: source("interpolated"), HasLocation: true}, placementNoTime},
		{"pending", placementFacts{HasTime: true, HasBracket: true}, placementPending},
		{"no bracket", placementFacts{HasTime: true}, placementNoBracket},
		{"no support", placementFacts{GPSSource: source("interpolated"), HasTime: true, HasLocation: true}, placementNoSupport},
		{"current", placementFacts{GPSSource: source("interpolated"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: closeBy, StoredConfidence: ptr(conf), NowConfidence: conf}, placementCurrent},
		{"current on both tolerances", placementFacts{GPSSource: source("interpolated"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: onEdge, StoredConfidence: ptr(0.8005), NowConfidence: conf}, placementCurrent},
		{"stale: moved", placementFacts{GPSSource: source("interpolated"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: farAway, StoredConfidence: ptr(conf), NowConfidence: conf}, placementStale},
		{"stale: only just moved", placementFacts{GPSSource: source("interpolated"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: ptr(1.01), StoredConfidence: ptr(conf), NowConfidence: conf}, placementStale},
		{"stale: confidence changed", placementFacts{GPSSource: source("interpolated"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: closeBy, StoredConfidence: ptr(0.75), NowConfidence: conf}, placementStale},
		{"stale: no stored confidence", placementFacts{GPSSource: source("interpolated"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: closeBy, NowConfidence: conf}, placementStale},

		// gps_source "none" reads as null: only the location and the bracket decide.
		{"none with a location and no time", placementFacts{GPSSource: source("none"), HasLocation: true}, placementNoTime},
		{"none, pending", placementFacts{GPSSource: source("none"), HasTime: true, HasBracket: true}, placementPending},
		{"none, no bracket", placementFacts{GPSSource: source("none"), HasTime: true}, placementNoBracket},
		{"none, no support", placementFacts{GPSSource: source("none"), HasTime: true, HasLocation: true}, placementNoSupport},
		{"none, stale", placementFacts{GPSSource: source("none"), HasTime: true, HasLocation: true, HasBracket: true, DeltaM: closeBy, NowConfidence: conf}, placementStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, placementState(tt.facts))
		})
	}
}

func TestPlacementWarnings(t *testing.T) {
	log := func(source string) *provenanceBracket {
		return &provenanceBracket{Source: source, Format: trackFormat(source), Kind: bracketKindLog}
	}
	derived := func(source, kind, origin string) *provenanceBracket {
		return &provenanceBracket{Source: source, Format: trackFormat(source), Kind: kind, Origin: &provenanceOrigin{State: origin}}
	}
	twoSided := func(before, after *provenanceBracket, gapS, km, kmh, confidence float64) *provenanceEstimate {
		return &provenanceEstimate{Confidence: confidence, Before: before, After: after, Arithmetic: provenanceArithmetic{
			Shape: string(geo.ShapeTwoSided), PairGapS: &gapS, PairKm: &km, ImpliedKmh: &kmh, Confidence: confidence,
		}}
	}
	gpx, nmea := log("gpx:a.gpx"), log("nmea:b.nmea")
	impossible := provenanceWarning{Code: warningPairImpossible, Level: warningLevelWarn}
	far := provenanceWarning{Code: warningPairFar, Level: warningLevelWarn}
	parked := provenanceWarning{Code: warningLoggerParked, Level: warningLevelInfo}
	zero := provenanceWarning{Code: warningConfidenceZero, Level: warningLevelInfo}
	orphan := provenanceWarning{Code: warningPointOrphan, Level: warningLevelWarn}

	tests := []struct {
		name string
		now  *provenanceEstimate
		want []provenanceWarning
	}{
		{"no estimate", nil, []provenanceWarning{}},
		{"quiet pair", twoSided(gpx, nmea, 60, 0.5, 30, 0.9), []provenanceWarning{}},
		{"impossible across formats", twoSided(gpx, nmea, 60, 5, 300, 0.9), []provenanceWarning{impossible}},
		{"impossible, exactly at the speed limit is not", twoSided(gpx, nmea, 60, 2.5, 150, 0.9), []provenanceWarning{far}},
		{"impossible needs the distance floor", twoSided(gpx, nmea, 1, 0.1, 360, 0.9), []provenanceWarning{}},
		{"impossible excludes far", twoSided(gpx, nmea, 60, 80, 4800, 0.9), []provenanceWarning{impossible}},
		{"same format is never impossible", twoSided(gpx, log("gpx:c.gpx"), 60, 5, 300, 0.9), []provenanceWarning{}},
		{"far across formats", twoSided(gpx, nmea, 3600, 2, 2, 0.9), []provenanceWarning{far}},
		{"far needs over a kilometre", twoSided(gpx, nmea, 3600, 1, 1, 0.9), []provenanceWarning{}},
		{"same format is never far", twoSided(gpx, log("gpx:c.gpx"), 3600, 50, 50, 0.9), []provenanceWarning{}},
		{"two unknown prefixes are two formats", twoSided(log("strava:a"), log("komoot:b"), 60, 5, 300, 0.9), []provenanceWarning{impossible}},
		{"parked", twoSided(gpx, log("gpx:a.gpx"), 600, 0.1, 0.6, 0.9), []provenanceWarning{parked}},
		{"parked at the 120 s floor", twoSided(gpx, log("gpx:a.gpx"), 120, 0.01, 0.3, 0.9), []provenanceWarning{parked}},
		{"below the 120 s floor", twoSided(gpx, log("gpx:a.gpx"), 119, 0.01, 0.3, 0.9), []provenanceWarning{}},
		{"two labels are two devices", twoSided(gpx, log("gpx:c.gpx"), 600, 0.1, 0.6, 0.9), []provenanceWarning{}},
		{"parked needs under 2 km/h", twoSided(gpx, log("gpx:a.gpx"), 600, 1, 6, 0.9), []provenanceWarning{}},
		{"derived points cannot be parked", twoSided(derived("exif", bracketKindExif, originLinked), derived("exif", bracketKindExif, originLinked), 600, 0.1, 0.6, 0.9), []provenanceWarning{}},
		{"confidence zero", twoSided(gpx, log("gpx:c.gpx"), 3600, 0.5, 0.5, 0), []provenanceWarning{zero}},
		{
			"confidence zero on one side",
			&provenanceEstimate{Confidence: 0, Before: gpx, Arithmetic: provenanceArithmetic{Shape: string(geo.ShapeOneSided)}},
			[]provenanceWarning{zero},
		},
		{"orphan before", twoSided(derived("exif", bracketKindExif, originOrphan), derived("exif", bracketKindExif, originLinked), 60, 0.01, 0.6, 0.9), []provenanceWarning{orphan}},
		{"orphan after", twoSided(gpx, derived("manual", bracketKindManual, originOrphan), 60, 0.01, 0.6, 0.9), []provenanceWarning{orphan}},
		{"both orphans warn once", twoSided(derived("exif", bracketKindExif, originOrphan), derived("manual", bracketKindManual, originOrphan), 60, 0.01, 0.6, 0.9), []provenanceWarning{orphan}},
		{"a linked point does not warn", twoSided(derived("exif", bracketKindExif, originLinked), gpx, 60, 0.01, 0.6, 0.9), []provenanceWarning{}},
		{"several at once, in table order", twoSided(gpx, derived("manual", bracketKindManual, originOrphan), 3600, 5, 400, 0), []provenanceWarning{impossible, zero, orphan}},
		{
			"exact never hits the pair checks",
			&provenanceEstimate{Confidence: 1, Before: gpx, After: gpx, Arithmetic: provenanceArithmetic{Shape: string(geo.ShapeExact)}},
			[]provenanceWarning{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := placementWarnings(tt.now)
			assert.NotNil(t, got, "the API promises an array")
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNewArithmetic(t *testing.T) {
	at := time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)
	before := &geo.Point{Lat: 10, Lng: 20, At: at.Add(-time.Minute)}
	after := &geo.Point{Lat: 10.01, Lng: 20, At: at.Add(time.Minute)}

	t.Run("one_sided has no pair", func(t *testing.T) {
		a := newArithmetic(geo.Explain(before, nil, at, time.Hour))
		assert.Equal(t, "one_sided", a.Shape)
		assert.Nil(t, a.PairGapS)
		assert.Nil(t, a.PairKm)
		assert.Nil(t, a.T)
		assert.Nil(t, a.DistanceFactor)
		assert.Nil(t, a.ImpliedKmh)
	})
	t.Run("exact has a pair of zero and no position along it", func(t *testing.T) {
		exact := &geo.Point{Lat: 10, Lng: 20, At: at}
		a := newArithmetic(geo.Explain(exact, exact, at, time.Hour))
		assert.Equal(t, "exact", a.Shape)
		assert.Equal(t, ptr(0.0), a.PairGapS)
		assert.Equal(t, ptr(0.0), a.PairKm)
		assert.Nil(t, a.T)
		assert.Equal(t, ptr(1.0), a.DistanceFactor)
		assert.Nil(t, a.ImpliedKmh)
	})
	t.Run("two_sided has everything", func(t *testing.T) {
		a := newArithmetic(geo.Explain(before, after, at, time.Hour))
		assert.Equal(t, "two_sided", a.Shape)
		assert.Equal(t, ptr(120.0), a.PairGapS)
		assert.NotNil(t, a.PairKm)
		assert.Equal(t, ptr(0.5), a.T)
		assert.NotNil(t, a.DistanceFactor)
		assert.NotNil(t, a.ImpliedKmh)
	})
}

func TestProvenanceTimeFacts(t *testing.T) {
	zone := ingest.CameraZone{Loc: time.FixedZone("Test/Zone", 330*60), Set: true}

	tests := []struct {
		name         string
		zone         ingest.CameraZone
		evidence     string
		wantRecorded string
		wantEvidence bool
		wantName     string
		wantSet      bool
		wantFallback bool
		wantOffset   int
	}{
		{"not recorded", zone, "", "not_recorded", false, "Test/Zone", true, false, 330},
		{"ingest", zone, `{"v":1,"by":"ingest"}`, "ingest", true, "Test/Zone", true, false, 330},
		{"backfill", zone, `{"v":1,"by":"backfill"}`, "backfill", true, "Test/Zone", true, false, 330},
		{"an unknown writer is not trusted", zone, `{"v":1,"by":"someone"}`, "not_recorded", false, "Test/Zone", true, false, 330},
		{"no by key", zone, `{"v":1}`, "not_recorded", false, "Test/Zone", true, false, 330},
		{"a zero zone reads as UTC", ingest.CameraZone{}, "", "not_recorded", false, "server zone (unset)", false, false, 0},
		{"unset on a UTC server", ingest.CameraZone{Loc: time.UTC, UTCFallback: true}, "", "not_recorded", false, "server zone (unset)", false, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &ProvenanceHandler{cameraZone: tt.zone}

			var evidence []byte
			if tt.evidence != "" {
				evidence = []byte(tt.evidence)
			}
			got := h.timeFacts(evidence)

			assert.Equal(t, tt.wantRecorded, got.Recorded)
			if tt.wantEvidence {
				assert.JSONEq(t, tt.evidence, string(got.Evidence))
			} else {
				encoded, err := json.Marshal(got.Evidence)
				assert.NoError(t, err)
				assert.Equal(t, "null", string(encoded))
			}
			assert.Equal(t, tt.wantName, got.ServerCameraZone.Name)
			assert.Equal(t, tt.wantSet, got.ServerCameraZone.Set)
			assert.Equal(t, tt.wantFallback, got.ServerCameraZone.UTCFallback)
			assert.Equal(t, tt.wantOffset, got.ServerCameraZone.OffsetNowMin)
		})
	}
}

// provenanceRequest is a GET for one id, as the router would hand it to the handler.
func provenanceRequest(id string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/media/x/provenance", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	return withTestUser(r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx)))
}

type scanFunc func(dest ...any) error

func (f scanFunc) Scan(dest ...any) error { return f(dest...) }

func TestProvenanceGet_Errors(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	takenAt := time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)
	boom := errors.New("boom")
	item := scanFunc(func(dest ...any) error {
		*dest[0].(*string) = id
		*dest[4].(*time.Time) = takenAt
		*dest[3].(**time.Time) = &takenAt
		return nil
	})
	empty := func(context.Context, string, ...any) (pgx.Rows, error) { return &mockRows{}, nil }

	tests := []struct {
		name     string
		id       string
		queryRow func(call int) pgx.Row
		query    func(call int) (pgx.Rows, error)
		want     int
	}{
		{"a malformed id never reaches the database", "not-a-uuid", nil, nil, http.StatusNotFound},
		{"no such item for this owner", id, func(int) pgx.Row { return noRowsRow{} }, nil, http.StatusNotFound},
		{"the item query fails", id, func(int) pgx.Row { return errRow{boom} }, nil, http.StatusInternalServerError},
		{"the bracket query fails", id, func(call int) pgx.Row {
			if call == 1 {
				return item
			}
			return errRow{boom}
		}, nil, http.StatusInternalServerError},
		{"the formats query fails", id, func(call int) pgx.Row {
			if call == 1 {
				return item
			}
			return noRowsRow{}
		}, func(int) (pgx.Rows, error) { return nil, boom }, http.StatusInternalServerError},
		// The item, then two bracket lookups each for the estimate, the independent estimate and the
		// nearest points outside the window, before the format's own bracket.
		{"a format's bracket query fails", id, func(call int) pgx.Row {
			switch {
			case call == 1:
				return item
			case call <= 7:
				return noRowsRow{}
			}
			return errRow{boom}
		}, func(call int) (pgx.Rows, error) {
			if call == 1 {
				return &mockRows{rows: [][]any{{"gpx"}}}, nil
			}
			return &mockRows{}, nil
		}, http.StatusInternalServerError},
		{"the track query fails", id, func(call int) pgx.Row {
			if call == 1 {
				return item
			}
			return noRowsRow{}
		}, func(call int) (pgx.Rows, error) {
			if call == 1 {
				return &mockRows{}, nil
			}
			return nil, boom
		}, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rowCalls, queryCalls := 0, 0
			db := &mockMatcherDB{
				queryRowFn: func(context.Context, string, ...any) pgx.Row {
					rowCalls++
					require.NotNil(t, tt.queryRow, "no query expected")
					return tt.queryRow(rowCalls)
				},
				queryFn: empty,
			}
			if tt.query != nil {
				db.queryFn = func(context.Context, string, ...any) (pgx.Rows, error) {
					queryCalls++
					return tt.query(queryCalls)
				}
			}
			rec := httptest.NewRecorder()

			(&ProvenanceHandler{db: db, matchWindow: time.Hour}).Get(rec, provenanceRequest(tt.id))

			assert.Equal(t, tt.want, rec.Code)
			assert.NotContains(t, rec.Body.String(), "boom", "a database error stays in the log")
		})
	}
}

func TestProvenanceGet_MissingUserPanics(t *testing.T) {
	h := &ProvenanceHandler{matchWindow: time.Hour}
	r := httptest.NewRequest(http.MethodGet, "/api/media/x/provenance", nil)

	assert.Panics(t, func() { h.Get(httptest.NewRecorder(), r) }, "an unscoped request is a loud 500, never a query")
}

func TestLoadTrack_ErrorsAreReturned(t *testing.T) {
	boom := errors.New("boom")
	window := []timeWindow{{from: time.Date(2001, 2, 3, 11, 0, 0, 0, time.UTC), to: time.Date(2001, 2, 3, 13, 0, 0, 0, time.UTC)}}

	tests := []struct {
		name string
		rows func(call int) (pgx.Rows, error)
	}{
		{"count query", func(int) (pgx.Rows, error) { return nil, boom }},
		{"count rows", func(int) (pgx.Rows, error) { return &mockRows{err: boom}, nil }},
		{"log points", func(call int) (pgx.Rows, error) {
			if call == 1 {
				return &mockRows{}, nil
			}
			return nil, boom
		}},
		{"derived points", func(call int) (pgx.Rows, error) {
			if call < 3 {
				return &mockRows{}, nil
			}
			return nil, boom
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			db := &mockMatcherDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) {
				calls++
				return tt.rows(calls)
			}}

			_, err := loadTrack(context.Background(), db, testOwnerID, window, 10)

			require.ErrorIs(t, err, boom)
		})
	}
}

func TestNewOtherFormat(t *testing.T) {
	takenAt := time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)
	point := func(source string, offset time.Duration, lat, lng float64) *geo.Point {
		return &geo.Point{Source: source, At: takenAt.Add(offset), Lat: lat, Lng: lng}
	}
	before := point("gpx:a.gpx", -2*time.Minute, 10, 20)
	after := point("gpx:a.gpx", 3*time.Minute, 10.01, 20)
	reference := &position{lat: 10, lng: 20.01}
	facts := otherFormatFacts{takenAt: takenAt, window: time.Hour, reference: reference}

	t.Run("a point on each side is this format's own estimate", func(t *testing.T) {
		row, ok := newOtherFormat("gpx", before, after, facts)

		require.True(t, ok)
		want := geo.Explain(before, after, takenAt, time.Hour)
		assert.Equal(t, "gpx", row.Format)
		assert.Equal(t, string(geo.ShapeTwoSided), row.Alone.Shape)
		assert.InDelta(t, want.Lat, row.Alone.Lat, 1e-12)
		assert.InDelta(t, want.Lng, row.Alone.Lng, 1e-12)
		assert.InDelta(t, want.Confidence, row.Alone.Confidence, 1e-12)
		assert.InDelta(t, geo.HaversineKm(10, 20.01, want.Lat, want.Lng)*1000, *row.Alone.DistanceM, 1e-6)
		require.NotNil(t, row.Before)
		require.NotNil(t, row.After)
		assert.Equal(t, 120.0, row.Before.GapS)
		assert.Equal(t, 180.0, row.After.GapS)
		assert.InDelta(t, geo.HaversineKm(10, 20.01, 10, 20)*1000, *row.Before.DistanceM, 1e-6)
		assert.False(t, row.Used)
		assert.Equal(t, time.UTC, row.Before.T.Location())
	})
	t.Run("one side only", func(t *testing.T) {
		row, ok := newOtherFormat("gpx", nil, after, facts)

		require.True(t, ok)
		assert.Nil(t, row.Before)
		assert.Equal(t, string(geo.ShapeOneSided), row.Alone.Shape)
		assert.Equal(t, after.Lat, row.Alone.Lat)
	})
	t.Run("a point at the exact time is both sides", func(t *testing.T) {
		exact := point("exif", 0, 10, 20)
		row, ok := newOtherFormat("exif", exact, exact, facts)

		require.True(t, ok)
		assert.Equal(t, string(geo.ShapeExact), row.Alone.Shape)
		assert.Equal(t, row.Before, row.After)
	})
	t.Run("no point is no row", func(t *testing.T) {
		_, ok := newOtherFormat("gpx", nil, nil, facts)
		assert.False(t, ok)
	})
	t.Run("points of the estimate are marked used", func(t *testing.T) {
		withNow := facts
		withNow.now = &provenanceEstimate{
			Before: newBracket(before, takenAt),
			After:  newBracket(point("nmea:b.nmea", time.Minute, 10, 20), takenAt),
		}
		row, ok := newOtherFormat("gpx", before, after, withNow)

		require.True(t, ok)
		assert.True(t, row.Before.Used)
		assert.False(t, row.After.Used, "the estimate's after point is another format's")
		assert.True(t, row.Used)
	})
}

func TestNewOtherFormat_TheItemsOwnPoint(t *testing.T) {
	takenAt := time.Date(2001, 2, 3, 12, 0, 0, 0, time.UTC)
	own := &geo.Point{Source: "exif", At: takenAt, Lat: 10, Lng: 20}
	item := func(source string, lat float64) provenanceItem {
		lng := 20.0
		return provenanceItem{GPSSource: &source, Lat: &lat, Lng: &lng}
	}
	tests := []struct {
		name  string
		item  provenanceItem
		point *geo.Point
		want  bool
	}{
		{"an own position's point", item("exif", 10), own, true},
		{"a pin's point", item("manual", 10), &geo.Point{Source: "manual", At: takenAt, Lat: 10, Lng: 20}, true},
		{"an estimate never wrote one", item("interpolated", 10), own, false},
		{"another photo in the same second, elsewhere", item("exif", 10.001), own, false},
		{"another format at the same place and time", item("exif", 10), &geo.Point{Source: "gpx:a.gpx", At: takenAt, Lat: 10, Lng: 20}, false},
		{"a point a second off", item("exif", 10), &geo.Point{Source: "exif", At: takenAt.Add(time.Second), Lat: 10, Lng: 20}, false},
		{"an item with no position", provenanceItem{GPSSource: ptr("exif")}, own, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row, ok := newOtherFormat(sourcePrefix(tt.point.Source), tt.point, tt.point,
				otherFormatFacts{takenAt: takenAt, window: time.Hour, item: tt.item})

			require.True(t, ok)
			assert.Equal(t, tt.want, row.Before.Self)
		})
	}
}

func TestCompareOtherFormats(t *testing.T) {
	side := func(gapS float64) *provenanceOtherPoint { return &provenanceOtherPoint{GapS: gapS} }
	rows := []provenanceOtherFormat{
		{Format: "one-sided-near", After: side(5)},
		{Format: "wide", Before: side(10), After: side(600)},
		{Format: "one-sided-far", Before: side(900)},
		{Format: "b-tight", Before: side(60), After: side(30)},
		{Format: "a-tight", Before: side(60), After: side(60)},
		{Format: "exact", Before: side(0), After: side(0)},
	}

	slices.SortFunc(rows, compareOtherFormats)

	var got []string
	for _, row := range rows {
		got = append(got, row.Format)
	}
	assert.Equal(t, []string{"exact", "a-tight", "b-tight", "wide", "one-sided-near", "one-sided-far"}, got,
		"both sides first, by the longer gap, then one side by its gap; the prefix breaks ties")
}
