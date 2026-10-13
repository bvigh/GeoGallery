package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Cycle 1: GPS data present → inserts into gps_tracks with source='exif' ---

func TestInsertExifGPSTrack_WithGPS_InsertsRow(t *testing.T) {
	const lat, lng = 47.5, 19.0
	takenAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	var capturedSQL string
	var capturedArgs []any

	db := &mockGPSTrackDB{
		execFn: func(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
			capturedSQL = sql
			capturedArgs = args
			return makeTag(1), nil
		},
	}

	err := insertExifGPSTrack(context.Background(), db, testOwnerID, lat, lng, takenAt)

	require.NoError(t, err)
	assert.Contains(t, strings.ToLower(capturedSQL), "gps_tracks")
	assert.Contains(t, capturedSQL, "exif")
	require.NotEmpty(t, capturedArgs)
	assert.Contains(t, capturedSQL, "owner_id")
	assert.Equal(t, testOwnerID, capturedArgs[0])
}
