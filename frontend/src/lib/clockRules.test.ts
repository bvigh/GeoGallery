import { describe, expect, it } from "vitest";
import {
  cameraName,
  cameraYears,
  CLOCK_OFFSETS,
  formatUtcOffset,
  libraryCameraLabel,
  rulePeriod,
  sameCamera,
} from "@/lib/clockRules";
import type { LibraryCamera } from "@/types";

describe("CLOCK_OFFSETS", () => {
  it("runs from UTC-12:00 to UTC+14:00 in 15-minute steps", () => {
    expect(CLOCK_OFFSETS).toHaveLength(105);
    expect(CLOCK_OFFSETS[0]).toBe(-720);
    expect(CLOCK_OFFSETS[CLOCK_OFFSETS.length - 1]).toBe(840);
    expect(CLOCK_OFFSETS.every((m, i) => i === 0 || m - CLOCK_OFFSETS[i - 1] === 15)).toBe(true);
    expect(CLOCK_OFFSETS).toContain(0);
    expect(CLOCK_OFFSETS).toContain(345);
  });
});

describe("formatUtcOffset", () => {
  it.each([
    [0, "UTC+00:00"],
    [120, "UTC+02:00"],
    [345, "UTC+05:45"],
    [-210, "UTC-03:30"],
    [-720, "UTC-12:00"],
    [840, "UTC+14:00"],
  ])("%d minutes", (minutes, want) => {
    expect(formatUtcOffset(minutes)).toBe(want);
  });
});

describe("cameraName", () => {
  it.each([
    ["Panasonic", "DC-TZ200", "Panasonic DC-TZ200"],
    ["Arashi Vision", "Insta360 ONE RS", "Arashi Vision Insta360 ONE RS"],
    ["Canon", "Canon EOS R5", "Canon EOS R5"],
    ["Canon", "canon eos r5", "canon eos r5"],
    ["NIKON CORPORATION", "NIKON D750", "NIKON CORPORATION NIKON D750"],
  ])("%s + %s", (make, model, want) => {
    expect(cameraName(make, model)).toBe(want);
  });
});

describe("sameCamera", () => {
  it("compares make and model without case or surrounding space, as ingest does", () => {
    const tz200 = { make: "Panasonic", model: "DC-TZ200" };
    expect(sameCamera(tz200, { make: " panasonic", model: "dc-tz200 " })).toBe(true);
    expect(sameCamera(tz200, { make: "Panasonic", model: "DC-TZ90" })).toBe(false);
    expect(sameCamera(tz200, { make: "Leica", model: "DC-TZ200" })).toBe(false);
  });
});

describe("rulePeriod", () => {
  it.each<[string | null, string | null, string]>([
    [null, null, "always"],
    ["2018-07-01", null, "from 2018-07-01"],
    [null, "2019-03-31", "until 2019-03-31"],
    ["2018-07-01", "2019-03-31", "2018-07-01 – 2019-03-31"],
    ["2020-01-01", "2020-01-01", "on 2020-01-01"],
  ])("%s to %s", (starts_on, ends_on, want) => {
    expect(rulePeriod({ starts_on, ends_on })).toBe(want);
  });
});

describe("cameraYears", () => {
  it.each<[string | null, string | null, string | null]>([
    ["2019-03-13T17:28:15Z", "2022-08-01T09:00:00Z", "2019–2022"],
    ["2024-01-01T00:00:00Z", "2024-12-31T23:59:59Z", "2024"],
    // A year is the UTC one, so the browser's zone cannot move it.
    ["2018-12-31T23:30:00Z", "2019-01-01T00:30:00Z", "2018–2019"],
    ["2019-03-13T17:28:15Z", null, "2019"],
    [null, "2022-08-01T09:00:00Z", "2022"],
    [null, null, null],
  ])("%s to %s", (first_taken_at, last_taken_at, want) => {
    expect(cameraYears({ first_taken_at, last_taken_at })).toBe(want);
  });
});

describe("libraryCameraLabel", () => {
  const camera: LibraryCamera = {
    make: "Panasonic",
    model: "DC-TZ200",
    items: 595,
    first_taken_at: "2019-03-13T17:28:15Z",
    last_taken_at: "2022-08-01T09:00:00Z",
  };

  it("names the camera, its items and its years", () => {
    expect(libraryCameraLabel(camera)).toBe("Panasonic DC-TZ200 · 595 items · 2019–2022");
  });

  it("counts one item as one", () => {
    expect(libraryCameraLabel({ ...camera, items: 1 })).toBe("Panasonic DC-TZ200 · 1 item · 2019–2022");
  });

  it("leaves the years out when no item is dated", () => {
    expect(libraryCameraLabel({ ...camera, first_taken_at: null, last_taken_at: null })).toBe("Panasonic DC-TZ200 · 595 items");
  });
});
