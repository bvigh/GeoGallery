import { describe, expect, it } from "vitest";
import {
  bracketKindChip,
  bracketPoints,
  cameraClock,
  checkSentence,
  confidenceNote,
  contextPointKind,
  formatConfidence,
  formatDistance,
  formatOffset,
  formatTimeNote,
  independenceLine,
  ITEM_GONE,
  leadSentence,
  NOT_OWNER,
  otherAloneCell,
  otherFormatPoints,
  otherSideCell,
  pointSide,
  REMATCH_FAILED,
  REMATCH_HEADING,
  REMATCH_SEGMENT_LABEL,
  REREAD_NOT_RECORDED,
  rereadSentence,
  shapeSentence,
  showsAloneEstimate,
  STANDING_LINE,
  stateBadge,
  timeOriginChip,
  VIDEO_TIME_MAY_BE_END,
  warningSentence,
  zoneChip,
} from "@/lib/evidence";
import type {
  Bracket,
  CaptureEvidence,
  Estimate,
  FileReread,
  OtherFormat,
  OtherPoint,
  OutsidePoint,
  PlacementState,
  ProvenanceResponse,
  ServerCameraZone,
} from "@/types";

function bracket(overrides: Partial<Bracket> = {}): Bracket {
  return {
    source: "gpx:walk-a.gpx",
    format: "gpx",
    kind: "log",
    t: "2000-01-01T00:00:00Z",
    lat: 10,
    lng: 20,
    accuracy_m: null,
    altitude_m: null,
    gap_s: 60,
    origin: null,
    ...overrides,
  };
}

function estimate(overrides: Partial<Estimate> = {}, arithmetic: Partial<Estimate["arithmetic"]> = {}): Estimate {
  return {
    lat: 10,
    lng: 20,
    confidence: 0.5,
    delta_m: 0,
    before: bracket({ gap_s: 60 }),
    after: bracket({ gap_s: 120 }),
    ...overrides,
    arithmetic: {
      shape: "two_sided",
      pair_gap_s: 180,
      pair_km: 2,
      t: 0.33,
      time_factor: 0.9,
      distance_factor: 0.9,
      confidence: 0.5,
      implied_kmh: 40,
      ...arithmetic,
    },
  };
}

const oneSided = estimate({ after: null }, { shape: "one_sided", pair_gap_s: null, pair_km: null, t: null });
const exact = estimate({ confidence: 1 }, { shape: "exact", pair_gap_s: 0, pair_km: 0, confidence: 1 });
const usesPhoto = estimate({ before: bracket({ kind: "exif", format: "exif", source: "exif" }) });
const usesPin = estimate({ after: bracket({ kind: "manual", format: "manual", source: "manual" }) });

type Facts = Pick<ProvenanceResponse, "item" | "placement">;

function facts(
  state: PlacementState,
  placement: Partial<Facts["placement"]> = {},
  item: Partial<Facts["item"]> = {}
): Facts {
  return {
    item: {
      id: "m1",
      filename: "photo-a.jpg",
      media_type: "photo",
      taken_at: "2000-01-01T00:00:00Z",
      created_at: "2000-01-02T00:00:00Z",
      lat: 10,
      lng: 20,
      gps_source: "interpolated",
      gps_confidence: 0.5,
      camera_model: null,
      width_px: null,
      height_px: null,
      duration_sec: null,
      ...item,
    },
    placement: {
      state,
      window_s: 3600,
      now: null,
      independent: null,
      nearest_outside: null,
      others: [],
      warnings: [],
      ...placement,
    },
  };
}

function outside(gap_s: number): OutsidePoint {
  return { source: "gpx:walk-a.gpx", format: "gpx", t: "2000-01-01T00:00:00Z", lat: 10, lng: 20, gap_s };
}

function independent(moved_m: number | null): NonNullable<Facts["placement"]["independent"]> {
  return { lat: 10, lng: 20, confidence: 0.5, moved_m, before: bracket(), after: null };
}

function evidence(overrides: Partial<CaptureEvidence> = {}): CaptureEvidence {
  return {
    v: 1,
    by: "ingest",
    src: "exif_original",
    zone: "camera",
    wall: "2000:01:01 12:00:00",
    gps_utc: null,
    wall_minus_gps_s: null,
    offset_tag: null,
    offset_ignored: false,
    offset_used_min: 60,
    camera_zone: "Etc/GMT-1",
    camera_zone_utc_fallback: false,
    make: null,
    model: null,
    exif_partial: false,
    creation_time: null,
    file_mtime: null,
    duration_s: null,
    encoder: null,
    codec: null,
    transcoded: false,
    ...overrides,
  };
}

function reread(rule: FileReread["rule_today"], file: Partial<FileReread["file"]> = {}): FileReread {
  return {
    basis: "reread_now",
    stored_taken_at: "2000-01-01T11:00:00Z",
    uploaded_at: "2000-01-02T00:00:00Z",
    file: {
      wall_clock: "2000:01:01 12:00:00",
      wall_tag: "DateTimeOriginal",
      offset_tag: null,
      offset_ignored: false,
      gps_stamp: null,
      wall_minus_gps_s: null,
      make: null,
      model: null,
      video: null,
      ...file,
    },
    rule_today: rule,
    delta_s: 0,
    class: "same",
    checks: [],
    tags: [],
  };
}

function rule(overrides: Partial<NonNullable<FileReread["rule_today"]>> = {}): NonNullable<FileReread["rule_today"]> {
  return {
    source: "exif_original",
    zone_rule: "camera",
    offset_min: 60,
    zone_name: "Etc/GMT-1",
    taken_at: "2000-01-01T11:00:00Z",
    ...overrides,
  };
}

const serverZone: ServerCameraZone = { name: "Etc/GMT-1", set: true, utc_fallback: false, offset_now_min: 60 };

describe("stateBadge", () => {
  it.each<[PlacementState, string]>([
    ["current", "Up to date"],
    ["stale", "Would change"],
    ["no_support", "No support"],
    ["pending", "Not placed yet"],
    ["no_bracket", "No track nearby"],
    ["no_time", "No capture time"],
    ["own_position", "From the file"],
    ["manual", "Placed by hand"],
  ])("%s", (state, want) => {
    expect(stateBadge(state)).toBe(want);
  });
});

describe("shapeSentence", () => {
  it.each([
    ["exact", exact, "A track point at the same second."],
    ["one_sided, only a point before", oneSided, "Copied from one point 1 min away, with no distance check."],
    [
      "one_sided, only a point after",
      estimate({ before: null, after: bracket({ gap_s: 300 }) }, { shape: "one_sided" }),
      "Copied from one point 5 min away, with no distance check.",
    ],
    ["two_sided, one format", estimate(), "Between two points 1 min and 2 min away."],
    [
      "two_sided, two formats",
      estimate({ before: bracket({ format: "gpx", gap_s: 30 }), after: bracket({ format: "kml", gap_s: 3600 }) }),
      "Between two points from different sources (gpx and kml), 30 s and 1 h away.",
    ],
  ])("%s", (_name, input, want) => {
    expect(shapeSentence(input)).toBe(want);
  });
});

describe("leadSentence", () => {
  const reason = "Your tracks, this item's time or the match window changed.";

  it.each<[string, Facts, string]>([
    ["current says how the bracket placed it", facts("current", { now: exact }), "A track point at the same second."],
    [
      "stale that would move",
      facts("stale", { now: estimate({ delta_m: 1500 }) }),
      `Re-matching would move this 1.5 km. ${reason}`,
    ],
    [
      "stale just over the tolerance",
      facts("stale", { now: estimate({ delta_m: 1.4 }) }),
      `Re-matching would move this 1 m. ${reason}`,
    ],
    [
      "stale with only the confidence changed",
      facts("stale", { now: estimate({ delta_m: 0.4, confidence: 0.5 }) }, { gps_confidence: 0.75 }),
      `Re-matching would keep this position and change its confidence from 75.0% to 50.0%. ${reason}`,
    ],
    [
      "stale at exactly the distance tolerance",
      facts("stale", { now: estimate({ delta_m: 1, confidence: 0.5 }) }, { gps_confidence: 0.75 }),
      `Re-matching would keep this position and change its confidence from 75.0% to 50.0%. ${reason}`,
    ],
    [
      "stale with no stored confidence",
      facts("stale", { now: estimate({ delta_m: 0, confidence: 0.5 }) }, { gps_confidence: null }),
      `Re-matching would keep this position and change its confidence from not recorded to 50.0%. ${reason}`,
    ],
    [
      "stale that would become an exact match",
      facts("stale", { now: exact }, { gps_confidence: 0.4 }),
      `Re-matching would keep this position and change its confidence from 40.0% to 100%. ${reason}`,
    ],
    [
      "no_support",
      facts("no_support"),
      "No track point supports this estimate any more. The matcher never clears a location, so it stays until you place it by hand.",
    ],
    [
      "pending",
      facts("pending", { now: estimate() }),
      "A track point exists within the window, but the matcher has not placed this item yet.",
    ],
    [
      "no_bracket with both nearest points",
      facts("no_bracket", { nearest_outside: { before: outside(5400), after: outside(90000) } }),
      "No track point within 1 h. The nearest is earlier by 1 h 30 min and later by 1 d 1 h.",
    ],
    [
      "no_bracket with only an earlier point",
      facts("no_bracket", { nearest_outside: { before: outside(5400), after: null } }),
      "No track point within 1 h. The nearest is earlier by 1 h 30 min, and there is none later.",
    ],
    [
      "no_bracket with only a later point",
      facts("no_bracket", { nearest_outside: { before: null, after: outside(7200) } }),
      "No track point within 1 h. The nearest is later by 2 h, and there is none earlier.",
    ],
    [
      "no_bracket with no point",
      facts("no_bracket", { nearest_outside: { before: null, after: null } }),
      "You have no track points at all.",
    ],
    ["no_time", facts("no_time"), "This item has no capture time, so it cannot be matched."],
    ["own_position of a photo", facts("own_position"), "The file gave this position."],
    [
      "own_position of a 360 photo",
      facts("own_position", {}, { media_type: "photo_360" }),
      "The file gave this position.",
    ],
    [
      "own_position of a video",
      facts("own_position", {}, { media_type: "video" }),
      "The video's location tag gave this position. It can be the phone's last known fix.",
    ],
    [
      "own_position of a 360 video",
      facts("own_position", {}, { media_type: "video_360" }),
      "The video's location tag gave this position. It can be the phone's last known fix.",
    ],
    ["manual", facts("manual"), "You placed this by hand."],
  ])("%s", (_name, input, want) => {
    expect(leadSentence(input)).toBe(want);
  });

  it("reads the window from the response", () => {
    expect(
      leadSentence(facts("no_bracket", { window_s: 1800, nearest_outside: { before: outside(2000), after: null } }))
    ).toContain("within 30 min.");
  });
});

describe("warningSentence", () => {
  const now = estimate({}, { implied_kmh: 412.6, pair_km: 3.26 });

  it.each([
    [
      "pair_impossible",
      "These two points come from different formats and would need 413 km/h to connect, so at least one is unreliable for this moment.",
    ],
    [
      "pair_far",
      "These two points come from different formats and are 3.3 km apart, so the position between them is uncertain.",
    ],
    ["logger_parked", "This logger barely moved between these points. It may have been parked."],
    [
      "confidence_zero",
      "Confidence 0: the two points are a full window or more apart, or at least 100 km apart. The item is placed anyway, because the matcher has no lower limit.",
    ],
    [
      "point_orphan",
      "No item exists at this point's time, so the photo or pin that wrote it was probably deleted. The matcher still uses it.",
    ],
  ] as const)("%s", (code, want) => {
    expect(warningSentence(code, now)).toBe(want);
  });
});

describe("formatConfidence", () => {
  it.each([
    ["no value", null, undefined, "not recorded"],
    ["zero", 0, "two_sided", "0.0%"],
    ["a middle value, floored", 0.7349, "two_sided", "73.4%"],
    ["a value just under a tenth", 0.72999, "one_sided", "72.9%"],
    ["a round value", 0.5, "two_sided", "50.0%"],
    ["near 100, floored", 0.9985, "two_sided", "99.8%"],
    ["near 100, capped", 0.9995, "two_sided", "99.9%"],
    ["1.0 on a two-sided bracket is capped", 1, "two_sided", "99.9%"],
    ["1.0 on a one-sided bracket is capped", 1, "one_sided", "99.9%"],
    ["1.0 with an unknown shape is capped", 1, undefined, "99.9%"],
    ["an exact bracket", 1, "exact", "100%"],
  ] as const)("%s", (_name, confidence, shape, want) => {
    expect(formatConfidence(confidence, shape)).toBe(want);
  });
});

describe("confidenceNote", () => {
  const veryClose = "Very close points, not a correct position.";

  it.each([
    [
      "exact",
      1,
      "exact",
      "100% means a track point at the same second. It says nothing about whether the position or the clock is right.",
    ],
    [
      "zero",
      0,
      "two_sided",
      "Confidence 0: the two points are a full window or more apart, or at least 100 km apart. The item is placed anyway, because the matcher has no lower limit.",
    ],
    ["0.995, the Map card's 100%", 0.995, "two_sided", veryClose],
    ["just under 1 and one-sided", 0.999, "one_sided", veryClose],
    ["1.0 without an exact bracket", 1, "two_sided", veryClose],
    ["just below 0.995", 0.9949, "two_sided", null],
    ["an ordinary value", 0.5, "one_sided", null],
  ] as const)("%s", (_name, confidence, shape, want) => {
    expect(confidenceNote(confidence, shape)).toBe(want);
  });

  it("never explains a non-exact 100 as a same-instant point", () => {
    expect(confidenceNote(1, "two_sided")).not.toContain("same second");
    expect(confidenceNote(1, "one_sided")).not.toContain("same second");
  });
});

describe("fixed lines", () => {
  it.each([
    [STANDING_LINE, "A wrong clock looks equally consistent here."],
    [REMATCH_FAILED, "Re-match did not finish; some items may have changed."],
    [REMATCH_HEADING, "What a re-match would use"],
    [REMATCH_SEGMENT_LABEL, "what a re-match would use"],
    [REREAD_NOT_RECORDED, "Read now with today's rules. The rule used at upload was not recorded."],
    [NOT_OWNER, "Only the owner can see how this item was placed"],
    [ITEM_GONE, "This item is no longer available."],
    [VIDEO_TIME_MAY_BE_END, "On some cameras a video's time is when the recording ended."],
  ])("%s", (line, want) => {
    expect(line).toBe(want);
  });
});

describe("independenceLine", () => {
  it.each<[string, Facts, string | null]>([
    [
      "a derived point with an independent estimate",
      facts("current", { now: usesPhoto, independent: independent(250) }),
      "Without photos and pins this would move 250 m.",
    ],
    [
      "a pin as the point after",
      facts("stale", { now: usesPin, independent: independent(1200) }),
      "Without photos and pins this would move 1.2 km.",
    ],
    [
      "a derived point and no independent estimate",
      facts("current", { now: usesPhoto, independent: null }),
      "No log supports this. It rests only on other photos or pins.",
    ],
    [
      "own_position with an independent estimate",
      facts("own_position", { independent: independent(45) }),
      "The logs at that moment are 45 m from this position.",
    ],
    [
      "manual with an independent estimate",
      facts("manual", { independent: independent(2500) }),
      "The logs at that moment are 2.5 km from this position.",
    ],
    [
      "own_position and no independent estimate",
      facts("own_position", { independent: null }),
      "No log point is within 1 h of this item's time.",
    ],
    [
      "manual and no independent estimate",
      facts("manual", { independent: null, window_s: 7200 }),
      "No log point is within 2 h of this item's time.",
    ],
    ["only log points", facts("current", { now: estimate(), independent: independent(0) }), null],
    ["a state with no estimate", facts("no_bracket"), null],
    ["own_position with no capture time", facts("own_position", {}, { taken_at: null }), null],
    ["manual with no capture time", facts("manual", {}, { taken_at: null }), null],
    ["a position that could not be measured", facts("own_position", { independent: independent(null) }), null],
    ["a derived point, not measured", facts("current", { now: usesPhoto, independent: independent(null) }), null],
  ])("%s", (_name, input, want) => {
    expect(independenceLine(input)).toBe(want);
  });
});

describe("formatTimeNote", () => {
  const estimated =
    "This format's times may be estimated by the exporter. One point cannot be told apart from a recorded one.";

  it.each([
    ["kml", estimated],
    ["kmz", estimated],
    ["geojson", estimated],
    [
      "google_timeline",
      "This point may be a visit midpoint, which can be hours from any real presence, at a place snapped to a venue that can sit far from the device. It cannot be told apart from a path point.",
    ],
    ["gpx", null],
    ["exif", null],
    ["other", null],
  ])("%s", (format, want) => {
    expect(formatTimeNote(format)).toBe(want);
  });
});

describe("bracketKindChip", () => {
  const linked = { state: "linked" as const, media: [{ id: "m2", filename: "photo-b.jpg" }] };

  it.each<[string, Pick<Bracket, "kind" | "origin">, string]>([
    ["a log point", { kind: "log", origin: null }, "Log"],
    ["a linked photo", { kind: "exif", origin: linked }, "Your photo photo-b.jpg"],
    ["a linked pin", { kind: "manual", origin: linked }, "Your pin on photo-b.jpg"],
    [
      "a burst",
      {
        kind: "exif",
        origin: {
          state: "burst",
          media: [
            { id: "m2", filename: "photo-b.jpg" },
            { id: "m3", filename: "photo-c.jpg" },
            { id: "m4", filename: "photo-d.jpg" },
          ],
        },
      },
      "3 items at this second",
    ],
    ["an orphan photo", { kind: "exif", origin: { state: "orphan", media: [] } }, "A deleted item"],
    ["an orphan pin", { kind: "manual", origin: { state: "orphan", media: [] } }, "A deleted item"],
  ])("%s", (_name, input, want) => {
    expect(bracketKindChip(input)).toBe(want);
  });
});

describe("bracketPoints", () => {
  it("lists both points of a two-sided bracket, earlier first", () => {
    const now = estimate();
    expect(bracketPoints(now)).toEqual([now.before, now.after]);
  });

  it("lists the only point of a one-sided bracket, on either side", () => {
    expect(bracketPoints(oneSided)).toEqual([oneSided.before]);
    const onlyAfter = estimate({ before: null }, { shape: "one_sided" });
    expect(bracketPoints(onlyAfter)).toEqual([onlyAfter.after]);
  });

  it("lists an exact bracket's one row once", () => {
    expect(bracketPoints(exact)).toEqual([exact.before]);
  });

  it("is empty without an estimate", () => {
    expect(bracketPoints(null)).toEqual([]);
  });
});

describe("contextPointKind", () => {
  it.each([
    ["recorded", "log"],
    ["semantic", "log"],
    ["time_may_be_estimated", "log"],
    ["other", "log"],
    ["derived", "another photo or pin"],
  ] as const)("%s", (kind, want) => {
    expect(contextPointKind(kind)).toBe(want);
  });
});

describe("timeOriginChip", () => {
  it.each([
    ["exif_original", "EXIF capture time", null],
    ["exif_ifd0", "EXIF general date, may be an edit time", null],
    ["container", "Video container time", null],
    ["mtime", "File modified time, may be wrong", "A modified time is often a copy or download time."],
    ["none", "No capture time", null],
  ] as const)("recorded %s", (src, text, note) => {
    expect(timeOriginChip(evidence({ src }), null)).toEqual({ text, note });
  });

  it.each([
    ["exif_original", "EXIF capture time"],
    ["exif_ifd0", "EXIF general date, may be an edit time"],
    ["container", "Video container time"],
  ] as const)("follows the re-read's %s when nothing was recorded", (source, text) => {
    expect(timeOriginChip(null, reread(rule({ source })))).toEqual({ text, note: null });
  });

  it("prefers the recorded origin over the re-read", () => {
    expect(timeOriginChip(evidence({ src: "mtime" }), reread(rule())).text).toBe("File modified time, may be wrong");
  });

  it("reads not recorded until the re-read arrives", () => {
    expect(timeOriginChip(null, null)).toEqual({ text: "not recorded", note: null });
  });

  it("reads not recorded when the re-read finds no time", () => {
    expect(timeOriginChip(null, reread(null))).toEqual({ text: "not recorded", note: null });
  });
});

describe("zoneChip", () => {
  it.each<[string, CaptureEvidence, string]>([
    [
      "the GPS clock, with its residual",
      evidence({ zone: "gps", wall_minus_gps_s: 3603, offset_used_min: 60 }),
      "GPS clock, residual 3 s",
    ],
    [
      "a residual below the whole offset",
      evidence({ zone: "gps", wall_minus_gps_s: 3510, offset_used_min: 60 }),
      "GPS clock, residual 1 min 30 s",
    ],
    ["the GPS clock with no residual to show", evidence({ zone: "gps", wall_minus_gps_s: null }), "GPS clock"],
    ["the offset tag", evidence({ zone: "offset", offset_tag: "+01:00" }), "OffsetTimeOriginal"],
    ["a named camera zone", evidence({ zone: "camera" }), "Etc/GMT-1; nothing in the file confirms this"],
    [
      "CAMERA_TIMEZONE unset",
      evidence({ zone: "camera", camera_zone: "server zone (unset)" }),
      "the server's own zone, because CAMERA_TIMEZONE is unset; nothing in the file confirms this",
    ],
  ])("%s", (_name, input, want) => {
    expect(zoneChip(input, null, serverZone)).toEqual({ text: want, notes: [], warning: null });
  });

  it("warns that a camera-zone time was read as UTC on a UTC fallback", () => {
    const chip = zoneChip(
      evidence({ zone: "camera", camera_zone: "server zone (unset)", camera_zone_utc_fallback: true }),
      null,
      serverZone
    );
    expect(chip?.warning).toBe("This time was read as UTC, because CAMERA_TIMEZONE is unset and the server's zone is UTC.");
  });

  it.each(["gps", "offset"] as const)("gives no UTC warning when the %s rule set the offset", (zone) => {
    expect(zoneChip(evidence({ zone, camera_zone_utc_fallback: true }), null, serverZone)?.warning).toBeNull();
  });

  it("adds the ignored offset", () => {
    const chip = zoneChip(evidence({ offset_ignored: true, offset_tag: "+01:00" }), null, serverZone);
    expect(chip?.notes).toEqual(["The file declares +01:00, which is ignored for this make."]);
  });

  it.each([
    ["a container time", evidence({ src: "container", zone: null })],
    ["a modified time", evidence({ src: "mtime", zone: null })],
    ["no time", evidence({ src: "none", zone: null })],
  ])("has no chip for %s", (_name, input) => {
    expect(zoneChip(input, null, serverZone)).toBeNull();
  });

  it("uses the re-read when nothing was recorded", () => {
    const chip = zoneChip(
      null,
      reread(rule({ zone_rule: "gps", offset_min: 60 }), { wall_minus_gps_s: 3605 }),
      serverZone
    );
    expect(chip?.text).toBe("GPS clock, residual 5 s");
  });

  it("names the re-read's camera zone and carries today's UTC fallback", () => {
    const chip = zoneChip(null, reread(rule({ zone_name: "server zone (unset)" })), {
      name: "server zone (unset)",
      set: false,
      utc_fallback: true,
      offset_now_min: 0,
    });
    expect(chip?.text).toContain("the server's own zone, because CAMERA_TIMEZONE is unset");
    expect(chip?.warning).toContain("read as UTC");
  });

  it("falls back to the server's zone when the re-read names none", () => {
    expect(zoneChip(null, reread(rule({ zone_name: null })), serverZone)?.text).toBe(
      "Etc/GMT-1; nothing in the file confirms this"
    );
  });

  it("carries the re-read's ignored offset", () => {
    const chip = zoneChip(null, reread(rule(), { offset_ignored: true, offset_tag: "+01:00" }), serverZone);
    expect(chip?.notes).toEqual(["The file declares +01:00, which is ignored for this make."]);
  });

  it.each([
    ["no re-read yet", null],
    ["a re-read with no time", reread(null)],
    ["a container time", reread(rule({ source: "container", zone_rule: null, offset_min: null, zone_name: null }))],
  ])("has no chip with %s", (_name, input) => {
    expect(zoneChip(null, input, serverZone)).toBeNull();
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
  ])("%d minutes", (minutes, want) => {
    expect(formatOffset(minutes)).toBe(want);
  });
});

describe("cameraClock", () => {
  const video = "No wall clock was written. A video container stores UTC.";

  it.each<[string, CaptureEvidence, string, string | null]>([
    [
      "the GPS clock",
      evidence({ zone: "gps", offset_used_min: 60 }),
      "2000:01:01 12:00:00 +01:00",
      "measured",
    ],
    [
      "the offset tag",
      evidence({ zone: "offset", offset_used_min: -210 }),
      "2000:01:01 12:00:00 -03:30",
      "declared",
    ],
    ["the camera zone", evidence({ zone: "camera", offset_used_min: 0 }), "2000:01:01 12:00:00 +00:00", "assumed"],
    ["the general date tag", evidence({ src: "exif_ifd0", zone: "camera" }), "2000:01:01 12:00:00 +01:00", "assumed"],
    ["a video container", evidence({ src: "container", zone: null, offset_used_min: null, wall: null }), video, null],
    [
      "a modified time",
      evidence({ src: "mtime", zone: null, offset_used_min: null, wall: null }),
      "No wall clock. This is the file's modified date.",
      "file date",
    ],
    [
      "an unparseable wall tag under a modified time",
      evidence({ src: "mtime", zone: null, offset_used_min: null, wall: "garbled" }),
      "No wall clock. This is the file's modified date.",
      "file date",
    ],
    ["no time at all", evidence({ src: "none", zone: null, offset_used_min: null, wall: null }), "No wall clock.", null],
  ])("recorded: %s", (_name, input, text, word) => {
    expect(cameraClock(input, null)).toEqual({ text, word });
  });

  it.each<[string, FileReread, string, string | null]>([
    ["the GPS clock", reread(rule({ zone_rule: "gps" })), "2000:01:01 12:00:00 +01:00", "measured"],
    ["the offset tag", reread(rule({ zone_rule: "offset", offset_min: 120 })), "2000:01:01 12:00:00 +02:00", "declared"],
    ["the camera zone", reread(rule({ zone_rule: "camera" })), "2000:01:01 12:00:00 +01:00", "assumed"],
    [
      "a container time",
      reread(rule({ source: "container", zone_rule: null, offset_min: null, zone_name: null }), { wall_clock: null }),
      video,
      null,
    ],
    ["no capture time", reread(null, { wall_clock: null }), "No wall clock.", null],
    ["a wall clock that does not parse", reread(null, { wall_clock: "garbled" }), "garbled", null],
  ])("re-read: %s", (_name, input, text, word) => {
    expect(cameraClock(null, input)).toEqual({ text, word });
  });

  it("reads not recorded with neither evidence nor a re-read", () => {
    expect(cameraClock(null, null)).toEqual({ text: "not recorded", word: null });
  });

  it("prefers the recorded wall clock over the re-read", () => {
    expect(cameraClock(evidence({ wall: "2000:02:02 08:00:00" }), reread(rule())).text).toContain("2000:02:02 08:00:00");
  });
});

describe("checkSentence", () => {
  const offsetTag = "+01:00";

  it.each<[string, number | null, string]>([
    ["taken_after_upload", 7200, "The stored time is 2 h after the upload. A correct clock cannot produce that."],
    ["ifd0_may_be_edit_time", null, "This time comes from the general date tag, which editing software can rewrite."],
    ["offset_ignored", null, "The file declares +01:00, which is ignored for this make."],
    [
      "gps_clock_rejected",
      -905,
      "The photo's GPS clock was not used: it is 15 min 5 s from the camera clock, which is not within 2 minutes of a real UTC offset.",
    ],
    ["video_time_may_be_end", null, "On some cameras a video's time is when the recording ended."],
  ])("%s", (id, delta_s, want) => {
    expect(checkSentence({ id, level: "info", delta_s }, offsetTag)).toBe(want);
  });

  it("claims no difference for a GPS clock with no camera clock beside it", () => {
    const text = checkSentence({ id: "gps_clock_rejected", level: "info", delta_s: null }, offsetTag);
    expect(text).toBe("The photo's GPS clock was not used: there is no readable camera clock to compare it with.");
  });

  it("has no sentence for a check this version does not know", () => {
    expect(checkSentence({ id: "from_a_later_step", level: "ok", delta_s: null }, offsetTag)).toBeNull();
  });
});

describe("rereadSentence", () => {
  const photoMessage = "No capture time could be read from this file. This reader understands EXIF in JPEG and TIFF only.";
  const videoMessage = "No capture time could be read from this video's container.";
  const mtimeNote = "The stored time is the file's modified date.";
  type Row = [string, FileReread["class"], number | null, "photo" | "video" | "video_360", CaptureEvidence | null, string];

  it.each<Row>([
    [
      "a re-encoded video, from the evidence",
      "unverifiable_transcoded",
      null,
      "video",
      evidence({ transcoded: true }),
      "This video was re-encoded at upload, so its time cannot be checked against the file.",
    ],
    [
      "a re-encoded video, by the heuristic",
      "unverifiable_transcoded",
      null,
      "video",
      null,
      "This video may have been re-encoded at upload, so its time may not be checkable against the file.",
    ],
    ["a photo with no readable time", "no_file_time", null, "photo", null, photoMessage],
    ["a video with no readable time", "no_file_time", null, "video", null, videoMessage],
    ["a 360 video with no readable time", "no_file_time", null, "video_360", null, videoMessage],
    [
      "a photo stored from its modified time",
      "no_file_time",
      null,
      "photo",
      evidence({ src: "mtime" }),
      `${photoMessage} ${mtimeNote}`,
    ],
    [
      "a video stored from its modified time",
      "no_file_time",
      null,
      "video",
      evidence({ src: "mtime" }),
      `${videoMessage} ${mtimeNote}`,
    ],
    ["a time from another source", "no_file_time", null, "photo", evidence({ src: "none" }), photoMessage],
    [
      "a time the item lacks",
      "stored_has_no_time",
      null,
      "photo",
      null,
      "The file holds a capture time, but this item has none stored. Re-reading does not change that.",
    ],
    ["agreement", "same", 0, "photo", null, "Today's rules agree with the stored time."],
    [
      "a whole offset ahead",
      "whole_offset",
      3600,
      "photo",
      null,
      "Differs by exactly 1 h. An older rule or a changed setting would explain it.",
    ],
    [
      "a whole offset behind",
      "whole_offset",
      -20700,
      "photo",
      null,
      "Differs by exactly 5 h 45 min. An older rule or a changed setting would explain it.",
    ],
    ["any other difference", "other", -125, "photo", null, "Today's rules differ by 2 min 5 s."],
  ])("%s", (_name, cls, delta_s, mediaType, ev, want) => {
    expect(rereadSentence({ class: cls, delta_s }, mediaType, ev)).toBe(want);
  });

  it("never calls agreement correct", () => {
    expect(rereadSentence({ class: "same", delta_s: 0 }, "photo", null)).not.toMatch(/correct/i);
  });
});

describe("formatDistance", () => {
  it.each([
    [0, "0 m"],
    [0.4, "0 m"],
    [999.4, "999 m"],
    [999.6, "1.0 km"],
    [1000, "1.0 km"],
    [1234, "1.2 km"],
    [125000, "125.0 km"],
  ])("%d metres", (meters, want) => {
    expect(formatDistance(meters)).toBe(want);
  });
});

describe("each format on its own", () => {
  const point = (overrides: Partial<OtherPoint> = {}): OtherPoint => ({
    source: "gpx:a.gpx",
    t: "2000-01-01T00:00:30Z",
    lat: 10,
    lng: 20,
    gap_s: 30,
    accuracy_m: null,
    distance_m: 100,
    used: false,
    self: false,
    ...overrides,
  });
  const row = (overrides: Partial<OtherFormat> = {}): OtherFormat => ({
    format: "gpx",
    before: point(),
    after: point({ t: "2000-01-01T00:02:00Z", gap_s: 60 }),
    alone: { lat: 10, lng: 20, shape: "two_sided", confidence: 0.9, distance_m: 2500 },
    used: false,
    ...overrides,
  });

  describe("otherFormatPoints", () => {
    it("lists both sides, earlier first", () => {
      expect(otherFormatPoints(row()).map((p) => p.t)).toEqual(["2000-01-01T00:00:30Z", "2000-01-01T00:02:00Z"]);
    });
    it("lists a point at the exact time once", () => {
      const exact = point({ gap_s: 0 });
      expect(otherFormatPoints({ before: exact, after: { ...exact } })).toHaveLength(1);
    });
    it("lists one side", () => {
      expect(otherFormatPoints({ before: null, after: point() })).toHaveLength(1);
    });
  });

  it.each<[string, OtherPoint | null, string]>([
    ["a gap", point({ gap_s: 90 }), "1 min 30 s"],
    ["no point on that side", null, "none"],
    ["the item's own point", point({ gap_s: 0, self: true }), "this item"],
  ])("a side cell: %s", (_name, side, want) => {
    expect(otherSideCell(side)).toBe(want);
  });

  it.each<[string, OtherFormat, string]>([
    ["a distance", row(), "2.5 km"],
    ["no position to measure from", row({ alone: { lat: 10, lng: 20, shape: "two_sided", confidence: 0.9, distance_m: null } }), "not recorded"],
    ["only the item's own point", row({ before: point({ self: true }), after: point({ self: true }) }), "this item"],
    ["the own point and another", row({ before: point({ self: true }) }), "2.5 km"],
  ])("the alone cell: %s", (_name, other, want) => {
    expect(otherAloneCell(other)).toBe(want);
  });

  it.each<[string, OtherFormat, boolean]>([
    ["a pair of its own", row(), true],
    ["one side, which sits on its point", row({ after: null, alone: { lat: 10, lng: 20, shape: "one_sided", confidence: 0.5, distance_m: 1 } }), false],
    ["an exact point", row({ alone: { lat: 10, lng: 20, shape: "exact", confidence: 1, distance_m: 1 } }), false],
    ["the estimate's own pair", row({ before: point({ used: true }), after: point({ used: true }), used: true }), false],
    ["one of the estimate's points and one of its own", row({ before: point({ used: true }), used: true }), true],
  ])("marks where it alone would place the item: %s", (_name, other, want) => {
    expect(showsAloneEstimate(other)).toBe(want);
  });

  it.each<[string, string | null, "before" | "after" | null]>([
    ["2000-01-01T00:00:30Z", "2000-01-01T00:01:00Z", "before"],
    ["2000-01-01T00:01:30Z", "2000-01-01T00:01:00Z", "after"],
    ["2000-01-01T00:01:00Z", "2000-01-01T00:01:00Z", null],
    ["2000-01-01T00:01:00Z", null, null],
  ])("pointSide(%s, %s)", (t, takenAt, want) => {
    expect(pointSide(t, takenAt)).toBe(want);
  });
});
