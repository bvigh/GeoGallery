import { afterEach, describe, expect, it, vi } from "vitest";
import { browserTimeZone, formatDuration, formatInstant, formatOffset, formatSpan } from "@/lib/time";

// CI runners are UTC, so every expectation names its zone, and the locale is fixed.
const british = { locale: "en-GB" };

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllEnvs();
});

function stubReportedZone(timeZone: string) {
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
    timeZone,
  } as Intl.ResolvedDateTimeFormatOptions);
}

describe("formatInstant", () => {
  it.each([
    ["UTC", "2025-06-01T10:00:00Z", "UTC", "1 Jun 2025, 10:00:00 UTC"],
    ["an hour-offset zone in summer", "2025-06-01T10:00:00Z", "Europe/Warsaw", "1 Jun 2025, 12:00:00 Europe/Warsaw"],
    ["an hour-offset zone in winter", "2025-01-15T10:00:00Z", "Europe/Warsaw", "15 Jan 2025, 11:00:00 Europe/Warsaw"],
    ["a zone west of UTC", "2025-06-01T10:00:00Z", "America/New_York", "1 Jun 2025, 06:00:00 America/New_York"],
    ["a half-hour zone", "2025-06-01T10:00:00Z", "Asia/Kolkata", "1 Jun 2025, 15:30:00 Asia/Kolkata"],
    ["the next day over the date line", "2025-06-01T20:00:00Z", "Pacific/Auckland", "2 Jun 2025, 08:00:00 Pacific/Auckland"],
    ["a non-UTC offset in the input", "2025-06-01T12:00:00+02:00", "UTC", "1 Jun 2025, 10:00:00 UTC"],
  ])("formats %s", (_name, iso, zone, want) => {
    expect(formatInstant(iso, zone, british)).toBe(want);
  });

  it.each([
    ["the last second before Warsaw's clocks go forward", "2025-03-30T00:59:59Z", "Europe/Warsaw", "30 Mar 2025, 01:59:59 Europe/Warsaw"],
    ["the first second after Warsaw's clocks go forward", "2025-03-30T01:00:00Z", "Europe/Warsaw", "30 Mar 2025, 03:00:00 Europe/Warsaw"],
    ["the last second of summer time in Warsaw", "2025-10-26T00:59:59Z", "Europe/Warsaw", "26 Oct 2025, 02:59:59 Europe/Warsaw"],
    ["the first second of winter time in Warsaw", "2025-10-26T01:00:00Z", "Europe/Warsaw", "26 Oct 2025, 02:00:00 Europe/Warsaw"],
    ["the last second before New York's clocks go forward", "2025-03-09T06:59:59Z", "America/New_York", "9 Mar 2025, 01:59:59 America/New_York"],
    ["the first second after New York's clocks go forward", "2025-03-09T07:00:00Z", "America/New_York", "9 Mar 2025, 03:00:00 America/New_York"],
  ])("crosses a daylight-saving boundary: %s", (_name, iso, zone, want) => {
    expect(formatInstant(iso, zone, british)).toBe(want);
  });

  it("drops the seconds on request and keeps the zone", () => {
    expect(formatInstant("2025-06-01T10:00:30Z", "Europe/Warsaw", { ...british, seconds: false })).toBe(
      "1 Jun 2025, 12:00 Europe/Warsaw"
    );
  });

  it("names the zone in any locale", () => {
    for (const locale of ["en-US", "de-DE", "ja-JP"]) {
      expect(formatInstant("2025-06-01T10:00:00Z", "Europe/Warsaw", { locale })).toMatch(/ Europe\/Warsaw$/);
    }
  });

  it("does not depend on the process's own zone", () => {
    const before = formatInstant("2025-06-01T10:00:00Z", "Europe/Warsaw", british);
    vi.stubEnv("TZ", "Pacific/Auckland");
    expect(formatInstant("2025-06-01T10:00:00Z", "Europe/Warsaw", british)).toBe(before);
  });

  it("does not throw on an unreadable value", () => {
    expect(formatInstant("not a time", "UTC")).toBe("Invalid date");
  });
});

describe("browserTimeZone", () => {
  it("returns the zone the browser reports", () => {
    stubReportedZone("Asia/Tokyo");
    expect(browserTimeZone()).toBe("Asia/Tokyo");
  });

  it("falls back to UTC when the browser cannot name its zone", () => {
    stubReportedZone("Etc/Unknown");
    expect(browserTimeZone()).toBe("UTC");
  });
});

describe("formatDuration", () => {
  it.each([
    [0, "0 s"],
    [0.4, "under 1 s"],
    [1, "1 s"],
    [45, "45 s"],
    [59.6, "1 min"],
    [60, "1 min"],
    [90, "1 min 30 s"],
    [3600, "1 h"],
    [3605, "1 h 5 s"],
    [3725, "1 h 2 min"],
    [5400, "1 h 30 min"],
    [20700, "5 h 45 min"],
    [86400, "1 d"],
    [90000, "1 d 1 h"],
    [31536000, "365 d"],
  ])("formats %d seconds", (seconds, want) => {
    expect(formatDuration(seconds)).toBe(want);
  });

  it.each([
    [-90, "1 min 30 s"],
    [-0.4, "under 1 s"],
    [-3600, "1 h"],
  ])("ignores the sign of %d", (seconds, want) => {
    expect(formatDuration(seconds)).toBe(want);
  });
});

describe("formatSpan", () => {
  const day = 86400;
  it.each([
    ["a short span exactly", 3725, "1 h 2 min"],
    ["59 days exactly", 59 * day + 7 * 3600, "59 d 7 h"],
    ["60 days in months", 60 * day, "about 2 months"],
    ["396 days in months", 396 * day, "about 13 months"],
    ["just under two years in months", 729 * day, "about 24 months"],
    ["two years in years", 731 * day, "about 2 years"],
    ["a GPS week rollover in years", 1024 * 7 * day, "about 20 years"],
    ["4887 days in years", 4887 * day + 7 * 3600, "about 13 years"],
  ])("formats %s", (_name, seconds, want) => {
    expect(formatSpan(seconds)).toBe(want);
  });

  it("ignores the sign", () => {
    expect(formatSpan(-396 * day)).toBe("about 13 months");
  });
});

describe("formatOffset", () => {
  it.each([
    [0, "+00:00"],
    [60, "+01:00"],
    [330, "+05:30"],
    [345, "+05:45"],
    [-210, "-03:30"],
    [-600, "-10:00"],
    [-720, "-12:00"],
    [840, "+14:00"],
  ])("%d minutes", (minutes, want) => {
    expect(formatOffset(minutes)).toBe(want);
  });
});
