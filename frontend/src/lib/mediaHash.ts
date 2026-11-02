// The dedup hash the backend stores in media.file_hash (mediaHash in
// backend/internal/handlers/register.go), so the Upload page can skip files the library
// already has instead of sending them only for the server to skip. Its definition is frozen to
// match every existing row, quirks included: see the Go comment. Both sides check
// mediaHash.vectors.json, which the Go test writes.
//
// A wrong guess costs nothing but bandwidth: the server hashes every file it receives itself.

const HEAD_SIZE = 1024 * 1024;
const CHUNK = 64 * 1024;
// ingest.IsVideoExt. A video is hashed by its head alone, as the server does for every video it
// probes with ffprobe, which is every video without EXIF dimensions (in practice, all of them).
const VIDEO_EXTS = new Set([".mp4", ".mov", ".avi", ".mkv", ".3gp"]);

function isVideo(name: string): boolean {
  const dot = name.lastIndexOf(".");
  return dot >= 0 && VIDEO_EXTS.has(name.slice(dot).toLowerCase());
}

/** Web Crypto exists only in a secure context (https or localhost). Without it, upload everything. */
export function canHashFiles(): boolean {
  return typeof crypto !== "undefined" && crypto.subtle !== undefined;
}

/** Reads at most 128 KB of the file: the byte ranges the hash covers. */
export async function mediaHash(file: File): Promise<string> {
  const size = file.size;
  const headLen = Math.min(size, HEAD_SIZE);
  let hashedSize: number;
  let ranges: [number, number][];
  if (isVideo(file.name)) {
    hashedSize = headLen;
    ranges = headLen <= 2 * CHUNK ? [[0, headLen]] : [[0, CHUNK], [headLen - CHUNK, headLen]];
  } else if (size <= 2 * CHUNK) {
    hashedSize = size;
    ranges = [[0, size]];
  } else {
    // The tail is the last 64 KB of what follows the first 1 MB: nothing up to 1 MB.
    hashedSize = size;
    ranges = [[0, CHUNK], [Math.max(size - CHUNK, headLen), size]];
  }

  const sizeBytes = new ArrayBuffer(8);
  new DataView(sizeBytes).setBigUint64(0, BigInt(hashedSize), true);
  const parts = await Promise.all(ranges.map(([from, to]) => file.slice(from, to).arrayBuffer()));
  const data = new Uint8Array(8 + parts.reduce((n, p) => n + p.byteLength, 0));
  data.set(new Uint8Array(sizeBytes), 0);
  let offset = 8;
  for (const part of parts) {
    data.set(new Uint8Array(part), offset);
    offset += part.byteLength;
  }
  const digest = await crypto.subtle.digest("SHA-256", data);
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
}
