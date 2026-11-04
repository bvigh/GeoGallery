// @vitest-environment node
import { describe, expect, it } from "vitest";
import config from "./tailwind.config.js";

// Evaluates a media query of "(feature: value)" terms joined by "and" for a viewport, so the
// short screen can be checked against real phone sizes (jsdom has no matchMedia).
function matches(query: string, width: number, height: number): boolean {
  return query.split(/\s+and\s+/).every((term) => {
    const m = /^\((min-|max-)?(width|height|aspect-ratio|orientation):\s*([^)]+)\)$/.exec(term.trim());
    if (!m) throw new Error(`unsupported media feature: ${term}`);
    const [, prefix, feature, value] = m;
    if (feature === "orientation") return (value === "portrait") === height >= width;
    let actual = feature === "width" ? width : height;
    let limit = parseFloat(value);
    if (feature === "aspect-ratio") {
      const [w, h] = value.split("/").map(Number);
      actual = width / height;
      limit = w / h;
    }
    if (prefix === "min-") return actual >= limit;
    if (prefix === "max-") return actual <= limit;
    return actual === limit;
  });
}

describe("short screen", () => {
  const short: string = config.theme.extend.screens.short.raw;

  // With interactive-widget=resizes-content the on-screen keyboard shrinks the layout viewport, so
  // a phone held upright while typing is about as short as one held sideways. Only the sideways
  // phone gets the row layouts (EditLocationModal's bottom bar, the game's result card, …).
  it.each([
    { name: "portrait phone, keyboard open", width: 360, height: 400, want: false },
    { name: "large portrait phone, keyboard open", width: 412, height: 480, want: false },
    { name: "portrait phone, keyboard open, wider than tall", width: 412, height: 380, want: false },
    { name: "largest portrait phone, keyboard open", width: 430, height: 450, want: false },
    { name: "portrait phone", width: 360, height: 800, want: false },
    { name: "smallest sideways phone", width: 568, height: 320, want: true },
    { name: "sideways phone", width: 640, height: 360, want: true },
    { name: "sideways iPhone", width: 844, height: 390, want: true },
    { name: "large sideways phone", width: 932, height: 430, want: true },
    { name: "sideways phone, keyboard open", width: 844, height: 200, want: true },
    { name: "tablet in portrait", width: 768, height: 1024, want: false },
    { name: "tablet sideways", width: 1024, height: 768, want: false },
    { name: "laptop", width: 1280, height: 800, want: false },
    { name: "desktop", width: 1920, height: 1080, want: false },
  ])("$name ($width×$height): $want", ({ width, height, want }) => {
    expect(matches(short, width, height)).toBe(want);
  });
});
