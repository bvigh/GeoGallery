import { describe, expect, it } from "vitest";
import {
  BASE_ROTATE_SPEED,
  DEFAULT_FOV,
  MAX_CAMERA_FOV,
  MAX_FOV,
  MIN_FOV,
  cameraFovFor,
  cameraToShortFov,
  clampFov,
  maxFovFor,
  rotateSpeedFor,
  shortToCameraFov,
  wheelZoomFactor,
  zoomFov,
} from "./sphereMath";

const DEG = Math.PI / 180;
// Degrees across the width of a camera with this vertical FOV and aspect.
const horizontal = (vfov: number, aspect: number) => (2 * Math.atan(Math.tan((vfov * DEG) / 2) * aspect)) / DEG;

describe("shortToCameraFov", () => {
  it.each([
    { name: "16:9 desktop", aspect: 16 / 9 },
    { name: "square", aspect: 1 },
    { name: "sideways phone", aspect: 2.16 },
  ])("leaves landscape values alone ($name)", ({ aspect }) => {
    expect(shortToCameraFov(75, aspect)).toBe(75);
    expect(cameraToShortFov(75, aspect)).toBe(75);
  });

  it.each([
    { aspect: 0.46, fov: 75 },
    { aspect: 0.5625, fov: 30 },
    { aspect: 0.75, fov: 110 },
  ])("shows the short-side FOV across the width on portrait (aspect $aspect, $fov°)", ({ aspect, fov }) => {
    const vertical = shortToCameraFov(fov, aspect);
    expect(vertical).toBeGreaterThan(fov);
    expect(horizontal(vertical, aspect)).toBeCloseTo(fov, 9);
    expect(cameraToShortFov(vertical, aspect)).toBeCloseTo(fov, 9);
  });

  it("matches the known portrait figure", () => {
    // 75° across a 0.46 aspect phone needs ~118° vertically.
    expect(shortToCameraFov(75, 0.46)).toBeCloseTo(118.1, 1);
  });

  it("treats a broken aspect as landscape", () => {
    expect(shortToCameraFov(75, NaN)).toBe(75);
    expect(shortToCameraFov(75, 0)).toBe(75);
  });
});

describe("maxFovFor and clampFov", () => {
  it("allows the full range on landscape", () => {
    expect(maxFovFor(16 / 9)).toBe(MAX_FOV);
    expect(clampFov(200, 16 / 9)).toBe(MAX_FOV);
    expect(clampFov(1, 16 / 9)).toBe(MIN_FOV);
    expect(clampFov(100, 16 / 9)).toBe(100);
  });

  it("zooms out further than the old 100° limit", () => {
    expect(MAX_FOV).toBeGreaterThan(100);
  });

  it("keeps the camera's vertical FOV under its cap on a tall phone", () => {
    const aspect = 0.46;
    const max = maxFovFor(aspect);
    expect(max).toBeLessThan(MAX_FOV);
    expect(max).toBeGreaterThan(100);
    expect(cameraFovFor(MAX_FOV, aspect)).toBeCloseTo(MAX_CAMERA_FOV, 9);
    expect(MAX_CAMERA_FOV).toBeLessThan(180);
  });

  it("replaces a missing or broken value with the default", () => {
    expect(clampFov(NaN)).toBe(DEFAULT_FOV);
    expect(clampFov(Infinity)).toBe(DEFAULT_FOV);
  });
});

describe("cameraFovFor", () => {
  it("is the stored value on desktop", () => {
    expect(cameraFovFor(75, 16 / 9)).toBe(75);
    expect(cameraFovFor(42.5, 1.6)).toBe(42.5);
  });

  it("converts and clamps on portrait", () => {
    expect(cameraFovFor(75, 0.5)).toBeCloseTo(shortToCameraFov(75, 0.5), 9);
    expect(cameraFovFor(10, 0.5)).toBeCloseTo(shortToCameraFov(MIN_FOV, 0.5), 9);
  });
});

describe("zoomFov", () => {
  it("magnifies by the ratio of tan(fov/2)", () => {
    const zoomed = zoomFov(75, 2, 16 / 9);
    expect(Math.tan((75 * DEG) / 2) / Math.tan((zoomed * DEG) / 2)).toBeCloseTo(2, 9);
  });

  it("zooms out with a factor below 1 and back again", () => {
    const out = zoomFov(60, 0.8, 1.5);
    expect(out).toBeGreaterThan(60);
    expect(zoomFov(out, 1 / 0.8, 1.5)).toBeCloseTo(60, 9);
  });

  it("stays in range", () => {
    expect(zoomFov(75, 100, 1.5)).toBe(MIN_FOV);
    expect(zoomFov(75, 0.01, 1.5)).toBe(MAX_FOV);
    expect(zoomFov(75, 0.01, 0.46)).toBeCloseTo(maxFovFor(0.46), 9);
  });

  it("ignores a broken factor", () => {
    expect(zoomFov(75, 0, 1.5)).toBe(75);
    expect(zoomFov(75, NaN, 1.5)).toBe(75);
  });
});

describe("rotateSpeedFor", () => {
  it("keeps the old speed at the default FOV", () => {
    expect(rotateSpeedFor(DEFAULT_FOV)).toBeCloseTo(BASE_ROTATE_SPEED, 12);
  });

  it("turns slower when zoomed in and faster when zoomed out", () => {
    expect(Math.abs(rotateSpeedFor(30))).toBeLessThan(Math.abs(BASE_ROTATE_SPEED));
    expect(Math.abs(rotateSpeedFor(110))).toBeGreaterThan(Math.abs(BASE_ROTATE_SPEED));
    expect(rotateSpeedFor(30)).toBeLessThan(0);
  });

  it("follows the picture's scale at the centre", () => {
    // Half the tan(fov/2) means half the degrees per pixel.
    const narrow = (2 * Math.atan(Math.tan((DEFAULT_FOV * DEG) / 2) / 2)) / DEG;
    expect(rotateSpeedFor(narrow)).toBeCloseTo(BASE_ROTATE_SPEED / 2, 12);
  });
});

describe("wheelZoomFactor", () => {
  it.each([
    { name: "scroll down zooms out", e: { deltaY: 100, deltaMode: 0, ctrlKey: false }, out: true },
    { name: "scroll up zooms in", e: { deltaY: -100, deltaMode: 0, ctrlKey: false }, out: false },
    { name: "pinch apart zooms in", e: { deltaY: -5, deltaMode: 0, ctrlKey: true }, out: false },
  ])("$name", ({ e, out }) => {
    const f = wheelZoomFactor(e);
    expect(out ? f < 1 : f > 1).toBe(true);
  });

  it("changes the default view by about 5° per mouse-wheel notch, as before", () => {
    const fov = zoomFov(DEFAULT_FOV, wheelZoomFactor({ deltaY: 100, deltaMode: 0, ctrlKey: false }), 16 / 9);
    expect(fov - DEFAULT_FOV).toBeGreaterThan(4);
    expect(fov - DEFAULT_FOV).toBeLessThan(7);
  });

  it("scales line and page deltas to pixels", () => {
    const px = wheelZoomFactor({ deltaY: 48, deltaMode: 0, ctrlKey: false });
    expect(wheelZoomFactor({ deltaY: 3, deltaMode: 1, ctrlKey: false })).toBeCloseTo(px, 12);
    expect(wheelZoomFactor({ deltaY: 1, deltaMode: 2, ctrlKey: false })).toBeCloseTo(
      wheelZoomFactor({ deltaY: 100, deltaMode: 0, ctrlKey: false }),
      12
    );
  });

  it("zooms a trackpad pinch faster than a scroll of the same delta", () => {
    const scroll = wheelZoomFactor({ deltaY: -10, deltaMode: 0, ctrlKey: false });
    const pinch = wheelZoomFactor({ deltaY: -10, deltaMode: 0, ctrlKey: true });
    expect(pinch).toBeGreaterThan(scroll);
  });

  it("keeps a trackpad pinch's exact scale", () => {
    expect(wheelZoomFactor({ deltaY: -100 * Math.log(1.2), deltaMode: 0, ctrlKey: true })).toBeCloseTo(1.2, 12);
  });

  it.each([
    { name: "Chrome Ctrl+wheel in", e: { deltaY: -100, deltaMode: 0, ctrlKey: true } },
    { name: "Chrome Ctrl+wheel out", e: { deltaY: 100, deltaMode: 0, ctrlKey: true } },
    { name: "a 120 px Ctrl+wheel notch", e: { deltaY: 120, deltaMode: 0, ctrlKey: true } },
    { name: "Firefox Ctrl+wheel in lines", e: { deltaY: -3, deltaMode: 1, ctrlKey: true } },
    { name: "a Ctrl+wheel page", e: { deltaY: 1, deltaMode: 2, ctrlKey: true } },
  ])("moves the default view by a notch, not a leap, on $name", ({ e }) => {
    const change = Math.abs(zoomFov(DEFAULT_FOV, wheelZoomFactor(e), 16 / 9) - DEFAULT_FOV);
    expect(change).toBeGreaterThan(1);
    expect(change).toBeLessThan(10);
  });

  it("caps one event, so a page or a flick cannot leap across the range", () => {
    const notch = wheelZoomFactor({ deltaY: 100, deltaMode: 0, ctrlKey: false });
    expect(wheelZoomFactor({ deltaY: 3, deltaMode: 2, ctrlKey: false })).toBeCloseTo(notch, 12);
    expect(wheelZoomFactor({ deltaY: 5000, deltaMode: 0, ctrlKey: false })).toBeCloseTo(notch, 12);
    expect(wheelZoomFactor({ deltaY: -5000, deltaMode: 0, ctrlKey: false })).toBeCloseTo(1 / notch, 12);
  });
});
