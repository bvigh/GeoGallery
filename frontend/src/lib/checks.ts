// Every title, sentence and table cell of the Checks view, and the order of its track file table, as
// pure functions (docs/gps-debugging.md, Step 2). As in lib/evidence.ts, a measurement is never
// called correct or wrong: a check says what deserves a look, and why.

import { formatCount } from "@/lib/numbers";
import { matchesSearch } from "@/lib/search";
import { formatSpan } from "@/lib/time";
import type { AgreementFile, AgreementResponse, CheckCode, ChecksCheck, ChecksFile, ChecksGroup } from "@/types";

export const NOTHING_FOUND = "Nothing found.";
export const CHECKS_TIMEOUT = "The checks took too long and were stopped. Try again in a moment.";
export const COMPARING = "Comparing files… This can take up to a minute.";

export const RESIDUALS_CAPTION =
  "The typical distance between each format's points and your photos that have their own position, at the same moment (within 5 s). It measures disagreement and does not say which side is right. Photos whose clock was off add to it slightly.";
export const NO_RESIDUALS = "None of your photos with their own position has a point from your track files within 5 s.";

export const COMPARE_NOTE =
  "Compare files measures each log file against your other formats at up to 20 moments, and how evenly its times are spaced.";
const LEFT_OUT = 'Files marked "not reached" were left out.';
export const TRUNCATED_NOTE = `The time limit stopped the comparison before every file was done. ${LEFT_OUT}`;
export const COMPARE_BUSY = "The server is busy with other comparisons. Try again in a minute.";

// The comparison's median distance: the larger, the further the file is from the other formats.
export const DISTANCE_COLUMN = "Distance to other formats";

// What the track file table's columns mean; the last two only once the files were compared.
export const FILE_COLUMN_NOTES: [column: string, note: string][] = [
  [
    "Kind",
    "Recorded logs keep the device's own times. Google Timeline's semantic points may be visit midpoints. Times in KML, KMZ and GeoJSON may be estimated by the exporter. Derived points were written from your own photos and pins.",
  ],
  ["Items placed", "Estimates whose nearest point before or after comes from this file."],
];
export const COMPARED_COLUMN_NOTES: [column: string, note: string][] = [
  [
    DISTANCE_COLUMN,
    "The median distance to the nearest point of another format within 5 minutes, at up to 20 moments across the file. Positions of your photos and your pins are not compared: they come from your own items.",
  ],
  [
    "Spacing",
    "The share of gaps between consecutive points that equal the file's most common gap. Fixed-rate recorders and exporters that spread times evenly both score high.",
  ],
];

// Maps, not object literals: the keys come from the server, and "constructor" must not find a function.
const CHECK_TITLES = new Map<string, string>([
  ["pair_impossible", "Impossible speeds between formats"],
  ["future_track_points", "Track points in the future"],
  ["taken_after_upload", "Items dated after their upload"],
  ["stale_estimates", "Out-of-date estimates"],
  ["pair_far", "Far-apart points from different formats"],
  ["orphan_points", "Points left by deleted items"],
  // What the check counts: an estimate on such a point that other formats could place as well. One
  // that only these formats can place is not counted, so "Nothing found" must not deny it exists.
  ["time_may_be_estimated_used", "Estimates on KML, KMZ or GeoJSON points that other formats could place"],
]);

// What each check counts: the two point checks count track points.
const CHECK_NOUNS = new Map<string, string>([
  ["pair_impossible", "estimate"],
  ["future_track_points", "point"],
  ["taken_after_upload", "item"],
  ["stale_estimates", "estimate"],
  ["pair_far", "estimate"],
  ["orphan_points", "point"],
  ["time_may_be_estimated_used", "estimate"],
]);

// A code this version does not know (a newer server) shows as written.
export function checkTitle(code: CheckCode): string {
  return CHECK_TITLES.get(code) ?? code;
}

// Point counts run into millions, so every count is grouped ("27,558 points").
function counted(n: number, noun: string): string {
  return `${formatCount(n)} ${n === 1 ? noun : `${noun}s`}`;
}

// "12 estimates", "1 point".
export function checkTotal({ code, total }: Pick<ChecksCheck, "code" | "total">): string {
  const noun = CHECK_NOUNS.get(code);
  return noun ? counted(total, noun) : `${formatCount(total)} found`;
}

const SEVERITY_RANK: Record<ChecksCheck["severity"], number> = { high: 0, low: 1 };

// High severity first, each in the server's order (sort is stable).
export function bySeverity(checks: readonly ChecksCheck[]): ChecksCheck[] {
  return [...checks].sort((a, b) => SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity]);
}

// Metres below 1 km, one decimal below 10 km, then whole kilometres, so a column running from a few
// metres to thousands of kilometres stays readable.
export function formatDistance(meters: number): string {
  const rounded = Math.round(meters);
  if (rounded < 1000) return `${rounded} m`;
  const km = Math.round(meters / 100) / 10;
  return km < 10 ? `${km.toFixed(1)} km` : `${Math.round(meters / 1000)} km`;
}

// Floored, so 100% means every gap is the most common one.
export function formatShare(share: number): string {
  if (share >= 1) return "100%";
  if (share <= 0) return "0%";
  const percent = Math.min(99, Math.floor(share * 100 + 1e-9));
  return percent < 1 ? "under 1%" : `${percent}%`;
}

// The derived labels in words: their points were written from the owner's own items.
const DERIVED_FILE_NAMES = new Map([
  ["exif", "Positions of your photos"],
  ["manual", "Your pins"],
]);
const DERIVED_PHRASES = new Map([
  ["exif", "your photos"],
  ["manual", "your pins"],
]);

// A track file as the table names it: a derived label in words, else the file name after the format
// prefix. A label without one, which a GeoGallery row can keep, is shown whole.
export function fileName(source: string): string {
  const derived = DERIVED_FILE_NAMES.get(source);
  if (derived) return derived;
  const colon = source.indexOf(":");
  return colon >= 0 && colon < source.length - 1 ? source.slice(colon + 1) : source;
}

// The same inside a sentence.
function sourcePhrase(source: string): string {
  return DERIVED_PHRASES.get(source) ?? fileName(source);
}

// "time_may_be_estimated" → "time may be estimated".
export function kindName(kind: string): string {
  return kind.replace(/_/g, " ");
}

type Evidence = ChecksGroup["evidence"];

function numberOf(evidence: Evidence, key: string): number | null {
  const value = evidence[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function textOf(evidence: Evidence, key: string): string | null {
  const value = evidence[key];
  return typeof value === "string" ? value : null;
}

// One finding: what was found, further evidence (a sentence each), and the instants to show in UTC
// and the browser zone (null: a year JSON cannot carry).
export interface FindingText {
  sentence: string;
  notes: string[];
  instants: { label: string; iso: string | null }[];
}

function finding(sentence: string, ...notes: (string | null)[]): FindingText {
  return { sentence, notes: notes.filter((note): note is string => note !== null), instants: [] };
}

// The two raw prefixes of a pair key ("esoko+gpx"). A prefix with a "+" of its own makes the key
// ambiguous, and the label is then shown as written.
function pairFormats({ key, label }: ChecksGroup): string {
  const parts = key.split("+");
  return parts.length === 2 && parts[0] !== "" && parts[1] !== "" ? `${parts[0]} and ${parts[1]}` : label;
}

// The file most of the group's points come from. The server sends none when two files share the
// top count (one suspect file against the one log of its days puts both in every estimate), and
// with no file used twice none stands out either.
function topSourceNote(evidence: Evidence): string | null {
  const source = textOf(evidence, "top_source");
  const estimates = numberOf(evidence, "top_source_estimates");
  if (source === null || estimates === null || estimates < 2) return null;
  return `Most often from ${sourcePhrase(source)} (${counted(estimates, "estimate")}).`;
}

function pairText(group: ChecksGroup, impossible: boolean): FindingText {
  const { count: n, evidence } = group;
  const one = n === 1;
  const between = `${counted(n, "estimate")} ${one ? "lies" : "lie"} between points from ${pairFormats(group)}`;
  const upTo = one ? "" : "up to ";
  if (impossible) {
    const kmh = numberOf(evidence, "max_kmh");
    const speed = kmh === null ? "an impossible speed" : `${upTo}${Math.round(kmh)} km/h`;
    const unreliable = one ? "one of the two is unreliable at that moment" : "one point of each pair is unreliable at those moments";
    return finding(`${between} that would need ${speed} to connect, so at least ${unreliable}.`, topSourceNote(evidence));
  }
  const km = numberOf(evidence, "max_km");
  const apart = km === null ? "far apart" : `${upTo}${formatDistance(km * 1000)} apart`;
  const uncertain = one ? "the position between them is" : "the positions between them are";
  return finding(`${between} that are ${apart}, so ${uncertain} uncertain.`, topSourceNote(evidence));
}

// The items a derived point takes its time from, one and several.
const DERIVED_ITEMS = new Map([
  ["exif", ["that photo is", "those photos are"]],
  ["manual", ["the item you placed is", "the items you placed are"]],
]);

// A derived point in the future has its item's time; a log's comes from its receiver.
function futureText(group: ChecksGroup): FindingText {
  const { count: n, key, evidence } = group;
  const one = n === 1;
  const derived = DERIVED_PHRASES.get(key);
  const items = DERIVED_ITEMS.get(key);
  let sentence: string;
  if (derived && items) {
    const from = one ? `one of ${derived}` : derived;
    sentence = `${counted(n, "point")} written from ${from} ${one ? "is" : "are"} dated more than a day in the future, as ${items[one ? 0 : 1]}.`;
  } else {
    sentence = `${counted(n, "point")} of ${fileName(key)} ${one ? "is" : "are"} dated more than a day in the future. A receiver that kept the wrong GPS week writes such times.`;
  }
  const first = textOf(evidence, "first");
  const last = textOf(evidence, "last");
  return {
    sentence,
    notes: [],
    instants: one ? [{ label: "Time", iso: first }] : [{ label: "First", iso: first }, { label: "Last", iso: last }],
  };
}

function locatedNote(n: number, located: number): string {
  if (n === 1) return located > 0 ? "It has a location." : "It has no location.";
  if (located === 0) return "None of them has a location.";
  if (located === n) return "All of them have a location.";
  return `${formatCount(located)} of them ${located === 1 ? "has" : "have"} a location.`;
}

// The key is "" when GeoGallery stored no camera model: rows from before Step 1 never had one, and
// most videos carry none it reads. So the model is unknown, which says nothing about the camera.
function afterUploadText(group: ChecksGroup): FindingText {
  const { count: n, label, evidence } = group;
  const one = n === 1;
  const items = label === "" ? `${counted(n, "item")} whose camera model is not known` : `${counted(n, "item")} from ${label}`;
  // A camera whose clock is years off is common enough: "about 13 months", not "396 d".
  const ahead = numberOf(evidence, "max_ahead_s");
  const sentence = one
    ? `${items} is dated ${ahead === null ? "" : `${formatSpan(ahead)} `}after its upload.`
    : `${items} are dated after their upload${ahead === null ? "" : `, up to ${formatSpan(ahead)} after`}.`;
  const located = numberOf(evidence, "located");
  return finding(`${sentence} A correct clock cannot produce that.`, located === null ? null : locatedNote(n, located));
}

function staleText(group: ChecksGroup): FindingText {
  const { count: n, key, evidence } = group;
  const one = n === 1;
  if (key === "no_support") {
    return finding(
      one
        ? "1 estimate has no track point within the match window any more. The matcher never clears a location, so it stays until you place it by hand."
        : `${counted(n, "estimate")} have no track point within the match window any more. The matcher never clears a location, so they stay until you place them by hand.`
    );
  }
  // Within 1 m an estimate counts as in place, as in the evidence view.
  const moved = numberOf(evidence, "max_delta_m");
  let change: string;
  if (moved === null) change = `${counted(n, "estimate")} would change if re-matched.`;
  else if (moved > 1) {
    change = one
      ? `1 estimate would move ${formatDistance(moved)} if re-matched.`
      : `${counted(n, "estimate")} would move (up to ${formatDistance(moved)}) or change confidence if re-matched.`;
  } else {
    change = one
      ? "1 estimate would keep its position but change its confidence if re-matched."
      : `${counted(n, "estimate")} would keep their positions but change their confidence if re-matched.`;
  }
  const reason = one
    ? "Your tracks, this item's time or the match window changed since it was placed."
    : "Your tracks, these items' times or the match window changed since they were placed.";
  return finding(`${change} ${reason}`);
}

function orphanText(group: ChecksGroup): FindingText {
  const { count: n, key, evidence } = group;
  const one = n === 1;
  let sentence: string;
  if (key === "manual") {
    sentence = one
      ? "1 point written from one of your pins has no item at its time any more, so the item you placed by hand was probably deleted."
      : `${counted(n, "point")} written from your pins have no item at their time any more, so the items you placed by hand were probably deleted.`;
  } else {
    sentence = one
      ? "1 point written from one of your photos has no item at its time any more, so that photo was probably deleted."
      : `${counted(n, "point")} written from your photos have no item at their time any more, so those photos were probably deleted.`;
  }
  const estimates = numberOf(evidence, "estimates");
  const them = one ? "it" : "them";
  let note: string | null = null;
  if (estimates === 0) note = `No estimate rests on ${them}.`;
  else if (estimates !== null) note = `${counted(estimates, "estimate")} still ${estimates === 1 ? "rests" : "rest"} on ${them}.`;
  return finding(sentence, note);
}

const ESTIMATED_TIME_FORMATS = new Map([
  ["kml", "KML"],
  ["geojson", "GeoJSON"],
  ["kmz", "KMZ"],
]);

// KMZ is listed apart: its time spacing was never checked, so the comparison leaves it out as well.
function estimatedTimesText(group: ChecksGroup): FindingText {
  const { count: n, key, evidence } = group;
  const format = ESTIMATED_TIME_FORMATS.get(key) ?? key;
  const kmz = key === "kmz";
  const using = numberOf(evidence, "using");
  let subject: string;
  if (using === null) subject = `${counted(n, "estimate")} that ${n === 1 ? "uses" : "use"} a ${format} point`;
  else if (using === 1) subject = `The only estimate that uses a ${format} point`;
  else if (n === using) subject = `All ${counted(using, "estimate")} that use a ${format} point`;
  else subject = `${formatCount(n)} of the ${counted(using, "estimate")} that use a ${format} point`;

  const formatNote = kmz
    ? `KMZ's time spacing has not been checked, so ${n === 1 ? "this estimate" : "these estimates"} may be fine.`
    : "This format's times may be estimated by the exporter.";

  const without = `Without ${kmz ? "KML, KMZ and GeoJSON" : "KML and GeoJSON"} points`;
  const over100 = numberOf(evidence, "over_100m");
  const over1km = numberOf(evidence, "over_1km");
  const most = numberOf(evidence, "max_move_m");
  let move: string | null = null;
  if (n === 1) {
    if (most !== null) move = `${without}, it would move ${formatDistance(most)}.`;
  } else if (over100 !== null && over1km !== null) {
    const atMost = most === null ? "" : ` (at most ${formatDistance(most)})`;
    const moved =
      over100 === 0
        ? "none would move over 100 m"
        : `${formatCount(over100)} would move over 100 m and ${over1km === 0 ? "none" : formatCount(over1km)} over 1 km`;
    move = `${without}, ${moved}${atMost}.`;
  }
  return finding(`${subject} could also be placed from other formats.`, formatNote, move);
}

// One group of a check, with the evidence keys of the API contract.
export function findingText(code: CheckCode, group: ChecksGroup): FindingText {
  switch (code) {
    case "pair_impossible":
      return pairText(group, true);
    case "pair_far":
      return pairText(group, false);
    case "future_track_points":
      return futureText(group);
    case "taken_after_upload":
      return afterUploadText(group);
    case "stale_estimates":
      return staleText(group);
    case "orphan_points":
      return orphanText(group);
    case "time_may_be_estimated_used":
      return estimatedTimesText(group);
    default:
      // A check this version does not know (a newer server).
      return finding(`${group.label || group.key}: ${formatCount(group.count)} found.`);
  }
}

// What a group's media_ids are. Orphan points count points, and their ids are the estimates resting
// on them; every other check's ids are what it counts.
function groupItems(code: CheckCode, group: ChecksGroup): { noun: string; total: number } {
  if (code === "taken_after_upload") return { noun: "item", total: group.count };
  const total = code === "orphan_points" ? numberOf(group.evidence, "estimates") : group.count;
  return { noun: "estimate", total: total ?? group.media_ids.length };
}

// The button that steps through a group's items in the evidence view.
export function openLabel(code: CheckCode, group: ChecksGroup): string {
  return `Open ${counted(group.media_ids.length, groupItems(code, group).noun)}`;
}

// Said when a group has more items than its media_ids, which the server caps.
export function cappedNote(code: CheckCode, group: ChecksGroup): string | null {
  const { total } = groupItems(code, group);
  const shown = group.media_ids.length;
  return total > shown ? `The first ${formatCount(shown)} of ${formatCount(total)}, oldest first.` : null;
}

export type FileColumn =
  | "file"
  | "format"
  | "kind"
  | "points"
  | "first"
  | "last"
  | "items_placed"
  | "agreement"
  | "spacing";
export type SortDirection = "ascending" | "descending"; // aria-sort's values
export interface FileSort {
  column: FileColumn;
  direction: SortDirection;
}

// The newest file first: a log dated far in the future comes to the top.
export const DEFAULT_FILE_SORT: FileSort = { column: "last", direction: "descending" };

const TEXT_COLUMNS: readonly FileColumn[] = ["file", "format", "kind"];

// A click on a column's header: the same column turns round, another starts from A or the largest.
export function nextFileSort(sort: FileSort, column: FileColumn): FileSort {
  if (sort.column === column) {
    return { column, direction: sort.direction === "ascending" ? "descending" : "ascending" };
  }
  return { column, direction: TEXT_COLUMNS.includes(column) ? "ascending" : "descending" };
}

// The comparison so far, by source. GET /api/checks/agreement compares the log files from an index
// on, as far as its time budget goes, and says where to go on (next_from), so a comparison takes as
// many requests as the server needs.
export interface Comparison {
  files: ReadonlyMap<string, AgreementFile>;
  total: number; // log files in the latest answer's inventory
  next: number | null; // the index to ask from next; null once the last file was compared
}

// One answer added. A track imported between two requests can move the index, so a file may be
// compared twice: it keeps the later values.
export function mergeComparison(previous: Comparison | null, answer: AgreementResponse): Comparison {
  const files = new Map(previous?.files);
  for (const file of answer.files) files.set(file.source, file);
  return { files, total: answer.total_files, next: answer.next_from };
}

// An answer that compared nothing (the deadline cut its first chunk) would be asked again forever.
export function madeProgress(from: number, answer: AgreementResponse): boolean {
  return answer.next_from === null || answer.next_from > from;
}

// The comparison as the view shows it. A new run keeps the last comparison on screen until its first
// answer; `answered` says the data is the running comparison's own, so its count is the progress.
export interface AgreementState {
  status: "idle" | "loading" | "done" | "stopped" | "failed" | "busy";
  data: Comparison | null;
  answered: boolean;
}

export const NO_COMPARISON: AgreementState = { status: "idle", data: null, answered: false };

// "Compared 1,500 of 4,466 files…" while the comparison runs, with a full stop once it is done.
function compareProgress({ files, total }: Comparison, running: boolean): string {
  return `Compared ${formatCount(files.size)} of ${counted(total, "file")}${running ? "…" : "."}`;
}

// The line under Compare files. A comparison that stopped before its last file says why, and that
// the files it left read "not reached".
export function compareStatusText({ status, data, answered }: AgreementState): string {
  const leftOut = data !== null && data.next !== null;
  const andLeftOut = (reason: string) => (leftOut ? `${reason} ${LEFT_OUT}` : reason);
  switch (status) {
    case "idle":
      return COMPARE_NOTE;
    case "loading":
      return answered && data ? compareProgress(data, true) : COMPARING;
    case "done":
      return data ? compareProgress(data, false) : "";
    case "stopped":
      return TRUNCATED_NOTE;
    case "failed":
      return andLeftOut(leftOut ? "Could not compare every file." : "Could not compare the files.");
    case "busy":
      return andLeftOut(COMPARE_BUSY);
  }
}

// A comparison that stopped before its last file can go on from there. One that compared nothing has
// nothing to go on from: Compare again starts it over.
export function canResume({ status, data }: AgreementState): boolean {
  const stopped = status === "stopped" || status === "failed" || status === "busy";
  return stopped && data !== null && data.next !== null && data.files.size > 0;
}

// A track file with its comparison, once the files were compared and the file was reached.
export interface FileRow {
  file: ChecksFile;
  agreement: AgreementFile | null;
}

export function fileRows(files: readonly ChecksFile[], compared: Comparison | null): FileRow[] {
  return files.map((file) => ({ file, agreement: compared?.files.get(file.source) ?? null }));
}

function instantValue(iso: string | null): number | null {
  if (iso === null) return null;
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? null : ms;
}

const SORT_VALUES: Record<FileColumn, (row: FileRow) => string | number | null> = {
  file: (row) => fileName(row.file.source),
  format: (row) => row.file.format,
  kind: (row) => kindName(row.file.kind),
  points: (row) => row.file.points,
  first: (row) => instantValue(row.file.first),
  last: (row) => instantValue(row.file.last),
  items_placed: (row) => row.file.items_placed,
  agreement: (row) => row.agreement?.median_m ?? null,
  spacing: (row) => row.agreement?.modal_gap_share ?? null,
};

// File names sort as people read them: "walk 2" before "walk 10".
const collator = new Intl.Collator(undefined, { numeric: true });

function compareValues(a: string | number, b: string | number): number {
  return typeof a === "number" && typeof b === "number" ? a - b : collator.compare(String(a), String(b));
}

// By one column. A missing value (a year JSON cannot carry, a file not compared, too few matches)
// goes last in both directions, and equal values keep their labels' order, whatever the direction.
export function sortFiles(rows: readonly FileRow[], { column, direction }: FileSort): FileRow[] {
  const valueOf = SORT_VALUES[column];
  const sign = direction === "ascending" ? 1 : -1;
  return rows
    .map((row) => ({ row, value: valueOf(row) }))
    .sort((a, b) => {
      if (a.value !== b.value) {
        if (a.value === null) return 1;
        if (b.value === null) return -1;
        const order = compareValues(a.value, b.value);
        if (order !== 0) return sign * order;
      }
      const left = a.row.file.source;
      const right = b.row.file.source;
      return left < right ? -1 : left > right ? 1 : 0;
    })
    .map(({ row }) => row);
}

export function matchesFile(file: ChecksFile, query: string): boolean {
  return matchesSearch(query, file.source, fileName(file.source), file.format, kindName(file.kind));
}

export const NOT_COMPARED = "not compared";
export const NOT_REACHED = "not reached";
export const NOT_YET_COMPARED = "comparing…";

// Where the comparison on screen stands, for the files it has no values for.
export type CompareStage = "running" | "stopped" | "complete";

export function compareStage({ status, data }: AgreementState): CompareStage {
  if (data === null || data.next === null) return "complete";
  return status === "loading" ? "running" : "stopped";
}

// A file without a comparison: derived points are never compared, and a log file is missing while
// the comparison has yet to reach it, when it stopped first, or when the file was imported since.
function notCompared(file: ChecksFile, stage: CompareStage): string {
  if (file.kind === "derived" || stage === "complete") return NOT_COMPARED;
  return stage === "running" ? NOT_YET_COMPARED : NOT_REACHED;
}

export interface AgreementCell {
  text: string;
  detail: string | null;
}

export function agreementCell(row: FileRow, stage: CompareStage): AgreementCell {
  const { agreement } = row;
  if (!agreement) return { text: notCompared(row.file, stage), detail: null };
  return {
    text: agreement.median_m === null ? "too few overlaps" : formatDistance(agreement.median_m),
    detail: `${agreement.matched} of ${agreement.samples} matched`,
  };
}

export function spacingCell(row: FileRow, stage: CompareStage): string {
  const { agreement } = row;
  if (!agreement) return notCompared(row.file, stage);
  return agreement.modal_gap_share === null ? "too few points" : formatShare(agreement.modal_gap_share);
}

const NAME_SEPARATORS = new Set(["_", ".", "-", "/"]);

// A file name cut after each separator ("2019_07_14.nmea" → "2019_", "07_", "14.", "nmea"), so it
// can wrap there rather than inside a word.
export function nameParts(name: string): string[] {
  const parts: string[] = [];
  let start = 0;
  for (let i = 0; i < name.length; i++) {
    if (NAME_SEPARATORS.has(name[i])) {
      parts.push(name.slice(start, i + 1));
      start = i + 1;
    }
  }
  if (start < name.length) parts.push(name.slice(start));
  return parts;
}
