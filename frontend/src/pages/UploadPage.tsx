import { useState, useRef } from "react";
import { Upload, X } from "lucide-react";
import { api, ApiError } from "@/api/client";
import { MAX_FILE_BYTES, planUploadBatches } from "@/lib/uploadBatches";
import { canHashFiles, mediaHash } from "@/lib/mediaHash";
import type { Album } from "@/types";
import { AlbumPickerDialog } from "@/components/albums/AlbumPickerDialog";

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
}

interface UploadResult {
  uploaded: string[];
  skipped: string[];
  failed: string[];
  // Set when the upload went into an album: how many files the album gained,
  // new uploads and duplicates already in the library alike.
  album?: { name: string; added: number };
}

function plural(n: number) {
  return `${n} file${n !== 1 ? "s" : ""}`;
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
      if (i % 50 === 0) onProgress(`Checking for files already in your library (${i} of ${files.length})`);
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

  const handleGpsImport = async (selected: File[]) => {
    if (selected.length === 0) return;
    setImporting(true);
    setGpsError(null);
    setGpsResult(null);
    const { batches, tooLarge } = planUploadBatches(selected, GPS_BATCH_BYTES, GPS_MAX_FILE_BYTES, GPS_BATCH_FILES);
    const summary: GpsSummary = {
      inserted: 0,
      skipped: 0,
      empty: [],
      failed: tooLarge.map((f) => `${f.name}: larger than ${GPS_MAX_FILE_BYTES / MiB} MiB`),
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
      } catch (e: unknown) {
        summary.failed.push(...batch.map((f) => f.name));
        errors.push(e instanceof Error ? e.message : "Import failed");
      }
    }
    setGpsResult(summary);
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
      setProgress(`Uploading batch ${i + 1} of ${batches.length} (${done} file(s) done)`);
      const form = new FormData();
      for (const file of batch) {
        form.append("files", file);
        form.append("file_mtime", String(file.lastModified)); // ms since epoch
      }
      try {
        // Every batch is its own request, so each one names the album.
        const data = await api.upload(form, album?.id);
        albumAdded += data.album_added ?? 0;
        uploaded.push(...(data.uploaded ?? []));
        skipped.push(...(data.skipped ?? unreported(batch, data.uploaded, data.failed)));
        failedNames.push(...(data.failed ?? []));
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
            <p className="mb-2 text-sm text-muted-foreground">
              {files.length} file{files.length !== 1 ? "s" : ""} selected
            </p>
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
              Uploaded {plural(result.uploaded.length)}, skipped {result.skipped.length}, failed{" "}
              {result.failed.length}
            </p>
            {result.album && (
              <p className="mt-1 text-sm font-medium">
                Added {plural(result.album.added)} to "{result.album.name}"
              </p>
            )}
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
                Imported {gpsResult.inserted} point(s), {gpsResult.skipped} already imported
              </p>
              <ResultList
                title={`${plural(gpsResult.empty.length)} without timestamped points`}
                names={gpsResult.empty}
              />
              <ResultList title={`${plural(gpsResult.failed.length)} failed`} names={gpsResult.failed} error />
            </>
          )}
          {gpsError && <p className="mt-2 text-red-500">{gpsError}</p>}
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
