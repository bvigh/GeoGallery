import { describe, expect, it } from "vitest";
import { formatCount } from "@/lib/numbers";

describe("formatCount", () => {
  it.each([
    [0, "en-US", "0"],
    [999, "en-US", "999"],
    [27558, "en-US", "27,558"],
    [6246824, "en-US", "6,246,824"],
    [27558, "de-DE", "27.558"],
    [-1200, "en-US", "-1,200"],
    [1234.6, "en-US", "1,235"],
  ])("formats %d in %s", (n, locale, want) => {
    expect(formatCount(n, locale)).toBe(want);
  });

  it("keeps one formatter per locale, so a second call gives the same text", () => {
    expect(formatCount(1000, "en-GB")).toBe("1,000");
    expect(formatCount(1000, "en-GB")).toBe("1,000");
    expect(formatCount(1000, "de-DE")).toBe("1.000");
  });
});
