// Editing capture times by hand: the wall clock the edit dialog starts from, the zone it is read
// in, and the amount and selection of a shift (PATCH /api/media/{id}/taken-at and
// POST /api/media/shift-time). All pure.

import { CLOCK_OFFSETS, formatUtcOffset } from "@/lib/clockRules";
import { formatCount } from "@/lib/numbers";
import { formatDuration } from "@/lib/time";
import type { MediaItem, SetTakenAtResult, ShiftTimesResult, TakenAtZone } from "@/types";

// The backend stores no time whose UTC year falls outside these (jsonCanCarry), and refuses a wall
// clock outside them.
export const FIRST_YEAR = 1;
export const LAST_YEAR = 9998;

// The most items one shift request may name (maxShiftMediaIDs).
export const MAX_SHIFT_ITEMS = 5000;

// The largest shift the backend takes: 100 years of 365.25 days.
export const MAX_SHIFT_SECONDS = 3_155_760_000;

// formatToParts in a fixed locale, so the parts are ASCII digits whatever the browser's language.
// The era tells a year before 1 (1 BC reads as year 1) from a real one.
const wallFormatters = new Map<string, Intl.DateTimeFormat>();

function wallFormatter(timeZone: string): Intl.DateTimeFormat {
  let formatter = wallFormatters.get(timeZone);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat("en-US", {
      timeZone,
      era: "short",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    });
    wallFormatters.set(timeZone, formatter);
  }
  return formatter;
}

// The wall clock an instant showed in an IANA zone, as "YYYY-MM-DDTHH:MM:SS" (the layout the
// backend takes), with that zone's daylight saving on that date. null for a value that is not an
// instant, a zone the browser does not know, or a year the backend cannot store. Fractions of a
// second are dropped.
export function wallClockIn(iso: string, timeZone: string): string | null {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return null;
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = wallFormatter(timeZone).formatToParts(date);
  } catch {
    return null;
  }
  const part: Record<string, string> = {};
  for (const { type, value } of parts) part[type] = value;
  const year = Number(part.year);
  if (part.era !== "AD" || !(year >= FIRST_YEAR && year <= LAST_YEAR)) return null;
  // Some engines write midnight as 24 even with h23.
  const hour = part.hour === "24" ? "00" : part.hour;
  return `${String(year).padStart(4, "0")}-${part.month}-${part.day}T${hour}:${part.minute}:${part.second}`;
}

// The UTC offset an instant had in an IANA zone, in minutes (its wall clock there minus the instant,
// to the second), or null where wallClockIn gives no wall clock.
function offsetAt(ms: number, timeZone: string): number | null {
  const wall = wallClockIn(new Date(ms).toISOString(), timeZone);
  if (wall === null) return null;
  return (Date.parse(`${wall}Z`) - Math.floor(ms / 1000) * 1000) / 60000;
}

const HOUR_MS = 3_600_000;

// Whether an instant's wall clock in an IANA zone is one the zone showed twice (the hour repeated
// when daylight saving time ends), and the instant's own UTC offset there, in minutes. Read back
// in the zone, such a wall clock names the earlier of the two instants (the backend's choice), so
// an edit that starts from it keeps the instant only with its own offset. null for a value that
// is not an instant, a zone the browser does not know, or a year the backend cannot store.
export function repeatedWallClock(iso: string, timeZone: string): { repeated: boolean; offsetMin: number } | null {
  const parsed = Date.parse(iso);
  if (Number.isNaN(parsed)) return null;
  const ms = Math.floor(parsed / 1000) * 1000;
  const wall = wallClockIn(new Date(ms).toISOString(), timeZone);
  const offsetMin = offsetAt(ms, timeZone);
  if (wall === null || offsetMin === null) return null;
  // Every offset the zone used within two days either side, as the backend looks for them.
  const offsets = new Set<number>();
  for (let hours = -48; hours <= 48; hours++) {
    const other = offsetAt(ms + hours * HOUR_MS, timeZone);
    if (other !== null && other !== offsetMin) offsets.add(other);
  }
  const wallMs = Date.parse(`${wall}Z`);
  for (const other of offsets) {
    const candidate = wallMs - other * 60000;
    if (wallClockIn(new Date(candidate).toISOString(), timeZone) === wall) return { repeated: true, offsetMin };
  }
  return { repeated: false, offsetMin };
}

// A wall clock as the values of a date input ("YYYY-MM-DD") and a time input ("HH:MM:SS"); both
// empty for none.
export function splitWall(wall: string | null): { date: string; time: string } {
  if (wall === null) return { date: "", time: "" };
  const [date = "", time = ""] = wall.split("T");
  return { date, time };
}

function isLeapYear(year: number): boolean {
  return (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
}

function daysInMonth(year: number, month: number): number {
  if (month === 2) return isLeapYear(year) ? 29 : 28;
  return [4, 6, 9, 11].includes(month) ? 30 : 31;
}

const DATE_VALUE = /^(\d{4})-(\d{2})-(\d{2})$/;
// A time input leaves the seconds out when they are zero in some browsers, and can add a fraction.
const TIME_VALUE = /^(\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?$/;

// The wall clock a date input and a time input hold together, or null when either is empty, not a
// real date or time, or outside the years the backend takes. A fraction of a second is dropped.
export function joinWall(date: string, time: string): string | null {
  const d = DATE_VALUE.exec(date);
  const t = TIME_VALUE.exec(time);
  if (!d || !t) return null;
  const [year, month, day] = [Number(d[1]), Number(d[2]), Number(d[3])];
  if (year < FIRST_YEAR || year > LAST_YEAR || month < 1 || month > 12) return null;
  if (day < 1 || day > daysInMonth(year, month)) return null;
  const seconds = t[3] ?? "00";
  if (Number(t[1]) > 23 || Number(t[2]) > 59 || Number(seconds) > 59) return null;
  return `${d[1]}-${d[2]}-${d[3]}T${t[1]}:${t[2]}:${seconds}`;
}

// The zone select's values: an IANA name as it is, a fixed offset as "offset:<minutes>". IANA names
// never contain a colon.
const OFFSET_PREFIX = "offset:";

export function zoneChoiceValue(zone: TakenAtZone): string {
  return "zone" in zone ? zone.zone : `${OFFSET_PREFIX}${zone.utcOffsetMin}`;
}

// The zone a select value names, or null for "" or an offset the backend does not take.
export function parseZoneChoice(value: string): TakenAtZone | null {
  if (value.startsWith(OFFSET_PREFIX)) {
    const text = value.slice(OFFSET_PREFIX.length);
    const minutes = Number(text);
    return /^-?\d+$/.test(text) && CLOCK_OFFSETS.includes(minutes) ? { utcOffsetMin: minutes } : null;
  }
  return value === "" ? null : { zone: value };
}

// "Europe/Warsaw", or "UTC+02:00" for a fixed offset.
export function takenAtZoneLabel(zone: TakenAtZone): string {
  return "zone" in zone ? zone.zone : formatUtcOffset(zone.utcOffsetMin);
}

// A shift as the dialog's fields hold it: a direction and four counts as typed.
export interface ShiftAmount {
  direction: "later" | "earlier";
  days: string;
  hours: string;
  minutes: string;
  seconds: string;
}

export const EMPTY_SHIFT: ShiftAmount = { direction: "later", days: "", hours: "", minutes: "", seconds: "" };

const UNIT_SECONDS: [field: Exclude<keyof ShiftAmount, "direction">, seconds: number][] = [
  ["days", 86400],
  ["hours", 3600],
  ["minutes", 60],
  ["seconds", 1],
];

// The shift in seconds, positive for later; null when a field is not a whole number of 0 or more.
// An empty field counts as 0. A count may run past its unit (36 hours).
export function shiftSeconds(amount: ShiftAmount): number | null {
  let total = 0;
  for (const [field, size] of UNIT_SECONDS) {
    const text = amount[field].trim();
    if (text === "") continue;
    if (!/^\d+$/.test(text)) return null;
    total += Number(text) * size;
  }
  return amount.direction === "earlier" && total !== 0 ? -total : total;
}

// A shift's size in every unit it has, largest first: "1 d 2 h 3 min 4 s". formatDuration names
// each unit, but alone it keeps only the two largest, and a shift must be stated exactly. A
// fraction of a second (a time stored with milliseconds, set to a whole second) is "under 1 s".
export function formatShiftAmount(seconds: number): string {
  if (Math.abs(seconds) > 0 && Math.abs(seconds) < 1) return formatDuration(seconds);
  let left = Math.round(Math.abs(seconds));
  if (left === 0) return formatDuration(0);
  const parts: string[] = [];
  for (const [, size] of UNIT_SECONDS) {
    const count = Math.floor(left / size);
    if (count === 0) continue;
    parts.push(formatDuration(count * size));
    left -= count * size;
  }
  return parts.join(" ");
}

// "1 h later", "2 d 30 min earlier".
export function formatShift(deltaSeconds: number): string {
  return `${formatShiftAmount(deltaSeconds)} ${deltaSeconds < 0 ? "earlier" : "later"}`;
}

// An instant moved by a shift, in RFC 3339 UTC; null when the value is not an instant or the result
// falls outside the years the backend can store.
export function shiftInstant(iso: string, deltaSeconds: number): string | null {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return null;
  const moved = new Date(ms + deltaSeconds * 1000);
  if (Number.isNaN(moved.getTime())) return null;
  const year = moved.getUTCFullYear();
  if (year < FIRST_YEAR || year > LAST_YEAR) return null;
  return moved.toISOString();
}

// "1 item", "5,000 items".
export function itemCount(n: number): string {
  return `${formatCount(n)} ${n === 1 ? "item" : "items"}`;
}

// Items whose file names no camera model, by kind. Most videos are among them (ingest reads a
// model only from a photo's EXIF, or a Panasonic video's), so the kind at least separates a
// phone's photos from a camera's videos.
const NO_MODEL_LABELS: Record<MediaItem["media_type"], string> = {
  photo: "No camera model in the file: photos",
  video: "No camera model in the file: videos",
  photo_360: "No camera model in the file: 360° photos",
  video_360: "No camera model in the file: 360° videos",
};

// A group key no model can have: models are trimmed text, never starting with NUL.
function noModelKey(mediaType: MediaItem["media_type"]): string {
  return `\u0000${mediaType}`;
}

// The caller's own dated items from one camera (MediaItem.camera_model, the EXIF model), or of
// one kind without a model.
export interface CameraGroup {
  key: string; // the model as stored, trimmed; NUL and the media type for items without one
  label: string;
  ids: string[];
  first: { id: string; filename: string; taken_at: string }; // the earliest item
  last: string; // the latest taken_at
}

export interface ShiftCandidates {
  groups: CameraGroup[]; // most items first, then by label
  ownUndated: number; // the caller's items without a time: nothing to shift
  others: number; // items someone else owns (an album gallery): only their owner can change them
}

// What a shift can apply to among a gallery's items, by camera: a trip often mixes a phone whose
// clock was right with a camera whose clock was off.
export function shiftCandidates(items: MediaItem[], ownerId: string): ShiftCandidates {
  const byKey = new Map<string, { group: CameraGroup; firstMs: number; lastMs: number }>();
  let ownUndated = 0;
  let others = 0;
  for (const item of items) {
    if (item.owner_id !== ownerId) {
      others++;
      continue;
    }
    const ms = item.taken_at === null ? NaN : Date.parse(item.taken_at);
    if (item.taken_at === null || Number.isNaN(ms)) {
      ownUndated++;
      continue;
    }
    const model = item.camera_model?.trim() ?? "";
    const key = model || noModelKey(item.media_type);
    const entry = byKey.get(key);
    if (!entry) {
      byKey.set(key, {
        group: {
          key,
          label: model || (NO_MODEL_LABELS[item.media_type] ?? NO_MODEL_LABELS.photo),
          ids: [item.id],
          first: { id: item.id, filename: item.filename, taken_at: item.taken_at },
          last: item.taken_at,
        },
        firstMs: ms,
        lastMs: ms,
      });
      continue;
    }
    entry.group.ids.push(item.id);
    if (ms < entry.firstMs) {
      entry.firstMs = ms;
      entry.group.first = { id: item.id, filename: item.filename, taken_at: item.taken_at };
    }
    if (ms > entry.lastMs) {
      entry.lastMs = ms;
      entry.group.last = item.taken_at;
    }
  }
  const groups = [...byKey.values()]
    .map((entry) => entry.group)
    .sort((a, b) => b.ids.length - a.ids.length || a.label.localeCompare(b.label, "en"));
  return { groups, ownUndated, others };
}

// The earliest of these groups' items, which the shift dialog previews.
export function earliestItem(groups: CameraGroup[]): CameraGroup["first"] | null {
  let earliest: CameraGroup["first"] | null = null;
  for (const group of groups) {
    if (earliest === null || Date.parse(group.first.taken_at) < Date.parse(earliest.taken_at)) {
      earliest = group.first;
    }
  }
  return earliest;
}

// Whether shifting every item of these groups keeps each time within the years the backend can
// store: their earliest and latest times are the ones that could leave them.
export function shiftStaysInRange(groups: CameraGroup[], deltaSeconds: number): boolean {
  return groups.every(
    (group) => shiftInstant(group.first.taken_at, deltaSeconds) !== null && shiftInstant(group.last, deltaSeconds) !== null
  );
}

// The days a group spans, in a zone, naming it: "13 Mar 2019 – 20 Mar 2019 Europe/Warsaw", or one
// day when it spans one.
export function formatDayRange(firstIso: string, lastIso: string, timeZone: string, locale?: string): string {
  const format = new Intl.DateTimeFormat(locale, { timeZone, year: "numeric", month: "short", day: "numeric" });
  const first = format.format(new Date(firstIso));
  const last = format.format(new Date(lastIso));
  return `${first === last ? first : `${first} – ${last}`} ${timeZone}`;
}

// Said after a change whose follow-up failed: the time is saved, but the change already cleared
// the estimated locations near the old and new times, and the matcher and trip detection did not
// finish. Re-match (POST /api/gps-matcher/run) places the estimates again; trip detection runs
// again after the next upload or time change.
const FOLLOWUP_FAILED_REST =
  "estimated locations near the old and new times were cleared and not placed again, and trips were not detected again. In Why here?, Re-match my library places items that wait for an estimate; trips are detected again after your next upload or time change.";

export const FOLLOWUP_FAILED = `The time is saved, but ${FOLLOWUP_FAILED_REST}`;

// The same after a shift, which may have moved many times.
export const FOLLOWUP_FAILED_SHIFT = `The times are saved, but ${FOLLOWUP_FAILED_REST}`;

// The notice after a shift: "Shifted 3 items 1 h later."
export function shiftedMessage(result: ShiftTimesResult, deltaSeconds: number): string {
  let text = `Shifted ${itemCount(result.shifted)} ${formatShift(deltaSeconds)}.`;
  if (result.undated > 0) {
    text += ` ${itemCount(result.undated)} without a capture time ${result.undated === 1 ? "was" : "were"} left as before.`;
  }
  return result.followup_failed ? `${text} ${FOLLOWUP_FAILED_SHIFT}` : text;
}

// The notice after one item's time was set, or null when there is nothing to add to the new time
// on screen.
export function timeSavedMessage(result: SetTakenAtResult): string | null {
  return result.followup_failed ? FOLLOWUP_FAILED : null;
}

// Said when a shift's request failed without an answer (a gateway timeout, a lost connection): the
// backend may have committed it, and shifting the same items again would move them twice.
export const SHIFT_OUTCOME_UNKNOWN =
  "The server did not answer in time. The shift may have been saved: check the items' times before shifting again.";
