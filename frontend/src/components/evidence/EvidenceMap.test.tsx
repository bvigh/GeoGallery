import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import L from "leaflet";
import { api } from "@/api/client";
import { EvidenceMap } from "@/components/evidence/EvidenceMap";
import { EvidenceView } from "@/components/evidence/EvidenceView";
import { makeBracket, makeEstimate, makeOtherFormat, makeOtherPoint, makeResponse } from "@/components/evidence/evidenceTestUtils";
import { TOP_MARKER_Z_INDEX_OFFSET } from "@/components/map/markers";
import { POINT_HIT_RADIUS_PX } from "@/lib/track";
import type { ProvenanceResponse } from "@/types";

// Real Leaflet in jsdom: nothing is painted, but the map's layers are what EvidenceMap gave it.

const captured = vi.hoisted(() => ({ maps: [] as L.Map[] }));

vi.mock("react-leaflet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-leaflet")>();
  const React = await import("react");
  // Only adds a child that hands the map out: every test below drives a real L.Map.
  function Capture() {
    captured.maps.push(actual.useMap());
    return null;
  }
  return {
    ...actual,
    MapContainer: ({ children, ...props }: React.ComponentProps<typeof actual.MapContainer>) =>
      React.createElement(actual.MapContainer, props, React.createElement(Capture), children),
  };
});

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  api: { mediaProvenance: vi.fn() },
}));

afterEach(() => {
  captured.maps.length = 0;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function renderMap(data: ProvenanceResponse, { inert = false, onPick = vi.fn() } = {}) {
  const tree = (d: ProvenanceResponse) => <EvidenceMap data={d} inert={inert} onPick={onPick} />;
  const view = render(tree(data));
  const map = captured.maps[captured.maps.length - 1];
  const layers = <T extends L.Layer>(type: new (...args: never[]) => T) => {
    const found: T[] = [];
    map.eachLayer((layer) => {
      if (layer instanceof type) found.push(layer);
    });
    return found;
  };
  return { ...view, map, layers, onPick, update: (d: ProvenanceResponse) => view.rerender(tree(d)) };
}

// Gives the map a measured size, as a browser would, and tells Leaflet to read it.
function sizeMap(map: L.Map, width = 600, height = 400) {
  const container = map.getContainer();
  Object.defineProperty(container, "clientWidth", { value: width, configurable: true });
  Object.defineProperty(container, "clientHeight", { value: height, configurable: true });
  map.invalidateSize({ animate: false });
}

function click(map: L.Map, at: L.Point) {
  map.fire("click", { latlng: map.containerPointToLatLng(at), containerPoint: at });
}

const pixel = (map: L.Map, lat: number, lng: number) => map.latLngToContainerPoint([lat, lng]);

describe("EvidenceMap", () => {
  it("isolates the map's stacking and opens the OSM credit in a new tab", () => {
    const { container } = renderMap(makeResponse("a"));
    expect(container.querySelector(".leaflet-container")).toHaveClass("isolate");
    const credit = container.querySelector<HTMLAnchorElement>('.leaflet-control-attribution a[href*="openstreetmap.org"]');
    expect(credit).toHaveAttribute("target", "_blank");
    expect(credit).toHaveAttribute("rel", "noopener noreferrer");
  });

  // A click is the only way to open a point card, and it focuses a keyboard-enabled map, whose
  // listener on document then stops every arrow before it reaches the view's on window.
  it("leaves the arrows to the view after a click on the map", async () => {
    vi.mocked(api.mediaProvenance).mockResolvedValue(makeResponse("a"));
    const onNext = vi.fn();
    render(
      <EvidenceView mediaId="a" isOwn onClose={vi.fn()} onNext={onNext} onLocationSaved={vi.fn()} onRematched={vi.fn()} />
    );
    await screen.findByText("a.jpg");
    const arrow = () => fireEvent.keyDown(document.activeElement ?? document.body, { key: "ArrowRight", keyCode: 39 });

    arrow();
    expect(onNext).toHaveBeenCalledTimes(1);

    fireEvent.mouseDown(document.querySelector(".leaflet-container")!);
    arrow();
    expect(onNext).toHaveBeenCalledTimes(2);
  });

  it("puts a green marker on the stored position, lifted over its neighbours", () => {
    const { layers } = renderMap(makeResponse("a", { item: { lat: 11, lng: 21 } }));
    const markers = layers(L.Marker);
    expect(markers).toHaveLength(1);
    expect(markers[0].getLatLng()).toEqual(L.latLng(11, 21));
    expect(markers[0].options.zIndexOffset).toBe(TOP_MARKER_Z_INDEX_OFFSET);
    expect((markers[0].options.icon as L.DivIcon).options.html).toContain("#22c55e");
  });

  it("keeps the marker out of the way of a click on a point beneath it", () => {
    const { layers } = renderMap(makeResponse("a"));
    expect(layers(L.Marker)[0].options.interactive).toBe(false);
  });

  it("falls back to the estimate's position, then to no marker", () => {
    const pending = makeResponse("a", {
      item: { lat: null, lng: null },
      placement: { state: "pending", now: makeEstimate({ lat: 12, lng: 22 }) },
    });
    expect(renderMap(pending).layers(L.Marker)[0].getLatLng()).toEqual(L.latLng(12, 22));

    const none = makeResponse("b", { item: { lat: null, lng: null }, placement: { state: "no_bracket", now: null } });
    expect(renderMap(none).layers(L.Marker)).toHaveLength(0);
  });

  it("draws the bracket points and the straight segment between them, thick", () => {
    const { layers } = renderMap(makeResponse("a"));
    const lines = layers(L.Polyline).filter((line) => !(line instanceof L.CircleMarker));
    const segment = lines.find((line) => line.options.weight === 6)!;
    expect((segment.getLatLngs() as L.LatLng[]).map((p) => [p.lat, p.lng])).toEqual([
      [10, 20],
      [10.2, 20.2],
    ]);
    expect(layers(L.CircleMarker)).toHaveLength(2);
    // A click must reach the map, which then picks the nearest point of everything drawn.
    expect(segment.options.interactive).toBe(false);
    for (const dot of layers(L.CircleMarker)) expect(dot.options.interactive).toBe(false);
  });

  it("draws one point and no segment for an exact bracket", () => {
    const row = makeBracket({ gap_s: 0 });
    const exact = makeEstimate(
      { before: row, after: row, confidence: 1 },
      { shape: "exact", pair_gap_s: 0, pair_km: 0, t: null, confidence: 1, implied_kmh: null, distance_factor: 1 }
    );
    const { layers } = renderMap(makeResponse("a", { placement: { now: exact } }));
    expect(layers(L.CircleMarker)).toHaveLength(1);
    expect(layers(L.Polyline).filter((line) => !(line instanceof L.CircleMarker))).toHaveLength(0);
  });

  it("draws no segment for a one-sided bracket", () => {
    const oneSided = makeEstimate({ after: null }, { shape: "one_sided", pair_gap_s: null, pair_km: null, t: null });
    const { layers } = renderMap(makeResponse("a", { placement: { now: oneSided } }));
    expect(layers(L.CircleMarker)).toHaveLength(1);
    expect(layers(L.Polyline).filter((line) => !(line instanceof L.CircleMarker))).toHaveLength(0);
  });

  it("labels the segment only in the stale state", () => {
    const fresh = renderMap(makeResponse("a"));
    expect(fresh.layers(L.Tooltip)).toHaveLength(0);

    const stale = renderMap(makeResponse("b", { placement: { state: "stale", now: makeEstimate({ delta_m: 500 }) } }));
    expect(stale.layers(L.Tooltip)).toHaveLength(1);
    expect(stale.container).toHaveTextContent("what a re-match would use");
  });

  it("keeps the item marker out of the tab order, since it does nothing", () => {
    const { layers } = renderMap(makeResponse("a"));
    const element = layers(L.Marker)[0].getElement()!;
    expect(element).not.toHaveAttribute("tabindex");
    expect(element).not.toHaveAttribute("role");
  });

  it("moves the segment's label with the segment when the item changes", () => {
    const at = (lat: number, lng: number) =>
      makeResponse("a", {
        placement: {
          state: "stale",
          now: makeEstimate({
            delta_m: 500,
            before: makeBracket({ t: "2000-01-01T00:00:00Z", lat, lng }),
            after: makeBracket({ t: "2000-01-01T00:03:00Z", lat: lat + 0.2, lng: lng + 0.2 }),
          }),
        },
      });
    const { layers, update } = renderMap(at(10, 20));

    update(at(12, 22));

    const [tooltip] = layers(L.Tooltip);
    expect(layers(L.Tooltip)).toHaveLength(1);
    expect(tooltip.getLatLng()!.lat).toBeCloseTo(12.1, 1);
    expect(tooltip.getLatLng()!.lng).toBeCloseTo(22.1, 1);
  });

  describe("each format on its own", () => {
    const thinLines = (layers: ReturnType<typeof renderMap>["layers"]) =>
      layers(L.Polyline).filter((line) => !(line instanceof L.CircleMarker) && line.options.dashArray === "4 6");
    const diamonds = (layers: ReturnType<typeof renderMap>["layers"]) =>
      layers(L.Marker).filter((marker) => (marker.options.icon as L.DivIcon).options.html?.toString().includes("rotate(45deg)"));
    const oneSided = makeOtherFormat({
      format: "gpx",
      before: makeOtherPoint({ source: "gpx:b.gpx", lat: 10.5, lng: 20.5 }),
      after: null,
      alone: { lat: 10.5, lng: 20.5, shape: "one_sided", confidence: 0.4, distance_m: 900 },
    });

    it("draws its points in its colour, a thin line between them and a diamond where it alone would put the item", () => {
      const { layers } = renderMap(
        makeResponse("a", { placement: { now: null, state: "manual", others: [makeOtherFormat(), oneSided] } })
      );

      const dots = layers(L.CircleMarker);
      expect(dots.map((dot) => [dot.getLatLng().lat, dot.options.fillColor])).toEqual(
        expect.arrayContaining([
          [10.3, "#7c3aed"],
          [10.4, "#7c3aed"],
          [10.5, "#2563eb"],
        ])
      );
      expect(dots).toHaveLength(3);
      const [line] = thinLines(layers);
      expect((line.getLatLngs() as L.LatLng[]).map((p) => [p.lat, p.lng])).toEqual([
        [10.3, 20.3],
        [10.4, 20.4],
      ]);
      expect(thinLines(layers)).toHaveLength(1);
      const [diamond] = diamonds(layers);
      expect(diamonds(layers)).toHaveLength(1);
      expect(diamond.getLatLng()).toEqual(L.latLng(10.33, 20.33));
      expect((diamond.options.icon as L.DivIcon).options.html).toContain("#7c3aed");
    });

    it("draws nothing twice: the estimate's own points, their pair and the item's own point", () => {
      const used = makeOtherFormat({
        before: makeOtherPoint({ t: "2000-01-01T00:00:00Z", lat: 10, lng: 20, used: true }),
        after: makeOtherPoint({ t: "2000-01-01T00:03:00Z", lat: 10.2, lng: 20.2, used: true }),
        used: true,
      });
      const own = makeOtherPoint({ source: "exif", t: "2000-01-01T00:01:00Z", lat: 10.1, lng: 20.1, self: true });
      const self = makeOtherFormat({ format: "exif", before: own, after: own, alone: { lat: 10.1, lng: 20.1, shape: "exact", confidence: 1, distance_m: 0 } });
      const { layers } = renderMap(makeResponse("a", { placement: { others: [used, self] } }));

      expect(layers(L.CircleMarker)).toHaveLength(2); // the bracket's
      expect(thinLines(layers)).toHaveLength(0);
      expect(diamonds(layers)).toHaveLength(0);
    });

    it("keeps the diamonds out of the way of a click and out of the tab order", () => {
      const { layers } = renderMap(makeResponse("a", { placement: { others: [makeOtherFormat()] } }));
      const [diamond] = diamonds(layers);
      expect(diamond.options.interactive).toBe(false);
      expect(diamond.getElement()).not.toHaveAttribute("tabindex");
    });
  });

  it("draws the context track", () => {
    const { layers } = renderMap(
      makeResponse("a", {
        context: {
          sources: [{ label: "gpx:a.gpx", format: "gpx", kind: "recorded" }],
          points: [
            { t: "2000-01-01T00:00:00Z", lat: 10, lng: 20, s: 0, acc: null, alt: null },
            { t: "2000-01-01T00:00:10Z", lat: 10.01, lng: 20, s: 0, acc: null, alt: null },
          ],
        },
      })
    );
    expect(layers(L.Polyline).some((line) => line.options.pane === "track")).toBe(true);
  });

  describe("fit", () => {
    const boundsOf = (spy: ReturnType<typeof vi.spyOn>) => spy.mock.calls[0][0] as L.LatLngBounds;

    it("waits for a size, because fitBounds on a 0×0 map sets the zoom to infinity", () => {
      const fit = vi.spyOn(L.Map.prototype, "fitBounds");
      const { map } = renderMap(makeResponse("a"));
      expect(fit).not.toHaveBeenCalled();

      sizeMap(map);
      expect(fit).toHaveBeenCalledTimes(1);
      expect(Number.isFinite(map.getZoom())).toBe(true);
    });

    it("covers the marker, the bracket points and every format's own points", () => {
      const fit = vi.spyOn(L.Map.prototype, "fitBounds");
      const { map } = renderMap(
        makeResponse("a", {
          placement: { others: [makeOtherFormat({ after: makeOtherPoint({ t: "2000-01-01T00:02:00Z", lat: 40, lng: 50 }) })] },
        })
      );
      sizeMap(map);
      const bounds = boundsOf(fit);
      expect(bounds.contains([40, 50])).toBe(true);
      expect(bounds.contains([10.1, 20.1])).toBe(true);
      expect(bounds.contains([10.2, 20.2])).toBe(true);
    });

    it("uses the nearest outside points when nothing else is there", () => {
      const fit = vi.spyOn(L.Map.prototype, "fitBounds");
      const { map } = renderMap(
        makeResponse("a", {
          item: { lat: null, lng: null },
          placement: {
            state: "no_bracket",
            now: null,
            nearest_outside: {
              before: { source: "gpx:a.gpx", format: "gpx", t: "2000-01-01T00:00:00Z", lat: 30, lng: 40, gap_s: 7200 },
              after: null,
            },
          },
          context: {
            sources: [{ label: "gpx:a.gpx", format: "gpx", kind: "recorded" }],
            points: [{ t: "2000-01-01T00:00:00Z", lat: 70, lng: 80, s: 0, acc: null, alt: null }],
          },
        })
      );
      sizeMap(map);
      const bounds = boundsOf(fit);
      expect(bounds.contains([30, 40])).toBe(true);
      expect(bounds.contains([70, 80])).toBe(false);
    });

    it("then the context track", () => {
      const fit = vi.spyOn(L.Map.prototype, "fitBounds");
      const { map } = renderMap(
        makeResponse("a", {
          item: { lat: null, lng: null },
          placement: { state: "no_bracket", now: null },
          context: {
            sources: [{ label: "gpx:a.gpx", format: "gpx", kind: "recorded" }],
            points: [{ t: "2000-01-01T00:00:00Z", lat: 70, lng: 80, s: 0, acc: null, alt: null }],
          },
        })
      );
      sizeMap(map);
      expect(boundsOf(fit).contains([70, 80])).toBe(true);
    });

    it("shows the world with nothing to fit", () => {
      const fit = vi.spyOn(L.Map.prototype, "fitBounds");
      const { map, update } = renderMap(makeResponse("a"));
      sizeMap(map);
      expect(fit).toHaveBeenCalledTimes(1);

      update(makeResponse("b", { item: { lat: null, lng: null }, placement: { state: "no_time", now: null } }));
      expect(fit).toHaveBeenCalledTimes(1);
      expect(map.getZoom()).toBe(2);
      expect(map.getCenter()).toEqual(L.latLng(20, 0));
    });

    it("refits when the item changes", () => {
      const fit = vi.spyOn(L.Map.prototype, "fitBounds");
      const { map, update } = renderMap(makeResponse("a"));
      sizeMap(map);
      expect(fit).toHaveBeenCalledTimes(1);
      update(makeResponse("b", { item: { lat: 50, lng: 60 } }));
      expect(fit).toHaveBeenCalledTimes(2);
    });
  });

  describe("picking a point", () => {
    const withTrack = () =>
      makeResponse("a", {
        context: {
          sources: [{ label: "gpx:track.gpx", format: "gpx", kind: "recorded" }],
          points: [{ t: "2000-01-01T00:00:40Z", lat: 10.15, lng: 20.15, s: 0, acc: 7, alt: 200 }],
        },
      });

    it("opens the bracket point under the click, with what the response says about it", () => {
      const { map, onPick } = renderMap(withTrack());
      click(map, pixel(map, 10, 20));
      expect(onPick).toHaveBeenCalledTimes(1);
      expect(onPick.mock.calls[0][0]).toMatchObject({ format: "gpx", gapS: 60, accuracyM: 5, kind: "Log" });
    });

    it("gives a context point only its format-level kind", () => {
      const { map, onPick } = renderMap(
        makeResponse("a", {
          placement: { now: null, state: "manual" },
          item: { lat: 40, lng: 50 },
          context: {
            sources: [{ label: "exif", format: "exif", kind: "derived" }],
            points: [{ t: "2000-01-01T00:01:30Z", lat: 10.15, lng: 20.15, s: 0, acc: null, alt: null }],
          },
        })
      );
      click(map, pixel(map, 10.15, 20.15));
      expect(onPick.mock.calls[0][0]).toMatchObject({ kind: "another photo or pin", gapS: 30 });
    });

    it("opens a format's own point, with its side of the capture time", () => {
      const { map, onPick } = renderMap(makeResponse("a", { placement: { others: [makeOtherFormat()] } }));
      click(map, pixel(map, 10.4, 20.4));
      expect(onPick.mock.calls[0][0]).toMatchObject({ format: "nmea", gapS: 60, side: "after", kind: "log" });
    });

    it("prefers the bracket over the same row in the context track", () => {
      const { map, onPick } = renderMap(
        makeResponse("a", {
          context: {
            sources: [{ label: "gpx:walk-a.gpx", format: "gpx", kind: "recorded" }],
            points: [{ t: "2000-01-01T00:00:00Z", lat: 10, lng: 20, s: 0, acc: null, alt: null }],
          },
        })
      );
      click(map, pixel(map, 10, 20));
      expect(onPick.mock.calls[0][0].kind).toBe("Log");
      expect(onPick.mock.calls[0][0].accuracyM).toBe(5);
    });

    it("reports a click away from any point", () => {
      const { map, onPick } = renderMap(withTrack());
      click(map, pixel(map, 10, 20).add([POINT_HIT_RADIUS_PX.mouse + 5, 0]));
      expect(onPick).toHaveBeenCalledWith(null);
    });

    it("uses the wider radius on a coarse pointer", () => {
      vi.stubGlobal("matchMedia", (query: string) => ({ matches: query === "(pointer: coarse)" }));
      const { map, onPick } = renderMap(withTrack());
      click(map, pixel(map, 10, 20).add([POINT_HIT_RADIUS_PX.coarse - 1, 0]));
      expect(onPick.mock.calls[0][0]).toMatchObject({ gapS: 60 });
    });

    it("ignores clicks while it shows a previous item", () => {
      const { map, onPick } = renderMap(withTrack(), { inert: true });
      click(map, pixel(map, 10, 20));
      expect(onPick).not.toHaveBeenCalled();
    });
  });
});
