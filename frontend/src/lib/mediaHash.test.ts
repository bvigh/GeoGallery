// @vitest-environment node
// Node's File and Web Crypto: jsdom's File cannot be sliced into an ArrayBuffer.
import { describe, it, expect } from "vitest";
import { canHashFiles, mediaHash } from "@/lib/mediaHash";
import vectors from "../../../backend/internal/handlers/testdata/media_hash_vectors.json";

// The file every vector hashes: byte i is i % 251 (vectorContent in media_hash_test.go).
function vectorFile(name: string, size: number): File {
  const bytes = new Uint8Array(size);
  for (let i = 0; i < size; i++) bytes[i] = i % 251;
  return new File([bytes], name);
}

describe("mediaHash", () => {
  it("is available under Node", () => {
    expect(canHashFiles()).toBe(true);
  });

  it.each(vectors.vectors)("matches the backend for $filename ($size bytes)", async ({ filename, size, hash }) => {
    expect(await mediaHash(vectorFile(filename, size))).toBe(hash);
  });

  it("covers photos and videos on both sides of the 128 KB and 1 MB edges", () => {
    expect(vectors.content).toBe("byte i of the file is i % 251");
    const sizes = vectors.vectors.map((v) => v.size);
    expect(Math.min(...sizes)).toBeLessThanOrEqual(128 * 1024);
    expect(Math.max(...sizes)).toBeGreaterThan(1024 * 1024 + 64 * 1024);
    expect(vectors.vectors.some((v) => v.filename.endsWith(".mp4"))).toBe(true);
  });

  it("ignores everything a photo hash does not cover", async () => {
    // Between 128 KB and 1 MB only the size and the first 64 KB count.
    const a = vectorFile("a.jpg", 500 * 1024);
    const bytes = new Uint8Array(await a.arrayBuffer());
    bytes[480 * 1024] ^= 0xff; // inside the last 64 KB
    expect(await mediaHash(new File([bytes], "b.jpg"))).toBe(await mediaHash(a));
    // A video of the same bytes also hashes its last 64 KB.
    expect(await mediaHash(new File([bytes], "b.mp4"))).not.toBe(await mediaHash(new File([a], "a.mp4")));
  });
});
