package handlers

import "fmt"

// readableMediaCond returns the WHERE fragment restricting media to rows user $n may read: their
// own, or media in an album they are a member of. It is the single place that encodes read
// access; write paths keep a literal owner_id filter, and RunGPSMatcher and RunTripDetect must
// never use it.
// alias is required (e.g. "m"): the EXISTS subquery must not resolve id/owner_id to album_media columns.
func readableMediaCond(alias string, n int) string {
	return fmt.Sprintf(`(%[1]s.owner_id = $%[2]d OR EXISTS (
		SELECT 1 FROM album_media am
		JOIN album_members mem ON mem.album_id = am.album_id
		WHERE am.media_id = %[1]s.id AND mem.user_id = $%[2]d))`, alias, n)
}
