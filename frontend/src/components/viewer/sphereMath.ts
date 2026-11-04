// Field-of-view math for the 360° viewers.
//
// A view's FOV (initial_fov, keyframe fov, what captureRef returns) is measured across the
// SHORTER side of the viewport. The stored values were made on desktops as three.js's vertical
// FOV, and on a landscape viewport (aspect >= 1) the two are the same, so they keep their meaning.
// On a portrait phone the same value spans the width instead: a 75° view shows 75° across, where
// a vertical 75° would leave only ~39° across a 0.46 aspect screen.

const DEG = Math.PI / 180;

export const DEFAULT_FOV = 75;
// Narrower than 30° only magnifies texture pixels: a 5760 px wide video is 16 px per degree, and
// phone GPUs may downscale big photos to a 4096 px texture.
export const MIN_FOV = 30;
// 110° across the short side is half again the default view and about the widest a flat
// (rectilinear) projection stays readable. Desktop gets 10° more than the old 100° vertical.
export const MAX_FOV = 110;
// The camera's vertical FOV never exceeds 140°: a flat projection stretches its edges by
// 1/cos²(half-angle), which is 8.5× at 140° and grows without bound towards 180°. On a very tall
// screen this caps the short side a little below MAX_FOV (~103° at aspect 0.46).
export const MAX_CAMERA_FOV = 140;

// OrbitControls' rotateSpeed at the default FOV; negative turns the view the way the finger drags.
export const BASE_ROTATE_SPEED = -0.3;

// Wheel zoom per pixel of deltaY: one 100 px mouse-wheel notch changes 75° by ~5°, as before.
const WHEEL_RATE = 0.001;
// A trackpad pinch arrives as a ctrl+wheel whose deltaY is about -100·ln(scale).
const PINCH_WHEEL_RATE = 0.01;
// A trackpad pinch sends a few pixels per event. Ctrl held over a mouse wheel sends whole notches
// (±100–120 px, or line/page units), which at the pinch rate would jump ~40° in one notch.
const PINCH_MAX_DELTA = 50;
// Caps one event, so a page-mode delta or a fast flick cannot leap to a zoom limit.
const MAX_WHEEL_DELTA = 100;

/** Vertical camera FOV (degrees) that shows `fov` across the shorter side at this aspect (w/h). */
export function shortToCameraFov(fov: number, aspect: number): number {
  if (!(aspect > 0) || aspect >= 1) return fov;
  return (2 * Math.atan(Math.tan((fov * DEG) / 2) / aspect)) / DEG;
}

/** The inverse of shortToCameraFov. */
export function cameraToShortFov(cameraFov: number, aspect: number): number {
  if (!(aspect > 0) || aspect >= 1) return cameraFov;
  return (2 * Math.atan(Math.tan((cameraFov * DEG) / 2) * aspect)) / DEG;
}

/** Widest short-side FOV a viewport of this aspect allows. */
export function maxFovFor(aspect: number): number {
  return Math.min(MAX_FOV, cameraToShortFov(MAX_CAMERA_FOV, aspect));
}

/** `fov` limited to the zoom range at this aspect; a missing or broken value becomes the default. */
export function clampFov(fov: number, aspect = 1): number {
  if (!Number.isFinite(fov)) return DEFAULT_FOV;
  return Math.min(maxFovFor(aspect), Math.max(MIN_FOV, fov));
}

/** The vertical FOV to give the three.js camera for a short-side `fov`. */
export function cameraFovFor(fov: number, aspect: number): number {
  return shortToCameraFov(clampFov(fov, aspect), aspect);
}

/**
 * Short-side FOV after magnifying the picture by `factor` (> 1 zooms in). A flat projection
 * magnifies by the ratio of tan(fov/2), so a pinch keeps the picture under the fingers.
 */
export function zoomFov(fov: number, factor: number, aspect: number): number {
  if (!(factor > 0) || !Number.isFinite(factor)) return clampFov(fov, aspect);
  return clampFov((2 * Math.atan(Math.tan((fov * DEG) / 2) / factor)) / DEG, aspect);
}

/**
 * OrbitControls.rotateSpeed for a camera FOV, so a drag moves the picture with the pointer at
 * any zoom: OrbitControls turns a fixed angle per pixel, while the picture's degrees per pixel
 * at the centre follow tan(fov/2). Equals BASE_ROTATE_SPEED at the default FOV.
 */
export function rotateSpeedFor(cameraFov: number): number {
  return (BASE_ROTATE_SPEED * Math.tan((cameraFov * DEG) / 2)) / Math.tan((DEFAULT_FOV * DEG) / 2);
}

/**
 * Magnification for one wheel event. A ctrlKey event with a small pixel delta is a trackpad
 * pinch; anything else, Ctrl+mouse wheel included, zooms like a mouse-wheel notch.
 */
export function wheelZoomFactor(e: Pick<WheelEvent, "deltaY" | "deltaMode" | "ctrlKey">): number {
  let dy = e.deltaY;
  if (e.deltaMode === 1) dy *= 16; // lines
  else if (e.deltaMode === 2) dy *= 100; // pages
  const pinch = e.ctrlKey && e.deltaMode === 0 && Math.abs(dy) < PINCH_MAX_DELTA;
  const clamped = Math.max(-MAX_WHEEL_DELTA, Math.min(MAX_WHEEL_DELTA, dy));
  return Math.exp(-clamped * (pinch ? PINCH_WHEEL_RATE : WHEEL_RATE));
}
