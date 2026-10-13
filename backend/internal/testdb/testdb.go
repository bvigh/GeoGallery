// Package testdb creates throwaway PostGIS databases for integration tests.
// Tests using it are skipped unless TEST_DATABASE_URL points at a server whose
// user may CREATE DATABASE (the docker compose user is a superuser).
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"codeberg.org/bvigh/geogallery/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// MigrationsDir returns the absolute path of backend/migrations, independent of the test's working directory.
func MigrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

// NewEmpty creates a uniquely named, unmigrated database and returns its DSN.
// The database is dropped when the test finishes.
func NewEmpty(t *testing.T) string {
	t.Helper()
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	u, err := url.Parse(adminDSN)
	require.NoError(t, err, "parse TEST_DATABASE_URL")
	require.Contains(t, []string{"postgres", "postgresql"}, u.Scheme,
		"TEST_DATABASE_URL must be a postgres:// URL, not a keyword/value DSN")

	b := make([]byte, 8)
	_, err = rand.Read(b)
	require.NoError(t, err)
	name := "gg_test_" + hex.EncodeToString(b)

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminDSN)
	require.NoError(t, err, "connect to TEST_DATABASE_URL")
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	require.NoError(t, err, "create test database")

	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, adminDSN)
		if err != nil {
			t.Errorf("testdb: connect for drop of %s: %v", name, err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("testdb: drop %s: %v", name, err)
		}
	})

	u.Path = "/" + name
	return u.String()
}

// New creates a fully migrated database and returns a pool connected to it.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := NewEmpty(t)
	require.NoError(t, db.RunMigrations(dsn, MigrationsDir()))
	pool, err := db.Connect(context.Background(), dsn)
	require.NoError(t, err)
	// Registered after NewEmpty's drop, so it runs first (cleanups are LIFO).
	t.Cleanup(pool.Close)
	return pool
}
