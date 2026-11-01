import { useState, useRef } from "react";
import { Upload } from "lucide-react";
import { api } from "@/api/client";
import { MAX_FILE_BYTES, planUploadBatches } from "@/lib/uploadBatches";
import type { GpsImportResult } from "@/types";

const GiB = 1024 ** 3;

export function UploadPage() {
  const [files, setFiles] = useState<FileList | null>(null);
  const [uploading, setUploading] = useState(false);
  const [result, setResult] = useState<string[] | null>(null);
  const [failed, setFailed] = useState<string[]>([]);
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
    setFailed([]);

    // One request for everything would exceed the 4 GiB request limit (413), so send batches.
    const { batches, tooLarge } = planUploadBatches(Array.from(files));
    const uploaded: string[] = [];
    const failedNames: string[] = tooLarge.map(
      (f) => `${f.name} (larger than ${(MAX_FILE_BYTES / GiB).toFixed(1)} GiB)`
    );
    const errors: string[] = [];

    for (const [i, batch] of batches.entries()) {
      setProgress(`Uploading batch ${i + 1} of ${batches.length} (${uploaded.length} file(s) done)`);
      const form = new FormData();
      for (const file of batch) {
        form.append("files", file);
        form.append("file_mtime", String(file.lastModified)); // ms since epoch
      }
      try {
        const data = await api.upload(form);
        uploaded.push(...(data.uploaded ?? []));
        failedNames.push(...(data.failed ?? []));
      } catch (e: unknown) {
        errors.push(e instanceof Error ? e.message : "Upload failed");
        failedNames.push(...batch.map((f) => f.name));
      }
    }

    setResult(uploaded);
    setFailed(failedNames);
    setError(errors.length > 0 ? [...new Set(errors)].join("; ") : null);
    setProgress(null);
    setUploading(false);
    if (failedNames.length === 0) {
      setFiles(null);
      if (inputRef.current) inputRef.current.value = "";
    }
  };

  return (
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
        <div className="mt-4 rounded-lg border bg-muted p-3 text-sm">
          <p className="font-medium">Uploaded {result.length} file(s)</p>
          <ul className="mt-1 list-disc pl-4 text-muted-foreground">
            {result.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        </div>
      )}

      {failed.length > 0 && (
        <div className="mt-4 rounded-lg border border-red-300 p-3 text-sm">
          <p className="font-medium text-red-500">{failed.length} file(s) failed</p>
          <ul className="mt-1 list-disc pl-4 text-muted-foreground">
            {failed.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        </div>
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
  );
}
