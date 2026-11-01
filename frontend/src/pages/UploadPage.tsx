import { useState, useRef } from "react";
import { Upload } from "lucide-react";
import { api } from "@/api/client";
import { MAX_FILE_BYTES, planUploadBatches } from "@/lib/uploadBatches";
import type { GpsImportResult } from "@/types";

const GiB = 1024 ** 3;

interface UploadResult {
  uploaded: string[];
  skipped: string[];
  failed: string[];
}

function plural(n: number) {
  return `${n} file${n !== 1 ? "s" : ""}`;
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

// Long selections produce hundreds of names, so each list scrolls inside a fixed height.
function ResultList({ title, names, error }: { title: string; names: string[]; error?: boolean }) {
  if (names.length === 0) return null;
  return (
    <div className={`mt-4 rounded-lg border p-3 text-sm ${error ? "border-red-300" : "bg-muted"}`}>
      <p className={`font-medium ${error ? "text-red-500" : ""}`}>{title}</p>
      <ul className="mt-1 max-h-48 list-disc overflow-y-auto pl-4 text-muted-foreground">
        {names.map((f, i) => (
          <li key={i}>{f}</li>
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
  const [gpsResult, setGpsResult] = useState<GpsImportResult | null>(null);
  const [gpsError, setGpsError] = useState<string | null>(null);

  const handleGpsImport = async (file: File | undefined) => {
    if (!file) return;
    setImporting(true);
    setGpsError(null);
    setGpsResult(null);
    try {
      setGpsResult(await api.importGpsTracks(file));
    } catch (e: unknown) {
      setGpsError(e instanceof Error ? e.message : "Import failed");
    } finally {
      setImporting(false);
      if (gpsInputRef.current) gpsInputRef.current.value = "";
    }
  };

  const handleUpload = async () => {
    if (!files || files.length === 0) return;
    setUploading(true);
    setError(null);
    setResult(null);

    // One request for everything would exceed the 4 GiB request limit (413), so send batches.
    const { batches, tooLarge } = planUploadBatches(Array.from(files));
    const uploaded: string[] = [];
    const skipped: string[] = [];
    const failedNames: string[] = tooLarge.map(
      (f) => `${f.name} (larger than ${(MAX_FILE_BYTES / GiB).toFixed(1)} GiB)`
    );
    const errors: string[] = [];

    for (const [i, batch] of batches.entries()) {
      const done = uploaded.length + skipped.length;
      setProgress(`Uploading batch ${i + 1} of ${batches.length} (${done} file(s) done)`);
      const form = new FormData();
      for (const file of batch) {
        form.append("files", file);
        form.append("file_mtime", String(file.lastModified)); // ms since epoch
      }
      try {
        const data = await api.upload(form);
        uploaded.push(...(data.uploaded ?? []));
        skipped.push(...(data.skipped ?? unreported(batch, data.uploaded, data.failed)));
        failedNames.push(...(data.failed ?? []));
      } catch (e: unknown) {
        errors.push(e instanceof Error ? e.message : "Upload failed");
        failedNames.push(...batch.map((f) => f.name));
      }
    }

    setResult({ uploaded, skipped, failed: failedNames });
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
      <div className="mx-auto max-w-xl p-6">
        <h1 className="mb-4 text-xl font-semibold">Upload Media</h1>

        <div
          className="flex cursor-pointer flex-col items-center rounded-lg border-2 border-dashed p-10 text-muted-foreground hover:border-primary hover:text-primary transition-colors"
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

        {files && files.length > 0 && (
          <div className="mt-4">
            <p className="mb-2 text-sm text-muted-foreground">
              {files.length} file{files.length !== 1 ? "s" : ""} selected
            </p>
            <button
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
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
          <p className="font-medium">GPS track import (JSON)</p>
          <p className="mt-1 text-muted-foreground">
            A JSON array of <code className="rounded bg-muted px-1">{"{recorded_at, lat, lng, source}"}</code> points.
            Media without EXIF GPS is matched against the imported track.
          </p>
          <button
            className="mt-2 rounded-lg border px-3 py-1.5 text-sm font-medium hover:bg-accent disabled:opacity-50"
            onClick={() => gpsInputRef.current?.click()}
            disabled={importing}
          >
            {importing ? "Importing…" : "Choose JSON file"}
          </button>
          <input
            ref={gpsInputRef}
            type="file"
            className="hidden"
            aria-label="GPS track JSON file"
            accept=".json,application/json"
            onChange={(e) => handleGpsImport(e.target.files?.[0])}
          />
          {gpsResult && (
            <p className="mt-2 text-muted-foreground">
              Imported {gpsResult.inserted} point(s), {gpsResult.skipped} skipped, {gpsResult.errors.length} invalid
            </p>
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
