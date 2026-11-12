// Pure helpers for drawing a track and picking a point on it. Nothing here interpolates.

const MIN_BREAK_GAP_S = 300;
const GOOGLE_TIMELINE_BREAK_GAP_S = 7200;

// How far a map click may land from a point, in pixels.
export const POINT_HIT_RADIUS_PX = { coarse: 22, mouse: 8 } as const;

const FORMAT_COLORS = new Map([
  ["gpx", "#2563eb"],
  ["tcx", "#0891b2"],
  ["nmea", "#7c3aed"],
  ["esoko", "#db2777"],
  ["samsung_health", "#059669"],
  ["google_timeline", "#d97706"],
  ["kml", "#ea580c"],
  ["kmz", "#ea580c"],
  ["geojson", "#ca8a04"],
  ["exif", "#16a34a"],
  ["manual", "#dc2626"],
]);
const OTHER_FORMAT_COLOR = "#64748b";

// One colour per format, shared by a track's line and that format's dot on the map.
export function formatColor(format: string): string {
  return FORMAT_COLORS.get(format) ?? OTHER_FORMAT_COLOR;
}

// The largest gap a line of this format is still drawn across.
export function breakGapSeconds(format: string, bucketS: number): number {
  return format === "google_timeline" ? GOOGLE_TIMELINE_BREAK_GAP_S : Math.max(MIN_BREAK_GAP_S, 3 * bucketS);
}

// Splits one source's points, in time order, into lines that break at time gaps, so the map draws
// no straight line across a stretch with no data.
export function splitSegments<T extends { t: string }>(points: readonly T[], format: string, bucketS: number): T[][] {
  const maxGapMs = breakGapSeconds(format, bucketS) * 1000;
  const segments: T[][] = [];
  let previous = 0;
  for (const point of points) {
    const at = Date.parse(point.t);
    if (segments.length === 0 || at - previous > maxGapMs) segments.push([]);
    segments[segments.length - 1].push(point);
    previous = at;
  }
  return segments;
}

export interface PixelPosition {
  x: number;
  y: number;
}

// The point closest to a click, within radiusPx (inclusive); the earlier one on a tie, null when
// none is that close. `project` gives a point's position in the same pixels as the click.
export function nearestPoint<T>(
  click: PixelPosition,
  points: readonly T[],
  project: (point: T) => PixelPosition,
  radiusPx: number
): T | null {
  const radiusSquared = radiusPx * radiusPx;
  let best: T | null = null;
  let bestSquared = Infinity;
  for (const point of points) {
    const { x, y } = project(point);
    const squared = (x - click.x) ** 2 + (y - click.y) ** 2;
    if (squared <= radiusSquared && squared < bestSquared) {
      best = point;
      bestSquared = squared;
    }
  }
  return best;
}
