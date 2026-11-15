// Every sentence, chip text and confidence wording of the "Why here?" view, as pure functions
// (docs/gps-debugging.md, Step 1). The wording carries invariants: today's rules agreeing is never
// "correct", 100% is only explained as a same-instant point for an exact bracket, and a confidence
// is never described as accuracy.

import { cameraName, formatUtcOffset } from "@/lib/clockRules";
import { formatDuration, formatInstant, formatOffset } from "@/lib/time";
import { formatShift } from "@/lib/timeEdit";
import { SERVER_ZONE_UNSET } from "@/lib/timeZones";
import type {
  Bracket,
  BracketShape,
  CameraZoneFrom,
  CaptureEvidence,
  CaptureSource,
  Estimate,
  EvidenceClockRule,
  FileReread,
  MediaItem,
  OtherFormat,
  OtherPoint,
  PlacementState,
  ProvenanceResponse,
  ServerCameraZone,
  TimeEdit,
  TrackSource,
  WarningCode,
  ZoneRule,
} from "@/types";

export const NOT_RECORDED = "not recorded";

export const STANDING_LINE = "A wrong clock looks equally consistent here.";
export const REMATCH_FAILED = "Re-match did not finish; some items may have changed.";
export const REMATCH_HEADING = "What a re-match would use";
export const REMATCH_SEGMENT_LABEL = "what a re-match would use";
export const REREAD_NOT_RECORDED = "Read now with today's rules. The rule used at upload was not recorded.";
export const NOT_OWNER = "Only the owner can see how this item was placed";
export const ITEM_GONE = "This item is no longer available.";

const CONFIDENCE_ZERO =
  "Confidence 0: the two points are a full window or more apart, or at least 100 km apart. The item is placed anyway, because the matcher has no lower limit.";
const CONFIDENCE_EXACT =
  "100% means a track point at the same second. It says nothing about whether the position or the clock is right.";
const CONFIDENCE_NEAR_FULL = "Very close points, not a correct position.";

const ESTIMATED_TIMES_NOTE =
  "This format's times may be estimated by the exporter. One point cannot be told apart from a recorded one.";
const VISIT_MIDPOINT_NOTE =
  "This point may be a visit midpoint, which can be hours from any real presence, at a place snapped to a venue that can sit far from the device. It cannot be told apart from a path point.";

export const VIDEO_TIME_MAY_BE_END = "On some cameras a video's time is when the recording ended.";

type ProvenanceFacts = Pick<ProvenanceResponse, "item" | "placement">;

function isVideo(mediaType: MediaItem["media_type"]): boolean {
  return mediaType === "video" || mediaType === "video_360";
}

export function formatDistance(meters: number): string {
  const rounded = Math.round(meters);
  return rounded < 1000 ? `${rounded} m` : `${(meters / 1000).toFixed(1)} km`;
}

// One decimal, floored and capped at 99.9%, so only an exact bracket ever reads 100%.
export function formatConfidence(confidence: number | null, shape?: BracketShape): string {
  if (confidence === null) return NOT_RECORDED;
  if (shape === "exact") return "100%";
  const tenths = Math.min(999, Math.floor(confidence * 1000));
  return `${(tenths / 10).toFixed(1)}%`;
}

// What to say under the confidence, when it needs saying.
export function confidenceNote(confidence: number, shape: BracketShape): string | null {
  if (shape === "exact") return CONFIDENCE_EXACT;
  if (confidence === 0) return CONFIDENCE_ZERO;
  if (confidence >= 0.995) return CONFIDENCE_NEAR_FULL;
  return null;
}

const STATE_BADGE: Record<PlacementState, string> = {
  current: "Up to date",
  stale: "Would change",
  no_support: "No support",
  pending: "Not placed yet",
  no_bracket: "No track nearby",
  no_time: "No capture time",
  own_position: "From the file",
  manual: "Placed by hand",
};

export function stateBadge(state: PlacementState): string {
  return STATE_BADGE[state];
}

export function shapeSentence(estimate: Pick<Estimate, "before" | "after" | "arithmetic">): string {
  const { before, after, arithmetic } = estimate;
  if (arithmetic.shape === "exact") return "A track point at the same second.";
  if (arithmetic.shape === "two_sided" && before && after) {
    const a = formatDuration(before.gap_s);
    const b = formatDuration(after.gap_s);
    return before.format === after.format
      ? `Between two points ${a} and ${b} away.`
      : `Between two points from different sources (${before.format} and ${after.format}), ${a} and ${b} away.`;
  }
  return `Copied from one point ${formatDuration((before ?? after)?.gap_s ?? 0)} away, with no distance check.`;
}

function staleSentence(now: Estimate, storedConfidence: number | null): string {
  const reason = "Your tracks, this item's time or the match window changed.";
  const delta = now.delta_m ?? 0;
  if (delta > 1) return `Re-matching would move this ${formatDistance(delta)}. ${reason}`;
  const from = formatConfidence(storedConfidence);
  const to = formatConfidence(now.confidence, now.arithmetic.shape);
  return `Re-matching would keep this position and change its confidence from ${from} to ${to}. ${reason}`;
}

function noBracketSentence(placement: ProvenanceFacts["placement"]): string {
  const before = placement.nearest_outside?.before;
  const after = placement.nearest_outside?.after;
  const lead = `No track point within ${formatDuration(placement.window_s)}.`;
  if (before && after) {
    return `${lead} The nearest is earlier by ${formatDuration(before.gap_s)} and later by ${formatDuration(after.gap_s)}.`;
  }
  if (before) return `${lead} The nearest is earlier by ${formatDuration(before.gap_s)}, and there is none later.`;
  if (after) return `${lead} The nearest is later by ${formatDuration(after.gap_s)}, and there is none earlier.`;
  return "You have no track points at all.";
}

// The plain sentence beside the state badge.
export function leadSentence({ item, placement }: ProvenanceFacts): string {
  const { state, now } = placement;
  switch (state) {
    case "current":
      return now ? shapeSentence(now) : "";
    case "stale":
      return now ? staleSentence(now, item.gps_confidence) : "";
    case "no_support":
      return "No track point supports this estimate any more. The matcher never clears a location, so it stays until you place it by hand.";
    case "pending":
      return "A track point exists within the window, but the matcher has not placed this item yet.";
    case "no_bracket":
      return noBracketSentence(placement);
    case "no_time":
      return "This item has no capture time, so it cannot be matched.";
    case "own_position":
      return isVideo(item.media_type)
        ? "The video's location tag gave this position. It can be the phone's last known fix."
        : "The file gave this position.";
    case "manual":
      return "You placed this by hand.";
  }
}

export function warningSentence(code: WarningCode, now: Estimate): string {
  const { implied_kmh, pair_km } = now.arithmetic;
  switch (code) {
    case "pair_impossible":
      return `These two points come from different formats and would need ${Math.round(implied_kmh ?? 0)} km/h to connect, so at least one is unreliable for this moment.`;
    case "pair_far":
      return `These two points come from different formats and are ${formatDistance((pair_km ?? 0) * 1000)} apart, so the position between them is uncertain.`;
    case "logger_parked":
      return "This logger barely moved between these points. It may have been parked.";
    case "confidence_zero":
      return CONFIDENCE_ZERO;
    case "point_orphan":
      return "No item exists at this point's time, so the photo or pin that wrote it was probably deleted. The matcher still uses it.";
  }
}

// The points of a bracket, earlier first. An exact bracket has one: both sides are the same row.
export function bracketPoints(estimate: Pick<Estimate, "before" | "after" | "arithmetic"> | null): Bracket[] {
  if (!estimate) return [];
  const { before, after, arithmetic } = estimate;
  const sides = arithmetic.shape === "exact" ? [before ?? after] : [before, after];
  return sides.filter((side): side is Bracket => side !== null);
}

function usesDerivedPoint(now: Estimate): boolean {
  return [now.before, now.after].some((point) => point !== null && point.kind !== "log");
}

// The line below the per-format rows, or null when none applies. An item without a capture time
// was never compared with the logs, so it gets none.
export function independenceLine({ item, placement }: ProvenanceFacts): string | null {
  const { state, now, independent, window_s } = placement;
  if (state === "own_position" || state === "manual") {
    if (item.taken_at === null) return null;
    if (!independent) return `No log point is within ${formatDuration(window_s)} of this item's time.`;
    if (independent.moved_m === null) return null;
    return `The logs at that moment are ${formatDistance(independent.moved_m)} from this position.`;
  }
  if (!now || !usesDerivedPoint(now)) return null;
  if (!independent) return "No log supports this. It rests only on other photos or pins.";
  if (independent.moved_m === null) return null;
  return `Without photos and pins this would move ${formatDistance(independent.moved_m)}.`;
}

// The points of one format's own bracket, earlier first. A point at the item's exact time is both
// sides, and is listed once.
export function otherFormatPoints(row: Pick<OtherFormat, "before" | "after">): OtherPoint[] {
  const { before, after } = row;
  if (before && after && before.source === after.source && before.t === after.t) return [before];
  return [before, after].filter((point): point is OtherPoint => point !== null);
}

export const OTHER_FORMATS_HEADING = "Each format on its own";
export const OTHER_FORMATS_NOTE =
  "Before and After are each format's nearest point on that side within the match window. Alone is how far that format's points by themselves would place this item from its marker.";
const NO_POINT = "none";
const THIS_ITEM = "this item";

// A Before or After cell: the gap to that side's point.
export function otherSideCell(point: OtherPoint | null): string {
  if (!point) return NO_POINT;
  return point.self ? THIS_ITEM : formatDuration(point.gap_s);
}

// The Alone cell: how far this format's own estimate is from the item's marker.
export function otherAloneCell(row: OtherFormat): string {
  if (otherFormatPoints(row).every((point) => point.self)) return THIS_ITEM;
  return row.alone.distance_m === null ? NOT_RECORDED : formatDistance(row.alone.distance_m);
}

// Whether the map marks where this format alone would place the item. A one-sided or exact estimate
// sits on its point, and one made of the estimate's own points is the estimate.
export function showsAloneEstimate(row: OtherFormat): boolean {
  if (row.alone.shape !== "two_sided") return false;
  return !(row.before?.used && row.after?.used);
}

// Which side of the item's time t is on, or null at the same instant or with no capture time.
export function pointSide(t: string, takenAt: string | null): "before" | "after" | null {
  if (takenAt === null) return null;
  const difference = Date.parse(t) - Date.parse(takenAt);
  if (difference === 0 || Number.isNaN(difference)) return null;
  return difference < 0 ? "before" : "after";
}

// A format-level note: stored rows carry no time kind, so it can never be about one point.
export function formatTimeNote(format: string): string | null {
  switch (format) {
    case "kml":
    case "kmz":
    case "geojson":
      return ESTIMATED_TIMES_NOTE;
    case "google_timeline":
      return VISIT_MIDPOINT_NOTE;
    default:
      return null;
  }
}

export function bracketKindChip(bracket: Pick<Bracket, "kind" | "origin">): string {
  const { kind, origin } = bracket;
  if (kind === "log" || !origin) return "Log";
  if (origin.state === "burst") return `${origin.media.length} items at this second`;
  if (origin.state === "orphan") return "A deleted item";
  const filename = origin.media[0].filename;
  return kind === "exif" ? `Your photo ${filename}` : `Your pin on ${filename}`;
}

// A point of the context track only has its format's kind: TrackResponse carries no origin.
export function contextPointKind(kind: TrackSource["kind"]): string {
  return kind === "derived" ? "another photo or pin" : "log";
}

export interface TimeOriginChip {
  text: string;
  note: string | null;
}

const TIME_ORIGIN: Record<CaptureSource, TimeOriginChip> = {
  exif_original: { text: "EXIF capture time", note: null },
  exif_ifd0: { text: "EXIF general date, may be an edit time", note: null },
  container: { text: "Video container time", note: null },
  mtime: { text: "File modified time, may be wrong", note: "A modified time is often a copy or download time." },
  none: { text: "No capture time", note: null },
};

export function timeOriginChip(evidence: CaptureEvidence | null, reread: FileReread | null): TimeOriginChip {
  const source = evidence?.src ?? reread?.rule_today?.source;
  return source ? TIME_ORIGIN[source] : { text: NOT_RECORDED, note: null };
}

function offsetIgnoredSentence(tag: string | null): string {
  return `The file declares ${tag ?? "an offset"}, which is ignored for this make.`;
}

export interface ZoneChip {
  text: string;
  notes: string[];
  warning: string | null;
}

interface ZoneFacts {
  recorded: boolean; // from capture_evidence, not the file re-read
  rule: ZoneRule | null;
  wallMinusGpsS: number | null;
  offsetMin: number | null;
  offsetTag: string | null;
  offsetIgnored: boolean;
  clockRule: EvidenceClockRule | null;
  cameraZone: string;
  cameraZoneFrom: CameraZoneFrom; // who named cameraZone; the server where nothing says
  cameraZoneUtcFallback: boolean;
}

function zoneFacts(
  evidence: CaptureEvidence | null,
  reread: FileReread | null,
  serverZone: ServerCameraZone
): ZoneFacts | null {
  if (evidence) {
    return {
      recorded: true,
      rule: evidence.zone,
      wallMinusGpsS: evidence.wall_minus_gps_s,
      offsetMin: evidence.offset_used_min,
      offsetTag: evidence.offset_tag,
      offsetIgnored: evidence.offset_ignored,
      // Rows recorded before clock rules existed lack the key.
      clockRule: evidence.clock_rule ?? null,
      cameraZone: evidence.camera_zone,
      // Rows recorded before per-user zones lack the key: they were read in the server's.
      cameraZoneFrom: evidence.camera_zone_from ?? "server",
      cameraZoneUtcFallback: evidence.camera_zone_utc_fallback,
    };
  }
  if (!reread?.rule_today) return null;
  // The zone the re-read used: the one chosen for the item's upload, or the owner's today, or the
  // server's. Only the server's can be the UTC fallback.
  const from = reread.rule_today.zone_from ?? serverZone.from ?? "server";
  return {
    recorded: false,
    rule: reread.rule_today.zone_rule,
    wallMinusGpsS: reread.file.wall_minus_gps_s,
    offsetMin: reread.rule_today.offset_min,
    offsetTag: reread.file.offset_tag,
    offsetIgnored: reread.file.offset_ignored,
    clockRule: reread.rule_today.clock_rule ?? null,
    cameraZone: reread.rule_today.zone_name ?? serverZone.name,
    cameraZoneFrom: from,
    cameraZoneUtcFallback: from === "server" && serverZone.utc_fallback,
  };
}

function zoneChipText(facts: ZoneFacts & { rule: ZoneRule }): string {
  switch (facts.rule) {
    case "gps": {
      if (facts.wallMinusGpsS === null || facts.offsetMin === null) return "GPS clock";
      return `GPS clock, residual ${formatDuration(facts.wallMinusGpsS - facts.offsetMin * 60)}`;
    }
    case "offset":
      return "OffsetTimeOriginal";
    case "clock_rule":
      return facts.clockRule ? clockRuleName(facts.clockRule) : "Your camera clock rule";
    case "camera": {
      const zone =
        facts.cameraZone === SERVER_ZONE_UNSET
          ? "the server's own zone, because CAMERA_TIMEZONE is unset"
          : facts.cameraZone;
      return `${zone}${CAMERA_ZONE_NAMED_BY[facts.cameraZoneFrom]}; nothing in the file confirms this`;
    }
  }
}

// "Your rule for Panasonic DC-TZ200: clock showed UTC+02:00", in the words of the Settings page.
function clockRuleName(rule: EvidenceClockRule): string {
  return `Your rule for ${cameraName(rule.make, rule.model)}: clock showed ${formatUtcOffset(rule.offset_min)}`;
}

// What follows a camera zone's name in its chip: who named it. The server's zone needs no words.
const CAMERA_ZONE_NAMED_BY: Record<CameraZoneFrom, string> = {
  batch: ", chosen for its upload",
  user: ", your camera zone",
  server: "",
};

export const CLOCK_RULE_KEEPS_TIME = "Changing this rule in Settings does not move this time.";
export const CAMERA_ZONE_KEEPS_TIME = "Changing your camera zone in Settings does not move this time.";

function zoneNotes(facts: ZoneFacts): string[] {
  const notes = facts.offsetIgnored ? [offsetIgnoredSentence(facts.offsetTag)] : [];
  // The recorded rule is a copy: the rule itself may have been changed or deleted since.
  if (facts.rule === "clock_rule" && facts.recorded) notes.push(CLOCK_RULE_KEEPS_TIME);
  // Likewise the owner's zone setting: a new one dates new uploads only.
  if (facts.rule === "camera" && facts.recorded && facts.cameraZoneFrom === "user") notes.push(CAMERA_ZONE_KEEPS_TIME);
  return notes;
}

// How the UTC offset was obtained, from the recorded evidence or else the file re-read. Null when
// no EXIF time applied, so no offset was needed. edited: the owner changed the stored time since,
// so what the file's time was read as no longer describes it.
export function zoneChip(
  evidence: CaptureEvidence | null,
  reread: FileReread | null,
  serverZone: ServerCameraZone,
  edited = false
): ZoneChip | null {
  const facts = zoneFacts(evidence, reread, serverZone);
  if (!facts || facts.rule === null) return null;
  const read = edited ? "The file's time was read as UTC" : "This time was read as UTC";
  return {
    text: zoneChipText({ ...facts, rule: facts.rule }),
    notes: zoneNotes(facts),
    warning:
      facts.rule === "camera" && facts.cameraZoneUtcFallback
        ? `${read}, because CAMERA_TIMEZONE is unset and the server's zone is UTC.`
        : null,
  };
}

export type OffsetWord = "measured" | "declared" | "your rule" | "assumed" | "file date";

export interface CameraClock {
  text: string;
  word: OffsetWord | null;
}

const OFFSET_WORD: Record<ZoneRule, OffsetWord> = {
  gps: "measured",
  offset: "declared",
  clock_rule: "your rule",
  camera: "assumed",
};

const NO_WALL_CLOCK_VIDEO: CameraClock = { text: "No wall clock was written. A video container stores UTC.", word: null };

function wallClock(wall: string | null, offsetMin: number | null, rule: ZoneRule | null): CameraClock {
  const offset = offsetMin === null ? "" : ` ${formatOffset(offsetMin)}`;
  return { text: `${wall ?? NOT_RECORDED}${offset}`, word: rule ? OFFSET_WORD[rule] : null };
}

// The Camera row of the capture time: the wall clock as written, from the recorded evidence or else
// the file re-read. The browser never derives one by applying an offset. edited: the owner changed
// the stored time since, so a file dated by its modified time no longer gave the stored one.
export function cameraClock(
  evidence: CaptureEvidence | null,
  reread: FileReread | null,
  edited = false
): CameraClock {
  if (evidence) {
    switch (evidence.src) {
      case "exif_original":
      case "exif_ifd0":
        return wallClock(evidence.wall, evidence.offset_used_min, evidence.zone);
      case "container":
        return NO_WALL_CLOCK_VIDEO;
      case "mtime":
        return edited
          ? { text: "No wall clock in the file.", word: null }
          : { text: "No wall clock. This is the file's modified date.", word: "file date" };
      case "none":
        return { text: "No wall clock.", word: null };
    }
  }
  if (!reread) return { text: NOT_RECORDED, word: null };
  const rule = reread.rule_today;
  if (!rule) return { text: reread.file.wall_clock ?? "No wall clock.", word: null };
  if (rule.source === "container") return NO_WALL_CLOCK_VIDEO;
  return wallClock(reread.file.wall_clock, rule.offset_min, rule.zone_rule);
}

// One check of the file re-read, or null for an id this version has no sentence for.
export function checkSentence(check: FileReread["checks"][number], offsetTag: string | null): string | null {
  const delta = formatDuration(check.delta_s ?? 0);
  switch (check.id) {
    case "taken_after_upload":
      return `The stored time is ${delta} after the upload. A correct clock cannot produce that.`;
    case "ifd0_may_be_edit_time":
      return "This time comes from the general date tag, which editing software can rewrite.";
    case "offset_ignored":
      return offsetIgnoredSentence(offsetTag);
    case "gps_clock_rejected":
      if (check.delta_s === null) {
        return "The photo's GPS clock was not used: there is no readable camera clock to compare it with.";
      }
      return `The photo's GPS clock was not used: it is ${delta} from the camera clock, which is not within 2 minutes of a real UTC offset.`;
    case "video_time_may_be_end":
      return VIDEO_TIME_MAY_BE_END;
    default:
      return null;
  }
}

// What the file re-read found, compared with the stored time. Agreement is never called correct:
// it is exactly what a repeatable clock error would show. A time the owner changed by hand is
// compared too, and the sentence then says why it need not match.
export function rereadSentence(
  reread: Pick<FileReread, "class" | "delta_s">,
  mediaType: MediaItem["media_type"],
  evidence: CaptureEvidence | null,
  edit: TimeEdit | null = null
): string {
  const text = rereadFinding(reread, mediaType, evidence, edit);
  // whole_offset and no_file_time say it in their own words.
  if (!edit || (reread.class !== "same" && reread.class !== "other")) return text;
  return `${text} You ${editVerb(edit)} the stored time by hand, so it need not match the file.`;
}

function editVerb(edit: TimeEdit): string {
  return edit.kind === "shift" ? "shifted" : "set";
}

function rereadFinding(
  reread: Pick<FileReread, "class" | "delta_s">,
  mediaType: MediaItem["media_type"],
  evidence: CaptureEvidence | null,
  edit: TimeEdit | null
): string {
  const delta = formatDuration(reread.delta_s ?? 0);
  switch (reread.class) {
    case "unverifiable_transcoded":
      return evidence
        ? "This video was re-encoded at upload, so its time cannot be checked against the file."
        : "This video may have been re-encoded at upload, so its time may not be checkable against the file.";
    case "no_file_time": {
      const read = isVideo(mediaType)
        ? "No capture time could be read from this video's container."
        : "No capture time could be read from this file. This reader understands EXIF in JPEG and TIFF only.";
      if (edit) return `${read} The stored time is the one you ${editVerb(edit)} by hand.`;
      return evidence?.src === "mtime" ? `${read} The stored time is the file's modified date.` : read;
    }
    case "stored_has_no_time":
      return "The file holds a capture time, but this item has none stored. Re-reading does not change that.";
    case "same":
      return "Today's rules agree with the stored time.";
    case "whole_offset":
      return edit
        ? `Differs by exactly ${delta}. Your change explains the difference.`
        : `Differs by exactly ${delta}. An older rule or a changed setting would explain it.`;
    case "other":
      return `Today's rules differ by ${delta}.`;
  }
}

// Who moves a stored time: the owner, in the edit and shift dialogs, or an administrator.
export const STORED_TIME_MOVES = "A stored time moves only when you edit or shift it, or an administrator runs server retime.";

// Today's reading used one of the owner's clock rules and differs from the stored time: say which
// rule, and that a stored time does not follow it on its own. server retime leaves a time the
// owner changed by hand alone, which the card says once beside the change itself.
export function rereadClockRuleNote(
  reread: Pick<FileReread, "class" | "rule_today">,
  edit: TimeEdit | null = null
): string | null {
  const rule = reread.rule_today;
  if (rule?.zone_rule !== "clock_rule" || !rule.clock_rule) return null;
  if (reread.class !== "whole_offset" && reread.class !== "other") return null;
  const { make, model, offset_min } = rule.clock_rule;
  const text = `Today's reading uses your rule for ${cameraName(make, model)} (clock showed ${formatUtcOffset(offset_min)}).`;
  return edit ? text : `${text} ${STORED_TIME_MOVES}`;
}

// Says that server retime moved the stored time, and by how much, or null for any other writer.
// The earlier time itself is listed with the other instants.
export function retimeSentence(evidence: CaptureEvidence | null, takenAt: string | null): string | null {
  if (evidence?.by !== "retime") return null;
  const from = evidence.retimed_from ?? null;
  if (from === null) return "Re-timed on the server. The time stored before was not recorded.";
  const seconds = takenAt === null ? Number.NaN : (Date.parse(takenAt) - Date.parse(from)) / 1000;
  if (Number.isNaN(seconds) || seconds === 0) return "Re-timed on the server.";
  return `Re-timed on the server: ${formatDuration(seconds)} ${seconds < 0 ? "earlier" : "later"} than the time stored before.`;
}

// ── A time the owner changed (media.time_edit) ──
// capture_evidence still says how the file's own time was read; these say that the owner changed
// the stored time afterwards, in the edit dialog or with a shift.

export const EDIT_KEEPS_TIME = "A time you changed by hand stays when an administrator runs server retime.";

// "You set this time by hand on 15 Nov 2026, 19:00:00 Europe/Warsaw. It moved 1 h earlier."
// The change's own instant is shown in timeZone, which the output names. A difference that rounds
// to no whole second (a stored time with milliseconds, set to its own second) is not a move.
export function timeEditSentence(edit: TimeEdit, timeZone: string): string {
  const at = formatInstant(edit.at, timeZone);
  const delta = edit.delta_s;
  const moved = delta !== null && Math.round(Math.abs(delta)) !== 0;
  let text: string;
  if (edit.kind === "shift") {
    text = moved ? `You shifted this time ${formatShift(delta)} on ${at}.` : `You shifted this time on ${at}.`;
  } else if (edit.from === null) {
    text = `You set this time by hand on ${at}. The item had no capture time before.`;
  } else {
    text = moved ? `You set this time by hand on ${at}. It moved ${formatShift(delta)}.` : `You set this time by hand on ${at}.`;
  }
  // An earlier change gave the item its first time: Before your first change has nothing to show.
  if (edit.original === null && edit.from !== null) text += " It had no capture time before your first change.";
  return text;
}

// What the owner entered for a time set by hand: the wall clock and the zone or offset it was read
// in. Null for a shift.
export function timeEditEntered(edit: TimeEdit): string | null {
  if (edit.kind !== "set" || edit.wall === null) return null;
  const wall = edit.wall.replace("T", " ");
  const offset = edit.offset_min === null ? null : formatUtcOffset(edit.offset_min);
  if (edit.zone !== null) return offset ? `Entered as ${wall} in ${edit.zone}, ${offset} on that date.` : `Entered as ${wall} in ${edit.zone}.`;
  return offset ? `Entered as ${wall} at ${offset}.` : `Entered as ${wall}.`;
}

function sameInstant(a: string | null, b: string | null): boolean {
  return a !== null && b !== null && Date.parse(a) === Date.parse(b);
}

// The times a change leaves behind, for the list of instants: the stored time before the last
// change, and the one before the first change when that differs.
export function timeEditInstants(edit: TimeEdit | null | undefined): [label: string, iso: string | null][] {
  if (!edit) return [];
  const original = sameInstant(edit.original, edit.from) ? null : edit.original;
  return [
    ["Before your change", edit.from],
    ["Before your first change", original],
  ];
}
