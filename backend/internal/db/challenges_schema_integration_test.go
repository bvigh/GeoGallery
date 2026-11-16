package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bvigh/geogallery/internal/testdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// challengeSchemaFixture: Alice's challenge with Bob as its other player and two rounds on Alice's
// photos; Alice and Bob have each guessed the first. Carol does not play.
type challengeSchemaFixture struct {
	pool              *pgxpool.Pool
	alice, bob, carol string
	challenge         string
	media1, media2    string
	spare             string // a photo of Alice's that is in no round
}

const (
	insertChallengeSQL = `
		INSERT INTO challenges (owner_id, name, max_dist_km, bounds_south, bounds_west, bounds_north, bounds_east)
		VALUES ($1, $2, $3, 47, 19, 47.5, 19.5) RETURNING id::text`
	insertChallengeMemberSQL = `INSERT INTO challenge_members (challenge_id, user_id) VALUES ($1, $2)`
	insertChallengeItemSQL   = `
		INSERT INTO challenge_items (challenge_id, position, media_id, location, gps_confidence)
		VALUES ($1, $2, $3, ST_SetSRID(ST_MakePoint(19.04, 47.5), 4326), $4)`
	insertChallengeScoreSQL = `
		INSERT INTO challenge_scores (challenge_id, position, user_id, guess, distance_km, location_score, total_score)
		VALUES ($1, $2, $3, ST_SetSRID(ST_MakePoint(19, 47), 4326), 55.7, 4000, 4000)`
)

func execSQL(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func newChallengeSchemaFixture(t *testing.T) challengeSchemaFixture {
	t.Helper()
	pool := testdb.New(t)
	f := challengeSchemaFixture{
		pool:  pool,
		alice: insertAlbumUser(t, pool, "alice"),
		bob:   insertAlbumUser(t, pool, "bob"),
		carol: insertAlbumUser(t, pool, "carol"),
	}
	f.media1 = insertAlbumMedia(t, pool, f.alice, "a1.jpg")
	f.media2 = insertAlbumMedia(t, pool, f.alice, "a2.jpg")
	f.spare = insertAlbumMedia(t, pool, f.alice, "a3.jpg")

	require.NoError(t, pool.QueryRow(context.Background(), insertChallengeSQL, f.alice, "Family five", 500.0).Scan(&f.challenge))
	execSQL(t, pool, insertChallengeMemberSQL, f.challenge, f.alice)
	execSQL(t, pool, insertChallengeMemberSQL, f.challenge, f.bob)
	execSQL(t, pool, insertChallengeItemSQL, f.challenge, 1, f.media1, 1.0)
	execSQL(t, pool, insertChallengeItemSQL, f.challenge, 2, f.media2, 0.5)
	execSQL(t, pool, insertChallengeScoreSQL, f.challenge, 1, f.alice)
	execSQL(t, pool, insertChallengeScoreSQL, f.challenge, 1, f.bob)
	return f
}

func TestChallengesSchema_Constraints(t *testing.T) {
	f := newChallengeSchemaFixture(t)
	const insertChallenge = `
		INSERT INTO challenges (owner_id, name, max_dist_km, bounds_south, bounds_west, bounds_north, bounds_east)
		VALUES ($1, $2, $3, 47, 19, 47.5, 19.5)`
	const insertItemNoLocation = `
		INSERT INTO challenge_items (challenge_id, position, media_id, gps_confidence) VALUES ($1, 3, $2, 1)`
	dave := insertAlbumUser(t, f.pool, "dave") // a player who owns nothing
	execSQL(t, f.pool, insertChallengeMemberSQL, f.challenge, dave)

	// wantName is the violated constraint, or the column for not-null violations (23502).
	tests := []struct {
		name     string
		sql      string
		args     []any
		wantCode string
		wantName string
	}{
		{"a third round", insertChallengeItemSQL, []any{f.challenge, 3, f.spare, 0.0}, "", ""},
		{"a player guesses a round", insertChallengeScoreSQL, []any{f.challenge, 2, f.bob}, "", ""},
		{"a name of 200 characters", insertChallenge, []any{f.alice, strings.Repeat("a", 200), 1.0}, "", ""},

		{"a second guess on the round", insertChallengeScoreSQL, []any{f.challenge, 1, f.bob}, "23505", "challenge_scores_pkey"},
		{"a guess by someone who does not play", insertChallengeScoreSQL, []any{f.challenge, 1, f.carol},
			"23503", "challenge_scores_challenge_id_user_id_fkey"},
		{"a guess on a round that is not there", insertChallengeScoreSQL, []any{f.challenge, 5, f.bob},
			"23503", "challenge_scores_challenge_id_position_fkey"},

		{"round 0", insertChallengeItemSQL, []any{f.challenge, 0, f.spare, 1.0}, "23514", "challenge_items_position_check"},
		{"round 6", insertChallengeItemSQL, []any{f.challenge, 6, f.spare, 1.0}, "23514", "challenge_items_position_check"},
		{"a round number twice", insertChallengeItemSQL, []any{f.challenge, 1, f.spare, 1.0}, "23505", "challenge_items_pkey"},
		{"an item in two rounds", insertChallengeItemSQL, []any{f.challenge, 4, f.media1, 1.0},
			"23505", "challenge_items_challenge_id_media_id_key"},
		{"a round on unknown media", insertChallengeItemSQL, []any{f.challenge, 4, unknownID, 1.0},
			"23503", "challenge_items_media_id_fkey"},
		{"a round of an unknown challenge", insertChallengeItemSQL, []any{unknownID, 1, f.spare, 1.0},
			"23503", "challenge_items_challenge_id_fkey"},
		{"a confidence over 1", insertChallengeItemSQL, []any{f.challenge, 4, f.spare, 1.5},
			"23514", "challenge_items_gps_confidence_check"},
		{"a round without an answer", insertItemNoLocation, []any{f.challenge, f.spare}, "23502", "location"},

		{"a player twice", insertChallengeMemberSQL, []any{f.challenge, f.bob}, "23505", "challenge_members_pkey"},
		{"a player of an unknown challenge", insertChallengeMemberSQL, []any{unknownID, f.carol},
			"23503", "challenge_members_challenge_id_fkey"},
		{"an unknown player", insertChallengeMemberSQL, []any{f.challenge, unknownID}, "23503", "challenge_members_user_id_fkey"},

		{"a challenge without an owner", insertChallenge, []any{nil, "x", 1.0}, "23502", "owner_id"},
		{"a challenge of an unknown owner", insertChallenge, []any{unknownID, "x", 1.0}, "23503", "challenges_owner_id_fkey"},
		{"an empty name", insertChallenge, []any{f.alice, "", 1.0}, "23514", "challenges_name_check"},
		{"a name over 200 characters", insertChallenge, []any{f.alice, strings.Repeat("a", 201), 1.0}, "23514", "challenges_name_check"},
		{"a scale under 1 km", insertChallenge, []any{f.alice, "x", 0.5}, "23514", "challenges_max_dist_km_check"},
		{"deleting a player's user is restricted", `DELETE FROM users WHERE id = $1`, []any{dave},
			"23503", "challenge_members_user_id_fkey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.pool.Exec(context.Background(), tc.sql, tc.args...)
			if tc.wantCode == "" {
				assert.NoError(t, err)
				return
			}
			var pgErr *pgconn.PgError
			require.True(t, errors.As(err, &pgErr), "want PgError, got %v", err)
			assert.Equal(t, tc.wantCode, pgErr.Code)
			gotName := pgErr.ConstraintName
			if tc.wantCode == "23502" {
				gotName = pgErr.ColumnName
			}
			assert.Equal(t, tc.wantName, gotName)
		})
	}
}

func TestChallengesSchema_LeavingTakesOnlyThatPlayersGuesses(t *testing.T) {
	f := newChallengeSchemaFixture(t)
	execSQL(t, f.pool, insertChallengeScoreSQL, f.challenge, 2, f.bob)
	// Bob plays in another challenge too; leaving this one must not touch his guess there.
	var other string
	require.NoError(t, f.pool.QueryRow(context.Background(), insertChallengeSQL, f.alice, "Other", 500.0).Scan(&other))
	execSQL(t, f.pool, insertChallengeMemberSQL, other, f.bob)
	execSQL(t, f.pool, insertChallengeItemSQL, other, 1, f.media1, 1.0)
	execSQL(t, f.pool, insertChallengeScoreSQL, other, 1, f.bob)

	execSQL(t, f.pool, `DELETE FROM challenge_members WHERE challenge_id = $1 AND user_id = $2`, f.challenge, f.bob)

	assert.Zero(t, countRows(t, f.pool,
		`SELECT COUNT(*) FROM challenge_scores WHERE challenge_id = $1 AND user_id = $2`, f.challenge, f.bob))
	assert.Equal(t, 1, countRows(t, f.pool,
		`SELECT COUNT(*) FROM challenge_scores WHERE challenge_id = $1 AND user_id = $2`, f.challenge, f.alice),
		"Alice's guess stays")
	assert.Equal(t, 1, countRows(t, f.pool, `SELECT COUNT(*) FROM challenge_scores WHERE challenge_id = $1`, other),
		"Bob's guess elsewhere stays")
	assert.Equal(t, 2, countRows(t, f.pool, `SELECT COUNT(*) FROM challenge_items WHERE challenge_id = $1`, f.challenge),
		"the rounds stay")
}

func TestChallengesSchema_DeletingMediaTakesItsRoundAndGuesses(t *testing.T) {
	f := newChallengeSchemaFixture(t)
	execSQL(t, f.pool, insertChallengeScoreSQL, f.challenge, 2, f.bob)

	execSQL(t, f.pool, `DELETE FROM media WHERE id = $1`, f.media1)

	assert.Equal(t, 1, countRows(t, f.pool, `SELECT COUNT(*) FROM challenge_items WHERE challenge_id = $1`, f.challenge))
	assert.Equal(t, 2, countRows(t, f.pool, `SELECT MIN(position) FROM challenge_items WHERE challenge_id = $1`, f.challenge),
		"the other round keeps its number")
	assert.Equal(t, 1, countRows(t, f.pool, `SELECT COUNT(*) FROM challenge_scores WHERE challenge_id = $1`, f.challenge),
		"only the guess on the round that stays")
	assert.Equal(t, 2, countRows(t, f.pool, `SELECT COUNT(*) FROM challenge_members WHERE challenge_id = $1`, f.challenge))
	assert.Equal(t, 1, countRows(t, f.pool, `SELECT COUNT(*) FROM challenges WHERE id = $1`, f.challenge),
		"the challenge itself stays")
}

func TestChallengesSchema_DeletingChallengeCascadesAndKeepsMedia(t *testing.T) {
	f := newChallengeSchemaFixture(t)
	var other string
	require.NoError(t, f.pool.QueryRow(context.Background(), insertChallengeSQL, f.bob, "Bob's", 500.0).Scan(&other))
	execSQL(t, f.pool, insertChallengeMemberSQL, other, f.bob)
	execSQL(t, f.pool, insertChallengeItemSQL, other, 1, f.media1, 1.0)

	execSQL(t, f.pool, `DELETE FROM challenges WHERE id = $1`, f.challenge)

	for _, table := range []string{"challenge_members", "challenge_items", "challenge_scores"} {
		assert.Zero(t, countRows(t, f.pool, `SELECT COUNT(*) FROM `+table+` WHERE challenge_id = $1`, f.challenge), table)
	}
	assert.Equal(t, 3, countRows(t, f.pool, `SELECT COUNT(*) FROM media`), "no media is deleted")
	assert.Equal(t, 1, countRows(t, f.pool, `SELECT COUNT(*) FROM challenge_items WHERE challenge_id = $1`, other),
		"another challenge on the same photo stays")
}
