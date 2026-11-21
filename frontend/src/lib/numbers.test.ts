import { describe, expect, it } from "vitest";
import { formatBytes, formatCount } from "@/lib/numbers";

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

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [1, "1 B"],
    [999, "999 B"],
    [1000, "1 kB"],
    [1234, "1.2 kB"],
    [99_949, "99.9 kB"],
    [99_950, "100 kB"],
    [123_456, "123 kB"],
    [999_499, "999 kB"],
    [999_500, "1 MB"],
    [999_960, "1 MB"],
    [34_567_890, "34.6 MB"],
    [5_368_709_120, "5.4 GB"],
    [123_456_789_012, "123 GB"],
    [999_999_999_999, "1 TB"],
    [1_500_000_000_000, "1.5 TB"],
    [2_000_000_000_000_000, "2 PB"],
    [3_000_000_000_000_000_000, "3,000 PB"],
  ])("formats %d bytes as %s", (n, want) => {
    expect(formatBytes(n, "en-US")).toBe(want);
  });

  it("follows the locale's decimal mark and grouping, not its unit names", () => {
    expect(formatBytes(1234, "de-DE")).toBe("1,2 kB");
    expect(formatBytes(512, "fr-FR")).toBe("512 B");
    expect(formatBytes(3e18, "de-DE")).toBe("3.000 PB");
  });
});
