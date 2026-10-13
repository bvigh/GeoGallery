package handlers

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

type tripDetectDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RunTripDetect regroups ownerID's unassigned media into auto-detected trips.
func RunTripDetect(ctx context.Context, db tripDetectDB, ownerID string, gapHours int) error {
	_, err := db.Exec(ctx, `SELECT auto_detect_trips($1::uuid, $2::int)`, ownerID, gapHours)
	return err
}
