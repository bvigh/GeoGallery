package handlers

import (
	"context"
	"time"
)

func insertExifGPSTrack(ctx context.Context, db gpsTrackDB, lat, lng float64, recordedAt time.Time) error {
	_, err := db.Exec(ctx, `
		INSERT INTO gps_tracks (source, recorded_at, location)
		VALUES ('exif', $1, ST_SetSRID(ST_MakePoint($2, $3), 4326))
		ON CONFLICT DO NOTHING
	`, recordedAt, lng, lat)
	return err
}
