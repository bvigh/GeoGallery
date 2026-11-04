// Pan/zoom math for ZoomableImage. Coordinates are CSS pixels relative to the viewport (the
// element the image fills). The image is drawn with transform-origin 0 0, so a point p of the
// unzoomed layout lands at p * scale + (x, y).

export interface ZoomState {
  scale: number;
  x: number;
  y: number;
}

export interface Point {
  x: number;
  y: number;
}

export interface Size {
  width: number;
  height: number;
}

export interface Rect extends Point, Size {}

export type SwipeDirection = "prev" | "next";

export const IDENTITY: ZoomState = { scale: 1, x: 0, y: 0 };
export const MIN_SCALE = 1;
// The double-tap zoom level.
export const DOUBLE_TAP_SCALE = 2.5;
// Every image zooms at least this far, however small its file.
export const MIN_MAX_SCALE = 4;
// Keeps a huge panorama from turning into a layer tens of thousands of pixels wide.
export const MAX_MAX_SCALE = 32;
// A swipe must travel this far horizontally, and at most SWIPE_MAX_SLOPE times as far vertically.
export const SWIPE_MIN_DISTANCE = 50;
export const SWIPE_MAX_SLOPE = 0.6;

// Below this the image counts as fitted, so float noise from a pinch back to 1 is not "zoomed".
const ZOOM_EPSILON = 1e-3;

export function isZoomed(state: ZoomState): boolean {
  return state.scale > MIN_SCALE + ZOOM_EPSILON;
}

// Where object-fit: contain draws the image at scale 1: as large as fits, centered. Without a
// known natural size (not loaded yet) the whole viewport counts as the image.
export function fitRect(natural: Size | null, viewport: Size): Rect {
  if (!natural || natural.width <= 0 || natural.height <= 0) {
    return { x: 0, y: 0, width: viewport.width, height: viewport.height };
  }
  const ratio = Math.min(viewport.width / natural.width, viewport.height / natural.height);
  const width = natural.width * ratio;
  const height = natural.height * ratio;
  return { x: (viewport.width - width) / 2, y: (viewport.height - height) / 2, width, height };
}

// Enough to show the image at twice its natural resolution, within [MIN_MAX_SCALE, MAX_MAX_SCALE].
export function maxScale(natural: Size | null, viewport: Size): number {
  const fitted = fitRect(natural, viewport);
  if (!natural || fitted.width <= 0) return MIN_MAX_SCALE;
  const full = (2 * natural.width) / fitted.width;
  return Math.min(MAX_MAX_SCALE, Math.max(MIN_MAX_SCALE, full));
}

export function clampScale(scale: number, max: number): number {
  return Math.min(max, Math.max(MIN_SCALE, scale));
}

// Changes the scale while the content under origin stays under it.
export function zoomAt(state: ZoomState, origin: Point, scale: number): ZoomState {
  const k = scale / state.scale;
  return {
    scale,
    x: origin.x - (origin.x - state.x) * k,
    y: origin.y - (origin.y - state.y) * k,
  };
}

// One axis of clampPan: an image smaller than the viewport is centered; a larger one may not
// leave a gap at either edge.
function clampAxis(offset: number, scale: number, view: number, start: number, size: number): number {
  const scaled = size * scale;
  if (scaled <= view) return (view - scaled) / 2 - start * scale;
  const min = view - (start + size) * scale;
  const max = -start * scale;
  return Math.min(max, Math.max(min, offset));
}

// Keeps the image (content: its rect at scale 1) from being dragged away from the viewport.
export function clampPan(state: ZoomState, viewport: Size, content: Rect): ZoomState {
  return {
    scale: state.scale,
    x: clampAxis(state.x, state.scale, viewport.width, content.x, content.width),
    y: clampAxis(state.y, state.scale, viewport.height, content.y, content.height),
  };
}

export function clampZoom(state: ZoomState, viewport: Size, content: Rect, max: number): ZoomState {
  const scale = clampScale(state.scale, max);
  const scaled = scale === state.scale ? state : { ...state, scale };
  return clampPan(scaled, viewport, content);
}

// The image's rect on screen under state.
export function displayedRect(state: ZoomState, content: Rect): Rect {
  return {
    x: content.x * state.scale + state.x,
    y: content.y * state.scale + state.y,
    width: content.width * state.scale,
    height: content.height * state.scale,
  };
}

export function isInsideImage(point: Point, state: ZoomState, content: Rect): boolean {
  const r = displayedRect(state, content);
  return point.x >= r.x && point.x <= r.x + r.width && point.y >= r.y && point.y <= r.y + r.height;
}

export function midpoint(a: Point, b: Point): Point {
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
}

export function distance(a: Point, b: Point): number {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

export interface PinchFrame {
  mid: Point;
  dist: number;
}

// Two-finger pinch from the state at its start: scales by the change in finger distance and
// moves the content point that was under the start midpoint to the current midpoint, so the
// fingers both zoom and pan. The pan is not clamped here.
export function pinch(start: ZoomState, from: PinchFrame, to: PinchFrame, max: number): ZoomState {
  const ratio = from.dist > 0 ? to.dist / from.dist : 1;
  const scale = clampScale(start.scale * ratio, max);
  const k = scale / start.scale;
  return {
    scale,
    x: to.mid.x - (from.mid.x - start.x) * k,
    y: to.mid.y - (from.mid.y - start.y) * k,
  };
}

// Double tap: back to fit when zoomed, else DOUBLE_TAP_SCALE (or the max, if lower) at the tap.
export function doubleTapTarget(
  state: ZoomState,
  point: Point,
  viewport: Size,
  content: Rect,
  max: number,
): ZoomState {
  if (isZoomed(state)) return IDENTITY;
  const zoomed = zoomAt(state, point, clampScale(DOUBLE_TAP_SCALE, max));
  return clampPan(zoomed, viewport, content);
}

// The zoom factor for one wheel event. A trackpad pinch arrives as ctrlKey wheel events with
// small deltas, so it gets a steeper curve than a mouse wheel's ~100 px notches.
export function wheelZoomFactor(
  deltaY: number,
  deltaMode: number,
  ctrlKey: boolean,
  viewportHeight: number,
): number {
  const LINE = 16;
  const px = deltaMode === 1 ? deltaY * LINE : deltaMode === 2 ? deltaY * viewportHeight : deltaY;
  // Caps one event at ~2x so a fast flick or a page-mode delta cannot jump to the max.
  const clamped = Math.max(-100, Math.min(100, px));
  return Math.pow(2, -clamped * (ctrlKey ? 0.01 : 0.003));
}

// A mostly horizontal drag far enough to count as a swipe; right to left is "next".
export function swipeDirection(dx: number, dy: number): SwipeDirection | null {
  if (Math.abs(dx) < SWIPE_MIN_DISTANCE) return null;
  if (Math.abs(dy) > Math.abs(dx) * SWIPE_MAX_SLOPE) return null;
  return dx < 0 ? "next" : "prev";
}

export function toTransform(state: ZoomState): string {
  return `translate(${state.x}px, ${state.y}px) scale(${state.scale})`;
}
