package db_test

import (
	"context"
	"errors"
	"testing"

	"codeberg.org/bvigh/geogallery/internal/testdb"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const legacyOwnerID = "00000000-0000-0000-0000-000000000001"

func newMigrator(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()
	m, err := migrate.New("file://"+testdb.MigrationsDir(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close() })
	return m
}

func migrateTo(t *testing.T, m *migrate.Migrate, version uint) {
	t.Helper()
	require.NoError(t, m.Migrate(version), "migrate to %d", version)
	got, dirty, err := m.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, version, got)
}

// withConn runs fn on a short-lived connection, closed before the next migration step
// so no cached statement plans survive an ALTER.
func withConn(t *testing.T, dsn string, fn func(ctx context.Context, conn *pgx.Conn)) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer conn.Close(ctx)
	fn(ctx, conn)
}

func seedLegacyData(t *testing.T, dsn string) {
	t.Helper()
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		var tripID string
		require.NoError(t, conn.QueryRow(ctx,
			`INSERT INTO trips (name, start_date) VALUES ('Trip 2024-05-01', '2024-05-01') RETURNING id`,
		).Scan(&tripID))
		_, err := conn.Exec(ctx, `
			INSERT INTO media (filename, media_type, storage_key, taken_at, location, gps_source, trip_id, file_hash)
			VALUES ('legacy.jpg', 'photo', 'media/1/legacy.jpg', '2024-05-01T12:00:00Z',
			        ST_MakePoint(19.0402, 47.4979)::geography, 'exif', $1, 'legacyhash')`, tripID)
		require.NoError(t, err)
		_, err = conn.Exec(ctx, `
			INSERT INTO gps_tracks (source, recorded_at, location)
			VALUES ('google_timeline', '2024-05-01T11:00:00Z', ST_MakePoint(19.04, 47.49)::geography)`)
		require.NoError(t, err)
	})
}

func tableExists(ctx context.Context, t *testing.T, conn *pgx.Conn, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+name).Scan(&exists))
	return exists
}

var authTables = []string{"users", "sessions", "invites", "api_tokens"}

func assertUsersAuthSchema(t *testing.T, dsn string) {
	t.Helper()
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		for _, table := range authTables {
			assert.True(t, tableExists(ctx, t, conn, table), "table %s should exist", table)
		}

		var issuer, subject *string
		var role, displayName string
		require.NoError(t, conn.QueryRow(ctx,
			`SELECT issuer, subject, role, display_name FROM users WHERE id = $1`, legacyOwnerID,
		).Scan(&issuer, &subject, &role, &displayName))
		assert.Nil(t, issuer)
		assert.Nil(t, subject)
		assert.Equal(t, "admin", role)
		assert.Equal(t, "Owner", displayName)

		for _, table := range []string{"media", "trips", "gps_tracks"} {
			var n int
			require.NoError(t, conn.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n))
			assert.Equal(t, 1, n, "legacy %s row must survive the migration", table)
		}
	})
}

func TestMigration000007_UsersAuth(t *testing.T) {
	dsn := testdb.NewEmpty(t)
	m := newMigrator(t, dsn)

	migrateTo(t, m, 6)
	seedLegacyData(t, dsn)

	migrateTo(t, m, 7)
	assertUsersAuthSchema(t, dsn)

	migrateTo(t, m, 6)
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		for _, table := range authTables {
			assert.False(t, tableExists(ctx, t, conn, table), "table %s should be dropped by down migration", table)
		}
	})

	migrateTo(t, m, 7)
	assertUsersAuthSchema(t, dsn)
}

func TestMigration000007_Constraints(t *testing.T) {
	dsn := testdb.NewEmpty(t)
	m := newMigrator(t, dsn)
	migrateTo(t, m, 7)

	// wantName is the violated constraint, or the column for not-null violations (23502).
	tests := []struct {
		name     string
		sql      string
		wantCode string
		wantName string
	}{
		{"user with issuer and subject", `INSERT INTO users (issuer, subject) VALUES ('https://idp', 'sub-0')`, "", ""},
		{"issuer without subject", `INSERT INTO users (issuer) VALUES ('https://idp')`, "23514", "users_check"},
		{"subject without issuer", `INSERT INTO users (subject) VALUES ('sub-2')`, "23514", "users_check"},
		{"duplicate identity", `INSERT INTO users (issuer, subject) VALUES ('https://idp', 'sub-1')`, "23505", "users_issuer_subject_key"},
		{"invalid role", `INSERT INTO users (role) VALUES ('root')`, "23514", "users_role_check"},
		{"session without id_token", `INSERT INTO sessions (token_hash, user_id, expires_at)
			VALUES ('\x01', '` + legacyOwnerID + `', NOW())`, "23502", "id_token"},
		{"api token without expiry", `INSERT INTO api_tokens (user_id, name, token_hash, display_hint)
			VALUES ('` + legacyOwnerID + `', 'x', '\x02', 'gg_pat_abcdef')`, "23502", "expires_at"},
		{"api token empty name", `INSERT INTO api_tokens (user_id, name, token_hash, display_hint, expires_at)
			VALUES ('` + legacyOwnerID + `', '', '\x03', 'gg_pat_abcdef', NOW())`, "23514", "api_tokens_name_check"},
		{"api token name over 100 chars", `INSERT INTO api_tokens (user_id, name, token_hash, display_hint, expires_at)
			VALUES ('` + legacyOwnerID + `', repeat('a', 101), '\x04', 'gg_pat_abcdef', NOW())`, "23514", "api_tokens_name_check"},
		{"api token valid", `INSERT INTO api_tokens (user_id, name, token_hash, display_hint, expires_at)
			VALUES ('` + legacyOwnerID + `', repeat('a', 100), '\x05', 'gg_pat_abcdef', NOW())`, "", ""},
	}
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		_, err := conn.Exec(ctx, `INSERT INTO users (issuer, subject) VALUES ('https://idp', 'sub-1')`)
		require.NoError(t, err, "seed existing identity for the duplicate case")

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				_, err := conn.Exec(ctx, tc.sql)
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
	})
}
