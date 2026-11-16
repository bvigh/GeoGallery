package handlers

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// lockOwnerRow locks ownerID's users row for the rest of tx. The writes that take it first run one
// after the other for one owner:
//   - camera clock rules (POST and PUT): two POSTs could otherwise both count 99 rules and insert,
//     and two writes that put rules for one camera into overlapping periods at once could deadlock
//     in the exclusion constraint, which inserts its index entry before it checks for a conflict,
//     and one would get 40P01 (a 500) instead of 409;
//   - capture time changes (PATCH /api/media/{id}/taken-at, POST /api/media/shift-time): each locks
//     its own items, then clears the interpolations near a moved point's old time, which locks
//     further rows in no set order, so two at once could each wait for a row the other holds.
//
// FOR NO KEY UPDATE does not block the FOR KEY SHARE locks that inserts referencing the user take,
// so uploads go on meanwhile.
func lockOwnerRow(ctx context.Context, tx pgx.Tx, ownerID string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE`, ownerID)
	return err
}
