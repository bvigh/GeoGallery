import { useState, useRef } from "react";
import { Upload } from "lucide-react";

export function UploadPage() {
  const [files, setFiles] = useState<FileList | null>(null);
  const [uploading, setUploading] = useState(false);
  const [result, setResult] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const handleUpload = async () => {
    if (!files || files.length === 0) return;
    setUploading(true);
    setError(null);

    const form = new FormData();
    for (const file of Array.from(files)) {
      form.append("files", file);
    }

    try {
      const res = await fetch("/api/media/upload", { method: "POST", body: form });
      const data = await res.json();
      setResult(data.uploaded ?? []);
      setFiles(null);
      if (inputRef.current) inputRef.current.value = "";
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Upload failed");
    } finally {
      setUploading(false);
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
          accept="image/*,video/*"
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

      {error && (
        <p className="mt-4 text-sm text-red-500">{error}</p>
      )}

      <div className="mt-8 rounded-lg border p-4 text-sm text-muted-foreground">
        <p className="font-medium text-foreground">Bulk ingest (local folder)</p>
        <p className="mt-1">
          Copy files to the <code className="rounded bg-muted px-1">./ingest/</code> folder,
          then trigger a scan via:
        </p>
        <pre className="mt-2 rounded bg-muted p-2 text-xs">
          curl -X POST http://localhost:8080/api/ingest/scan
        </pre>
      </div>
    </div>
  );
}
