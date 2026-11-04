import { describe, expect, it } from "vitest";
import {
  IDENTITY,
  MAX_MAX_SCALE,
  MIN_MAX_SCALE,
  clampPan,
  clampScale,
  clampZoom,
  displayedRect,
  distance,
  doubleTapTarget,
  fitRect,
  isInsideImage,
  isZoomed,
  maxScale,
  midpoint,
  pinch,
  swipeDirection,
  toTransform,
  wheelZoomFactor,
  zoomAt,
  type Point,
  type ZoomState,
} from "./zoom";

const viewport = { width: 400, height: 300 };
// A 2:1 image in a 4:3 viewport: fitted to the width, letterboxed above and below.
const content = fitRect({ width: 800, height: 400 }, viewport);

// Where a point of the unzoomed layout ends up on screen.
function project(p: Point, s: ZoomState): Point {
  return { x: p.x * s.scale + s.x, y: p.y * s.scale + s.y };
}

describe("fitRect", () => {
  it.each([
    { name: "wide image", natural: { width: 800, height: 400 }, want: { x: 0, y: 50, width: 400, height: 200 } },
    { name: "tall image", natural: { width: 300, height: 600 }, want: { x: 125, y: 0, width: 150, height: 300 } },
    { name: "small image is scaled up", natural: { width: 40, height: 30 }, want: { x: 0, y: 0, width: 400, height: 300 } },
    { name: "unknown size", natural: null, want: { x: 0, y: 0, width: 400, height: 300 } },
    { name: "zero size", natural: { width: 0, height: 0 }, want: { x: 0, y: 0, width: 400, height: 300 } },
  ])("$name", ({ natural, want }) => {
    expect(fitRect(natural, viewport)).toEqual(want);
  });
});

describe("maxScale", () => {
  it.each([
    { name: "reaches twice the natural resolution", natural: { width: 4000, height: 2000 }, want: 20 },
    { name: "at least MIN_MAX_SCALE for a small file", natural: { width: 400, height: 200 }, want: MIN_MAX_SCALE },
    { name: "capped for a huge panorama", natural: { width: 40000, height: 4000 }, want: MAX_MAX_SCALE },
    { name: "MIN_MAX_SCALE before the image loads", natural: null, want: MIN_MAX_SCALE },
  ])("$name", ({ natural, want }) => {
    expect(maxScale(natural, viewport)).toBeCloseTo(want);
  });
});

describe("clampScale", () => {
  it.each([
    { scale: 0.5, want: 1 },
    { scale: 2, want: 2 },
    { scale: 9, want: 6 },
  ])("$scale -> $want", ({ scale, want }) => {
    expect(clampScale(scale, 6)).toBe(want);
  });
});

describe("isZoomed", () => {
  it("ignores float noise around fit", () => {
    expect(isZoomed(IDENTITY)).toBe(false);
    expect(isZoomed({ scale: 1.0000001, x: 0, y: 0 })).toBe(false);
    expect(isZoomed({ scale: 1.1, x: 0, y: 0 })).toBe(true);
  });
});

describe("zoomAt", () => {
  it("keeps the content under the origin in place", () => {
    const origin = { x: 120, y: 80 };
    const from: ZoomState = { scale: 1.5, x: -30, y: -10 };
    const under = { x: (origin.x - from.x) / from.scale, y: (origin.y - from.y) / from.scale };
    const to = zoomAt(from, origin, 3);
    expect(to.scale).toBe(3);
    const moved = project(under, to);
    expect(moved.x).toBeCloseTo(origin.x);
    expect(moved.y).toBeCloseTo(origin.y);
  });
});

describe("clampPan", () => {
  it("centers an image smaller than the viewport", () => {
    expect(clampPan({ scale: 1, x: 37, y: -12 }, viewport, content)).toEqual(IDENTITY);
  });

  it("centers the short axis and bounds the long one", () => {
    // 1.2x: 480 x 240, wider than the viewport but still shorter than it.
    const s = clampPan({ scale: 1.2, x: 500, y: 500 }, viewport, content);
    const r = displayedRect(s, content);
    expect(r.x).toBeCloseTo(0);
    expect(r.y + r.height / 2).toBeCloseTo(viewport.height / 2);
  });

  it.each([
    { name: "dragged right", x: 1000, y: 0, wantX: 0 },
    { name: "dragged left", x: -5000, y: 0, wantX: 400 - 800 },
  ])("stops at the edge when $name", ({ x, y, wantX }) => {
    // 2x of a full-viewport image: 800 x 600.
    const full = fitRect(null, viewport);
    const s = clampPan({ scale: 2, x, y }, viewport, full);
    expect(s.x).toBeCloseTo(wantX);
    expect(s.y).toBeCloseTo(0);
  });

  it("never shows a gap on a large, zoomed image", () => {
    for (const [x, y] of [[-9999, -9999], [9999, 9999], [-9999, 9999], [3, -7]]) {
      const r = displayedRect(clampPan({ scale: 4, x, y }, viewport, content), content);
      expect(r.x).toBeLessThanOrEqual(0);
      expect(r.y).toBeLessThanOrEqual(0);
      expect(r.x + r.width).toBeGreaterThanOrEqual(viewport.width);
      expect(r.y + r.height).toBeGreaterThanOrEqual(viewport.height);
    }
  });

  it("leaves an in-bounds pan alone", () => {
    const s = { scale: 4, x: -300, y: -400 };
    expect(clampPan(s, viewport, content)).toEqual(s);
  });
});

describe("clampZoom", () => {
  it("clamps the scale, then the pan", () => {
    expect(clampZoom({ scale: 0.5, x: 40, y: 40 }, viewport, content, 4)).toEqual(IDENTITY);
    expect(clampZoom({ scale: 10, x: 0, y: 0 }, viewport, content, 4).scale).toBe(4);
  });
});

describe("isInsideImage", () => {
  it.each([
    { name: "on the image", point: { x: 200, y: 150 }, state: IDENTITY, want: true },
    { name: "in the top letterbox", point: { x: 200, y: 20 }, state: IDENTITY, want: false },
    { name: "in the bottom letterbox", point: { x: 200, y: 280 }, state: IDENTITY, want: false },
    { name: "on the image once zoomed", point: { x: 200, y: 20 }, state: { scale: 2, x: -200, y: -150 }, want: true },
  ])("$name", ({ point, state, want }) => {
    expect(isInsideImage(point, state, content)).toBe(want);
  });
});

describe("midpoint and distance", () => {
  it("measures two fingers", () => {
    expect(midpoint({ x: 0, y: 0 }, { x: 30, y: 40 })).toEqual({ x: 15, y: 20 });
    expect(distance({ x: 0, y: 0 }, { x: 30, y: 40 })).toBe(50);
  });
});

describe("pinch", () => {
  it("scales by the finger distance and keeps the content under the midpoint", () => {
    const start: ZoomState = { scale: 1, x: 0, y: 0 };
    const from = { mid: { x: 100, y: 150 }, dist: 100 };
    const to = { mid: { x: 100, y: 150 }, dist: 250 };
    const s = pinch(start, from, to, 8);
    expect(s.scale).toBeCloseTo(2.5);
    const p = project(from.mid, s);
    expect(p.x).toBeCloseTo(100);
    expect(p.y).toBeCloseTo(150);
  });

  it("follows the midpoint as the fingers move", () => {
    const start: ZoomState = { scale: 2, x: -100, y: -50 };
    const from = { mid: { x: 200, y: 150 }, dist: 100 };
    const to = { mid: { x: 260, y: 120 }, dist: 150 };
    const s = pinch(start, from, to, 8);
    expect(s.scale).toBeCloseTo(3);
    const under = { x: (from.mid.x - start.x) / start.scale, y: (from.mid.y - start.y) / start.scale };
    const p = project(under, s);
    expect(p.x).toBeCloseTo(260);
    expect(p.y).toBeCloseTo(120);
  });

  it.each([
    { name: "max", dist: 10_000, want: 4 },
    { name: "fit", dist: 10, want: 1 },
  ])("stops at the $name", ({ dist, want }) => {
    const s = pinch(IDENTITY, { mid: { x: 0, y: 0 }, dist: 100 }, { mid: { x: 0, y: 0 }, dist }, 4);
    expect(s.scale).toBe(want);
  });

  it("survives a zero start distance", () => {
    const s = pinch(IDENTITY, { mid: { x: 0, y: 0 }, dist: 0 }, { mid: { x: 5, y: 5 }, dist: 50 }, 4);
    expect(s.scale).toBe(1);
  });
});

describe("doubleTapTarget", () => {
  it("zooms to 2.5x at the tap", () => {
    const tap = { x: 150, y: 140 };
    const s = doubleTapTarget(IDENTITY, tap, viewport, content, 8);
    expect(s.scale).toBe(2.5);
    const p = project(tap, s);
    expect(p.x).toBeCloseTo(tap.x);
    expect(p.y).toBeCloseTo(tap.y);
  });

  it("clamps a tap near the edge", () => {
    const s = doubleTapTarget(IDENTITY, { x: 0, y: 0 }, viewport, content, 8);
    expect(s).toEqual(clampPan(s, viewport, content));
    const r = displayedRect(s, content);
    expect(r.x).toBeCloseTo(0);
    expect(r.y).toBeCloseTo(0);
  });

  it("does not pass the max scale", () => {
    expect(doubleTapTarget(IDENTITY, { x: 200, y: 150 }, viewport, content, 2).scale).toBe(2);
  });

  it("goes back to fit when zoomed", () => {
    expect(doubleTapTarget({ scale: 3, x: -100, y: -100 }, { x: 1, y: 1 }, viewport, content, 8)).toEqual(IDENTITY);
  });
});

describe("wheelZoomFactor", () => {
  it("zooms in on a wheel up and out on a wheel down", () => {
    expect(wheelZoomFactor(-100, 0, false, 300)).toBeGreaterThan(1);
    expect(wheelZoomFactor(100, 0, false, 300)).toBeLessThan(1);
    expect(wheelZoomFactor(0, 0, false, 300)).toBe(1);
  });

  it("makes a trackpad pinch steeper than a scroll of the same delta", () => {
    expect(wheelZoomFactor(-10, 0, true, 300)).toBeGreaterThan(wheelZoomFactor(-10, 0, false, 300));
  });

  it("converts lines and pages to pixels", () => {
    expect(wheelZoomFactor(-3, 1, false, 300)).toBeCloseTo(wheelZoomFactor(-48, 0, false, 300));
    expect(wheelZoomFactor(-1, 2, false, 300)).toBeCloseTo(wheelZoomFactor(-300, 0, false, 300));
  });

  it("caps one event at 2x", () => {
    expect(wheelZoomFactor(-5000, 0, true, 300)).toBeCloseTo(2);
    expect(wheelZoomFactor(5000, 0, true, 300)).toBeCloseTo(0.5);
  });
});

describe("swipeDirection", () => {
  it.each([
    { name: "right to left", dx: -80, dy: 10, want: "next" },
    { name: "left to right", dx: 80, dy: -10, want: "prev" },
    { name: "too short", dx: -40, dy: 0, want: null },
    { name: "too steep", dx: -80, dy: 60, want: null },
    { name: "vertical", dx: 0, dy: 200, want: null },
  ])("$name", ({ dx, dy, want }) => {
    expect(swipeDirection(dx, dy)).toBe(want);
  });
});

describe("toTransform", () => {
  it("translates, then scales", () => {
    expect(toTransform({ scale: 2, x: -10, y: 5.5 })).toBe("translate(-10px, 5.5px) scale(2)");
  });
});
