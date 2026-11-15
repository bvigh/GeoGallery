import { describe, it, expect } from "vitest";
import { datedDays, dayIn, firstOnOrAfter, positionIndex } from "@/lib/galleryJump";

const at = (taken_at: string | null) => ({ taken_at });

describe("positionIndex", () => {
  it.each([
    ["1", 0],
    ["3", 2],
    ["5", 4],
    // Clamped to 1..N.
    ["0", 0],
    ["00", 0],
    ["6", 4],
    ["999", 4],
    ["9".repeat(400), 4],
    // Spaces around the number do not matter.
    [" 2 ", 1],
  ])("reads %j in a gallery of 5 as index %i", (typed, index) => {
    expect(positionIndex(typed, 5)).toBe(index);
  });

  it.each(["", " ", "abc", "1.5", "-1", "+2", "1e2", "2 3"])("reads no number from %j", (typed) => {
    expect(positionIndex(typed, 5)).toBeNull();
  });

  it("has nowhere to go in an empty gallery", () => {
    expect(positionIndex("1", 0)).toBeNull();
  });
});

describe("dayIn", () => {
  it.each([
    ["2025-06-01T16:00:00Z", "UTC", "2025-06-01"],
    ["2025-06-01T16:00:00Z", "Asia/Tokyo", "2025-06-02"],
    ["2025-06-02T02:00:00Z", "America/New_York", "2025-06-01"],
    ["2025-06-01T10:00:00Z", "Pacific/Kiritimati", "2025-06-02"],
    // 00:30 on the night summer time starts, and 23:30 on the night it ends.
    ["2025-03-29T23:30:00Z", "Europe/Budapest", "2025-03-30"],
    ["2025-10-25T21:30:00Z", "Europe/Budapest", "2025-10-25"],
    // A date field always writes four digits for the year.
    ["0005-03-01T12:00:00Z", "UTC", "0005-03-01"],
    ["9998-12-31T12:00:00Z", "UTC", "9998-12-31"],
  ])("puts %s on its day in %s", (iso, zone, day) => {
    expect(dayIn(iso, zone)).toBe(day);
  });

  it("has no day for a time it cannot read", () => {
    expect(dayIn("not a time", "UTC")).toBeNull();
  });
});

describe("datedDays", () => {
  it("spans the dated items in any order, in the zone", () => {
    const items = [at("2025-06-03T10:00:00Z"), at(null), at("2025-05-31T16:00:00Z"), at("2025-06-01T09:00:00Z")];
    expect(datedDays(items, "UTC")).toEqual({ first: "2025-05-31", last: "2025-06-03" });
    expect(datedDays(items, "Asia/Tokyo")).toEqual({ first: "2025-06-01", last: "2025-06-03" });
  });

  it("is null without a dated item", () => {
    expect(datedDays([], "UTC")).toBeNull();
    expect(datedDays([at(null), at("not a time")], "UTC")).toBeNull();
  });
});

describe("firstOnOrAfter", () => {
  const items = [
    at("2025-06-01T10:00:00Z"),
    at("2025-06-01T15:30:00Z"),
    at("2025-06-03T08:00:00Z"),
    at("2025-06-03T09:00:00Z"),
    at(null),
  ];

  it.each([
    ["2025-05-01", 0],
    ["2025-06-01", 0],
    ["2025-06-02", 2],
    ["2025-06-03", 2],
  ])("goes from %s to item %i", (day, index) => {
    expect(firstOnOrAfter(items, day, "UTC")).toBe(index);
  });

  it("never matches an undated item", () => {
    expect(firstOnOrAfter(items, "2025-06-04", "UTC")).toBe(-1);
    expect(firstOnOrAfter([at(null), at(null)], "2025-06-01", "UTC")).toBe(-1);
  });

  it("takes the day in the zone the gallery shows its times in", () => {
    // 15:30 UTC is 00:30 on 2 June in Tokyo.
    expect(firstOnOrAfter(items, "2025-06-02", "Asia/Tokyo")).toBe(1);
    // Midnight starts the day; a second before it belongs to the day before.
    const edge = [at("2025-06-01T14:59:59Z"), at("2025-06-01T15:00:00Z")];
    expect(firstOnOrAfter(edge, "2025-06-02", "Asia/Tokyo")).toBe(1);
    expect(firstOnOrAfter(edge, "2025-06-01", "Asia/Tokyo")).toBe(0);
  });

  it("goes by the gallery's own order", () => {
    const unsorted = [at("2025-06-05T10:00:00Z"), at("2025-06-03T10:00:00Z")];
    expect(firstOnOrAfter(unsorted, "2025-06-02", "UTC")).toBe(0);
  });

  it.each(["", "2025-6-1", "06/01/2025", "12025-06-01"])("matches nothing for %j", (day) => {
    expect(firstOnOrAfter(items, day, "UTC")).toBe(-1);
  });
});
