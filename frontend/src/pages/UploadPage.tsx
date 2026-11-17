import { memo, useEffect, useMemo, useState, useRef } from "react";
import { flushSync } from "react-dom";
import { AlertTriangle, Upload, X } from "lucide-react";
import { api, ApiError } from "@/api/client";
import { MAX_FILE_BYTES, planUploadBatches } from "@/lib/uploadBatches";
import { canHashFiles, mediaHash } from "@/lib/mediaHash";
import { formatCount } from "@/lib/numbers";
import { browserTimeZone, formatInstant, formatSpan, YEAR_OUT_OF_RANGE } from "@/lib/time";
import { SERVER_ZONE_UNSET, serverZoneLabel } from "@/lib/timeZones";
import type { Album, CameraZoneFrom, TimeReport, TrackFileReport, UserSettings } from "@/types";
import { AlbumPickerDialog } from "@/components/albums/AlbumPickerDialog";
import { CameraZoneSelect } from "@/components/CameraZoneSelect";
import { Fact } from "@/components/evidence/PointCard";

const GiB = 1024 ** 3;
const MiB = 1024 ** 2;

// The backend caps one track upload at 256 MiB, and Go refuses a form of more than 1000 parts.
// Exports come as hundreds of small files (Samsung Health, dashcams), so batches are capped by
// count as well as size. A batch is inserted and matched before the response, and dense formats
// hold ~20k points per MB, so batches stay small enough to answer within nginx's 60 s timeout.
const GPS_BATCH_BYTES = 32 * MiB;
const GPS_MAX_FILE_BYTES = 255 * MiB;
const GPS_BATCH_FILES = 500;
const GPS_ACCEPT = ".gpx,.tcx,.kml,.kmz,.geojson,.json,.nmea,.log,.gz";

interface GpsSummary {
  inserted: number;
  skipped: number;
  empty: string[];
  failed: string[];
  files: TrackFileReport[];
  finishedAt: number; // ms since epoch: the now a file's last point is compared with
}

interface UploadResult {
  uploaded: string[];
  skipped: string[];
  failed: string[];
  // Set when the upload went into an album: how many files the album gained,
  // new uploads and duplicates already in the library alike.
  album?: { name: string; added: number };
  // How the new files' capture times were read; null when the backend did not say.
  times: TimeReport | null;
}

function plural(n: number, noun = "file") {
  return `${formatCount(n)} ${noun}${n !== 1 ? "s" : ""}`;
}

function albumGoneMessage(name: string) {
  return `Could not upload into "${name}": the album no longer exists, or you are no longer a member`;
}

// Older backends omit `skipped` and leave duplicates out of the response, so a sent file
// that is neither uploaded nor failed was skipped. Names can repeat, so match by count.
function unreported(batch: File[], ...reported: (string[] | null | undefined)[]): string[] {
  const remaining = new Map<string, number>();
  for (const name of reported.flatMap((r) => r ?? [])) {
    remaining.set(name, (remaining.get(name) ?? 0) + 1);
  }
  return batch
    .map((f) => f.name)
    .filter((name) => {
      const n = remaining.get(name) ?? 0;
      if (n > 0) remaining.set(name, n - 1);
      return n === 0;
    });
}

// The most hashes one known-hashes request and media IDs one album add take.
const MAX_IDS_PER_REQUEST = 5000;

function chunks<T>(items: T[], size = MAX_IDS_PER_REQUEST): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}

interface KnownFiles {
  known: { file: File; mediaId: string }[];
  fresh: File[];
}

// Splits the selection into files the caller's library already holds and files to send, so a
// re-upload costs a few KB per file instead of the whole file. Any failure sends everything:
// the server deduplicates on its own.
async function findKnownFiles(files: File[], onProgress: (msg: string) => void): Promise<KnownFiles> {
  if (!canHashFiles()) return { known: [], fresh: files };
  try {
    const hashes: string[] = [];
    for (const [i, file] of files.entries()) {
      if (i % 50 === 0) {
        onProgress(`Checking for files already in your library (${formatCount(i)} of ${formatCount(files.length)})`);
      }
      hashes.push(await mediaHash(file));
    }
    const ids = new Map<string, string>();
    for (const part of chunks([...new Set(hashes)])) {
      for (const [hash, id] of Object.entries(await api.knownHashes(part))) ids.set(hash, id);
    }
    const result: KnownFiles = { known: [], fresh: [] };
    files.forEach((file, i) => {
      const mediaId = ids.get(hashes[i]);
      if (mediaId) result.known.push({ file, mediaId });
      else result.fresh.push(file);
    });
    return result;
  } catch (e) {
    console.warn("Duplicate check failed, sending every file:", e);
    return { known: [], fresh: files };
  }
}

// Long selections produce hundreds of names, so each list scrolls inside a fixed height.
function ResultList({ title, names, error }: { title: string; names: string[]; error?: boolean }) {
  if (names.length === 0) return null;
  return (
    <div className={`mt-4 rounded-lg border p-3 text-sm ${error ? "border-red-300" : "bg-muted"}`}>
      <p className={`font-medium ${error ? "text-red-500" : ""}`}>{title}</p>
      <ul className="mt-1 max-h-48 list-disc overflow-y-auto pl-4 text-muted-foreground">
        {names.map((f, i) => (
          // Camera file names have no spaces to break at.
          <li key={i} className="[overflow-wrap:anywhere]">
            {f}
          </li>
        ))}
      </ul>
    </div>
  );
}

// Adds two batches' counts. A count an older backend left out counts as 0.
function addCounts<T extends Record<string, number | undefined>>(a: T, b: T): T {
  const sum: Record<string, number | undefined> = { ...a };
  for (const key of Object.keys(b)) sum[key] = (sum[key] ?? 0) + (b[key] ?? 0);
  return sum as T;
}

// One report for all batches of an upload. They reach the same server with the same zone choice, so
// they share the first batch's camera zone.
function mergeTimeReports(a: TimeReport | null, b: TimeReport): TimeReport {
  if (!a) return b;
  return {
    by_source: addCounts(a.by_source, b.by_source),
    by_zone: addCounts(a.by_zone, b.by_zone),
    camera_zone: a.camera_zone,
    camera_zone_from: a.camera_zone_from,
    assumed_zone_is_utc: a.assumed_zone_is_utc || b.assumed_zone_is_utc,
    file_date_names: [...a.file_date_names, ...b.file_date_names],
  };
}

// The camera zone part of the capture time line, by who named the zone. An older backend's report has
// no source, which was always the server's.
function cameraZonePart(cameraZone: string, from: CameraZoneFrom | undefined): string {
  switch (from) {
    case "batch":
      return `this upload's zone ${cameraZone}`;
    case "user":
      return `assumed your camera zone ${cameraZone}`;
    default:
      return cameraZone === SERVER_ZONE_UNSET ? "assumed server zone" : `assumed camera zone ${cameraZone}`;
  }
}

// "38 from EXIF (10 GPS clock, 20 photo offset, 8 assumed camera zone Europe/Warsaw), 3 from file name
// (assumed camera zone Europe/Warsaw), 2 from file date": what dated the new files, sources and zone
// rules in the order ingest tries them. A name is always read in the camera zone, which by_zone counts
// with the EXIF times read in it. Empty when no file was new.
function captureTimesLine({
  by_source: source,
  by_zone: zone,
  camera_zone: cameraZone,
  camera_zone_from: from,
}: TimeReport): string {
  const counted = (parts: [count: number, text: string][]) =>
    parts.filter(([count]) => count > 0).map(([count, text]) => `${formatCount(count)} ${text}`);
  const named = source.filename ?? 0;
  const zonePart = cameraZonePart(cameraZone, from);
  const rules = counted([
    [zone.gps, "GPS clock"],
    [zone.clock_rule, "camera clock rule"],
    [zone.offset, "photo offset"],
    [zone.camera - named, zonePart],
  ]);
  return counted([
    [source.exif_original + source.exif_ifd0, rules.length > 0 ? `from EXIF (${rules.join(", ")})` : "from EXIF"],
    [source.container, "from video container"],
    [named, `from file name (${zonePart})`],
    [source.mtime, "from file date"],
    [source.none, "with no capture time"],
  ]).join(", ");
}

// The warning when files were dated in a camera zone that is UTC all year and that the upload did not
// choose (the backend never warns about a zone chosen for the upload). It speaks of those files only,
// and allows that the zone may be right (Iceland keeps UTC all year). A new zone dates new uploads
// only, and these files would be skipped as duplicates if sent again: only a shift by the owner or
// server retime moves them.
function assumedUtcWarning(cameraZone: string, from: CameraZoneFrom | undefined): string {
  const mine = from === "user";
  const read = mine
    ? "Your camera zone is UTC all year"
    : cameraZone === SERVER_ZONE_UNSET
      ? "CAMERA_TIMEZONE is unset and the server's zone is UTC"
      : "The assumed camera zone is UTC all year";
  const set = mine
    ? "Change your camera zone in Settings, or choose a zone for an upload."
    : "Set your own camera zone in Settings, or choose a zone for an upload.";
  return (
    `${read}, so the files dated in it were read as UTC. If the camera's clock did not show UTC, those times ` +
    `are off by its offset from UTC. ${set} A new zone dates new uploads only: these files keep their times ` +
    "until you shift them (Trips → Shift times…) or an administrator runs server retime, and uploading them " +
    "again skips them as duplicates."
  );
}

// How the capture times of the new files were read. Files the browser found in the library were
// never sent, and duplicates the server skipped were dated before, so neither is counted.
function CaptureTimes({ report }: { report: TimeReport }) {
  const [namesShown, setNamesShown] = useState(false);
  const line = captureTimesLine(report);
  if (!line) return null;
  const general = report.by_source.exif_ifd0;
  const names = report.file_date_names;
  return (
    <div className="mt-1 space-y-1 text-sm">
      <p>Capture times: {line}</p>
      {general > 0 && (
        <p className="text-muted-foreground">
          {general === 1 ? "1 EXIF time comes" : `${formatCount(general)} EXIF times come`} from the general date
          tag, which editing software can rewrite.
        </p>
      )}
      {names.length > 0 && (
        <div>
          <button
            type="button"
            aria-expanded={namesShown}
            onClick={() => setNamesShown((shown) => !shown)}
            className="flex items-center gap-1 text-left hover:text-foreground touch:min-h-11"
          >
            <span aria-hidden="true" className="w-3 text-xs text-muted-foreground">
              {namesShown ? "▾" : "▸"}
            </span>
            {plural(names.length)} from file date
          </button>
          {namesShown && (
            <ul className="mt-1 max-h-48 list-disc overflow-y-auto pl-4 text-muted-foreground">
              {names.map((name, i) => (
                <li key={i} className="[overflow-wrap:anywhere]">
                  {name}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {report.assumed_zone_is_utc && (
        <p className="flex gap-2">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" aria-hidden="true" />
          <span>{assumedUtcWarning(report.camera_zone, report.camera_zone_from)}</span>
        </p>
      )}
    </div>
  );
}

const DAY_S = 86400;

// How far a file's last point lies ahead of now, in seconds, when that is more than a day; else null.
function aheadOfNow(file: TrackFileReport, now: number): number | null {
  if (file.last === null) return null;
  const ahead = (Date.parse(file.last) - now) / 1000;
  return ahead > DAY_S ? ahead : null;
}

// A time of a track file. A track point has no camera clock: UTC and the browser zone, each labelled.
function TrackTime({ label, iso, zone }: { label: string; iso: string | null; zone: string }) {
  if (iso === null) return <Fact label={label}>{YEAR_OUT_OF_RANGE}</Fact>;
  return (
    <>
      <Fact label={`${label}, UTC`}>{formatInstant(iso, "UTC")}</Fact>
      <Fact label={`${label}, your browser`}>{formatInstant(iso, zone)}</Fact>
    </>
  );
}

// Rows of the track receipt rendered at a time, as in the Checks view's track files. A dashcam or
// Samsung Health export holds thousands of files, and formatting and laying out every row at once
// froze the page for seconds when the import finished.
const TRACK_FILES_PAGE = 200;

interface TrackRowProps {
  file: TrackFileReport;
  ahead: number | null; // seconds its last point lies ahead of now, when more than a day
  zone: string; // the browser's
  pageStart: boolean; // the first row of a page, which takes the focus when Show more adds it
}

// Memoized, so Show more formats only the rows it adds.
const TrackRow = memo(function TrackRow({ file, ahead, zone, pageStart }: TrackRowProps) {
  return (
    <li className="py-2" tabIndex={pageStart ? -1 : undefined}>
      <p className="font-medium [overflow-wrap:anywhere]">{file.file}</p>
      <p className="text-xs text-muted-foreground">
        {file.format} · {plural(file.points, "point")}
      </p>
      <dl className="mt-1 space-y-0.5 text-xs">
        <TrackTime label="First" iso={file.first} zone={zone} />
        <TrackTime label="Last" iso={file.last} zone={zone} />
      </dl>
      {ahead !== null && (
        <p className="mt-1 flex gap-2 text-xs">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-600" aria-hidden="true" />
          <span>Its last point is {formatSpan(ahead)} in the future, so its times are probably wrong.</span>
        </p>
      )}
    </li>
  );
});

// How many of the receipt's files end more than a day ahead; its list shows them first.
function aheadSummary(ahead: number, total: number): string {
  const one = ahead === 1;
  const which = one ? "1 of them ends" : `${formatCount(ahead)} of them end`;
  const first = ahead === total ? "" : one ? " It is listed first." : " They are listed first.";
  return `${which} more than a day in the future, so ${one ? "its" : "their"} times are probably wrong.${first}`;
}

// Every track file with points, from every batch of the import, with the span of its times: a log
// dated decades ahead (a receiver that lost its GPS week) shows on the day it is imported. File names
// are rendered as text only. Memoized: the page renders again on every progress message of a media
// upload.
const TrackFiles = memo(function TrackFiles({ files, now }: { files: TrackFileReport[]; now: number }) {
  const [limit, setLimit] = useState(TRACK_FILES_PAGE);
  const listRef = useRef<HTMLUListElement>(null);
  // Files dated ahead come first, so the first page holds them unless there are more than a page.
  const { rows, aheadCount } = useMemo(() => {
    const all = files.map((file) => ({ file, ahead: aheadOfNow(file, now) }));
    const dated = all.filter((row) => row.ahead !== null);
    return { rows: [...dated, ...all.filter((row) => row.ahead === null)], aheadCount: dated.length };
  }, [files, now]);
  if (rows.length === 0) return null;
  const zone = browserTimeZone();

  // The focus moves to the first row added, as a screen reader should read on from there. Left on
  // the button, it would fall to the page's start when the last page removes it.
  const showMore = () => {
    flushSync(() => setLimit(limit + TRACK_FILES_PAGE));
    (listRef.current?.children[limit] as HTMLElement | undefined)?.focus();
  };

  return (
    <div className="mt-4 rounded-lg border bg-muted p-3 text-sm">
      <p className="font-medium">{plural(rows.length)} with timestamped points</p>
      {aheadCount > 0 && rows.length > 1 && (
        <p className="mt-1 flex gap-2 text-xs">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-600" aria-hidden="true" />
          <span>{aheadSummary(aheadCount, rows.length)}</span>
        </p>
      )}
      <ul ref={listRef} className="mt-1 divide-y">
        {rows.slice(0, limit).map((row, i) => (
          <TrackRow key={i} file={row.file} ahead={row.ahead} zone={zone} pageStart={i % TRACK_FILES_PAGE === 0} />
        ))}
      </ul>
      {rows.length > limit && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={showMore}
            className="rounded-lg border bg-background px-3 py-1.5 font-medium hover:bg-accent touch:min-h-11"
          >
            Show more
          </button>
          <span className="text-xs text-muted-foreground">
            {formatCount(limit)} of {formatCount(rows.length)} shown
          </span>
        </div>
      )}
    </div>
  );
});

// The zone choice's first option: what an upload without a zone of its own is read in.
function defaultZoneLabel(settings: UserSettings | null): string {
  if (!settings) return "Default";
  if (settings.camera_timezone) return `Default: your camera zone, ${settings.camera_timezone}`;
  return `Default: ${serverZoneLabel(settings.defaults.camera_timezone)}`;
}

export function UploadPage() {
  const [files, setFiles] = useState<FileList | null>(null);
  const [uploading, setUploading] = useState(false);
  const [result, setResult] = useState<UploadResult | null>(null);
  const [progress, setProgress] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const gpsInputRef = useRef<HTMLInputElement>(null);
  const [importing, setImporting] = useState(false);
  const [gpsProgress, setGpsProgress] = useState<string | null>(null);
  const [gpsResult, setGpsResult] = useState<GpsSummary | null>(null);
  const [gpsError, setGpsError] = useState<string | null>(null);
  // null uploads into no album.
  const [album, setAlbum] = useState<Album | null>(null);
  const [pickerOpen, setPickerOpen] = useState(false);
  // The zone this upload's camera clocks show; "" leaves it to the caller's setting or the server's.
  const [zone, setZone] = useState("");
  // Only names the default in the zone choice; null until loaded, or when it cannot be.
  const [settings, setSettings] = useState<UserSettings | null>(null);

  useEffect(() => {
    let live = true;
    api
      .getSettings()
      .then((s) => {
        if (live) setSettings(s);
      })
      .catch((e) => console.warn("Could not load the camera zone setting:", e));
    return () => {
      live = false;
    };
  }, []);

  const handleGpsImport = async (selected: File[]) => {
    if (selected.length === 0) return;
    setImporting(true);
    setGpsError(null);
    setGpsResult(null);
    const { batches, tooLarge } = planUploadBatches(selected, GPS_BATCH_BYTES, GPS_MAX_FILE_BYTES, GPS_BATCH_FILES);
    const summary: Omit<GpsSummary, "finishedAt"> = {
      inserted: 0,
      skipped: 0,
      empty: [],
      failed: tooLarge.map((f) => `${f.name}: larger than ${GPS_MAX_FILE_BYTES / MiB} MiB`),
      files: [],
    };
    const errors: string[] = [];
    // A failed batch does not stop the others: each one is its own request.
    for (const [i, batch] of batches.entries()) {
      if (batches.length > 1) setGpsProgress(`Importing batch ${i + 1} of ${batches.length}`);
      try {
        const data = await api.uploadGpsTracks(batch);
        summary.inserted += data.inserted;
        summary.skipped += data.skipped;
        summary.empty.push(...data.empty);
        summary.failed.push(...data.failed.map((f) => `${f.file}: ${f.reason}`));
        summary.files.push(...(data.files ?? []));
      } catch (e: unknown) {
        summary.failed.push(...batch.map((f) => f.name));
        errors.push(e instanceof Error ? e.message : "Import failed");
      }
    }
    setGpsResult({ ...summary, finishedAt: Date.now() });
    setGpsError(errors.length > 0 ? [...new Set(errors)].join("; ") : null);
    setGpsProgress(null);
    setImporting(false);
    if (gpsInputRef.current) gpsInputRef.current.value = "";
  };

  // Reloads the album list and says whether the album is still in it (null: unknown).
  // An album that is gone is dropped from the choice, which goes back to None.
  const albumStillListed = async (id: string): Promise<boolean | null> => {
    let list: Album[];
    try {
      list = await api.listAlbums();
    } catch (e) {
      console.error("Failed to reload albums:", e);
      return null;
    }
    const listed = list.some((a) => a.id === id);
    if (!listed) setAlbum((cur) => (cur?.id === id ? null : cur));
    return listed;
  };

  const handleUpload = async () => {
    if (!files || files.length === 0) return;
    setUploading(true);
    setError(null);
    setResult(null);
    let albumAdded = 0;

    // The album list may be stale: the album was deleted, or the caller removed or left.
    // Find out before sending gigabytes the backend would refuse batch after batch.
    if (album && (await albumStillListed(album.id)) === false) {
      setError(albumGoneMessage(album.name));
      setUploading(false);
      return;
    }

    const { known, fresh } = await findKnownFiles(Array.from(files), setProgress);
    const uploaded: string[] = [];
    const skipped: string[] = [];
    const failedNames: string[] = [];
    const errors: string[] = [];
    let albumGone = false;
    let times: TimeReport | null = null;

    // Files already in the library are not sent, but still go into the album, as the server
    // does with the duplicates it skips.
    if (album && known.length > 0) {
      setProgress(`Adding ${plural(known.length)} already in your library to "${album.name}"`);
      try {
        for (const part of chunks([...new Set(known.map((k) => k.mediaId))])) {
          albumAdded += (await api.addToAlbum(album.id, { mediaIds: part })).added;
        }
        skipped.push(...known.map((k) => k.file.name));
      } catch (e: unknown) {
        failedNames.push(...known.map((k) => k.file.name));
        const is404 = e instanceof ApiError && e.status === 404;
        if (is404 || (await albumStillListed(album.id)) === false) {
          // Every upload would be refused the same way: do not send any.
          albumGone = true;
          errors.push(albumGoneMessage(album.name));
          failedNames.push(...fresh.map((f) => f.name));
        } else {
          errors.push(e instanceof Error ? e.message : "Adding to the album failed");
        }
      }
    } else {
      skipped.push(...known.map((k) => k.file.name));
    }

    // One request for everything would exceed the 4 GiB request limit (413), so send batches.
    const { batches, tooLarge } = planUploadBatches(albumGone ? [] : fresh);
    failedNames.push(...tooLarge.map((f) => `${f.name} (larger than ${(MAX_FILE_BYTES / GiB).toFixed(1)} GiB)`));

    for (const [i, batch] of batches.entries()) {
      const done = uploaded.length + skipped.length;
      setProgress(`Uploading batch ${i + 1} of ${batches.length} (${formatCount(done)} file(s) done)`);
      const form = new FormData();
      for (const file of batch) {
        form.append("files", file);
        form.append("file_mtime", String(file.lastModified)); // ms since epoch
      }
      try {
        // Every batch is its own request, so each one names the album.
        const data = await api.upload(form, album?.id, zone || undefined);
        albumAdded += data.album_added ?? 0;
        uploaded.push(...(data.uploaded ?? []));
        skipped.push(...(data.skipped ?? unreported(batch, data.uploaded, data.failed)));
        failedNames.push(...(data.failed ?? []));
        if (data.time_report) times = mergeTimeReports(times, data.time_report);
      } catch (e: unknown) {
        failedNames.push(...batch.map((f) => f.name));
        // The backend answers 404 for an album the caller cannot add to, before reading the files.
        // Without a buffering proxy it then closes the connection mid-body, which the browser
        // reports as a network error (or the dev proxy as a 5xx), so ask the album list too.
        const is404 = e instanceof ApiError && e.status === 404;
        if (album) {
          const listed = await albumStillListed(album.id);
          if (is404 || listed === false) {
            // Every further batch would be refused the same way: do not send them.
            errors.push(albumGoneMessage(album.name));
            failedNames.push(...batches.slice(i + 1).flatMap((b) => b.map((f) => f.name)));
            break;
          }
        }
        errors.push(e instanceof Error ? e.message : "Upload failed");
      }
    }

    setResult({
      uploaded,
      skipped,
      failed: failedNames,
      album: album ? { name: album.name, added: albumAdded } : undefined,
      times,
    });
    setError(errors.length > 0 ? [...new Set(errors)].join("; ") : null);
    setProgress(null);
    setUploading(false);
    if (failedNames.length === 0) {
      setFiles(null);
      if (inputRef.current) inputRef.current.value = "";
    }
  };

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-xl p-4 sm:p-6">
        <h1 className="mb-4 text-xl font-semibold">Upload Media</h1>

        <div
          className="flex cursor-pointer flex-col items-center rounded-lg border-2 border-dashed p-6 text-center text-muted-foreground transition-colors hover:border-primary hover:text-primary sm:p-10"
          onClick={() => inputRef.current?.click()}
        >
          <Upload className="mb-2 h-8 w-8" />
          <p className="text-sm">Click to select files, or drop them here</p>
          <p className="text-xs mt-1">Photos, videos, 360° supported</p>
          <input
            ref={inputRef}
            type="file"
            className="hidden"
            multiple
            accept="image/*,video/*,.3gp"
            onChange={(e) => setFiles(e.target.files)}
          />
        </div>

        <div className="mt-4 flex items-center gap-2 text-sm">
          <span className="font-medium">Add to album</span>
          <button
            type="button"
            onClick={() => setPickerOpen(true)}
            disabled={uploading}
            aria-label={`Add to album: ${album ? album.name : "None"}`}
            className="min-w-0 flex-1 truncate rounded-md border bg-background px-2 py-1 text-left hover:bg-muted disabled:opacity-50 touch:min-h-11"
          >
            {album ? (
              <>
                {album.name}
                {!album.is_owner && (
                  <span className="text-muted-foreground"> (by {album.owner_name ?? "another user"})</span>
                )}
              </>
            ) : (
              <span className="text-muted-foreground">None</span>
            )}
          </button>
          {album && (
            <button
              type="button"
              onClick={() => setAlbum(null)}
              disabled={uploading}
              aria-label="Upload into no album"
              title="Upload into no album"
              className="flex shrink-0 items-center justify-center rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-50 touch:h-11 touch:w-11"
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
        <div className="mt-2 flex items-center gap-2 text-sm">
          <label htmlFor="upload-camera-zone" className="shrink-0 font-medium">
            Camera time zone
          </label>
          <CameraZoneSelect
            id="upload-camera-zone"
            value={zone}
            onChange={setZone}
            defaultLabel={defaultZoneLabel(settings)}
            disabled={uploading}
            className="min-w-0 flex-1 rounded-md border bg-background px-2 py-1 disabled:opacity-50 touch:min-h-11 touch:text-base"
          />
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          {zone
            ? `The files of this upload whose own clocks give no offset are read in ${zone}, and keep that zone.`
            : "The zone camera clocks were set to, for files whose own clocks give no offset."}
        </p>
        <AlbumPickerDialog
          open={pickerOpen}
          onOpenChange={setPickerOpen}
          title="Add uploads to an album"
          description="New files and files already in your library are both added to the album."
          initialAlbumId={album?.id}
          confirmLabel="Choose"
          onPick={setAlbum}
        />

        {files && files.length > 0 && (
          <div className="mt-4">
            <p className="mb-2 text-sm text-muted-foreground">{plural(files.length)} selected</p>
            <button
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50 touch:min-h-11 touch:px-6"
              onClick={handleUpload}
              disabled={uploading}
            >
              {uploading ? "Uploading…" : "Upload"}
            </button>
            {progress && <p className="mt-2 text-sm text-muted-foreground">{progress}</p>}
          </div>
        )}

        {result && (
          <>
            <p className="mt-4 text-sm font-medium">
              Uploaded {plural(result.uploaded.length)}, skipped {formatCount(result.skipped.length)}, failed{" "}
              {formatCount(result.failed.length)}
            </p>
            {result.album && (
              <p className="mt-1 text-sm font-medium">
                Added {plural(result.album.added)} to "{result.album.name}"
              </p>
            )}
            {result.times && <CaptureTimes report={result.times} />}
            <ResultList title={`Uploaded ${plural(result.uploaded.length)}`} names={result.uploaded} />
            <ResultList
              title={`${plural(result.skipped.length)} skipped (already in your library)`}
              names={result.skipped}
            />
            <ResultList title={`${plural(result.failed.length)} failed`} names={result.failed} error />
          </>
        )}

        {error && (
          <p className="mt-4 text-sm text-red-500">{error}</p>
        )}

        <div className="mt-8 rounded-lg border p-4 text-sm">
          <p className="font-medium">GPS track import</p>
          <p className="mt-1 text-muted-foreground">
            GPX, TCX, KML, KMZ, GeoJSON, NMEA (dashcams), Google Timeline and Samsung Health JSON, esoko
            .log files, or a JSON array of{" "}
            <code className="rounded bg-muted px-1">{"{recorded_at, lat, lng, source}"}</code> points; any of them
            may be gzipped. Media without EXIF GPS is matched against the imported tracks.
          </p>
          <button
            className="mt-2 rounded-lg border px-3 py-1.5 text-sm font-medium hover:bg-accent disabled:opacity-50 touch:min-h-11"
            onClick={() => gpsInputRef.current?.click()}
            disabled={importing}
          >
            {importing ? "Importing…" : "Choose track files"}
          </button>
          <input
            ref={gpsInputRef}
            type="file"
            className="hidden"
            multiple
            aria-label="GPS track files"
            accept={GPS_ACCEPT}
            onChange={(e) => handleGpsImport(Array.from(e.target.files ?? []))}
          />
          {gpsProgress && <p className="mt-2 text-muted-foreground">{gpsProgress}</p>}
          {gpsResult && (
            <>
              <p className="mt-2 text-muted-foreground">
                Imported {formatCount(gpsResult.inserted)} point(s), {formatCount(gpsResult.skipped)} already imported
              </p>
              <ResultList
                title={`${plural(gpsResult.empty.length)} without timestamped points`}
                names={gpsResult.empty}
              />
              <ResultList title={`${plural(gpsResult.failed.length)} failed`} names={gpsResult.failed} error />
            </>
          )}
          {gpsError && <p className="mt-2 text-red-500">{gpsError}</p>}
          {/* Last: a page of files runs to thousands of pixels, and would push the failures out of sight. */}
          {gpsResult && <TrackFiles files={gpsResult.files} now={gpsResult.finishedAt} />}
        </div>

        <div className="mt-4 rounded-lg border p-4 text-sm text-muted-foreground">
          <p>
            Bulk folder ingest is an operator command:{" "}
            <code className="rounded bg-muted px-1">server scan --user &lt;id&gt;</code> (see README).
          </p>
        </div>
      </div>
    </div>
  );
}
