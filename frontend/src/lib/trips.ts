import type { Trip } from "@/types";

export interface TripYearGroup {
  // "2024", or null for trips without a start date.
  year: string | null;
  trips: Trip[];
}

// Trips grouped by the year of their start date, newest year first and undated trips
// last. Trips keep their order inside a group, which is the server's (newest first).
export function groupTripsByYear(trips: Trip[]): TripYearGroup[] {
  const groups = new Map<string | null, Trip[]>();
  for (const trip of trips) {
    const year = trip.start_date ? trip.start_date.slice(0, 4) : null;
    const group = groups.get(year);
    if (group) group.push(trip);
    else groups.set(year, [trip]);
  }
  return [...groups]
    .map(([year, trips]) => ({ year, trips }))
    .sort((a, b) => {
      if (a.year === b.year) return 0;
      if (a.year === null) return 1;
      if (b.year === null) return -1;
      return b.year.localeCompare(a.year);
    });
}
