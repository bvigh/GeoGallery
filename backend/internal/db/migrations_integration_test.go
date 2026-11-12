package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bvigh/geogallery/internal/testdb"
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

var ownedTables = []string{"media", "trips", "gps_tracks"}

func constraintExists(ctx context.Context, t *testing.T, conn *pgx.Conn, table, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = to_regclass($1) AND conname = $2)`,
		"public."+table, name).Scan(&exists))
	return exists
}

func columnExists(ctx context.Context, t *testing.T, conn *pgx.Conn, table, column string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		               WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`,
		table, column).Scan(&exists))
	return exists
}

func assertOwnershipSchema(t *testing.T, dsn string) {
	t.Helper()
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		for _, table := range ownedTables {
			var owners []string
			rows, err := conn.Query(ctx, `SELECT owner_id::text FROM `+table)
			require.NoError(t, err)
			for rows.Next() {
				var o string
				require.NoError(t, rows.Scan(&o))
				owners = append(owners, o)
			}
			require.NoError(t, rows.Err())
			assert.Equal(t, []string{legacyOwnerID}, owners, "legacy %s row is backfilled to the Owner", table)
		}

		var tripAssigned bool
		require.NoError(t, conn.QueryRow(ctx, `SELECT trip_id IS NOT NULL FROM media`).Scan(&tripAssigned))
		assert.True(t, tripAssigned, "trip assignment survives the composite FK swap")

		for table, name := range map[string]string{
			"media":      "media_owner_file_hash_key",
			"gps_tracks": "gps_tracks_owner_source_recorded_at_key",
			"trips":      "trips_id_owner_key",
		} {
			assert.True(t, constraintExists(ctx, t, conn, table, name), "%s.%s", table, name)
		}
		assert.True(t, constraintExists(ctx, t, conn, "media", "media_trip_owner_fkey"))
		assert.False(t, constraintExists(ctx, t, conn, "media", "media_trip_id_fkey"))
		assert.False(t, constraintExists(ctx, t, conn, "media", "media_file_hash_key"))

		inserts := map[string]string{
			"media": `INSERT INTO media (filename, media_type, storage_key, file_hash)
				VALUES ('n.jpg', 'photo', 'media/null/n.jpg', 'nullownerhash')`,
			"trips":      `INSERT INTO trips (name) VALUES ('no owner')`,
			"gps_tracks": `INSERT INTO gps_tracks (source, recorded_at, location) VALUES ('x', NOW(), ST_MakePoint(0, 0)::geography)`,
		}
		for _, table := range ownedTables {
			_, err := conn.Exec(ctx, inserts[table])
			var pgErr *pgconn.PgError
			require.True(t, errors.As(err, &pgErr), "%s insert without owner must fail, got %v", table, err)
			assert.Equal(t, "23502", pgErr.Code, table)
			assert.Equal(t, "owner_id", pgErr.ColumnName, table)
		}

		_, err := conn.Exec(ctx, `SELECT auto_detect_trips($1::uuid, 24)`, legacyOwnerID)
		require.NoError(t, err, "per-owner auto_detect_trips exists")
	})
}

func TestMigration000008_Ownership(t *testing.T) {
	dsn := testdb.NewEmpty(t)
	m := newMigrator(t, dsn)

	migrateTo(t, m, 6)
	seedLegacyData(t, dsn)

	require.NoError(t, m.Up())
	assertOwnershipSchema(t, dsn)

	migrateTo(t, m, 6)
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		for _, table := range ownedTables {
			assert.False(t, columnExists(ctx, t, conn, table, "owner_id"), "%s.owner_id dropped", table)
			var n int
			require.NoError(t, conn.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n))
			assert.Equal(t, 1, n, "legacy %s row survives the down migration", table)
		}
		assert.True(t, constraintExists(ctx, t, conn, "media", "media_file_hash_key"))
		assert.True(t, constraintExists(ctx, t, conn, "media", "media_trip_id_fkey"))
		assert.True(t, constraintExists(ctx, t, conn, "gps_tracks", "gps_tracks_source_recorded_at_key"))
		_, err := conn.Exec(ctx, `SELECT auto_detect_trips(24)`)
		require.NoError(t, err, "000004 auto_detect_trips(int) restored")
	})

	require.NoError(t, m.Up())
	assertOwnershipSchema(t, dsn)
}

var albumTables = []string{"albums", "album_members", "album_media"}

func assertAlbumsSchema(t *testing.T, dsn string) {
	t.Helper()
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		for _, table := range albumTables {
			assert.True(t, tableExists(ctx, t, conn, table), "table %s should exist", table)
		}
		for _, c := range []struct{ table, name string }{
			{"media", "media_id_owner_key"},
			{"album_members", "album_members_pkey"},
			{"album_media", "album_media_album_id_owner_id_fkey"},
			{"album_media", "album_media_media_id_owner_id_fkey"},
		} {
			assert.True(t, constraintExists(ctx, t, conn, c.table, c.name), "%s.%s", c.table, c.name)
		}

		var n int
		require.NoError(t, conn.QueryRow(ctx, `SELECT COUNT(*) FROM media`).Scan(&n))
		assert.Equal(t, 1, n, "legacy media row survives")
	})
}

// seedLegacyAlbum puts the legacy media into an album of the Owner, so the down
// migration has to drop populated tables.
func seedLegacyAlbum(t *testing.T, dsn string) {
	t.Helper()
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		var albumID string
		require.NoError(t, conn.QueryRow(ctx,
			`INSERT INTO albums (owner_id, name) VALUES ($1, 'Family') RETURNING id`, legacyOwnerID,
		).Scan(&albumID))
		_, err := conn.Exec(ctx, `INSERT INTO album_members (album_id, user_id) VALUES ($1, $2)`, albumID, legacyOwnerID)
		require.NoError(t, err)
		tag, err := conn.Exec(ctx, `
			INSERT INTO album_media (album_id, media_id, owner_id) SELECT $1, id, owner_id FROM media`, albumID)
		require.NoError(t, err)
		require.Equal(t, int64(1), tag.RowsAffected())
	})
}

func TestMigration000009_Albums(t *testing.T) {
	dsn := testdb.NewEmpty(t)
	m := newMigrator(t, dsn)

	migrateTo(t, m, 6)
	seedLegacyData(t, dsn)
	migrateTo(t, m, 8)

	migrateTo(t, m, 9)
	assertAlbumsSchema(t, dsn)
	seedLegacyAlbum(t, dsn)

	migrateTo(t, m, 8)
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		for _, table := range albumTables {
			assert.False(t, tableExists(ctx, t, conn, table), "table %s should be dropped by down migration", table)
		}
		assert.False(t, constraintExists(ctx, t, conn, "media", "media_id_owner_key"))
	})
	assertOwnershipSchema(t, dsn)

	migrateTo(t, m, 9)
	assertAlbumsSchema(t, dsn)
}

// assertCaptureEvidenceColumn checks the column's definition and that the row seedLegacyData wrote
// holds no evidence: existing rows are never backfilled by the migration.
func assertCaptureEvidenceColumn(t *testing.T, dsn string) {
	t.Helper()
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		var dataType, nullable string
		var columnDefault *string
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT data_type, is_nullable, column_default FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'media' AND column_name = 'capture_evidence'`,
		).Scan(&dataType, &nullable, &columnDefault))
		assert.Equal(t, "jsonb", dataType)
		assert.Equal(t, "YES", nullable)
		assert.Nil(t, columnDefault)

		var total, withEvidence int
		require.NoError(t, conn.QueryRow(ctx,
			`SELECT COUNT(*), COUNT(capture_evidence) FROM media`,
		).Scan(&total, &withEvidence))
		assert.Equal(t, 1, total, "the legacy media row survives")
		assert.Zero(t, withEvidence, "existing rows stay NULL")
	})
}

func TestMigration000010_CaptureEvidence(t *testing.T) {
	dsn := testdb.NewEmpty(t)
	m := newMigrator(t, dsn)

	migrateTo(t, m, 6)
	seedLegacyData(t, dsn)
	migrateTo(t, m, 9)
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		assert.False(t, columnExists(ctx, t, conn, "media", "capture_evidence"))
	})

	migrateTo(t, m, 10)
	assertCaptureEvidenceColumn(t, dsn)
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		_, err := conn.Exec(ctx, `UPDATE media SET capture_evidence = '{"v": 1, "by": "ingest"}'`)
		require.NoError(t, err)
	})

	migrateTo(t, m, 9)
	withConn(t, dsn, func(ctx context.Context, conn *pgx.Conn) {
		assert.False(t, columnExists(ctx, t, conn, "media", "capture_evidence"))
		var n int
		require.NoError(t, conn.QueryRow(ctx, `SELECT COUNT(*) FROM media`).Scan(&n))
		assert.Equal(t, 1, n, "the down migration keeps the media rows")
	})

	migrateTo(t, m, 10)
	assertCaptureEvidenceColumn(t, dsn)
}
