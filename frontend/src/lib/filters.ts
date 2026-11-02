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
