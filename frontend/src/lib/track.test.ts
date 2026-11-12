import { describe, expect, it, vi } from "vitest";
import { breakGapSeconds, formatColor, nearestPoint, POINT_HIT_RADIUS_PX, splitSegments } from "@/lib/track";

// Points at the given offsets, in seconds, from a fixed instant.
function pointsAt(...offsets: number[]) {
  return offsets.map((offset) => ({ t: new Date(Date.UTC(2000, 0, 1) + offset * 1000).toISOString(), offset }));
}

// The offsets of each segment, to compare with what a test expects.
function offsets(segments: { offset: number }[][]) {
  return segments.map((segment) => segment.map((point) => point.offset));
}

describe("formatColor", () => {
  it("gives formats their own colour and every unknown one the same", () => {
    expect(formatColor("gpx")).not.toBe(formatColor("exif"));
    expect(formatColor("something-new")).toBe(formatColor("other"));
    // A raw prefix from a file can be any text, including an Object property name.
    expect(formatColor("constructor")).toBe(formatColor("other"));
  });
});

describe("breakGapSeconds", () => {
  it.each([
    ["no downsampling", "gpx", 0, 300],
    ["a small bucket", "gpx", 60, 300],
    ["the bucket at the floor", "gpx", 100, 300],
    ["a large bucket", "esoko", 200, 600],
    ["Google Timeline", "google_timeline", 0, 7200],
    ["Google Timeline, downsampled", "google_timeline", 200, 7200],
    ["an unknown format", "other", 0, 300],
  ])("%s", (_name, format, bucketS, want) => {
    expect(breakGapSeconds(format, bucketS)).toBe(want);
  });
});

describe("splitSegments", () => {
  it.each([
    ["nothing", [], "gpx", 0, []],
    ["one point", [0], "gpx", 0, [[0]]],
    ["a gap below the limit", [0, 299], "gpx", 0, [[0, 299]]],
    ["a gap at the limit", [0, 300], "gpx", 0, [[0, 300]]],
    ["a gap over the limit", [0, 301], "gpx", 0, [[0], [301]]],
    ["several gaps", [0, 10, 400, 410, 900], "gpx", 0, [[0, 10], [400, 410], [900]]],
    ["a gap the bucket covers", [0, 600], "gpx", 200, [[0, 600]]],
    ["a gap over three buckets", [0, 601], "gpx", 200, [[0], [601]]],
    ["Timeline at its limit", [0, 7200], "google_timeline", 0, [[0, 7200]]],
    ["Timeline over its limit", [0, 7201], "google_timeline", 0, [[0], [7201]]],
  ])("splits %s", (_name, points, format, bucketS, want) => {
    expect(offsets(splitSegments(pointsAt(...points), format, bucketS))).toEqual(want);
  });

  it("measures the gap between instants, not their text", () => {
    const fourMinutes = [{ t: "2000-01-01T00:00:00Z" }, { t: "2000-01-01T02:04:00+02:00" }];
    expect(splitSegments(fourMinutes, "gpx", 0)).toEqual([fourMinutes]);

    const justOver = [{ t: "2000-01-01T00:00:00Z" }, { t: "2000-01-01T00:05:00.001Z" }];
    expect(splitSegments(justOver, "gpx", 0)).toEqual([[justOver[0]], [justOver[1]]]);
  });
});

describe("nearestPoint", () => {
  const points = [
    { id: "a", x: 0, y: 0 },
    { id: "b", x: 10, y: 0 },
    { id: "c", x: 0, y: 30 },
  ];
  const project = (point: { x: number; y: number }) => point;

  it.each([
    ["the closest point within the radius", { x: 8, y: 0 }, 8, "b"],
    ["a point exactly on the radius", { x: 8, y: 30 }, 8, "c"],
    ["the earlier point on a tie", { x: 5, y: 0 }, 8, "a"],
    ["nothing outside the radius", { x: 50, y: 50 }, 8, null],
    ["a wider radius reaching further", { x: 0, y: 12 }, 22, "a"],
  ])("finds %s", (_name, click, radius, want) => {
    expect(nearestPoint(click, points, project, radius)?.id ?? null).toBe(want);
  });

  it("finds nothing in an empty list", () => {
    expect(nearestPoint({ x: 0, y: 0 }, [], project, 22)).toBeNull();
  });

  it("measures in the pixels project returns, not the point's own fields", () => {
    const geographic = [{ lat: 1, lng: 2 }];
    const toPixels = vi.fn(() => ({ x: 100, y: 100 }));
    expect(nearestPoint({ x: 104, y: 103 }, geographic, toPixels, POINT_HIT_RADIUS_PX.mouse)).toBe(geographic[0]);
    expect(toPixels).toHaveBeenCalledWith(geographic[0]);
  });

  it("is forgiving on a coarse pointer", () => {
    expect(POINT_HIT_RADIUS_PX).toEqual({ coarse: 22, mouse: 8 });
  });
});
