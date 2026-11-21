// The words and cells of the storage sections, the Admin page's summary and Settings' own
// breakdown, all pure.
import type { AdminStorageUser, StorageAlbum, StorageTrip, StorageUse } from "@/types";
import { formatBytes, formatCount } from "@/lib/numbers";

export const UNNAMED_USER = "Unnamed user";

// How a user is named in the admin's summary: the display name, else the email.
export function storageUserName(u: Pick<AdminStorageUser, "display_name" | "email">): string {
  return u.display_name ?? u.email ?? UNNAMED_USER;
}

// What follows a user's name when they cannot sign in: unclaimed (the owner of the data stored
// before sign-in, until an admin invite claims it) or disabled.
export function storageUserStatus(u: Pick<AdminStorageUser, "claimed" | "disabled">): string | null {
  if (!u.claimed) return "unclaimed";
  if (u.disabled) return "disabled";
  return null;
}

export function itemsLabel(n: number): string {
  return `${formatCount(n)} ${n === 1 ? "item" : "items"}`;
}

export function trackPointsLabel(n: number): string {
  return `${formatCount(n)} track ${n === 1 ? "point" : "points"}`;
}

// A size cell: the bytes of the items whose size is known, and how many items it leaves out.
// "Unknown" when no item's size is known.
export function sizeLabel(use: StorageUse): string {
  if (use.items > 0 && use.unknown_items >= use.items) return "Unknown";
  const known = formatBytes(use.bytes);
  return use.unknown_items > 0 ? `${known} + ${formatCount(use.unknown_items)} unknown` : known;
}

// A trip's days as stored, never converted to a zone: "2024-05-01 – 2024-05-03".
export function tripDays(t: Pick<StorageTrip, "start_date" | "end_date">): string {
  const { start_date: start, end_date: end } = t;
  if (start && end) return start === end ? start : `${start} – ${end}`;
  if (start) return `from ${start}`;
  if (end) return `until ${end}`;
  return "no dates";
}

// Whose album it is, in the owner's own breakdown.
export function albumOwnerLabel(a: Pick<StorageAlbum, "is_owner" | "owner_name">): string {
  return a.is_owner ? "You" : (a.owner_name ?? UNNAMED_USER);
}

function unknownStart(n: number): string {
  return `The size of ${itemsLabel(n)} is unknown: ${n === 1 ? "it was" : "they were"} stored before sizes were recorded.`;
}

// The admin's notice while some sizes are unknown, null when every size is known.
export function unknownSizesNote(total: StorageUse): string | null {
  if (total.unknown_items === 0) return null;
  return (
    `${unknownStart(total.unknown_items)} The sizes below leave them out and say how many. ` +
    "Run server backfill-sizes on the server to read their sizes from the bucket."
  );
}

// The owner's notice while some of their sizes are unknown, null when every size is known.
export function ownUnknownSizesNote(total: StorageUse): string | null {
  if (total.unknown_items === 0) return null;
  return (
    `${unknownStart(total.unknown_items)} The sizes below leave them out and say how many, ` +
    "until an administrator reads their sizes."
  );
}
