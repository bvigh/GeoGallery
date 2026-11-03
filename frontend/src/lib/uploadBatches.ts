// The backend and frontend nginx both cap one upload request at 4 GiB.
export const MAX_REQUEST_BYTES = 4 * 1024 ** 3;
// Headroom for multipart boundaries, part headers and the file_mtime fields.
export const MAX_FILE_BYTES = MAX_REQUEST_BYTES - 16 * 1024 ** 2;
// Smaller requests keep server-side spooling bounded and let a failure cost one batch only.
export const TARGET_BATCH_BYTES = 512 * 1024 ** 2;

export interface UploadPlan {
  batches: File[][];
  tooLarge: File[];
}

/**
 * Groups files into requests of at most targetBytes and maxFiles files each; a larger file gets a
 * request of its own.
 */
export function planUploadBatches(
  files: File[],
  targetBytes = TARGET_BATCH_BYTES,
  maxFileBytes = MAX_FILE_BYTES,
  maxFiles = Infinity
): UploadPlan {
  const batches: File[][] = [];
  const tooLarge: File[] = [];
  let current: File[] = [];
  let currentBytes = 0;

  for (const file of files) {
    if (file.size > maxFileBytes) {
      tooLarge.push(file);
      continue;
    }
    if (current.length > 0 && (currentBytes + file.size > targetBytes || current.length >= maxFiles)) {
      batches.push(current);
      current = [];
      currentBytes = 0;
    }
    current.push(file);
    currentBytes += file.size;
  }
  if (current.length > 0) batches.push(current);
  return { batches, tooLarge };
}
