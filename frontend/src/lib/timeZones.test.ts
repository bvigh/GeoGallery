import { afterEach, describe, expect, it, vi } from "vitest";
import { SERVER_ZONE_UNSET, serverZoneLabel, timeZoneNames } from "@/lib/timeZones";

describe("timeZoneNames", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("lists the browser's zones with UTC, sorted, each once", () => {
    vi.spyOn(Intl as unknown as { supportedValuesOf: (key: string) => string[] }, "supportedValuesOf").mockReturnValue([
      "Europe/Warsaw",
      "Africa/Abidjan",
      "Asia/Tokyo",
    ]);
    expect(timeZoneNames()).toEqual(["Africa/Abidjan", "Asia/Tokyo", "Europe/Warsaw", "UTC"]);
  });

  it("keeps the given names, so a stored value is always an option", () => {
    vi.spyOn(Intl as unknown as { supportedValuesOf: (key: string) => string[] }, "supportedValuesOf").mockReturnValue([
      "Asia/Kolkata",
      "UTC",
    ]);
    expect(timeZoneNames("Asia/Calcutta", null, undefined, "", "UTC")).toEqual(["Asia/Calcutta", "Asia/Kolkata", "UTC"]);
  });

  it("offers UTC and the given names in a browser without the list", () => {
    const intl = Intl as unknown as { supportedValuesOf?: unknown };
    const original = intl.supportedValuesOf;
    intl.supportedValuesOf = undefined;
    try {
      expect(timeZoneNames("Europe/Warsaw")).toEqual(["Europe/Warsaw", "UTC"]);
    } finally {
      intl.supportedValuesOf = original;
    }
  });

  it("lists real zones in this environment", () => {
    expect(timeZoneNames()).toContain("Europe/Warsaw");
  });
});

describe("serverZoneLabel", () => {
  it("names the zone, or the server's own when CAMERA_TIMEZONE is unset", () => {
    expect(serverZoneLabel("Europe/Warsaw")).toBe("Europe/Warsaw");
    expect(serverZoneLabel(SERVER_ZONE_UNSET)).toBe("the server's own zone");
  });
});
