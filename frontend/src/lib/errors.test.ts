import { describe, it, expect } from "vitest";
import { ApiError } from "@/api/client";
import { describeError } from "@/lib/errors";

describe("describeError", () => {
  it.each([
    ["403", new ApiError("failed: 403", 403), undefined, "Could not x: only the album owner can do that"],
    [
      "404",
      new ApiError("failed: 404", 404),
      undefined,
      "Could not x: the album no longer exists, or you are no longer a member",
    ],
    ["404 with its own reason", new ApiError("failed: 404", 404), "the user is gone", "Could not x: the user is gone"],
    ["another status", new ApiError("failed: 500", 500), undefined, "Could not x"],
    ["a network error", new TypeError("Failed to fetch"), undefined, "Could not x"],
  ])("describes %s", (_name, err, notFound, want) => {
    expect(describeError("Could not x", err, notFound)).toBe(want);
  });
});
