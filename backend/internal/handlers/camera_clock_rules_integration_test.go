package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/auth"
	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clockRulesAPI is a database with one user and the routes of newOwnershipRouter, which include the
// camera clock rule routes.
type clockRulesAPI struct {
	pool *pgxpool.Pool
	api  http.Handler
	user string
}

func newClockRulesAPI(t *testing.T) *clockRulesAPI {
	t.Helper()
	pool := testdb.New(t)
	return &clockRulesAPI{pool: pool, api: newOwnershipRouter(pool, &fakeObjectStore{}), user: createUser(t, pool, "clocks")}
}

// ruleBody is a POST or PUT body. Nil days are sent as null; an empty note is left out.
func ruleBody(t *testing.T, cameraMake, model string, offsetMin int, startsOn, endsOn *string, note string) string {
	t.Helper()
	body := map[string]any{"make": cameraMake, "model": model, "utc_offset_min": offsetMin, "starts_on": startsOn, "ends_on": endsOn}
	if note != "" {
		body["note"] = note
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	return string(b)
}

func decodeRuleItem(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) clockRuleItem {
	t.Helper()
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	var rule clockRuleItem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rule), rec.Body.String())
	return rule
}

func countClockRules(t *testing.T, pool *pgxpool.Pool, ownerID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM camera_clock_rules WHERE owner_id = $1`, ownerID).Scan(&n))
	return n
}

// storedClockRule is one camera_clock_rules row as the database holds it.
type storedClockRule struct {
	Owner, Make, Model   string
	OffsetMin            int
	StartsOn, EndsOn     *string
	Note                 string
	CreatedAt, UpdatedAt time.Time
}

func clockRuleRow(t *testing.T, pool *pgxpool.Pool, id string) storedClockRule {
	t.Helper()
	var r storedClockRule
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT owner_id::text, make, model, utc_offset_min, starts_on::text, ends_on::text, note, created_at, updated_at
		FROM camera_clock_rules WHERE id = $1`, id).Scan(
		&r.Owner, &r.Make, &r.Model, &r.OffsetMin, &r.StartsOn, &r.EndsOn, &r.Note, &r.CreatedAt, &r.UpdatedAt))
	return r
}

func TestCameraClockRules_CreateListUpdateDelete(t *testing.T) {
	c := newClockRulesAPI(t)
	ctx := context.Background()
	start := time.Now()

	rec := do(t, c.api, c.user, http.MethodGet, "/api/camera-clock-rules", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `[]`, rec.Body.String(), "a list, never null")

	// Make and model are stored as ingest.CleanTag leaves them: the no-break space and the NUL go,
	// which btrim in the table's check would not do.
	rec = do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules",
		ruleBody(t, " Panasonic ", "DC-TZ200\x00\t", 120, ptr("2018-07-01"), nil, "  left on summer time  "))
	tz200 := decodeRuleItem(t, rec, http.StatusCreated)
	assert.Equal(t, []string{"created_at", "ends_on", "id", "make", "model", "note", "starts_on", "updated_at", "utc_offset_min"},
		jsonKeys(t, rec.Body.Bytes()))
	canonical, ok := auth.CanonicalUUID(tz200.ID)
	assert.True(t, ok && canonical == tz200.ID, "a canonical UUID: %s", tz200.ID)
	assert.Equal(t, clockRuleItem{ID: tz200.ID, Make: "Panasonic", Model: "DC-TZ200", UTCOffsetMin: 120,
		StartsOn: ptr("2018-07-01"), Note: "left on summer time", CreatedAt: tz200.CreatedAt, UpdatedAt: tz200.UpdatedAt}, tz200)
	assert.Equal(t, tz200.CreatedAt, tz200.UpdatedAt)
	assert.WithinDuration(t, start, tz200.CreatedAt, time.Minute)
	assertRFC3339UTC(t, rec.Body.Bytes(), "created_at", "updated_at")
	row := clockRuleRow(t, c.pool, tz200.ID)
	assert.Equal(t, storedClockRule{Owner: c.user, Make: "Panasonic", Model: "DC-TZ200", OffsetMin: 120,
		StartsOn: ptr("2018-07-01"), Note: "left on summer time", CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, row)
	assert.True(t, row.CreatedAt.Equal(tz200.CreatedAt), "created_at as stored")

	rec = do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules",
		ruleBody(t, "Arashi Vision", "Insta360 OneRS", 60, ptr("2022-06-01"), ptr("2022-09-30"), ""))
	oneRS := decodeRuleItem(t, rec, http.StatusCreated)
	assert.Contains(t, rec.Body.String(), `"note":""`, "the note is always a string")
	assert.Contains(t, rec.Body.String(), `"ends_on":"2022-09-30"`)
	rec = do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules",
		ruleBody(t, "panasonic", "dc-tz200", 60, nil, ptr("2018-06-30"), ""))
	tz200Before := decodeRuleItem(t, rec, http.StatusCreated)
	assert.Contains(t, rec.Body.String(), `"starts_on":null`)

	t.Run("listed in loadClockRules' order", func(t *testing.T) {
		rec := do(t, c.api, c.user, http.MethodGet, "/api/camera-clock-rules", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{oneRS.ID, tz200Before.ID, tz200.ID}, decodeIDs(t, rec))
		loaded, err := loadClockRules(ctx, c.pool, c.user)
		require.NoError(t, err)
		ids := make([]string, 0, len(loaded))
		for _, r := range loaded {
			ids = append(ids, r.ID)
		}
		assert.Equal(t, ids, decodeIDs(t, rec))
		var listed []clockRuleItem
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listed))
		assert.Equal(t, tz200, listed[2], "the list carries what POST returned")
	})

	t.Run("PUT replaces the whole rule", func(t *testing.T) {
		before := clockRuleRow(t, c.pool, oneRS.ID)
		rec := do(t, c.api, c.user, http.MethodPut, "/api/camera-clock-rules/"+strings.ToUpper(oneRS.ID),
			ruleBody(t, "Arashi Vision", "Insta360 ONE RS", -210, nil, ptr("2022-12-31"), ""))
		got := decodeRuleItem(t, rec, http.StatusOK)
		assert.Equal(t, clockRuleItem{ID: oneRS.ID, Make: "Arashi Vision", Model: "Insta360 ONE RS", UTCOffsetMin: -210,
			EndsOn: ptr("2022-12-31"), CreatedAt: oneRS.CreatedAt, UpdatedAt: got.UpdatedAt}, got,
			"the note left out is cleared, the start left null is open")
		assert.True(t, got.UpdatedAt.After(oneRS.UpdatedAt), "updated_at moves")
		assertRFC3339UTC(t, rec.Body.Bytes(), "created_at", "updated_at")
		after := clockRuleRow(t, c.pool, oneRS.ID)
		assert.Equal(t, before.CreatedAt, after.CreatedAt)
		assert.Nil(t, after.StartsOn)
		assert.Equal(t, "", after.Note)
	})

	t.Run("DELETE removes it", func(t *testing.T) {
		rec := do(t, c.api, c.user, http.MethodDelete, "/api/camera-clock-rules/"+oneRS.ID, "")
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.Empty(t, rec.Body.String())

		rec = do(t, c.api, c.user, http.MethodGet, "/api/camera-clock-rules", "")
		assert.Equal(t, []string{tz200Before.ID, tz200.ID}, decodeIDs(t, rec))

		rec = do(t, c.api, c.user, http.MethodDelete, "/api/camera-clock-rules/"+oneRS.ID, "")
		assert.Equal(t, http.StatusNotFound, rec.Code, "already gone")
		rec = do(t, c.api, c.user, http.MethodPut, "/api/camera-clock-rules/"+oneRS.ID,
			ruleBody(t, "Arashi Vision", "Insta360 OneRS", 60, nil, nil, ""))
		assert.Equal(t, http.StatusNotFound, rec.Code, "PUT never creates")
		assert.Equal(t, 2, countClockRules(t, c.pool, c.user))
	})
}

// The order of checks on PUT and DELETE: a malformed ID is 404, then the body (400), then the
// lookup (404), as in the album routes.
func TestCameraClockRules_UnknownAndMalformedIDs(t *testing.T) {
	c := newClockRulesAPI(t)
	valid := ruleBody(t, "Panasonic", "DC-TZ200", 120, nil, nil, "")
	const invalid = `{"make":"Panasonic","model":"DC-TZ200","utc_offset_min":7}`
	tests := []struct {
		name, method, path, body string
		wantStatus               int
	}{
		{"PUT unknown id", http.MethodPut, "/api/camera-clock-rules/" + unknownUUID, valid, http.StatusNotFound},
		{"PUT unknown id, invalid body", http.MethodPut, "/api/camera-clock-rules/" + unknownUUID, invalid, http.StatusBadRequest},
		{"PUT unknown id, unknown key", http.MethodPut, "/api/camera-clock-rules/" + unknownUUID, `{"id":"x"}`, http.StatusBadRequest},
		{"PUT malformed id", http.MethodPut, "/api/camera-clock-rules/not-a-uuid", valid, http.StatusNotFound},
		{"PUT malformed id, invalid body", http.MethodPut, "/api/camera-clock-rules/not-a-uuid", invalid, http.StatusNotFound},
		{"DELETE unknown id", http.MethodDelete, "/api/camera-clock-rules/" + unknownUUID, "", http.StatusNotFound},
		{"DELETE malformed id", http.MethodDelete, "/api/camera-clock-rules/1", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, c.api, c.user, tt.method, tt.path, tt.body)
			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}
	assert.Zero(t, countClockRules(t, c.pool, c.user))
}

// A rule is checked before anything is written, with a short reason.
func TestCameraClockRules_InvalidBodiesWriteNothing(t *testing.T) {
	c := newClockRulesAPI(t)
	existing := insertClockRule(t, c.pool, c.user, "Panasonic", "DC-TZ200", 120, nil, nil)
	before := clockRuleRow(t, c.pool, existing)

	tests := []struct {
		name, body, wantBody string
	}{
		{"no make", ruleBody(t, " ", "DC-TZ200", 120, nil, nil, ""), "make must be 1-200 characters"},
		{"model too long", ruleBody(t, "Panasonic", strings.Repeat("M", 201), 120, nil, nil, ""), "model must be 1-200 characters"},
		{"no offset", `{"make":"Panasonic","model":"DC-TZ202"}`, "utc_offset_min is required"},
		{"offset off the grid", ruleBody(t, "Panasonic", "DC-TZ202", 100, nil, nil, ""), "utc_offset_min must be a multiple of 15"},
		{"offset out of range", ruleBody(t, "Panasonic", "DC-TZ202", 900, nil, nil, ""), "utc_offset_min must be a multiple of 15"},
		{"loose date", ruleBody(t, "Panasonic", "DC-TZ202", 60, ptr("2022-6-1"), nil, ""), "starts_on must be a date"},
		{"impossible date", ruleBody(t, "Panasonic", "DC-TZ202", 60, nil, ptr("2023-02-29"), ""), "ends_on must be a date"},
		{"year out of range", ruleBody(t, "Panasonic", "DC-TZ202", 60, ptr("1899-12-31"), nil, ""), "starts_on must be a date"},
		{"period backwards", ruleBody(t, "Panasonic", "DC-TZ202", 60, ptr("2022-02-01"), ptr("2022-01-31"), ""), "starts_on must not be after ends_on"},
		{"note too long", ruleBody(t, "Panasonic", "DC-TZ202", 60, nil, nil, strings.Repeat("n", 501)), "note must be at most 500 characters"},
		{"note with NUL", ruleBody(t, "Panasonic", "DC-TZ202", 60, nil, nil, "a\x00b"), "note must not contain NUL"},
		{"unknown key", `{"make":"Panasonic","model":"DC-TZ202","utc_offset_min":60,"owner_id":"` + unknownUUID + `"}`, "invalid body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, req := range []struct{ method, path string }{
				{http.MethodPost, "/api/camera-clock-rules"},
				{http.MethodPut, "/api/camera-clock-rules/" + existing},
			} {
				rec := do(t, c.api, c.user, req.method, req.path, tt.body)
				assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", req.method, rec.Body.String())
				assert.Contains(t, rec.Body.String(), tt.wantBody, req.method)
			}
		})
	}
	assert.Equal(t, 1, countClockRules(t, c.pool, c.user))
	assert.Equal(t, before, clockRuleRow(t, c.pool, existing))
}

// One camera of one owner has one rule on any day: 409, compared without case and after CleanTag,
// on POST and on PUT. Adjacent periods, another model and another owner's camera do not overlap.
func TestCameraClockRules_Overlap(t *testing.T) {
	c := newClockRulesAPI(t)
	other := createUser(t, c.pool, "other-clocks")
	until2022 := insertClockRule(t, c.pool, c.user, "Panasonic", "DC-TZ200", 120, ptr("2018-07-01"), ptr("2022-12-31"))

	conflicts := []struct {
		name string
		body string
	}{
		{"the last day", ruleBody(t, "Panasonic", "DC-TZ200", 60, ptr("2022-12-31"), nil, "")},
		{"the first day", ruleBody(t, "Panasonic", "DC-TZ200", 60, nil, ptr("2018-07-01"), "")},
		{"inside", ruleBody(t, "Panasonic", "DC-TZ200", 60, ptr("2020-01-01"), ptr("2020-01-31"), "")},
		{"open both ways", ruleBody(t, "Panasonic", "DC-TZ200", 60, nil, nil, "")},
		{"another case", ruleBody(t, "PANASONIC", "dc-tz200", 60, ptr("2019-01-01"), nil, "")},
		{"spaces CleanTag removes", ruleBody(t, " Panasonic", "DC-TZ200\x00", 60, ptr("2019-01-01"), nil, "")},
	}
	for _, tt := range conflicts {
		t.Run("POST overlapping "+tt.name, func(t *testing.T) {
			rec := do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules", tt.body)
			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "overlaps another rule for this camera\n", rec.Body.String())
		})
	}
	assert.Equal(t, 1, countClockRules(t, c.pool, c.user), "no conflicting rule was written")

	rec := do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "Panasonic", "DC-TZ200", 60, ptr("2023-01-01"), nil, ""))
	from2023 := decodeRuleItem(t, rec, http.StatusCreated)
	rec = do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "Panasonic", "DC-TZ202", 60, nil, nil, ""))
	assert.Equal(t, http.StatusCreated, rec.Code, "another model: %s", rec.Body.String())
	rec = do(t, c.api, other, http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "Panasonic", "DC-TZ200", 60, nil, nil, ""))
	assert.Equal(t, http.StatusCreated, rec.Code, "another owner's camera: %s", rec.Body.String())

	t.Run("PUT into another rule's period", func(t *testing.T) {
		before := clockRuleRow(t, c.pool, from2023.ID)
		rec := do(t, c.api, c.user, http.MethodPut, "/api/camera-clock-rules/"+from2023.ID,
			ruleBody(t, "panasonic", "DC-TZ200", 60, ptr("2022-06-01"), nil, ""))
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, "overlaps another rule for this camera\n", rec.Body.String())
		assert.Equal(t, before, clockRuleRow(t, c.pool, from2023.ID))
	})

	t.Run("PUT within its own period is no overlap with itself", func(t *testing.T) {
		rec := do(t, c.api, c.user, http.MethodPut, "/api/camera-clock-rules/"+until2022,
			ruleBody(t, "Panasonic", "DC-TZ200", 120, ptr("2018-01-01"), ptr("2022-12-31"), "wider"))
		got := decodeRuleItem(t, rec, http.StatusOK)
		assert.Equal(t, ptr("2018-01-01"), got.StartsOn)
	})
}

// No owner has more than maxClockRulesPerOwner rules. PUT and DELETE still work at the cap, and
// another owner is not affected.
func TestCameraClockRules_Cap(t *testing.T) {
	c := newClockRulesAPI(t)
	other := createUser(t, c.pool, "other-clocks")
	var first string
	for i := range maxClockRulesPerOwner {
		id := insertClockRule(t, c.pool, c.user, "Panasonic", fmt.Sprintf("Model %03d", i), 60, nil, nil)
		if i == 0 {
			first = id
		}
	}

	rec := do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "Canon", "EOS R6", 60, nil, nil, ""))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "at most 100 camera clock rules\n", rec.Body.String())
	assert.Equal(t, maxClockRulesPerOwner, countClockRules(t, c.pool, c.user))

	rec = do(t, c.api, c.user, http.MethodPut, "/api/camera-clock-rules/"+first, ruleBody(t, "Panasonic", "Model 000", 120, nil, nil, ""))
	assert.Equal(t, http.StatusOK, rec.Code, "PUT at the cap: %s", rec.Body.String())

	rec = do(t, c.api, other, http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "Canon", "EOS R6", 60, nil, nil, ""))
	assert.Equal(t, http.StatusCreated, rec.Code, "another owner: %s", rec.Body.String())

	require.Equal(t, http.StatusNoContent, do(t, c.api, c.user, http.MethodDelete, "/api/camera-clock-rules/"+first, "").Code)
	rec = do(t, c.api, c.user, http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "Canon", "EOS R6", 60, nil, nil, ""))
	assert.Equal(t, http.StatusCreated, rec.Code, "below the cap again: %s", rec.Body.String())
}

// Concurrent POSTs of one owner cannot pass the cap together: the count and the insert run under a
// lock on the owner, so of 20 POSTs at 95 rules exactly 5 succeed.
func TestCameraClockRules_CapHoldsUnderConcurrentPosts(t *testing.T) {
	c := newClockRulesAPI(t)
	const existing, posts = maxClockRulesPerOwner - 5, 20
	for i := range existing {
		insertClockRule(t, c.pool, c.user, "Panasonic", fmt.Sprintf("Model %03d", i), 60, nil, nil)
	}

	statuses := make([]int, posts)
	bodies := make([]string, posts)
	for i := range posts {
		bodies[i] = ruleBody(t, "Canon", fmt.Sprintf("Concurrent %02d", i), 60, nil, nil, "")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range posts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/camera-clock-rules", strings.NewReader(bodies[i]))
			req.Header.Set(testUserHeader, c.user)
			rec := httptest.NewRecorder()
			c.api.ServeHTTP(rec, req)
			statuses[i] = rec.Code
		}()
	}
	close(start)
	wg.Wait()

	counts := map[int]int{}
	for _, s := range statuses {
		counts[s]++
	}
	assert.Equal(t, map[int]int{http.StatusCreated: 5, http.StatusConflict: posts - 5}, counts)
	assert.Equal(t, maxClockRulesPerOwner, countClockRules(t, c.pool, c.user))
}

// POST and PUT of one owner wait for the owner lock (lockOwnerRow), so they run one after the
// other.
func TestCameraClockRules_WritesWaitForTheOwnerLock(t *testing.T) {
	c := newClockRulesAPI(t)
	id := insertClockRule(t, c.pool, c.user, "Panasonic", "DC-TZ200", 120, nil, ptr("2018-12-31"))
	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"PUT", http.MethodPut, "/api/camera-clock-rules/" + id, ruleBody(t, "Panasonic", "DC-TZ200", 60, nil, ptr("2019-12-31"), ""), http.StatusOK},
		{"POST", http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "Panasonic", "DC-TZ90", 60, nil, nil, ""), http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			tx, err := c.pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE`, c.user)
			require.NoError(t, err)

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- do(t, c.api, c.user, tt.method, tt.path, tt.body) }()
			select {
			case rec := <-done:
				t.Fatalf("answered %d while another write held the owner lock: %s", rec.Code, rec.Body.String())
			case <-time.After(300 * time.Millisecond):
			}
			require.NoError(t, tx.Commit(ctx))
			select {
			case rec := <-done:
				assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			case <-time.After(10 * time.Second):
				t.Fatal("still waiting after the lock was released")
			}
		})
	}
}

// Two writes at once that put rules for one camera into overlapping periods: one is saved and the
// other is 409, never a 500 from a deadlock in the exclusion constraint.
func TestCameraClockRules_ConcurrentOverlappingWrites(t *testing.T) {
	c := newClockRulesAPI(t)
	const rounds = 20
	for i := range rounds {
		model := fmt.Sprintf("Round %02d", i)
		a := insertClockRule(t, c.pool, c.user, "Panasonic", model, 60, ptr("2010-01-01"), ptr("2010-12-31"))
		b := insertClockRule(t, c.pool, c.user, "Panasonic", model, 60, ptr("2011-01-01"), ptr("2011-12-31"))
		type write struct{ method, path, body string }
		writes := []write{
			{http.MethodPut, "/api/camera-clock-rules/" + a, ruleBody(t, "Panasonic", model, 60, ptr("2030-01-01"), ptr("2030-06-30"), "")},
			{http.MethodPut, "/api/camera-clock-rules/" + b, ruleBody(t, "Panasonic", model, 60, ptr("2030-03-01"), ptr("2030-12-31"), "")},
		}
		if i%2 == 1 {
			writes[1] = write{http.MethodPost, "/api/camera-clock-rules", ruleBody(t, "panasonic", model, 60, ptr("2030-03-01"), ptr("2030-12-31"), "")}
		}

		statuses := make([]int, len(writes))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j, wr := range writes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				statuses[j] = do(t, c.api, c.user, wr.method, wr.path, wr.body).Code
			}()
		}
		close(start)
		wg.Wait()

		saved := 0
		for j, status := range statuses {
			switch status {
			case http.StatusConflict:
			case http.StatusOK, http.StatusCreated:
				saved++
			default:
				t.Errorf("round %d, %s %s: status %d", i, writes[j].method, writes[j].path, status)
			}
		}
		assert.Equal(t, 1, saved, "round %d: %v", i, statuses)
	}
}

// The SQLSTATEs the table really raises map to 409 and 400, never a 500, should a check the handler
// makes first ever miss one.
func TestClockRuleWriteError_FromTheDatabase(t *testing.T) {
	c := newClockRulesAPI(t)
	insertClockRule(t, c.pool, c.user, "Panasonic", "DC-TZ200", 120, nil, nil)
	insert := func(cameraMake string, offsetMin int, startsOn, endsOn *string, note string) error {
		_, err := c.pool.Exec(context.Background(), `
			INSERT INTO camera_clock_rules (owner_id, make, model, utc_offset_min, starts_on, ends_on, note)
			VALUES ($1, $2, 'DC-TZ200', $3, $4::date, $5::date, $6)`, c.user, cameraMake, offsetMin, startsOn, endsOn, note)
		return err
	}
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"overlap", insert("PANASONIC", 60, nil, nil, ""), http.StatusConflict, "overlaps another rule for this camera"},
		{"make with space around", insert(" Canon", 60, nil, nil, ""), http.StatusBadRequest, "camera_clock_rules_make_check"},
		{"offset", insert("Canon", 61, nil, nil, ""), http.StatusBadRequest, "camera_clock_rules_utc_offset_min_check"},
		{"period", insert("Canon", 60, ptr("2022-02-01"), ptr("2022-01-01"), ""), http.StatusBadRequest, "camera_clock_rules_period_check"},
		{"infinity", insert("Canon", 60, ptr("infinity"), nil, ""), http.StatusBadRequest, "camera_clock_rules_finite_check"},
		{"note", insert("Canon", 60, nil, nil, strings.Repeat("n", 501)), http.StatusBadRequest, "camera_clock_rules_note_check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, tt.err)
			rec := httptest.NewRecorder()
			require.True(t, clockRuleWriteError(rec, tt.err), tt.err.Error())
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}

// insertCameraMedia stores an item with the make in capture_evidence and the model in camera_model,
// as ingest does since migration 10. A nil make leaves capture_evidence NULL, as on older rows.
func insertCameraMedia(t *testing.T, pool *pgxpool.Pool, ownerID, filename string, cameraMake, model, takenAt *string) {
	t.Helper()
	var evidence []byte
	if cameraMake != nil {
		var err error
		evidence, err = json.Marshal(map[string]any{"v": 1, "make": *cameraMake})
		require.NoError(t, err)
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, taken_at, camera_model, capture_evidence)
		VALUES ($1, $2, 'photo', $3, $4, $5::timestamptz, $6, $7)`,
		ownerID, filename, "media/"+ownerID+"/"+filename, "hash-"+ownerID+"-"+filename, takenAt, model, evidence)
	require.NoError(t, err)
}

func TestCameraClockRules_Cameras(t *testing.T) {
	c := newClockRulesAPI(t)
	ctx := context.Background()

	rec := do(t, c.api, c.user, http.MethodGet, "/api/cameras", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `[]`, rec.Body.String(), "a list, never null")

	insertCameraMedia(t, c.pool, c.user, "p1.jpg", ptr("Panasonic"), ptr("DC-TZ200"), ptr("2019-03-13T18:28:15+01:00"))
	insertCameraMedia(t, c.pool, c.user, "p2.jpg", ptr("Panasonic"), ptr("DC-TZ200"), ptr("2021-07-01T12:00:00Z"))
	insertCameraMedia(t, c.pool, c.user, "p3.jpg", ptr("Panasonic"), ptr("DC-TZ200"), nil)
	insertCameraMedia(t, c.pool, c.user, "p4.jpg", ptr("PANASONIC"), ptr("dc-tz200"), ptr("2022-12-31T23:00:00Z"))
	insertCameraMedia(t, c.pool, c.user, "s1.jpg", ptr("samsung"), ptr("SM-S921B"), ptr("2024-05-01T08:00:00Z"))
	insertCameraMedia(t, c.pool, c.user, "s2.jpg", ptr("samsung"), ptr("SM-S921B"), ptr("2024-05-02T08:00:00Z"))
	insertCameraMedia(t, c.pool, c.user, "a1.insv", ptr("Arashi Vision"), ptr("Insta360 OneRS"), ptr("2022-07-01T10:00:00Z"))
	insertCameraMedia(t, c.pool, c.user, "a2.insv", ptr("Arashi Vision"), ptr("Insta360 OneRS"), ptr("2022-08-01T10:00:00Z"))
	insertCameraMedia(t, c.pool, c.user, "u1.jpg", ptr("Canon"), ptr("EOS R6"), nil)
	// Not offered: no make (an older row, or evidence without one), or no model.
	insertCameraMedia(t, c.pool, c.user, "old.jpg", nil, ptr("DC-TZ200"), ptr("2017-01-01T00:00:00Z"))
	insertCameraMedia(t, c.pool, c.user, "nomodel.jpg", ptr("Panasonic"), nil, ptr("2017-01-01T00:00:00Z"))
	_, err := c.pool.Exec(ctx, `
		INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, taken_at, camera_model, capture_evidence)
		VALUES ($1, 'nullmake.jpg', 'photo', 'k-nullmake', 'h-nullmake', '2016-01-01T00:00:00Z', 'DC-TZ200', '{"make": null}'),
		       ($1, 'array.jpg', 'photo', 'k-array', 'h-array', '2016-01-01T00:00:00Z', 'DC-TZ200', '["Panasonic"]')`, c.user)
	require.NoError(t, err)
	// Another user's camera is never listed.
	other := createUser(t, c.pool, "other-cameras")
	insertCameraMedia(t, c.pool, other, "o1.jpg", ptr("Nikon"), ptr("Z6"), ptr("2020-01-01T00:00:00Z"))

	rec = do(t, c.api, c.user, http.MethodGet, "/api/cameras", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `[
		{"make":"Panasonic","model":"DC-TZ200","items":4,
		 "first_taken_at":"2019-03-13T17:28:15Z","last_taken_at":"2022-12-31T23:00:00Z"},
		{"make":"Arashi Vision","model":"Insta360 OneRS","items":2,
		 "first_taken_at":"2022-07-01T10:00:00Z","last_taken_at":"2022-08-01T10:00:00Z"},
		{"make":"samsung","model":"SM-S921B","items":2,
		 "first_taken_at":"2024-05-01T08:00:00Z","last_taken_at":"2024-05-02T08:00:00Z"},
		{"make":"Canon","model":"EOS R6","items":1,"first_taken_at":null,"last_taken_at":null}
	]`, rec.Body.String(), "most items first, then make and model; one camera whatever its case")

	t.Run("a year JSON cannot carry is null, not a 500", func(t *testing.T) {
		_, err := c.pool.Exec(ctx, `
			INSERT INTO media (owner_id, filename, media_type, storage_key, file_hash, taken_at, camera_model, capture_evidence)
			VALUES ($1, 'far.jpg', 'photo', 'k-far', 'h-far', '10000-01-01T00:00:00Z', 'EOS R6', '{"make": "Canon"}'),
			       ($1, 'bc.jpg', 'photo', 'k-bc', 'h-bc', '0002-01-01T00:00:00Z'::timestamptz - interval '3 years', 'EOS R6', '{"make": "Canon"}')`,
			c.user)
		require.NoError(t, err)
		rec := do(t, c.api, c.user, http.MethodGet, "/api/cameras", "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var cameras []libraryCamera
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cameras))
		require.Len(t, cameras, 4)
		assert.Equal(t, libraryCamera{Make: "Canon", Model: "EOS R6", Items: 3}, cameras[1],
			"second now, with both ends out of JSON's years")
	})
}

// assertRFC3339UTC checks that the named keys of a JSON object are RFC 3339 times written in UTC.
func assertRFC3339UTC(t *testing.T, body []byte, keys ...string) {
	t.Helper()
	var obj map[string]any
	require.NoError(t, json.Unmarshal(body, &obj))
	for _, k := range keys {
		s, ok := obj[k].(string)
		require.True(t, ok, "%s is a string: %v", k, obj[k])
		_, err := time.Parse(time.RFC3339Nano, s)
		assert.NoError(t, err, k)
		assert.True(t, strings.HasSuffix(s, "Z"), "%s in UTC: %s", k, s)
	}
}
