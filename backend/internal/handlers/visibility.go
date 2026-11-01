package handlers

import "fmt"

// readableMediaCond returns the WHERE fragment restricting media to rows the user may read.
// It is the single extension point for sharing; write paths keep a literal owner_id filter.
func readableMediaCond(alias string, n int) string {
	return fmt.Sprintf("%sowner_id = $%d", alias, n)
}
