import type { Album } from "@/types";

// When the caller last used the album: their latest addition to it, else its creation when
// they own it, so a just-created album ranks high. Null when they never used it.
function lastUsed(album: Album): number | null {
  const at = album.last_added_at ?? (album.is_owner ? album.created_at : null);
  return at === null ? null : Date.parse(at);
}

// A copy of albums, most recently used first and never-used ones last, for the album
// pickers. Ties keep their order, which is the server's (newest album first).
export function byRecentUse(albums: Album[]): Album[] {
  const used = new Map(albums.map((a) => [a.id, lastUsed(a)]));
  return [...albums].sort((a, b) => {
    const ua = used.get(a.id) ?? null;
    const ub = used.get(b.id) ?? null;
    if (ua === ub) return 0;
    if (ua === null) return 1;
    if (ub === null) return -1;
    return ub - ua;
  });
}
