// Every sentence, chip text and confidence wording of the "Why here?" view, as pure functions
// (docs/gps-debugging.md, Step 1). The wording carries invariants: today's rules agreeing is never
// "correct", 100% is only explained as a same-instant point for an exact bracket, and a confidence
// is never described as accuracy.

import { formatDuration } from "@/lib/time";
import type {
  Bracket,
  BracketShape,
  CaptureEvidence,
  CaptureSource,
  Estimate,
  FileReread,
  MediaItem,
  PlacementState,
  ProvenanceResponse,
  ServerCameraZone,
  TrackSource,
  WarningCode,
  ZoneRule,
} from "@/types";

export const NOT_RECORDED = "not recorded";
const SERVER_ZONE_UNSET = "server zone (unset)";

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
  rule: ZoneRule | null;
  wallMinusGpsS: number | null;
  offsetMin: number | null;
  offsetTag: string | null;
  offsetIgnored: boolean;
  cameraZone: string;
  cameraZoneUtcFallback: boolean;
}

function zoneFacts(
  evidence: CaptureEvidence | null,
  reread: FileReread | null,
  serverZone: ServerCameraZone
): ZoneFacts | null {
  if (evidence) {
    return {
      rule: evidence.zone,
      wallMinusGpsS: evidence.wall_minus_gps_s,
      offsetMin: evidence.offset_used_min,
      offsetTag: evidence.offset_tag,
      offsetIgnored: evidence.offset_ignored,
      cameraZone: evidence.camera_zone,
      cameraZoneUtcFallback: evidence.camera_zone_utc_fallback,
    };
  }
  if (!reread?.rule_today) return null;
  return {
    rule: reread.rule_today.zone_rule,
    wallMinusGpsS: reread.file.wall_minus_gps_s,
    offsetMin: reread.rule_today.offset_min,
    offsetTag: reread.file.offset_tag,
    offsetIgnored: reread.file.offset_ignored,
    cameraZone: reread.rule_today.zone_name ?? serverZone.name,
    cameraZoneUtcFallback: serverZone.utc_fallback,
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
    case "camera": {
      const zone =
        facts.cameraZone === SERVER_ZONE_UNSET
          ? "the server's own zone, because CAMERA_TIMEZONE is unset"
          : facts.cameraZone;
      return `${zone}; nothing in the file confirms this`;
    }
  }
}

// How the UTC offset was obtained, from the recorded evidence or else the file re-read. Null when
// no EXIF time applied, so no offset was needed.
export function zoneChip(
  evidence: CaptureEvidence | null,
  reread: FileReread | null,
  serverZone: ServerCameraZone
): ZoneChip | null {
  const facts = zoneFacts(evidence, reread, serverZone);
  if (!facts || facts.rule === null) return null;
  return {
    text: zoneChipText({ ...facts, rule: facts.rule }),
    notes: facts.offsetIgnored ? [offsetIgnoredSentence(facts.offsetTag)] : [],
    warning:
      facts.rule === "camera" && facts.cameraZoneUtcFallback
        ? "This time was read as UTC, because CAMERA_TIMEZONE is unset and the server's zone is UTC."
        : null,
  };
}

export type OffsetWord = "measured" | "declared" | "assumed" | "file date";

export interface CameraClock {
  text: string;
  word: OffsetWord | null;
}

const OFFSET_WORD: Record<ZoneRule, OffsetWord> = { gps: "measured", offset: "declared", camera: "assumed" };

const NO_WALL_CLOCK_VIDEO: CameraClock = { text: "No wall clock was written. A video container stores UTC.", word: null };

export function formatOffset(minutes: number): string {
  const absolute = Math.abs(minutes);
  const hours = String(Math.floor(absolute / 60)).padStart(2, "0");
  const rest = String(absolute % 60).padStart(2, "0");
  return `${minutes < 0 ? "-" : "+"}${hours}:${rest}`;
}

function wallClock(wall: string | null, offsetMin: number | null, rule: ZoneRule | null): CameraClock {
  const offset = offsetMin === null ? "" : ` ${formatOffset(offsetMin)}`;
  return { text: `${wall ?? NOT_RECORDED}${offset}`, word: rule ? OFFSET_WORD[rule] : null };
}

// The Camera row of the capture time: the wall clock as written, from the recorded evidence or else
// the file re-read. The browser never derives one by applying an offset.
export function cameraClock(evidence: CaptureEvidence | null, reread: FileReread | null): CameraClock {
  if (evidence) {
    switch (evidence.src) {
      case "exif_original":
      case "exif_ifd0":
        return wallClock(evidence.wall, evidence.offset_used_min, evidence.zone);
      case "container":
        return NO_WALL_CLOCK_VIDEO;
      case "mtime":
        return { text: "No wall clock. This is the file's modified date.", word: "file date" };
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
// it is exactly what a repeatable clock error would show.
export function rereadSentence(
  reread: Pick<FileReread, "class" | "delta_s">,
  mediaType: MediaItem["media_type"],
  evidence: CaptureEvidence | null
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
      return evidence?.src === "mtime" ? `${read} The stored time is the file's modified date.` : read;
    }
    case "stored_has_no_time":
      return "The file holds a capture time, but this item has none stored. Re-reading does not change that.";
    case "same":
      return "Today's rules agree with the stored time.";
    case "whole_offset":
      return `Differs by exactly ${delta}. An older rule or a changed setting would explain it.`;
    case "other":
      return `Today's rules differ by ${delta}.`;
  }
}
