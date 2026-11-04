// Pointer gesture helpers for the 360° viewers. They take plain ids, coordinates and times, so
// they hold no DOM state and test without events.

interface Point {
  x: number;
  y: number;
}

function distance(a: Point, b: Point): number {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

export interface Pinch {
  down(id: number, x: number, y: number): void;
  /** Spread of the pinch since the last move (> 1 = fingers apart), or null when not pinching. */
  move(id: number, x: number, y: number): number | null;
  up(id: number): void;
}

// Follows every pointer on an element and reports the first two as a pinch. The factor is
// incremental, so a finger lifted or added mid-gesture only restarts the measurement.
export function createPinch(): Pinch {
  const points = new Map<number, Point>();
  // Distance between the first two pointers at the last event; 0 when fewer than two are down.
  let last = 0;
  const spread = () => {
    const [a, b] = points.values();
    return a && b ? distance(a, b) : 0;
  };
  return {
    down(id, x, y) {
      points.set(id, { x, y });
      last = spread();
    },
    move(id, x, y) {
      if (!points.has(id)) return null;
      points.set(id, { x, y });
      const prev = last;
      last = spread();
      return prev > 0 && last > 0 ? last / prev : null;
    },
    up(id) {
      points.delete(id);
      last = spread();
    },
  };
}

// Movement up to this still counts as a tap. Fingers wobble more than a mouse, and a mouse
// nudge of a few pixels is a deliberate drag (fine-tuning a 360° view, panning a photo).
export const TAP_SLOP_TOUCH = 10;
export const TAP_SLOP_MOUSE = 4;
export const TAP_MAX_MS = 400;

/** How far a pointer of this type (PointerEvent.pointerType) may move and still tap. */
export function tapSlop(pointerType: string): number {
  return pointerType === "mouse" ? TAP_SLOP_MOUSE : TAP_SLOP_TOUCH;
}

export interface TapDetector {
  /** `primary`: a touch, a pen contact or the main mouse button. */
  down(id: number, x: number, y: number, time: number, primary: boolean, pointerType: string): void;
  move(id: number, x: number, y: number): void;
  /** True when this release ends a tap. */
  up(id: number, x: number, y: number, time: number): boolean;
  cancel(id: number): void;
}

// A tap is one primary pointer pressed and released quickly in place. A drag, a long press and
// anything that becomes a pinch are not taps.
export function createTapDetector(): TapDetector {
  const down = new Set<number>();
  let start: (Point & { id: number; time: number; slop: number }) | null = null;
  return {
    down(id, x, y, time, primary, pointerType) {
      down.add(id);
      start = down.size === 1 && primary ? { id, x, y, time, slop: tapSlop(pointerType) } : null;
    },
    move(id, x, y) {
      if (start?.id === id && distance(start, { x, y }) > start.slop) start = null;
    },
    up(id, x, y, time) {
      down.delete(id);
      const s = start;
      if (s?.id !== id) return false;
      start = null;
      return distance(s, { x, y }) <= s.slop && time - s.time <= TAP_MAX_MS;
    },
    cancel(id) {
      down.delete(id);
      if (start?.id === id) start = null;
    },
  };
}
