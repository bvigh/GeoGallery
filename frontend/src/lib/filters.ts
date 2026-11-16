import type { FilterState } from "@/types";

export const emptyFilters: FilterState = {
  fromDate: "",
  toDate: "",
  tripIds: [],
  albumIds: [],
  mediaTypes: [],
  owner: "all",
};

/** Query params for GET /api/media and /api/game/round; unset filters are left out. */
export function filterParams(filters: FilterState): Record<string, string | string[]> {
  const params: Record<string, string | string[]> = {};
  if (filters.fromDate) params.from_date = filters.fromDate;
  if (filters.toDate) params.to_date = filters.toDate;
  if (filters.tripIds.length) params.trip_id = filters.tripIds;
  if (filters.albumIds.length) params.album_id = filters.albumIds;
  if (filters.mediaTypes.length) params.media_type = filters.mediaTypes;
  if (filters.owner !== "all") params.owner = filters.owner;
  return params;
}

/** Number of active filter groups. Ticked trips and albums are one group (the backend ORs them). */
export function activeFilterCount(filters: FilterState): number {
  return [
    filters.fromDate || filters.toDate,
    filters.tripIds.length > 0 || filters.albumIds.length > 0,
    filters.mediaTypes.length > 0,
    filters.owner !== "all",
  ].filter(Boolean).length;
}

/** What a trip is known by across trip detection, which gives every trip nobody edited a new ID. */
type TripKey = { id: string; name: string; start_date: string | null; end_date: string | null };

/**
 * The ticked trips after a time change, which ran trip detection: every trip nobody edited has a
 * new ID. The edited item's old trip, if ticked, takes its new one along (when listed). Any other
 * ticked trip no longer listed becomes the listed trip with the name and dates it had in `before`,
 * as an auto trip the change did not touch comes back ("Trip " + its first day); the old trip too,
 * for the item's neighbours it kept. A tick that matches nothing is dropped, so the panel never
 * holds a tick it cannot show or untick. "unassigned" always stays, and no trip is ticked twice.
 * Returns the same array when nothing changes.
 */
export function retargetTripIds(
  tripIds: string[],
  oldTripId: string | null,
  newTripId: string | null,
  before: TripKey[],
  listed: TripKey[]
): string[] {
  const known = new Set(listed.map((t) => t.id));
  const next: string[] = [];
  const tick = (id: string | null | undefined) => {
    if (id != null && !next.includes(id)) next.push(id);
  };
  for (const id of tripIds) {
    if (oldTripId !== null && id === oldTripId && newTripId !== null && known.has(newTripId)) tick(newTripId);
    if (id === "unassigned" || known.has(id)) {
      tick(id);
      continue;
    }
    const was = before.find((t) => t.id === id);
    if (!was) continue;
    tick(
      listed.find((t) => t.name === was.name && t.start_date === was.start_date && t.end_date === was.end_date)?.id
    );
  }
  return next.length === tripIds.length && next.every((id, i) => id === tripIds[i]) ? tripIds : next;
}
