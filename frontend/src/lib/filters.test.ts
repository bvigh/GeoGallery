import { describe, it, expect } from "vitest";
import { activeFilterCount, emptyFilters, filterParams, retargetTripIds } from "@/lib/filters";
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

describe("retargetTripIds", () => {
  const trip = (id: string, start: string | null, end: string | null = start) => ({
    id,
    name: `Trip ${start}`,
    start_date: start,
    end_date: end,
  });
  // Before the change: two auto trips and a named one. Trip detection then re-IDed both auto trips
  // (t-a2, t-b2), the edited item went to t-new, and t-gone left no trip with its name and dates.
  const before = [
    trip("t-a", "2024-05-01", "2024-05-03"),
    trip("t-b", "2024-06-10", "2024-06-12"),
    trip("t-gone", "2024-08-01"),
    { id: "t-named", name: "Iceland", start_date: "2024-07-01", end_date: "2024-07-14" },
  ];
  const listed = [
    trip("t-a2", "2024-05-01", "2024-05-03"),
    trip("t-b2", "2024-06-10", "2024-06-12"),
    trip("t-new", "2024-05-02"),
    { id: "t-named", name: "Iceland", start_date: "2024-07-01", end_date: "2024-07-14" },
    trip("t-other", "2024-09-01"),
  ];

  it.each<[string, string[], string | null, string | null, string[]]>([
    ["moves the edited item's ticked trip to its new one", ["t-old"], "t-old", "t-new", ["t-new"]],
    ["drops it when the item is in no trip now", ["t-old"], "t-old", null, []],
    // Its neighbours kept the old trip's name and dates; the item went on to another one.
    ["keeps the old trip's neighbours beside the item's new trip", ["t-a"], "t-a", "t-new", ["t-new", "t-a2"]],
    ["keeps the old trip's neighbours when the item is in no trip now", ["t-a"], "t-a", null, ["t-a2"]],
    ["moves another ticked auto trip to its successor", ["t-a", "t-b"], "t-a", "t-a2", ["t-a2", "t-b2"]],
    ["moves ticked auto trips without the item's old trip", ["t-a", "t-b"], null, null, ["t-a2", "t-b2"]],
    ["drops a ticked trip that has no successor", ["t-gone", "t-other"], null, null, ["t-other"]],
    ["drops a ticked trip it never knew", ["t-unknown", "t-other"], null, null, ["t-other"]],
    ["keeps unassigned", ["unassigned", "t-gone"], "t-x", "t-new", ["unassigned"]],
    ["keeps a named trip the item left beside its new one", ["t-named"], "t-named", "t-new", ["t-new", "t-named"]],
    ["never ticks a trip twice", ["t-old", "t-new"], "t-old", "t-new", ["t-new"]],
    ["never ticks a successor twice", ["t-b", "t-b2"], null, null, ["t-b2"]],
    ["leaves an untouched old trip alone", ["t-other"], "t-old", "t-new", ["t-other"]],
    ["drops a new trip the list does not hold", ["t-old"], "t-old", "t-unlisted", []],
  ])("%s", (_name, tripIds, oldTripId, newTripId, want) => {
    expect(retargetTripIds(tripIds, oldTripId, newTripId, before, listed)).toEqual(want);
  });

  it("matches a successor on its name and both dates", () => {
    const b = trip("t-b", "2024-06-10", "2024-06-12");
    for (const fresh of [
      { ...b, id: "t-b2", name: "Trip 2024-06-11" },
      { ...b, id: "t-b2", start_date: "2024-06-11" },
      { ...b, id: "t-b2", end_date: "2024-06-13" },
    ]) {
      expect(retargetTripIds(["t-b"], null, null, [b], [fresh])).toEqual([]);
    }
  });

  it("returns the same array when nothing changes, so the filters are not set again", () => {
    const tripIds = ["unassigned", "t-other"];
    expect(retargetTripIds(tripIds, "t-old", "t-new", before, listed)).toBe(tripIds);
    const none: string[] = [];
    expect(retargetTripIds(none, "t-old", "t-new", before, listed)).toBe(none);
  });
});
