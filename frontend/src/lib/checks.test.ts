import { describe, expect, it } from "vitest";
import { makeAgreement, makeAgreementFile, makeComparison, makeFile, makeGroup } from "@/components/checks/checksTestUtils";
import {
  agreementCell,
  bySeverity,
  canResume,
  cappedNote,
  checkTitle,
  checkTotal,
  COMPARE_BUSY,
  COMPARE_NOTE,
  compareStage,
  compareStatusText,
  COMPARING,
  DEFAULT_FILE_SORT,
  fileName,
  fileRows,
  findingText,
  formatDistance,
  formatShare,
  kindName,
  madeProgress,
  matchesFile,
  mergeComparison,
  nameParts,
  nextFileSort,
  NO_COMPARISON,
  NOT_COMPARED,
  NOT_REACHED,
  NOT_YET_COMPARED,
  openLabel,
  sortFiles,
  spacingCell,
  TRUNCATED_NOTE,
  type AgreementState,
  type CompareStage,
  type FileColumn,
  type FileRow,
  type SortDirection,
} from "@/lib/checks";
import { formatCount } from "@/lib/numbers";
import type { CheckCode, ChecksCheck, ChecksGroup } from "@/types";

const ESTIMATED_TIMES = "This format's times may be estimated by the exporter.";

describe("checkTitle", () => {
  it.each<[CheckCode, string]>([
    ["pair_impossible", "Impossible speeds between formats"],
    ["future_track_points", "Track points in the future"],
    ["taken_after_upload", "Items dated after their upload"],
    ["stale_estimates", "Out-of-date estimates"],
    ["pair_far", "Far-apart points from different formats"],
    ["orphan_points", "Points left by deleted items"],
    // Titled by what it counts, so its total and "Nothing found for" stay true for an estimate that
    // only these formats can place.
    ["time_may_be_estimated_used", "Estimates on KML, KMZ or GeoJSON points that other formats could place"],
  ])("%s", (code, want) => {
    expect(checkTitle(code)).toBe(want);
  });

  it("shows a code it does not know as written, even one named like an object's own property", () => {
    expect(checkTitle("clock_drift" as CheckCode)).toBe("clock_drift");
    expect(checkTitle("constructor" as CheckCode)).toBe("constructor");
  });
});

describe("checkTotal", () => {
  it.each<[CheckCode, number, string]>([
    ["pair_impossible", 1, "1 estimate"],
    ["pair_impossible", 12, "12 estimates"],
    ["pair_far", 2, "2 estimates"],
    ["stale_estimates", 1, "1 estimate"],
    ["time_may_be_estimated_used", 3, "3 estimates"],
    ["future_track_points", 1, "1 point"],
    // Grouped in the browser's locale: "27,558 points" in English.
    ["future_track_points", 27558, `${formatCount(27558)} points`],
    ["future_track_points", 6246824, `${formatCount(6246824)} points`],
    ["orphan_points", 34, "34 points"],
    ["taken_after_upload", 1, "1 item"],
    ["taken_after_upload", 6, "6 items"],
    ["clock_drift" as CheckCode, 3, "3 found"],
    ["clock_drift" as CheckCode, 12000, `${formatCount(12000)} found`],
  ])("%s with %d", (code, total, want) => {
    expect(checkTotal({ code, total })).toBe(want);
  });

  it("groups a count's digits", () => {
    expect(formatCount(27558)).not.toBe("27558");
  });
});

describe("bySeverity", () => {
  const check = (code: CheckCode, severity: ChecksCheck["severity"]): ChecksCheck => ({ code, severity, total: 0, groups: [] });

  it("puts high severity first and keeps the server's order within each", () => {
    const checks = [
      check("pair_far", "low"),
      check("future_track_points", "high"),
      check("orphan_points", "low"),
      check("stale_estimates", "high"),
    ];
    expect(bySeverity(checks).map((c) => c.code)).toEqual(["future_track_points", "stale_estimates", "pair_far", "orphan_points"]);
    expect(checks[0].code).toBe("pair_far");
  });
});

describe("formatDistance", () => {
  it.each([
    [0, "0 m"],
    [12.4, "12 m"],
    [999.4, "999 m"],
    [999.6, "1.0 km"],
    [1000, "1.0 km"],
    [1500, "1.5 km"],
    [9949, "9.9 km"],
    [9950, "10 km"],
    [15234, "15 km"],
    [152345, "152 km"],
    [3000000, "3000 km"],
  ])("%d m", (meters, want) => {
    expect(formatDistance(meters)).toBe(want);
  });
});

describe("formatShare", () => {
  it.each([
    [1, "100%"],
    [0.9999, "99%"],
    [0.94, "94%"],
    [0.57, "57%"],
    [0.29, "29%"],
    [0.01, "1%"],
    [0.004, "under 1%"],
    [0, "0%"],
  ])("%d", (share, want) => {
    expect(formatShare(share)).toBe(want);
  });
});

describe("fileName", () => {
  it.each([
    ["exif", "Positions of your photos"],
    ["manual", "Your pins"],
    ["gpx:ride.gpx", "ride.gpx"],
    ["geogallery:a:b", "a:b"],
    ["exif:old.jpg", "old.jpg"],
    ["mytracker", "mytracker"],
    ["gpx:", "gpx:"],
    ["constructor", "constructor"],
  ])("%s", (source, want) => {
    expect(fileName(source)).toBe(want);
  });
});

describe("kindName", () => {
  it.each([
    ["recorded", "recorded"],
    ["semantic", "semantic"],
    ["time_may_be_estimated", "time may be estimated"],
    ["derived", "derived"],
    ["other", "other"],
  ])("%s", (kind, want) => {
    expect(kindName(kind)).toBe(want);
  });
});

describe("findingText", () => {
  const group = (overrides: Partial<ChecksGroup>) => makeGroup(overrides);

  describe("pair_impossible", () => {
    const pair = { key: "esoko+gpx", label: "esoko+gpx" };

    it.each<[string, Partial<ChecksGroup>, string, string[]]>([
      [
        "one estimate",
        { ...pair, count: 1, evidence: { max_kmh: 412.4, max_km: 3.2, top_source: "gpx:ride.gpx", top_source_estimates: 1 } },
        "1 estimate lies between points from esoko and gpx that would need 412 km/h to connect, so at least one of the two is unreliable at that moment.",
        [],
      ],
      [
        "several, most from one file",
        { ...pair, count: 3, evidence: { max_kmh: 980.6, max_km: 9, top_source: "esoko:car.log", top_source_estimates: 2 } },
        "3 estimates lie between points from esoko and gpx that would need up to 981 km/h to connect, so at least one point of each pair is unreliable at those moments.",
        ["Most often from car.log (2 estimates)."],
      ],
      [
        "several, most from photos",
        { ...pair, count: 4, evidence: { max_kmh: 200, max_km: 2, top_source: "exif", top_source_estimates: 4 } },
        "4 estimates lie between points from esoko and gpx that would need up to 200 km/h to connect, so at least one point of each pair is unreliable at those moments.",
        ["Most often from your photos (4 estimates)."],
      ],
      [
        "several, no file used twice",
        { ...pair, count: 2, evidence: { max_kmh: 200, max_km: 2, top_source: "esoko:car.log", top_source_estimates: 1 } },
        "2 estimates lie between points from esoko and gpx that would need up to 200 km/h to connect, so at least one point of each pair is unreliable at those moments.",
        [],
      ],
      [
        // Every estimate rests on the same two files, so neither stands out: the server sends none.
        "several, two files tied",
        { ...pair, count: 5, evidence: { max_kmh: 200, max_km: 2, top_source: null, top_source_estimates: null } },
        "5 estimates lie between points from esoko and gpx that would need up to 200 km/h to connect, so at least one point of each pair is unreliable at those moments.",
        [],
      ],
      [
        "several, most from one file, counted in thousands",
        { ...pair, count: 1500, evidence: { max_kmh: 200, max_km: 2, top_source: "esoko:car.log", top_source_estimates: 1200 } },
        `${formatCount(1500)} estimates lie between points from esoko and gpx that would need up to 200 km/h to connect, so at least one point of each pair is unreliable at those moments.`,
        [`Most often from car.log (${formatCount(1200)} estimates).`],
      ],
      [
        "no speed in the evidence",
        { ...pair, count: 1, evidence: { top_source: null, top_source_estimates: null } },
        "1 estimate lies between points from esoko and gpx that would need an impossible speed to connect, so at least one of the two is unreliable at that moment.",
        [],
      ],
      [
        "a prefix holding a plus sign",
        { key: "a+b+gpx", label: "a+b+gpx", count: 1, evidence: { max_kmh: 200 } },
        "1 estimate lies between points from a+b+gpx that would need 200 km/h to connect, so at least one of the two is unreliable at that moment.",
        [],
      ],
    ])("%s", (_name, overrides, sentence, notes) => {
      expect(findingText("pair_impossible", group(overrides))).toEqual({ sentence, notes, instants: [] });
    });
  });

  describe("pair_far", () => {
    const pair = { key: "gpx+manual", label: "gpx+manual" };

    it.each<[string, Partial<ChecksGroup>, string, string[]]>([
      [
        "one estimate",
        { ...pair, count: 1, evidence: { max_km: 1.5, top_source: "manual", top_source_estimates: 1 } },
        "1 estimate lies between points from gpx and manual that are 1.5 km apart, so the position between them is uncertain.",
        [],
      ],
      [
        "several",
        { ...pair, count: 5, evidence: { max_km: 42.4, top_source: "manual", top_source_estimates: 3 } },
        "5 estimates lie between points from gpx and manual that are up to 42 km apart, so the positions between them are uncertain.",
        ["Most often from your pins (3 estimates)."],
      ],
      [
        "several, two files tied",
        { ...pair, count: 4, evidence: { max_km: 3, top_source: null, top_source_estimates: null } },
        "4 estimates lie between points from gpx and manual that are up to 3.0 km apart, so the positions between them are uncertain.",
        [],
      ],
      [
        "no distance in the evidence",
        { ...pair, count: 2, evidence: {} },
        "2 estimates lie between points from gpx and manual that are far apart, so the positions between them are uncertain.",
        [],
      ],
    ])("%s", (_name, overrides, sentence, notes) => {
      expect(findingText("pair_far", group(overrides))).toEqual({ sentence, notes, instants: [] });
    });
  });

  describe("future_track_points", () => {
    const first = "2040-01-01T00:00:00Z";
    const last = "2040-01-02T00:00:00Z";
    const receiver = "A receiver that kept the wrong GPS week writes such times.";

    it.each<[string, Partial<ChecksGroup>, string]>([
      [
        "one point of a log",
        { key: "nmea:drive.nmea", label: "nmea:drive.nmea", count: 1, evidence: { first, last: first } },
        `1 point of drive.nmea is dated more than a day in the future. ${receiver}`,
      ],
      [
        "several points of a log",
        { key: "nmea:drive.nmea", label: "nmea:drive.nmea", count: 27, evidence: { first, last } },
        `27 points of drive.nmea are dated more than a day in the future. ${receiver}`,
      ],
      [
        "a log's points counted in thousands",
        { key: "nmea:drive.nmea", label: "nmea:drive.nmea", count: 27558, evidence: { first, last } },
        `${formatCount(27558)} points of drive.nmea are dated more than a day in the future. ${receiver}`,
      ],
      [
        "one point of a photo",
        { key: "exif", label: "exif", count: 1, evidence: { first, last: first } },
        "1 point written from one of your photos is dated more than a day in the future, as that photo is.",
      ],
      [
        "several points of photos",
        { key: "exif", label: "exif", count: 3, evidence: { first, last } },
        "3 points written from your photos are dated more than a day in the future, as those photos are.",
      ],
      [
        "one point of a pin",
        { key: "manual", label: "manual", count: 1, evidence: { first, last: first } },
        "1 point written from one of your pins is dated more than a day in the future, as the item you placed is.",
      ],
      [
        "several points of pins",
        { key: "manual", label: "manual", count: 2, evidence: { first, last } },
        "2 points written from your pins are dated more than a day in the future, as the items you placed are.",
      ],
    ])("%s", (_name, overrides, sentence) => {
      expect(findingText("future_track_points", group(overrides))).toMatchObject({ sentence, notes: [] });
    });

    it("gives one point its time, and several their first and last", () => {
      expect(findingText("future_track_points", group({ count: 1, evidence: { first, last: first } })).instants).toEqual([
        { label: "Time", iso: first },
      ]);
      expect(findingText("future_track_points", group({ count: 2, evidence: { first, last } })).instants).toEqual([
        { label: "First", iso: first },
        { label: "Last", iso: last },
      ]);
    });

    it("keeps a time JSON could not carry as null", () => {
      expect(findingText("future_track_points", group({ count: 2, evidence: { first: null, last: null } })).instants).toEqual([
        { label: "First", iso: null },
        { label: "Last", iso: null },
      ]);
    });
  });

  describe("taken_after_upload", () => {
    const clock = "A correct clock cannot produce that.";

    it.each<[string, Partial<ChecksGroup>, string, string[]]>([
      [
        "one item without a location",
        { key: "DC-TZ200", label: "DC-TZ200", count: 1, evidence: { max_ahead_s: 90000, located: 0 } },
        `1 item from DC-TZ200 is dated 1 d 1 h after its upload. ${clock}`,
        ["It has no location."],
      ],
      [
        "one item with a location",
        { key: "DC-TZ200", label: "DC-TZ200", count: 1, evidence: { max_ahead_s: 3600, located: 1 } },
        `1 item from DC-TZ200 is dated 1 h after its upload. ${clock}`,
        ["It has a location."],
      ],
      [
        // GeoGallery stored no model (rows from before Step 1, most videos): unknown, not missing.
        "several whose camera model is not known",
        { key: "", label: "", count: 6, evidence: { max_ahead_s: 63072000, located: 0 } },
        `6 items whose camera model is not known are dated after their upload, up to about 24 months after. ${clock}`,
        ["None of them has a location."],
      ],
      [
        "one whose camera model is not known",
        { key: "", label: "", count: 1, evidence: { max_ahead_s: 7200, located: 1 } },
        `1 item whose camera model is not known is dated 2 h after its upload. ${clock}`,
        ["It has a location."],
      ],
      [
        "one from a camera years ahead",
        { key: "DC-TZ200", label: "DC-TZ200", count: 1, evidence: { max_ahead_s: 4887 * 86400, located: 0 } },
        `1 item from DC-TZ200 is dated about 13 years after its upload. ${clock}`,
        ["It has no location."],
      ],
      [
        "several a year ahead",
        { key: "DC-TZ200", label: "DC-TZ200", count: 4, evidence: { max_ahead_s: 396 * 86400, located: 0 } },
        `4 items from DC-TZ200 are dated after their upload, up to about 13 months after. ${clock}`,
        ["None of them has a location."],
      ],
      [
        "items counted in thousands, some located",
        { key: "SM-G991B", label: "SM-G991B", count: 2500, evidence: { max_ahead_s: 120, located: 1200 } },
        `${formatCount(2500)} items from SM-G991B are dated after their upload, up to 2 min after. ${clock}`,
        [`${formatCount(1200)} of them have a location.`],
      ],
      [
        "several, all located",
        { key: "SM-G991B", label: "SM-G991B", count: 2, evidence: { max_ahead_s: 120, located: 2 } },
        `2 items from SM-G991B are dated after their upload, up to 2 min after. ${clock}`,
        ["All of them have a location."],
      ],
      [
        "several, one located",
        { key: "SM-G991B", label: "SM-G991B", count: 3, evidence: { max_ahead_s: 120, located: 1 } },
        `3 items from SM-G991B are dated after their upload, up to 2 min after. ${clock}`,
        ["1 of them has a location."],
      ],
      [
        "several, some located",
        { key: "SM-G991B", label: "SM-G991B", count: 3, evidence: { max_ahead_s: 120, located: 2 } },
        `3 items from SM-G991B are dated after their upload, up to 2 min after. ${clock}`,
        ["2 of them have a location."],
      ],
      [
        "no evidence",
        { key: "DC-TZ200", label: "DC-TZ200", count: 1, evidence: {} },
        `1 item from DC-TZ200 is dated after its upload. ${clock}`,
        [],
      ],
    ])("%s", (_name, overrides, sentence, notes) => {
      expect(findingText("taken_after_upload", group(overrides))).toEqual({ sentence, notes, instants: [] });
    });
  });

  describe("stale_estimates", () => {
    const one = "Your tracks, this item's time or the match window changed since it was placed.";
    const several = "Your tracks, these items' times or the match window changed since they were placed.";

    it.each<[string, Partial<ChecksGroup>, string]>([
      [
        "one with no support",
        { key: "no_support", label: "no_support", count: 1 },
        "1 estimate has no track point within the match window any more. The matcher never clears a location, so it stays until you place it by hand.",
      ],
      [
        "several with no support",
        { key: "no_support", label: "no_support", count: 4 },
        "4 estimates have no track point within the match window any more. The matcher never clears a location, so they stay until you place them by hand.",
      ],
      [
        "one that would move",
        { key: "stale", label: "stale", count: 1, evidence: { max_delta_m: 1500 } },
        `1 estimate would move 1.5 km if re-matched. ${one}`,
      ],
      [
        "several that would move",
        { key: "stale", label: "stale", count: 5, evidence: { max_delta_m: 250 } },
        `5 estimates would move (up to 250 m) or change confidence if re-matched. ${several}`,
      ],
      [
        "thousands that would move",
        { key: "stale", label: "stale", count: 4500, evidence: { max_delta_m: 250 } },
        `${formatCount(4500)} estimates would move (up to 250 m) or change confidence if re-matched. ${several}`,
      ],
      [
        "one whose confidence alone would change",
        { key: "stale", label: "stale", count: 1, evidence: { max_delta_m: 0.4 } },
        `1 estimate would keep its position but change its confidence if re-matched. ${one}`,
      ],
      [
        "several within the 1 m tolerance",
        { key: "stale", label: "stale", count: 2, evidence: { max_delta_m: 1 } },
        `2 estimates would keep their positions but change their confidence if re-matched. ${several}`,
      ],
      [
        "several with no distance in the evidence",
        { key: "stale", label: "stale", count: 2, evidence: {} },
        `2 estimates would change if re-matched. ${several}`,
      ],
    ])("%s", (_name, overrides, sentence) => {
      expect(findingText("stale_estimates", group(overrides))).toEqual({ sentence, notes: [], instants: [] });
    });
  });

  describe("orphan_points", () => {
    it.each<[string, Partial<ChecksGroup>, string, string[]]>([
      [
        "one photo point that nothing rests on",
        { key: "exif", label: "exif", count: 1, evidence: { estimates: 0 } },
        "1 point written from one of your photos has no item at its time any more, so that photo was probably deleted.",
        ["No estimate rests on it."],
      ],
      [
        "several photo points",
        { key: "exif", label: "exif", count: 32, evidence: { estimates: 6 } },
        "32 points written from your photos have no item at their time any more, so those photos were probably deleted.",
        ["6 estimates still rest on them."],
      ],
      [
        "thousands of photo points",
        { key: "exif", label: "exif", count: 3200, evidence: { estimates: 1600 } },
        `${formatCount(3200)} points written from your photos have no item at their time any more, so those photos were probably deleted.`,
        [`${formatCount(1600)} estimates still rest on them.`],
      ],
      [
        "one pin point that one estimate rests on",
        { key: "manual", label: "manual", count: 1, evidence: { estimates: 1 } },
        "1 point written from one of your pins has no item at its time any more, so the item you placed by hand was probably deleted.",
        ["1 estimate still rests on it."],
      ],
      [
        "several pin points that nothing rests on",
        { key: "manual", label: "manual", count: 2, evidence: { estimates: 0 } },
        "2 points written from your pins have no item at their time any more, so the items you placed by hand were probably deleted.",
        ["No estimate rests on them."],
      ],
      [
        "no evidence",
        { key: "exif", label: "exif", count: 2, evidence: {} },
        "2 points written from your photos have no item at their time any more, so those photos were probably deleted.",
        [],
      ],
    ])("%s", (_name, overrides, sentence, notes) => {
      expect(findingText("orphan_points", group(overrides))).toEqual({ sentence, notes, instants: [] });
    });
  });

  describe("time_may_be_estimated_used", () => {
    it.each<[string, Partial<ChecksGroup>, string, string[]]>([
      [
        "KML, some of the estimates using it",
        { key: "kml", label: "kml", count: 4, evidence: { using: 10, over_100m: 2, over_1km: 1, max_move_m: 1523 } },
        "4 of the 10 estimates that use a KML point could also be placed from other formats.",
        [ESTIMATED_TIMES, "Without KML and GeoJSON points, 2 would move over 100 m and 1 over 1 km (at most 1.5 km)."],
      ],
      [
        "GeoJSON, all of them, none far",
        { key: "geojson", label: "geojson", count: 3, evidence: { using: 3, over_100m: 0, over_1km: 0, max_move_m: 35 } },
        "All 3 estimates that use a GeoJSON point could also be placed from other formats.",
        [ESTIMATED_TIMES, "Without KML and GeoJSON points, none would move over 100 m (at most 35 m)."],
      ],
      [
        "KMZ, unproven and compared without all three",
        { key: "kmz", label: "kmz", count: 2, evidence: { using: 7, over_100m: 1, over_1km: 0, max_move_m: 340 } },
        "2 of the 7 estimates that use a KMZ point could also be placed from other formats.",
        [
          "KMZ's time spacing has not been checked, so these estimates may be fine.",
          "Without KML, KMZ and GeoJSON points, 1 would move over 100 m and none over 1 km (at most 340 m).",
        ],
      ],
      [
        "the only estimate using a format",
        { key: "kml", label: "kml", count: 1, evidence: { using: 1, over_100m: 1, over_1km: 0, max_move_m: 120 } },
        "The only estimate that uses a KML point could also be placed from other formats.",
        [ESTIMATED_TIMES, "Without KML and GeoJSON points, it would move 120 m."],
      ],
      [
        "one KMZ estimate of several",
        { key: "kmz", label: "kmz", count: 1, evidence: { using: 4, over_100m: 0, over_1km: 0, max_move_m: 20 } },
        "1 of the 4 estimates that use a KMZ point could also be placed from other formats.",
        [
          "KMZ's time spacing has not been checked, so this estimate may be fine.",
          "Without KML, KMZ and GeoJSON points, it would move 20 m.",
        ],
      ],
      [
        "no largest move",
        { key: "kml", label: "kml", count: 3, evidence: { using: 5, over_100m: 1, over_1km: 1, max_move_m: null } },
        "3 of the 5 estimates that use a KML point could also be placed from other formats.",
        [ESTIMATED_TIMES, "Without KML and GeoJSON points, 1 would move over 100 m and 1 over 1 km."],
      ],
      [
        "no evidence",
        { key: "kml", label: "kml", count: 2, evidence: {} },
        "2 estimates that use a KML point could also be placed from other formats.",
        [ESTIMATED_TIMES],
      ],
      [
        "thousands",
        { key: "kml", label: "kml", count: 1540, evidence: { using: 2870, over_100m: 1780, over_1km: 1300, max_move_m: 152000 } },
        `${formatCount(1540)} of the ${formatCount(2870)} estimates that use a KML point could also be placed from other formats.`,
        [ESTIMATED_TIMES, `Without KML and GeoJSON points, ${formatCount(1780)} would move over 100 m and ${formatCount(1300)} over 1 km (at most 152 km).`],
      ],
    ])("%s", (_name, overrides, sentence, notes) => {
      expect(findingText("time_may_be_estimated_used", group(overrides))).toEqual({ sentence, notes, instants: [] });
    });
  });

  it("says what a check it does not know found", () => {
    expect(findingText("clock_drift" as CheckCode, group({ key: "k", label: "Camera X", count: 2 }))).toEqual({
      sentence: "Camera X: 2 found.",
      notes: [],
      instants: [],
    });
  });
});

describe("openLabel and cappedNote", () => {
  const ids = (n: number) => Array.from({ length: n }, (_, i) => `m${i}`);

  it.each<[string, CheckCode, Partial<ChecksGroup>, string, string | null]>([
    ["estimates", "pair_impossible", { count: 3, media_ids: ids(3) }, "Open 3 estimates", null],
    ["one item", "taken_after_upload", { count: 1, media_ids: ids(1) }, "Open 1 item", null],
    ["more than the server lists", "stale_estimates", { count: 450, media_ids: ids(200) }, "Open 200 estimates", "The first 200 of 450, oldest first."],
    ["items over the cap", "taken_after_upload", { count: 201, media_ids: ids(200) }, "Open 200 items", "The first 200 of 201, oldest first."],
    // Orphan points count points; their ids are the estimates resting on them.
    ["the estimates on orphan points", "orphan_points", { count: 32, evidence: { estimates: 6 }, media_ids: ids(6) }, "Open 6 estimates", null],
    ["orphan estimates over the cap", "orphan_points", { count: 3, evidence: { estimates: 250 }, media_ids: ids(200) }, "Open 200 estimates", "The first 200 of 250, oldest first."],
    ["thousands over the cap", "stale_estimates", { count: 4500, media_ids: ids(200) }, "Open 200 estimates", `The first 200 of ${formatCount(4500)}, oldest first.`],
  ])("%s", (_name, code, overrides, label, note) => {
    const group = makeGroup(overrides);
    expect(openLabel(code, group)).toBe(label);
    expect(cappedNote(code, group)).toBe(note);
  });
});

describe("nextFileSort", () => {
  it("turns the sorted column round", () => {
    expect(nextFileSort({ column: "last", direction: "descending" }, "last")).toEqual({ column: "last", direction: "ascending" });
    expect(nextFileSort({ column: "file", direction: "ascending" }, "file")).toEqual({ column: "file", direction: "descending" });
  });

  it.each<[FileColumn, SortDirection]>([
    ["file", "ascending"],
    ["format", "ascending"],
    ["kind", "ascending"],
    ["points", "descending"],
    ["first", "descending"],
    ["items_placed", "descending"],
    ["agreement", "descending"],
    ["spacing", "descending"],
  ])("starts %s %s", (column, direction) => {
    expect(nextFileSort(DEFAULT_FILE_SORT, column)).toEqual({ column, direction });
  });

  it("starts with the newest file first", () => {
    expect(DEFAULT_FILE_SORT).toEqual({ column: "last", direction: "descending" });
  });
});

describe("fileRows", () => {
  it("joins each file with its comparison by source", () => {
    const gpx = makeFile({ source: "gpx:a.gpx" });
    const exif = makeFile({ source: "exif", format: "exif", kind: "derived" });
    const compared = makeAgreementFile({ source: "gpx:a.gpx" });
    expect(fileRows([gpx, exif], makeComparison([compared]))).toEqual([
      { file: gpx, agreement: compared },
      { file: exif, agreement: null },
    ]);
    expect(fileRows([gpx], null)).toEqual([{ file: gpx, agreement: null }]);
  });
});

describe("sortFiles", () => {
  const rows: FileRow[] = fileRows(
    [
      makeFile({ source: "gpx:b10.gpx", format: "gpx", kind: "recorded", points: 50, first: "2000-01-01T00:00:00Z", last: "2000-01-03T00:00:00Z", items_placed: 2 }),
      makeFile({ source: "kml:b2.kml", format: "kml", kind: "time_may_be_estimated", points: 50, first: null, last: "2000-01-02T00:00:00Z", items_placed: 7 }),
      makeFile({ source: "exif", format: "exif", kind: "derived", points: 9, first: "1999-12-31T00:00:00Z", last: null, items_placed: 0 }),
      // Half a second after gpx's first point: compared as strings, it would sort before it.
      makeFile({ source: "esoko:a.log", format: "esoko", kind: "recorded", points: 3000, first: "2000-01-01T00:00:00.500Z", last: "2000-01-01T00:00:01Z", items_placed: 2 }),
    ],
    makeComparison([
      makeAgreementFile({ source: "gpx:b10.gpx", median_m: 12, modal_gap_share: 0.94 }),
      makeAgreementFile({ source: "kml:b2.kml", median_m: null, modal_gap_share: 0.23 }),
      makeAgreementFile({ source: "esoko:a.log", median_m: 340, modal_gap_share: null }),
    ])
  );
  const order = (column: FileColumn, direction: SortDirection) =>
    sortFiles(rows, { column, direction }).map((row) => row.file.source);

  it.each<[FileColumn, string[], string[]]>([
    // File names as people read them: b2 before b10, and the photos' words among them.
    ["file", ["esoko:a.log", "kml:b2.kml", "gpx:b10.gpx", "exif"], ["exif", "gpx:b10.gpx", "kml:b2.kml", "esoko:a.log"]],
    ["format", ["esoko:a.log", "exif", "gpx:b10.gpx", "kml:b2.kml"], ["kml:b2.kml", "gpx:b10.gpx", "exif", "esoko:a.log"]],
    ["kind", ["exif", "esoko:a.log", "gpx:b10.gpx", "kml:b2.kml"], ["kml:b2.kml", "esoko:a.log", "gpx:b10.gpx", "exif"]],
    ["points", ["exif", "gpx:b10.gpx", "kml:b2.kml", "esoko:a.log"], ["esoko:a.log", "gpx:b10.gpx", "kml:b2.kml", "exif"]],
    ["first", ["exif", "gpx:b10.gpx", "esoko:a.log", "kml:b2.kml"], ["esoko:a.log", "gpx:b10.gpx", "exif", "kml:b2.kml"]],
    ["last", ["esoko:a.log", "kml:b2.kml", "gpx:b10.gpx", "exif"], ["gpx:b10.gpx", "kml:b2.kml", "esoko:a.log", "exif"]],
    ["items_placed", ["exif", "esoko:a.log", "gpx:b10.gpx", "kml:b2.kml"], ["kml:b2.kml", "esoko:a.log", "gpx:b10.gpx", "exif"]],
    ["agreement", ["gpx:b10.gpx", "esoko:a.log", "exif", "kml:b2.kml"], ["esoko:a.log", "gpx:b10.gpx", "exif", "kml:b2.kml"]],
    ["spacing", ["kml:b2.kml", "gpx:b10.gpx", "esoko:a.log", "exif"], ["gpx:b10.gpx", "kml:b2.kml", "esoko:a.log", "exif"]],
  ])("by %s, missing values last both ways and ties by label", (column, ascending, descending) => {
    expect(order(column, "ascending")).toEqual(ascending);
    expect(order(column, "descending")).toEqual(descending);
  });

  it("leaves the rows it was given in their order", () => {
    const before = rows.map((row) => row.file.source);
    sortFiles(rows, { column: "points", direction: "descending" });
    expect(rows.map((row) => row.file.source)).toEqual(before);
  });
});

describe("matchesFile", () => {
  it.each([
    ["the file name", "walk", makeFile({ source: "gpx:walk-a.gpx" }), true],
    ["the format", "nmea", makeFile({ source: "nmea:x.nmea", format: "nmea" }), true],
    ["the kind in words", "may be estimated", makeFile({ source: "kml:x.kml", format: "kml", kind: "time_may_be_estimated" }), true],
    ["the photos' words", "photos", makeFile({ source: "exif", format: "exif", kind: "derived" }), true],
    ["the whole label", "gpx:walk", makeFile({ source: "gpx:walk-a.gpx" }), true],
    ["another file", "nmea", makeFile({ source: "gpx:walk-a.gpx" }), false],
  ])("finds by %s: %s", (_name, query, file, want) => {
    expect(matchesFile(file, query)).toBe(want);
  });
});

describe("agreementCell and spacingCell", () => {
  const log = makeFile({ source: "gpx:a.gpx" });
  const derived = makeFile({ source: "exif", format: "exif", kind: "derived" });

  it("shows the median, how many samples matched, and the spacing", () => {
    const row = { file: log, agreement: makeAgreementFile({ source: "gpx:a.gpx", matched: 18, samples: 20, median_m: 1234, modal_gap_share: 0.94 }) };
    expect(agreementCell(row, "complete")).toEqual({ text: "1.2 km", detail: "18 of 20 matched" });
    expect(spacingCell(row, "complete")).toBe("94%");
    // A file the comparison reached shows its values whatever became of the rest.
    expect(agreementCell(row, "stopped")).toEqual({ text: "1.2 km", detail: "18 of 20 matched" });
  });

  it("says when too few samples matched or too few points were spaced", () => {
    const row = { file: log, agreement: makeAgreementFile({ source: "gpx:a.gpx", matched: 3, samples: 20, median_m: null, modal_gap_share: null }) };
    expect(agreementCell(row, "complete")).toEqual({ text: "too few overlaps", detail: "3 of 20 matched" });
    expect(spacingCell(row, "complete")).toBe("too few points");
  });

  it.each<[string, typeof log, CompareStage, string]>([
    ["a derived file is never compared", derived, "stopped", NOT_COMPARED],
    ["a derived file, while the comparison runs", derived, "running", NOT_COMPARED],
    ["a log the comparison has yet to reach", log, "running", NOT_YET_COMPARED],
    ["a log the comparison stopped before", log, "stopped", NOT_REACHED],
    ["a log imported since the comparison", log, "complete", NOT_COMPARED],
  ])("%s", (_name, file, stage, want) => {
    const row = { file, agreement: null };
    expect(agreementCell(row, stage)).toEqual({ text: want, detail: null });
    expect(spacingCell(row, stage)).toBe(want);
  });
});

describe("the comparison", () => {
  const a = makeAgreementFile({ source: "gpx:a.gpx", median_m: 10 });
  const b = makeAgreementFile({ source: "gpx:b.gpx", median_m: 20 });
  const c = makeAgreementFile({ source: "nmea:c.nmea", median_m: 30 });

  describe("mergeComparison", () => {
    it("adds each answer's files, and takes the latest total and where to go on", () => {
      const first = mergeComparison(null, makeAgreement({ files: [a, b], total_files: 3, next_from: 2 }));
      expect(first).toEqual(makeComparison([a, b], 3, 2));
      const second = mergeComparison(first, makeAgreement({ files: [c], total_files: 3, next_from: null }));
      expect(second).toEqual(makeComparison([a, b, c], 3, null));
      // The earlier comparison is left as it was.
      expect(first).toEqual(makeComparison([a, b], 3, 2));
    });

    it("keeps the later values of a file compared twice, when an import moved the index", () => {
      const again = makeAgreementFile({ source: "gpx:b.gpx", median_m: 25 });
      const merged = mergeComparison(makeComparison([a, b], 3, 2), makeAgreement({ files: [again, c], total_files: 4, next_from: null }));
      expect([...merged.files.values()]).toEqual([a, again, c]);
      expect(merged.total).toBe(4);
    });
  });

  describe("madeProgress", () => {
    it.each<[string, number, number | null, boolean]>([
      ["the last answer", 0, null, true],
      ["an answer that went on", 150, 300, true],
      ["an answer that compared nothing", 150, 150, false],
      ["an answer that went back", 150, 100, false],
    ])("%s", (_name, from, nextFrom, want) => {
      expect(madeProgress(from, makeAgreement({ files: [], next_from: nextFrom }))).toBe(want);
    });
  });

  const state = (status: AgreementState["status"], data: AgreementState["data"], answered = true): AgreementState => ({
    status,
    data,
    answered,
  });
  const partial = makeComparison([a, b], 1500, 2);
  const complete = makeComparison([a, b, c], 3, null);

  describe("compareStage", () => {
    it.each<[string, AgreementState, CompareStage]>([
      ["nothing compared yet", NO_COMPARISON, "complete"],
      ["a comparison that reached its last file", state("done", complete), "complete"],
      ["a comparison going on", state("loading", partial), "running"],
      ["a comparison that stopped", state("stopped", partial), "stopped"],
      ["a comparison whose request failed", state("failed", partial), "stopped"],
      ["a comparison the server was too busy for", state("busy", partial), "stopped"],
      // Compare again keeps the last comparison on screen until the new one answers.
      ["the last comparison, while a new one starts", state("loading", complete, false), "complete"],
    ])("%s", (_name, given, want) => {
      expect(compareStage(given)).toBe(want);
    });
  });

  describe("compareStatusText and canResume", () => {
    const leftOut = 'Files marked "not reached" were left out.';

    it.each<[string, AgreementState, string, boolean]>([
      ["before any comparison", NO_COMPARISON, COMPARE_NOTE, false],
      ["waiting for the first answer", state("loading", null, false), COMPARING, false],
      ["waiting for a new comparison's first answer", state("loading", complete, false), COMPARING, false],
      ["going on after an answer", state("loading", partial), `Compared 2 of ${formatCount(1500)} files…`, false],
      ["done", state("done", complete), "Compared 3 of 3 files.", false],
      ["done with one file", state("done", makeComparison([a])), "Compared 1 of 1 file.", false],
      ["stopped by the time limit", state("stopped", partial), TRUNCATED_NOTE, true],
      ["stopped before comparing anything", state("stopped", makeComparison([], 3, 0)), TRUNCATED_NOTE, false],
      ["failed part way", state("failed", partial), `Could not compare every file. ${leftOut}`, true],
      ["failed with nothing on screen", state("failed", null, false), "Could not compare the files.", false],
      ["failed, keeping a complete comparison", state("failed", complete, false), "Could not compare the files.", false],
      ["busy part way", state("busy", partial), `${COMPARE_BUSY} ${leftOut}`, true],
      ["busy with nothing on screen", state("busy", null, false), COMPARE_BUSY, false],
    ])("%s", (_name, given, text, resumable) => {
      expect(compareStatusText(given)).toBe(text);
      expect(canResume(given)).toBe(resumable);
    });

    it("never offers to go on while the comparison runs", () => {
      expect(canResume(state("loading", partial))).toBe(false);
    });

    it("says the time limit stopped it, and which files it left", () => {
      expect(TRUNCATED_NOTE).toBe(`The time limit stopped the comparison before every file was done. ${leftOut}`);
    });
  });
});

describe("nameParts", () => {
  it.each<[string, string[]]>([
    ["com.samsung.health.exercise.18.json", ["com.", "samsung.", "health.", "exercise.", "18.", "json"]],
    ["2020_07_19_040400_F.NMEA", ["2020_", "07_", "19_", "040400_", "F.", "NMEA"]],
    ["2023-10-28 Commute.gpx", ["2023-", "10-", "28 Commute.", "gpx"]],
    ["a/b", ["a/", "b"]],
    ["nmea", ["nmea"]],
    ["trailing_", ["trailing_"]],
    ["", []],
  ])("%s", (name, want) => {
    expect(nameParts(name)).toEqual(want);
    expect(nameParts(name).join("")).toBe(name);
  });
});
