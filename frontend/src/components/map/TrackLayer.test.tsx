import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import L from "leaflet";
import { MapContainer, useMap } from "react-leaflet";
import { makeTrack } from "@/components/evidence/evidenceTestUtils";
import { TrackLayer } from "@/components/map/TrackLayer";
import { breakGapSeconds } from "@/lib/track";
import type { TrackPoint, TrackResponse, TrackSource } from "@/types";

// Real Leaflet in jsdom: nothing is painted, but the map's layers are what TrackLayer gave it.

const BASE = Date.UTC(2000, 0, 1);

function point(s: number, offsetS: number, lat = 10 + offsetS / 1000): TrackPoint {
  return { t: new Date(BASE + offsetS * 1000).toISOString(), lat, lng: 20, s, acc: null, alt: null };
}

function source(label: string, kind: TrackSource["kind"], format = label.split(":")[0]): TrackSource {
  return { label, format, kind };
}

function renderTrack(track: TrackResponse) {
  const captured: { map: L.Map | null } = { map: null };
  function Capture() {
    captured.map = useMap();
    return null;
  }
  const tree = (t: TrackResponse) => (
    <MapContainer center={[10, 20]} zoom={10}>
      <Capture />
      <TrackLayer track={t} />
    </MapContainer>
  );
  const view = render(tree(track));
  const map = captured.map!;
  const lines = () => {
    const found: L.Polyline[] = [];
    map.eachLayer((layer) => {
      if (layer instanceof L.Polyline) found.push(layer);
    });
    return found;
  };
  return { map, lines, update: (t: TrackResponse) => view.rerender(tree(t)) };
}

const parts = (line: L.Polyline) => line.getLatLngs() as L.LatLng[][];

describe("TrackLayer", () => {
  it("draws one line per source label", () => {
    const { lines } = renderTrack(
      makeTrack({
        sources: [source("gpx:a.gpx", "recorded"), source("gpx:b.gpx", "recorded")],
        points: [point(0, 0), point(1, 10), point(0, 20), point(1, 30)],
      })
    );
    expect(lines()).toHaveLength(2);
    expect(parts(lines()[0]).flat()).toHaveLength(2);
  });

  it("breaks a line where the time gap is too long", () => {
    const gap = breakGapSeconds("gpx", 0);
    const { lines } = renderTrack(
      makeTrack({
        sources: [source("gpx:a.gpx", "recorded")],
        points: [point(0, 0), point(0, 60), point(0, 60 + gap + 1), point(0, 120 + gap + 1)],
      })
    );
    expect(lines()).toHaveLength(1);
    expect(parts(lines()[0]).map((part) => part.length)).toEqual([2, 2]);
  });

  it("keeps a line whole across a gap the downsampling explains", () => {
    const { lines } = renderTrack(
      makeTrack({
        bucket_s: 600,
        sources: [source("gpx:a.gpx", "recorded")],
        points: [point(0, 0), point(0, 1200)],
      })
    );
    expect(parts(lines()[0]).map((part) => part.length)).toEqual([2]);
  });

  it("still marks a point left alone between two gaps", () => {
    const gap = breakGapSeconds("gpx", 0);
    const { lines } = renderTrack(
      makeTrack({ sources: [source("gpx:a.gpx", "recorded")], points: [point(0, 0), point(0, gap + 1), point(0, 2 * gap + 2)] })
    );
    expect(parts(lines()[0]).map((part) => part.length)).toEqual([2, 2, 2]);
    expect(parts(lines()[0])[1][0].equals(parts(lines()[0])[1][1])).toBe(true);
  });

  it("draws the lone points of a dashed source solid, because a dashed dot is not painted", () => {
    const gap = breakGapSeconds("kml", 0);
    const { lines } = renderTrack(
      makeTrack({
        sources: [source("kml:a.kml", "time_may_be_estimated")],
        points: [point(0, 0), point(0, 10), point(0, gap + 11), point(0, 2 * gap + 12)],
      })
    );
    const [line, dots] = lines();
    expect(lines()).toHaveLength(2);
    expect(line.options.dashArray).toBe("6 8");
    expect(parts(line).map((part) => part.length)).toEqual([2]);
    expect(dots.options.dashArray).toBeUndefined();
    expect(parts(dots).map((part) => part.length)).toEqual([2, 2]);
  });

  it("draws no empty line when a dashed source is only lone points", () => {
    const gap = breakGapSeconds("kml", 0);
    const { lines } = renderTrack(
      makeTrack({ sources: [source("kml:a.kml", "time_may_be_estimated")], points: [point(0, 0), point(0, gap + 1)] })
    );
    expect(lines()).toHaveLength(1);
    expect(lines()[0].options.dashArray).toBeUndefined();
  });

  it("is not interactive and sits in its own pane, above the vectors and under the markers", () => {
    const { lines, map } = renderTrack(
      makeTrack({ sources: [source("gpx:a.gpx", "recorded")], points: [point(0, 0), point(0, 1)] })
    );
    expect(lines()[0].options.interactive).toBe(false);
    const pane = map.getPane(lines()[0].options.pane!)!;
    expect(pane.style.zIndex).toBe("450");
  });

  it.each([
    ["recorded", false, false],
    ["semantic", true, true],
    ["time_may_be_estimated", true, false],
  ] as const)("styles %s", (kind, dashed, lighter) => {
    const { lines } = renderTrack(
      makeTrack({ sources: [source("gpx:a.gpx", kind)], points: [point(0, 0), point(0, 1)] })
    );
    const { dashArray, opacity } = lines()[0].options;
    expect(Boolean(dashArray)).toBe(dashed);
    expect(opacity! < 0.6).toBe(lighter);
  });

  it("draws derived points as dots with no line between them", () => {
    const { lines } = renderTrack(
      makeTrack({ sources: [source("exif", "derived")], points: [point(0, 0), point(0, 10), point(0, 20)] })
    );
    expect(lines()).toHaveLength(1);
    const dots = parts(lines()[0]);
    expect(dots).toHaveLength(3);
    for (const [from, to] of dots) expect(from.equals(to)).toBe(true);
    expect(lines()[0].options.lineCap).toBe("round");
  });

  it("colours by format", () => {
    const { lines } = renderTrack(
      makeTrack({
        sources: [source("gpx:a.gpx", "recorded"), source("nmea:b.nmea", "recorded")],
        points: [point(0, 0), point(0, 1), point(1, 0), point(1, 1)],
      })
    );
    expect(lines()[0].options.color).not.toBe(lines()[1].options.color);
  });

  it("replaces its lines when the track changes, and draws none for an empty one", () => {
    const { lines, update } = renderTrack(
      makeTrack({ sources: [source("gpx:a.gpx", "recorded")], points: [point(0, 0), point(0, 1)] })
    );
    expect(lines()).toHaveLength(1);
    update(makeTrack());
    expect(lines()).toHaveLength(0);
  });

  it("skips a source without points", () => {
    const { lines } = renderTrack(makeTrack({ sources: [source("gpx:a.gpx", "recorded")] }));
    expect(lines()).toHaveLength(0);
  });
});
