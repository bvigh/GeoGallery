package testdb

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationsDir(t *testing.T) {
	_, err := os.Stat(filepath.Join(MigrationsDir(), "000001_init.up.sql"))
	assert.NoError(t, err)
}

func TestNew_MigratesAndDropsDatabase(t *testing.T) {
	var dbName string
	t.Run("create", func(t *testing.T) {
		pool := New(t)
		ctx := context.Background()
		require.NoError(t, pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName))
		var users int
		require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&users))
		assert.Equal(t, 1, users)
	})
	if dbName == "" {
		return // subtest skipped: TEST_DATABASE_URL unset
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, err)
	defer conn.Close(ctx)
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists))
	assert.False(t, exists, "database %s should be dropped after the test", dbName)
}

func TestNewEmpty_ReplacesOnlyPath(t *testing.T) {
	admin := os.Getenv("TEST_DATABASE_URL")
	dsn := NewEmpty(t)
	a, err := url.Parse(admin)
	require.NoError(t, err)
	d, err := url.Parse(dsn)
	require.NoError(t, err)
	assert.Equal(t, a.Host, d.Host)
	assert.Equal(t, a.User.String(), d.User.String())
	assert.Equal(t, a.RawQuery, d.RawQuery)
	assert.True(t, strings.HasPrefix(d.Path, "/gg_test_"), "path %q should name the generated test database", d.Path)
}
