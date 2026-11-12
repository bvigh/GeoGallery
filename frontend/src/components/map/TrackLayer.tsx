import { useEffect } from "react";
import { useMap } from "react-leaflet";
import L from "leaflet";
import { formatColor, splitSegments } from "@/lib/track";
import type { TrackPoint, TrackResponse, TrackSource } from "@/types";

// Above the other vectors (400), under the markers (600), so they stay clickable.
const TRACK_PANE = "track";
const TRACK_PANE_Z_INDEX = "450";

type LatLng = [number, number];

function lineStyle(source: TrackSource): L.PolylineOptions {
  const base: L.PolylineOptions = {
    color: formatColor(source.format),
    pane: TRACK_PANE,
    interactive: false,
    weight: 3,
    opacity: 0.85,
  };
  switch (source.kind) {
    case "recorded":
      return base;
    case "semantic":
      return { ...base, opacity: 0.5, dashArray: "6 8" };
    case "time_may_be_estimated":
      return { ...base, dashArray: "6 8" };
    case "derived":
      return { ...base, weight: 8, opacity: 1, lineCap: "round" };
    case "other":
      return { ...base, weight: 2, opacity: 0.7 };
  }
}

// A zero-length segment with a round cap draws a dot, which redraws faster than one marker each.
function dot({ lat, lng }: TrackPoint): LatLng[] {
  return [[lat, lng], [lat, lng]];
}

// The polylines of one source. A point alone between two gaps still gets a dot. Dashed sources draw
// their dots as a second, solid line: Chromium paints nothing for a zero-length subpath in a dashed
// path that has more than one subpath.
function sourceLines(source: TrackSource, points: TrackPoint[], bucketS: number): L.Polyline[] {
  const style = lineStyle(source);
  if (source.kind === "derived") return [L.polyline(points.map(dot), style)];
  const segments = splitSegments(points, source.format, bucketS);
  const toLatLngs = (segment: TrackPoint[]) =>
    segment.length === 1 ? dot(segment[0]) : segment.map((p): LatLng => [p.lat, p.lng]);
  if (!style.dashArray) return [L.polyline(segments.map(toLatLngs), style)];
  const lines = segments.filter((segment) => segment.length > 1).map(toLatLngs);
  const dots = segments.filter((segment) => segment.length === 1).map(toLatLngs);
  return [
    ...(lines.length > 0 ? [L.polyline(lines, style)] : []),
    ...(dots.length > 0 ? [L.polyline(dots, { ...style, dashArray: undefined })] : []),
  ];
}

// One line per source label, never per format. A line per format would zigzag between overlapping
// files of that format and invent movement, which a debugging view must not do.
function trackLines(track: TrackResponse): L.Polyline[] {
  return track.sources.flatMap((source, index) => {
    const points = track.points.filter((point) => point.s === index);
    return points.length === 0 ? [] : sourceLines(source, points, track.bucket_s);
  });
}

// The context track of one item. Purely visual: a map click finds its points through nearestPoint.
export function TrackLayer({ track }: { track: TrackResponse }) {
  const map = useMap();

  useEffect(() => {
    if (!map.getPane(TRACK_PANE)) map.createPane(TRACK_PANE).style.zIndex = TRACK_PANE_Z_INDEX;
    const lines = trackLines(track);
    lines.forEach((line) => line.addTo(map));
    return () => lines.forEach((line) => line.remove());
  }, [map, track]);

  return null;
}
