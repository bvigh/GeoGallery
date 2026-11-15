import { afterEach, describe, expect, it, vi } from "vitest";
import {
  EMPTY_SHIFT,
  earliestItem,
  FOLLOWUP_FAILED,
  FOLLOWUP_FAILED_SHIFT,
  formatDayRange,
  formatShift,
  formatShiftAmount,
  itemCount,
  joinWall,
  parseZoneChoice,
  repeatedWallClock,
  SHIFT_OUTCOME_UNKNOWN,
  shiftCandidates,
  shiftedMessage,
  shiftInstant,
  shiftSeconds,
  shiftStaysInRange,
  splitWall,
  takenAtZoneLabel,
  timeSavedMessage,
  wallClockIn,
  zoneChoiceValue,
  type ShiftAmount,
} from "@/lib/timeEdit";
import { formatCount } from "@/lib/numbers";
import type { MediaItem } from "@/types";

afterEach(() => {
  vi.unstubAllEnvs();
});

// CI runners are UTC, so every expectation names its zone.
describe("wallClockIn", () => {
  it.each([
    ["UTC", "2019-03-13T18:28:15Z", "UTC", "2019-03-13T18:28:15"],
    ["an hour-offset zone in winter", "2019-03-13T18:28:15Z", "Europe/Warsaw", "2019-03-13T19:28:15"],
    ["an hour-offset zone in summer", "2019-07-13T18:28:15Z", "Europe/Warsaw", "2019-07-13T20:28:15"],
    ["a zone west of UTC, the day before", "2019-03-13T02:00:00Z", "America/New_York", "2019-03-12T22:00:00"],
    ["a half-hour zone", "2019-03-13T18:28:15Z", "Asia/Kolkata", "2019-03-13T23:58:15"],
    ["midnight as 00, not 24", "2019-03-13T23:00:00Z", "Europe/Warsaw", "2019-03-14T00:00:00"],
    ["a fraction of a second dropped", "2019-03-13T18:28:15.987654321Z", "UTC", "2019-03-13T18:28:15"],
    ["a non-UTC offset in the input", "2019-03-13T20:28:15+02:00", "UTC", "2019-03-13T18:28:15"],
  ])("reads %s", (_name, iso, zone, want) => {
    expect(wallClockIn(iso, zone)).toBe(want);
  });

  it.each([
    ["the last second before Warsaw's clocks go forward", "2025-03-30T00:59:59Z", "2025-03-30T01:59:59"],
    ["the first second after Warsaw's clocks go forward", "2025-03-30T01:00:00Z", "2025-03-30T03:00:00"],
    ["the last second of summer time in Warsaw", "2025-10-26T00:59:59Z", "2025-10-26T02:59:59"],
    ["the first second of winter time in Warsaw", "2025-10-26T01:00:00Z", "2025-10-26T02:00:00"],
  ])("crosses a daylight-saving boundary: %s", (_name, iso, want) => {
    expect(wallClockIn(iso, "Europe/Warsaw")).toBe(want);
  });

  it.each([
    ["a year below 1000, padded to four digits", "0999-03-13T12:00:00Z", "UTC", "0999-03-13T12:00:00"],
    ["year 1", "0001-01-01T00:00:00Z", "UTC", "0001-01-01T00:00:00"],
    ["the last year the backend stores", "9998-12-31T23:59:59Z", "UTC", "9998-12-31T23:59:59"],
  ])("pads %s", (_name, iso, zone, want) => {
    expect(wallClockIn(iso, zone)).toBe(want);
  });

  it.each([
    ["not an instant", "yesterday", "UTC"],
    ["an unknown zone", "2019-03-13T18:28:15Z", "Mars/Olympus"],
    ["a year before 1, which reads as 1 BC", "0000-06-01T00:00:00Z", "UTC"],
    ["a year the backend cannot store", "9999-06-01T00:00:00Z", "UTC"],
  ])("gives null for %s", (_name, iso, zone) => {
    expect(wallClockIn(iso, zone)).toBeNull();
  });

  it("does not depend on the process's own zone", () => {
    const before = wallClockIn("2019-03-13T18:28:15Z", "Europe/Warsaw");
    vi.stubEnv("TZ", "Pacific/Auckland");
    expect(wallClockIn("2019-03-13T18:28:15Z", "Europe/Warsaw")).toBe(before);
  });
});

describe("repeatedWallClock", () => {
  // Read back in the zone, a repeated wall clock names the earlier instant (the backend's choice);
  // with its own offset it names the instant it came from.
  function roundTrip(iso: string, zone: string): number {
    const wall = wallClockIn(iso, zone)!;
    return Date.parse(`${wall}Z`) - repeatedWallClock(iso, zone)!.offsetMin * 60000;
  }

  it.each([
    // Warsaw's clocks went back from 03:00 CEST to 02:00 CET on 27 October 2024 (01:00 UTC).
    ["the second 02:30 in Warsaw, in winter time", "2024-10-27T01:30:00Z", "Europe/Warsaw", 60],
    ["the first 02:30 in Warsaw, in summer time", "2024-10-27T00:30:00Z", "Europe/Warsaw", 120],
    ["the first second of the repeated hour", "2024-10-27T00:00:00Z", "Europe/Warsaw", 120],
    ["the same wall clock again", "2024-10-27T01:00:00Z", "Europe/Warsaw", 60],
    ["the last second of the repeated hour", "2024-10-27T01:59:59Z", "Europe/Warsaw", 60],
    ["a fraction of a second, dropped", "2024-10-27T01:30:00.640Z", "Europe/Warsaw", 60],
    ["01:30 in New York, the first", "2024-11-03T05:30:00Z", "America/New_York", -240],
    ["01:30 in New York, the second", "2024-11-03T06:30:00Z", "America/New_York", -300],
    // Lord Howe Island moves its clocks by half an hour.
    ["01:45 on Lord Howe Island, the first", "2024-04-06T14:45:00Z", "Australia/Lord_Howe", 660],
    ["01:45 on Lord Howe Island, the second", "2024-04-06T15:15:00Z", "Australia/Lord_Howe", 630],
  ])("finds %s repeated, with the instant's own offset", (_name, iso, zone, offsetMin) => {
    expect(repeatedWallClock(iso, zone)).toEqual({ repeated: true, offsetMin });
    expect(roundTrip(iso, zone)).toBe(Math.floor(Date.parse(iso) / 1000) * 1000);
  });

  it.each([
    ["an ordinary winter time", "2024-01-15T12:00:00Z", "Europe/Warsaw", 60],
    ["an ordinary summer time", "2024-07-15T12:00:00Z", "Europe/Warsaw", 120],
    ["the last second before the repeated hour", "2024-10-26T23:59:59Z", "Europe/Warsaw", 120],
    ["the first second after it", "2024-10-27T02:00:00Z", "Europe/Warsaw", 60],
    ["the hour after the clocks went forward", "2025-03-30T01:00:00Z", "Europe/Warsaw", 120],
    ["UTC", "2024-10-27T01:30:00Z", "UTC", 0],
  ])("finds %s shown once", (_name, iso, zone, offsetMin) => {
    expect(repeatedWallClock(iso, zone)).toEqual({ repeated: false, offsetMin });
  });

  it.each([
    ["not an instant", "yesterday", "UTC"],
    ["an unknown zone", "2024-10-27T01:30:00Z", "Mars/Olympus"],
    ["a year the backend cannot store", "9999-06-01T00:00:00Z", "UTC"],
  ])("gives null for %s", (_name, iso, zone) => {
    expect(repeatedWallClock(iso, zone)).toBeNull();
  });

  it("does not depend on the process's own zone", () => {
    vi.stubEnv("TZ", "Pacific/Auckland");
    expect(repeatedWallClock("2024-10-27T01:30:00Z", "Europe/Warsaw")).toEqual({ repeated: true, offsetMin: 60 });
  });
});

describe("splitWall and joinWall", () => {
  it("splits a wall clock into a date and a time field, and joins them back", () => {
    expect(splitWall("2019-03-13T18:28:15")).toEqual({ date: "2019-03-13", time: "18:28:15" });
    expect(joinWall("2019-03-13", "18:28:15")).toBe("2019-03-13T18:28:15");
    expect(splitWall("0999-03-13T00:00:07")).toEqual({ date: "0999-03-13", time: "00:00:07" });
    expect(joinWall("0999-03-13", "00:00:07")).toBe("0999-03-13T00:00:07");
  });

  it("gives empty fields for no wall clock", () => {
    expect(splitWall(null)).toEqual({ date: "", time: "" });
  });

  it.each([
    ["seconds left out, as some browsers write zero seconds", "2019-03-13", "18:28", "2019-03-13T18:28:00"],
    ["a fraction dropped", "2019-03-13", "18:28:15.250", "2019-03-13T18:28:15"],
    ["a leap day", "2020-02-29", "12:00:00", "2020-02-29T12:00:00"],
    ["a leap day of a year divisible by 400", "2000-02-29", "12:00:00", "2000-02-29T12:00:00"],
    ["the last moment the backend takes", "9998-12-31", "23:59:59", "9998-12-31T23:59:59"],
  ])("joins %s", (_name, date, time, want) => {
    expect(joinWall(date, time)).toBe(want);
  });

  it.each([
    ["an empty date", "", "18:28:15"],
    ["an empty time", "2019-03-13", ""],
    ["a day the month lacks", "2019-02-29", "12:00:00"],
    ["the leap day of a century", "1900-02-29", "12:00:00"],
    ["the 31st of a 30-day month", "2019-04-31", "12:00:00"],
    ["month 13", "2019-13-01", "12:00:00"],
    ["day 0", "2019-03-00", "12:00:00"],
    ["year 0", "0000-03-13", "12:00:00"],
    ["year 9999", "9999-03-13", "12:00:00"],
    ["a five-digit year", "10000-03-13", "12:00:00"],
    ["hour 24", "2019-03-13", "24:00:00"],
    ["minute 60", "2019-03-13", "12:60:00"],
    ["second 60", "2019-03-13", "12:00:60"],
    ["another layout", "13/03/2019", "12:00:00"],
  ])("refuses %s", (_name, date, time) => {
    expect(joinWall(date, time)).toBeNull();
  });
});

describe("the zone choice", () => {
  it.each([
    [{ zone: "Europe/Warsaw" }, "Europe/Warsaw", "Europe/Warsaw"],
    [{ zone: "Etc/GMT+5" }, "Etc/GMT+5", "Etc/GMT+5"],
    [{ utcOffsetMin: 120 }, "offset:120", "UTC+02:00"],
    [{ utcOffsetMin: 0 }, "offset:0", "UTC+00:00"],
    [{ utcOffsetMin: -210 }, "offset:-210", "UTC-03:30"],
    [{ utcOffsetMin: 840 }, "offset:840", "UTC+14:00"],
  ])("encodes %o as a select value and back", (zone, value, label) => {
    expect(zoneChoiceValue(zone)).toBe(value);
    expect(parseZoneChoice(value)).toEqual(zone);
    expect(takenAtZoneLabel(zone)).toBe(label);
  });

  it.each(["", "offset:", "offset:7", "offset:900", "offset:-735", "offset:abc", "offset:1.5"])(
    "refuses %j",
    (value) => {
      expect(parseZoneChoice(value)).toBeNull();
    }
  );
});

describe("shiftSeconds", () => {
  const amount = (fields: Partial<ShiftAmount>): ShiftAmount => ({ ...EMPTY_SHIFT, ...fields });

  it.each([
    ["nothing typed", amount({}), 0],
    ["an hour later", amount({ hours: "1" }), 3600],
    ["an hour earlier", amount({ direction: "earlier", hours: "1" }), -3600],
    ["every unit", amount({ days: "1", hours: "2", minutes: "3", seconds: "4" }), 93784],
    ["a count past its unit", amount({ hours: "36" }), 129600],
    ["surrounding space", amount({ minutes: " 30 " }), 1800],
    ["leading zeros", amount({ seconds: "007" }), 7],
    ["zero earlier, which is no shift", amount({ direction: "earlier", hours: "0" }), 0],
  ])("reads %s", (_name, value, want) => {
    expect(shiftSeconds(value)).toBe(want);
  });

  it.each([
    ["a negative count", amount({ hours: "-1" })],
    ["a fraction", amount({ hours: "1.5" })],
    ["an exponent", amount({ days: "1e3" })],
    ["a word", amount({ minutes: "ten" })],
  ])("refuses %s", (_name, value) => {
    expect(shiftSeconds(value)).toBeNull();
  });
});

describe("formatShift", () => {
  it.each([
    [3600, "1 h later"],
    [-3600, "1 h earlier"],
    [93784, "1 d 2 h 3 min 4 s later"],
    [-90061, "1 d 1 h 1 min 1 s earlier"],
    [86400 * 3 + 30, "3 d 30 s later"],
    [45, "45 s later"],
    [-7200, "2 h earlier"],
    // A stored fraction of a second, set to its whole second.
    [-0.64, "under 1 s earlier"],
    [0.4, "under 1 s later"],
    [1.4, "1 s later"],
  ])("states %d s exactly, with its direction", (seconds, want) => {
    expect(formatShift(seconds)).toBe(want);
  });

  it("states a size without a direction", () => {
    expect(formatShiftAmount(-5400)).toBe("1 h 30 min");
    expect(formatShiftAmount(0)).toBe("0 s");
    expect(formatShiftAmount(-0.123)).toBe("under 1 s");
  });
});

describe("shiftInstant", () => {
  it.each([
    ["later", "2019-03-13T18:28:15Z", 3600, "2019-03-13T19:28:15.000Z"],
    ["earlier, across a day", "2019-03-13T00:30:00Z", -3600, "2019-03-12T23:30:00.000Z"],
    ["into year 1", "0001-01-01T01:00:00Z", -3600, "0001-01-01T00:00:00.000Z"],
  ])("moves an instant %s", (_name, iso, delta, want) => {
    expect(shiftInstant(iso, delta)).toBe(want);
  });

  it.each([
    ["before year 1", "0001-01-01T00:00:00Z", -1],
    ["past year 9998", "9998-12-31T23:59:59Z", 1],
    ["not an instant", "never", 60],
  ])("gives null %s", (_name, iso, delta) => {
    expect(shiftInstant(iso, delta)).toBeNull();
  });
});

describe("itemCount", () => {
  it("counts in the browser's grouping, singular for one", () => {
    expect(itemCount(1)).toBe("1 item");
    expect(itemCount(0)).toBe("0 items");
    expect(itemCount(5000)).toBe(`${formatCount(5000)} items`);
  });
});

function item(id: string, fields: Partial<MediaItem>): MediaItem {
  return {
    id,
    filename: `${id}.jpg`,
    media_type: "photo",
    lat: null,
    lng: null,
    gps_confidence: null,
    gps_source: null,
    taken_at: null,
    storage_key: `media/me/${id}.jpg`,
    trip_id: null,
    owner_id: "me",
    owner_name: "Me",
    camera_model: null,
    ...fields,
  };
}

describe("shiftCandidates", () => {
  it("groups the caller's dated items by camera, most items first", () => {
    const items = [
      item("p1", { camera_model: "SM-S921B", taken_at: "2019-03-13T10:00:00Z" }),
      item("c1", { camera_model: "DC-TZ200", taken_at: "2019-03-14T09:00:00Z" }),
      item("c2", { camera_model: "DC-TZ200", taken_at: "2019-03-12T08:00:00Z" }),
      item("c3", { camera_model: " DC-TZ200 ", taken_at: "2019-03-13T12:00:00.5Z" }),
      item("u1", { camera_model: null, taken_at: "2019-03-13T11:00:00Z" }),
      item("u2", { camera_model: "  ", taken_at: "2019-03-13T13:00:00Z" }),
    ];
    const { groups, ownUndated, others } = shiftCandidates(items, "me");
    expect(ownUndated).toBe(0);
    expect(others).toBe(0);
    expect(groups.map((g) => [g.label, g.ids])).toEqual([
      ["DC-TZ200", ["c1", "c2", "c3"]],
      ["No camera model in the file: photos", ["u1", "u2"]],
      ["SM-S921B", ["p1"]],
    ]);
    expect(groups[0].key).toBe("DC-TZ200");
    expect(groups[0].first).toEqual({ id: "c2", filename: "c2.jpg", taken_at: "2019-03-12T08:00:00Z" });
    expect(groups[0].last).toBe("2019-03-14T09:00:00Z");
    expect(groups[1].key).toBe("\u0000photo");
  });

  // Ingest reads a model only from a photo's EXIF (or a Panasonic video's), so a phone's videos and
  // an off camera's videos both come without one: the kind at least tells them from the photos.
  it("splits items without a camera model by kind", () => {
    const items = [
      item("v1", { media_type: "video", taken_at: "2019-03-13T10:00:00Z" }),
      item("v2", { media_type: "video", taken_at: "2019-03-13T11:00:00Z" }),
      item("p1", { media_type: "photo", taken_at: "2019-03-13T12:00:00Z" }),
      item("s1", { media_type: "photo_360", taken_at: "2019-03-13T13:00:00Z" }),
      item("w1", { media_type: "video_360", taken_at: "2019-03-13T14:00:00Z" }),
      item("w2", { media_type: "video_360", taken_at: "2019-03-13T15:00:00Z", camera_model: "Insta360 X3" }),
    ];
    expect(shiftCandidates(items, "me").groups.map((g) => [g.label, g.ids])).toEqual([
      ["No camera model in the file: videos", ["v1", "v2"]],
      ["Insta360 X3", ["w2"]],
      ["No camera model in the file: 360° photos", ["s1"]],
      ["No camera model in the file: 360° videos", ["w1"]],
      ["No camera model in the file: photos", ["p1"]],
    ]);
    // No two groups share a key, and none is a model's.
    expect(new Set(shiftCandidates(items, "me").groups.map((g) => g.key)).size).toBe(5);
  });

  it("never mixes a model named like a kind with the items without one", () => {
    const items = [
      item("a", { media_type: "video", taken_at: "2019-03-13T10:00:00Z", camera_model: "video" }),
      item("b", { media_type: "video", taken_at: "2019-03-13T11:00:00Z" }),
    ];
    expect(shiftCandidates(items, "me").groups.map((g) => g.ids)).toEqual([["b"], ["a"]]);
  });

  it("orders groups of one size by name", () => {
    const items = [
      item("b", { camera_model: "iPhone 12", taken_at: "2019-03-13T10:00:00Z" }),
      item("a", { camera_model: "DC-TZ200", taken_at: "2019-03-13T10:00:00Z" }),
    ];
    expect(shiftCandidates(items, "me").groups.map((g) => g.label)).toEqual(["DC-TZ200", "iPhone 12"]);
  });

  it("leaves out undated items and other people's items, counting them", () => {
    const items = [
      item("mine", { camera_model: "DC-TZ200", taken_at: "2019-03-13T10:00:00Z" }),
      item("undated", { camera_model: "DC-TZ200" }),
      item("theirs", { camera_model: "DC-TZ200", taken_at: "2019-03-13T10:00:00Z", owner_id: "bob" }),
      item("theirs-undated", { owner_id: "bob" }),
    ];
    const { groups, ownUndated, others } = shiftCandidates(items, "me");
    expect(groups.map((g) => g.ids)).toEqual([["mine"]]);
    expect(ownUndated).toBe(1);
    expect(others).toBe(2);
  });

  it("treats items without camera_model from an older backend as having no model", () => {
    const old = item("o1", { taken_at: "2019-03-13T10:00:00Z" });
    delete old.camera_model;
    expect(shiftCandidates([old], "me").groups.map((g) => g.label)).toEqual(["No camera model in the file: photos"]);
  });

  it("finds the earliest item across groups, and checks the shift keeps every time storable", () => {
    const { groups } = shiftCandidates(
      [
        item("a", { camera_model: "A", taken_at: "2019-03-13T10:00:00Z" }),
        item("b", { camera_model: "B", taken_at: "2019-03-12T10:00:00Z" }),
        item("b2", { camera_model: "B", taken_at: "9998-12-31T23:00:00Z" }),
      ],
      "me"
    );
    expect(earliestItem(groups)?.id).toBe("b");
    expect(earliestItem([])).toBeNull();
    expect(shiftStaysInRange(groups, 3599)).toBe(true);
    expect(shiftStaysInRange(groups, 3600)).toBe(false);
    expect(shiftStaysInRange(groups.filter((g) => g.label === "A"), 3600)).toBe(true);
  });
});

describe("formatDayRange", () => {
  it.each([
    ["two days", "2019-03-12T08:00:00Z", "2019-03-14T09:00:00Z", "Europe/Warsaw", "12 Mar 2019 – 14 Mar 2019 Europe/Warsaw"],
    ["one day", "2019-03-12T08:00:00Z", "2019-03-12T20:00:00Z", "Europe/Warsaw", "12 Mar 2019 Europe/Warsaw"],
    ["one UTC day that is two in the zone", "2019-03-12T01:00:00Z", "2019-03-12T23:30:00Z", "Europe/Warsaw", "12 Mar 2019 – 13 Mar 2019 Europe/Warsaw"],
    ["the day before west of UTC", "2019-03-12T02:00:00Z", "2019-03-12T03:00:00Z", "America/New_York", "11 Mar 2019 America/New_York"],
  ])("names %s and the zone", (_name, first, last, zone, want) => {
    expect(formatDayRange(first, last, zone, "en-GB")).toBe(want);
  });
});

describe("the notices after a change", () => {
  const shifted = { shifted: 3, undated: 0, trips: [{ id: "t1", items: 3 }], followup_failed: false };

  it("says how many items moved, and how far", () => {
    expect(shiftedMessage(shifted, 3600)).toBe("Shifted 3 items 1 h later.");
    expect(shiftedMessage({ ...shifted, shifted: 1 }, -90)).toBe("Shifted 1 item 1 min 30 s earlier.");
  });

  it("names the items left as they were for having no time", () => {
    expect(shiftedMessage({ ...shifted, undated: 1 }, 3600)).toBe(
      "Shifted 3 items 1 h later. 1 item without a capture time was left as before."
    );
    expect(shiftedMessage({ ...shifted, undated: 2 }, 3600)).toBe(
      "Shifted 3 items 1 h later. 2 items without a capture time were left as before."
    );
  });

  it("says when the follow-up failed, after a shift or an edit", () => {
    expect(shiftedMessage({ ...shifted, followup_failed: true }, 3600)).toBe(
      `Shifted 3 items 1 h later. ${FOLLOWUP_FAILED_SHIFT}`
    );
    expect(timeSavedMessage({ taken_at: "2019-03-13T17:28:15Z", trip_id: null, followup_failed: true })).toBe(
      FOLLOWUP_FAILED
    );
  });

  // The change cleared the estimates near both times before the follow-up failed.
  it("says what the failed follow-up left undone, and what puts it right", () => {
    expect(FOLLOWUP_FAILED).toBe(
      "The time is saved, but estimated locations near the old and new times were cleared and not placed again, " +
        "and trips were not detected again. In Why here?, Re-match my library places items that wait for an " +
        "estimate; trips are detected again after your next upload or time change."
    );
    expect(FOLLOWUP_FAILED_SHIFT).toBe(FOLLOWUP_FAILED.replace("The time is saved", "The times are saved"));
  });

  it("never asks to shift again after an answer that did not come", () => {
    expect(SHIFT_OUTCOME_UNKNOWN).toMatch(/may have been saved/);
    expect(SHIFT_OUTCOME_UNKNOWN).not.toMatch(/try again/i);
  });

  it("adds nothing to an edit whose follow-up ran", () => {
    expect(timeSavedMessage({ taken_at: "2019-03-13T17:28:15Z", trip_id: "t1", followup_failed: false })).toBeNull();
  });
});
