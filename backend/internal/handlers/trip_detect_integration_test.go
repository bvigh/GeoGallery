package handlers

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The capture times in these tests carry no offset, so PostgreSQL reads them in the session's zone,
// the zone in which auto_detect_trips takes an item's day: the day boundaries hold in any zone.

// tripOf returns the name of the trip media item id is in, or "" when it is in none.
func tripOf(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var name *string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT t.name FROM media m LEFT JOIN trips t ON t.id = m.trip_id WHERE m.id = $1`, id).Scan(&name))
	if name == nil {
		return ""
	}
	return *name
}

// ownerTrips lists ownerID's trips as "name start..end", with " named" for a user-edited one, by
// start date.
func ownerTrips(t *testing.T, pool *pgxpool.Pool, ownerID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT name || ' ' || COALESCE(start_date::text, '-') || '..' || COALESCE(end_date::text, '-')
		       || CASE WHEN user_edited THEN ' named' ELSE '' END
		FROM trips WHERE owner_id = $1 ORDER BY start_date NULLS FIRST, name`, ownerID)
	require.NoError(t, err)
	defer rows.Close()
	var trips []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		trips = append(trips, s)
	}
	require.NoError(t, rows.Err())
	return trips
}

// insertNamedTrip inserts a user-edited trip of ownerID's; a nil day is NULL.
func insertNamedTrip(t *testing.T, pool *pgxpool.Pool, ownerID, name string, start, end *string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO trips (owner_id, name, user_edited, start_date, end_date)
		VALUES ($1, $2, TRUE, $3::date, $4::date) RETURNING id::text`, ownerID, name, start, end).Scan(&id))
	return id
}

// photoAt inserts an unassigned photo of ownerID's taken at takenAt (no offset: the session's zone).
func photoAt(t *testing.T, pool *pgxpool.Pool, ownerID, filename, takenAt string) string {
	t.Helper()
	return insertMedia(t, pool, mediaFixture{owner: ownerID, filename: filename, takenAt: takenAt})
}

func TestTripDetect_NamedTripTakesMediaOnItsDays(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	owner := createUser(t, pool, "alice")
	holland := insertTrip(t, pool, owner, "Holland", true, "2019-03-13", "2019-03-18")
	kept := insertMedia(t, pool, mediaFixture{owner: owner, filename: "kept.jpg", takenAt: "2019-03-13 11:33:55", tripID: &holland})
	// An auto trip from an earlier run, holding an item on one of Holland's days.
	earlier := insertTrip(t, pool, owner, "Trip 2019-03-18", false, "2019-03-18", "2019-03-18")
	fromAuto := insertMedia(t, pool, mediaFixture{owner: owner, filename: "P1044071.JPG", takenAt: "2019-03-18 10:30:20", tripID: &earlier})
	first := photoAt(t, pool, owner, "first.jpg", "2019-03-13 00:00:00")
	last := photoAt(t, pool, owner, "last.jpg", "2019-03-18 23:59:59")
	before := photoAt(t, pool, owner, "before.jpg", "2019-03-12 23:59:59")
	after := photoAt(t, pool, owner, "after.jpg", "2019-03-19 00:00:00")
	undated := photoAt(t, pool, owner, "undated.jpg", "2019-03-14 12:00:00")
	_, err := pool.Exec(ctx, `UPDATE media SET taken_at = NULL WHERE id = $1`, undated)
	require.NoError(t, err)

	require.NoError(t, RunTripDetect(ctx, pool, owner, 24))

	for _, tt := range []struct{ name, id, want string }{
		{"already in the named trip", kept, "Holland"},
		{"in an auto trip on one of its days", fromAuto, "Holland"},
		{"at the first second of its first day", first, "Holland"},
		{"at the last second of its last day", last, "Holland"},
		{"the second before its first day", before, "Trip 2019-03-12"},
		{"the second after its last day", after, "Trip 2019-03-19"},
		{"without a capture time", undated, ""},
	} {
		assert.Equal(t, tt.want, tripOf(t, pool, tt.id), tt.name)
	}
	assert.Equal(t, []string{
		"Trip 2019-03-12 2019-03-12..2019-03-12",
		"Holland 2019-03-13..2019-03-18 named",
		"Trip 2019-03-19 2019-03-19..2019-03-19",
	}, ownerTrips(t, pool, owner), "the named trip keeps its dates, and no auto trip repeats them")
}

// Items a named trip takes are not there when the rest are grouped, so the auto trips on either
// side of it stay apart, as they do around the items a date edit moved into it.
func TestTripDetect_NamedTripSplitsTheItemsAroundIt(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	owner := createUser(t, pool, "alice")
	insertTrip(t, pool, owner, "Keukenhof", true, "2019-03-14", "2019-03-14")
	// 24 hours apart: one auto trip without the named one.
	dayBefore := photoAt(t, pool, owner, "a.jpg", "2019-03-13 12:00:00")
	sameDay := photoAt(t, pool, owner, "b.jpg", "2019-03-14 12:00:00")
	dayAfter := photoAt(t, pool, owner, "c.jpg", "2019-03-15 12:00:00")

	require.NoError(t, RunTripDetect(ctx, pool, owner, 24))

	assert.Equal(t, "Trip 2019-03-13", tripOf(t, pool, dayBefore))
	assert.Equal(t, "Keukenhof", tripOf(t, pool, sameDay))
	assert.Equal(t, "Trip 2019-03-15", tripOf(t, pool, dayAfter))
}

func TestTripDetect_OverlappingNamedTrips(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	owner := createUser(t, pool, "alice")
	insertTrip(t, pool, owner, "Europe", true, "2019-03-01", "2019-03-31")
	insertTrip(t, pool, owner, "Holland", true, "2019-03-13", "2019-03-18")
	insertTrip(t, pool, owner, "Amsterdam", true, "2019-03-15", "2019-03-16")
	insertTrip(t, pool, owner, "Haarlem", true, "2019-03-16", "2019-03-17") // as long as Amsterdam, a day later

	tests := []struct{ day, want string }{
		{"2019-03-05", "Europe"},
		{"2019-03-14", "Holland"},   // the narrowest trip that covers the day
		{"2019-03-15", "Amsterdam"}, // narrower than Holland and Europe
		{"2019-03-16", "Haarlem"},   // Amsterdam and Haarlem are as narrow: the later start
		{"2019-03-17", "Haarlem"},
		{"2019-03-18", "Holland"},
	}
	ids := make([]string, len(tests))
	for i, tt := range tests {
		ids[i] = photoAt(t, pool, owner, tt.day+".jpg", tt.day+" 12:00:00")
	}

	require.NoError(t, RunTripDetect(ctx, pool, owner, 24))

	for i, tt := range tests {
		assert.Equal(t, tt.want, tripOf(t, pool, ids[i]), tt.day)
	}
	var autoTrips int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM trips WHERE owner_id = $1 AND NOT user_edited`, owner).Scan(&autoTrips))
	assert.Zero(t, autoTrips)
}

// A date edit moves nothing into a trip without both dates, and neither does detection.
func TestTripDetect_NamedTripWithoutBothDatesTakesNothing(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	owner := createUser(t, pool, "alice")
	insertNamedTrip(t, pool, owner, "No end", ptr("2019-03-01"), nil)
	insertNamedTrip(t, pool, owner, "No start", nil, ptr("2019-03-31"))
	insertNamedTrip(t, pool, owner, "No dates", nil, nil)
	photo := photoAt(t, pool, owner, "p.jpg", "2019-03-14 12:00:00")

	require.NoError(t, RunTripDetect(ctx, pool, owner, 24))

	assert.Equal(t, "Trip 2019-03-14", tripOf(t, pool, photo))
}

// The report this fixes: an upload on a named trip's days went into a new auto trip with the same
// dates. The file time is 12:00 UTC on the middle day, inside the trip in any session zone.
func TestTripDetect_UploadJoinsTheNamedTripOnItsDays(t *testing.T) {
	pool := testdb.New(t)
	router := newOwnershipRouter(pool, &fakeObjectStore{})
	owner := createUser(t, pool, "alice")
	insertTrip(t, pool, owner, "Holland", true, "2019-03-12", "2019-03-14")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("files", "P1044071.JPG")
	require.NoError(t, err)
	_, err = fw.Write([]byte("a photo without EXIF"))
	require.NoError(t, err)
	mtime := time.Date(2019, 3, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	require.NoError(t, mw.WriteField("file_mtime", strconv.FormatInt(mtime, 10)))
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/media/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(testUserHeader, owner)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, []string{"P1044071.JPG"}, decodeUpload(t, rec).Uploaded)

	assert.Equal(t, "Holland", tripOf(t, pool, mediaIDByName(t, pool, owner, "P1044071.JPG")))
	assert.Equal(t, []string{"Holland 2019-03-12..2019-03-14 named"}, ownerTrips(t, pool, owner))
}
