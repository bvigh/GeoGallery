import { describe, it, expect } from "vitest";
import { activeFilterCount, emptyFilters, filterParams } from "@/lib/filters";
import type { FilterState } from "@/types";

const f = (overrides: Partial<FilterState>): FilterState => ({ ...emptyFilters, ...overrides });

describe("filterParams", () => {
  it.each([
    { label: "empty filters send nothing", filters: emptyFilters, want: {} },
    { label: "from date only", filters: f({ fromDate: "2024-01-01" }), want: { from_date: "2024-01-01" } },
    { label: "to date only", filters: f({ toDate: "2024-12-31" }), want: { to_date: "2024-12-31" } },
    {
      label: "trips including unassigned",
      filters: f({ tripIds: ["unassigned", "t1"] }),
      want: { trip_id: ["unassigned", "t1"] },
    },
    { label: "albums", filters: f({ albumIds: ["a1", "a2"] }), want: { album_id: ["a1", "a2"] } },
    { label: "media types", filters: f({ mediaTypes: ["photo_360"] }), want: { media_type: ["photo_360"] } },
    { label: "owner all is omitted", filters: f({ owner: "all" }), want: {} },
    { label: "owner mine", filters: f({ owner: "mine" }), want: { owner: "mine" } },
    { label: "owner shared", filters: f({ owner: "shared" }), want: { owner: "shared" } },
    {
      label: "everything",
      filters: {
        fromDate: "2024-01-01",
        toDate: "2024-12-31",
        tripIds: ["t1"],
        albumIds: ["a1"],
        mediaTypes: ["photo", "video"],
        owner: "shared",
      } satisfies FilterState,
      want: {
        from_date: "2024-01-01",
        to_date: "2024-12-31",
        trip_id: ["t1"],
        album_id: ["a1"],
        media_type: ["photo", "video"],
        owner: "shared",
      },
    },
  ])("$label", ({ filters, want }) => {
    expect(filterParams(filters)).toEqual(want);
  });
});

describe("activeFilterCount", () => {
  it.each([
    { label: "nothing set", filters: emptyFilters, want: 0 },
    { label: "both dates count once", filters: f({ fromDate: "2024-01-01", toDate: "2024-12-31" }), want: 1 },
    { label: "trips only", filters: f({ tripIds: ["t1", "t2"] }), want: 1 },
    { label: "albums only", filters: f({ albumIds: ["a1"] }), want: 1 },
    { label: "trips and albums together count once", filters: f({ tripIds: ["t1"], albumIds: ["a1"] }), want: 1 },
    { label: "media types", filters: f({ mediaTypes: ["photo"] }), want: 1 },
    { label: "owner all does not count", filters: f({ owner: "all" }), want: 0 },
    { label: "owner mine counts", filters: f({ owner: "mine" }), want: 1 },
    { label: "owner shared counts", filters: f({ owner: "shared" }), want: 1 },
    {
      label: "every group",
      filters: f({ toDate: "2024-12-31", albumIds: ["a1"], mediaTypes: ["video"], owner: "mine" }),
      want: 4,
    },
  ])("$label", ({ filters, want }) => {
    expect(activeFilterCount(filters)).toBe(want);
  });
});
