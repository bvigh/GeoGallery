package handlers

import (
	"context"
	"time"
)

func insertExifGPSTrack(ctx context.Context, db gpsTrackDB, ownerID string, lat, lng float64, recordedAt time.Time) error {
	_, err := db.Exec(ctx, `
		INSERT INTO gps_tracks (owner_id, source, recorded_at, location)
		VALUES ($1, 'exif', $2, ST_SetSRID(ST_MakePoint($3, $4), 4326))
		ON CONFLICT DO NOTHING
	`, ownerID, recordedAt, lng, lat)
	return err
}
