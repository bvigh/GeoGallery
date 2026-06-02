package handlers

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

type tripDetectDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func RunTripDetect(ctx context.Context, db tripDetectDB, gapHours int) error {
	_, err := db.Exec(ctx, `SELECT auto_detect_trips($1::int)`, gapHours)
	return err
}
