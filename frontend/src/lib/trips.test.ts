import { describe, it, expect } from "vitest";
import { groupTripsByYear } from "@/lib/trips";
import type { Trip } from "@/types";

function trip(id: string, start_date: string | null): Trip {
  return { id, name: id, start_date, end_date: null, media_count: 1, cover_key: null, user_edited: false };
}

const summary = (trips: Trip[]) =>
  groupTripsByYear(trips).map((g) => [g.year, g.trips.map((t) => t.id)]);

describe("groupTripsByYear", () => {
  it.each([
    ["no trips", [], []],
    ["one year", [trip("a", "2024-07-01"), trip("b", "2024-01-01")], [["2024", ["a", "b"]]]],
    [
      "newest year first, in server order",
      [trip("a", "2025-03-01"), trip("b", "2024-12-20"), trip("c", "2024-07-01"), trip("d", "2022-01-01")],
      [
        ["2025", ["a"]],
        ["2024", ["b", "c"]],
        ["2022", ["d"]],
      ],
    ],
    [
      "undated trips last",
      [trip("x", null), trip("a", "2023-05-01"), trip("y", null), trip("b", "2026-01-01")],
      [
        ["2026", ["b"]],
        ["2023", ["a"]],
        [null, ["x", "y"]],
      ],
    ],
    ["only undated trips", [trip("x", null)], [[null, ["x"]]]],
  ])("groups %s", (_name, trips, want) => {
    expect(summary(trips)).toEqual(want);
  });
});
