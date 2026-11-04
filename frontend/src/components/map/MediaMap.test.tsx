import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, it, expect, vi } from "vitest";
import L from "leaflet";
import "leaflet.markercluster";
import type { MediaItem } from "../../types";
import { MediaMap } from "./MediaMap";

// Real Leaflet and markercluster: the point of MediaMap is how few cluster operations an items
// change costs. jsdom gives the map no size, so no marker is ever drawn; the group's layers and
// the spied bulk calls show what MediaMap did.

function makeItem(id: string, overrides: Partial<MediaItem> = {}): MediaItem {
  return {
    id,
    filename: `photo-${id}.jpg`,
    media_type: "photo",
    lat: 40 + Number(id),
    lng: 10 + Number(id),
    gps_confidence: null,
    gps_source: null,
    taken_at: null,
    storage_key: `key-${id}`,
    trip_id: null,
    owner_id: "u1",
    owner_name: "Alice",
    ...overrides,
  };
}

const proto = L.MarkerClusterGroup.prototype as L.MarkerClusterGroup & {
  _moveChild: (...args: unknown[]) => void;
};

let addLayers: ReturnType<typeof vi.spyOn>;
let removeLayers: ReturnType<typeof vi.spyOn>;
let clearLayers: ReturnType<typeof vi.spyOn>;
let moveChild: ReturnType<typeof vi.spyOn>;
let createGroup: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  addLayers = vi.spyOn(proto, "addLayers");
  removeLayers = vi.spyOn(proto, "removeLayers");
  clearLayers = vi.spyOn(proto, "clearLayers");
  moveChild = vi.spyOn(proto, "_moveChild");
  createGroup = vi.spyOn(L, "markerClusterGroup");
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const group = () => createGroup.mock.results[0].value as L.MarkerClusterGroup;
// markercluster's own onAdd calls addLayers with what it queued (nothing, here): leave those out.
const bulkAdds = () => addLayers.mock.calls.filter((c: unknown[]) => (c[0] as L.Layer[]).length > 0);
const markers = () => group().getLayers() as L.Marker[];

function popupText(marker: L.Marker): string {
  const content = marker.getPopup()!.getContent() as (layer: L.Layer) => HTMLElement;
  return content(marker).textContent ?? "";
}

function markerFor(id: string): L.Marker {
  const marker = markers().find((m) => popupText(m).startsWith(`photo-${id}.jpg`));
  expect(marker, `marker for ${id}`).toBeDefined();
  return marker!;
}

describe("MediaMap", () => {
  it("renders without crashing when items is empty", () => {
    render(<MediaMap items={[]} />);
    expect(markers()).toHaveLength(0);
    expect(bulkAdds()).toHaveLength(0);
  });

  it("adds one marker per located item in a single bulk call", () => {
    const items = [
      makeItem("1"),
      makeItem("2"),
      makeItem("3", { lat: null, lng: null }),
      makeItem("4", { lng: null }),
    ];
    render(<MediaMap items={items} />);

    expect(bulkAdds()).toHaveLength(1);
    expect(bulkAdds()[0][0]).toHaveLength(2);
    expect(markers()).toHaveLength(2);
    expect(markerFor("1").getLatLng()).toEqual(L.latLng(41, 11));
  });

  it("touches no marker when re-rendered with the same items", () => {
    const items = [makeItem("1"), makeItem("2")];
    const { rerender } = render(<MediaMap items={items} />);

    rerender(<MediaMap items={items} />);
    rerender(<MediaMap items={[...items]} />);

    expect(bulkAdds()).toHaveLength(1);
    expect(removeLayers).not.toHaveBeenCalled();
    expect(moveChild).not.toHaveBeenCalled();
  });

  it("adds only the new markers when another page arrives", () => {
    const page1 = [makeItem("1"), makeItem("2")];
    const { rerender } = render(<MediaMap items={page1} />);

    rerender(<MediaMap items={[...page1, makeItem("3")]} />);

    expect(bulkAdds()).toHaveLength(2);
    expect(bulkAdds()[1][0]).toHaveLength(1);
    expect(moveChild).not.toHaveBeenCalled();
    expect(markers()).toHaveLength(3);
  });

  it("removes the markers of removed items, and clears the group when none are left", () => {
    const { rerender } = render(<MediaMap items={[makeItem("1"), makeItem("2")]} />);

    rerender(<MediaMap items={[makeItem("2")]} />);
    expect(removeLayers).toHaveBeenCalledTimes(1);
    expect(removeLayers.mock.calls[0][0]).toHaveLength(1);
    expect(markers()).toHaveLength(1);
    markerFor("2");

    rerender(<MediaMap items={[]} />);
    expect(clearLayers).toHaveBeenCalledTimes(1);
    expect(markers()).toHaveLength(0);
  });

  it("moves a marker only when its item's location changed", () => {
    const item = makeItem("1");
    const { rerender } = render(<MediaMap items={[item]} />);
    const marker = markerFor("1");

    rerender(<MediaMap items={[{ ...item, filename: "photo-1.jpg (renamed)" }]} />);
    expect(moveChild).not.toHaveBeenCalled();
    expect(popupText(marker)).toBe("photo-1.jpg (renamed)");

    rerender(<MediaMap items={[{ ...item, lat: 47.5, lng: 19.05 }]} />);
    expect(moveChild).toHaveBeenCalledTimes(1);
    expect(marker.getLatLng()).toEqual(L.latLng(47.5, 19.05));
    expect(bulkAdds()).toHaveLength(1);
  });

  it("shares one icon per look and swaps it when the look changes", () => {
    const { rerender } = render(
      <MediaMap items={[makeItem("1"), makeItem("2"), makeItem("3", { media_type: "photo_360" })]} />,
    );
    expect(markerFor("1").options.icon).toBe(markerFor("2").options.icon);
    expect(markerFor("3").options.icon).not.toBe(markerFor("1").options.icon);

    const before = markerFor("1").options.icon;
    rerender(<MediaMap items={[makeItem("1", { gps_confidence: 0.5 }), makeItem("2"), makeItem("3", { media_type: "photo_360" })]} />);
    expect(markerFor("1").options.icon).not.toBe(before);
    expect(markerFor("2").options.icon).toBe(before);
  });

  it("shows the filename and date in the popup without interpreting HTML", () => {
    render(<MediaMap items={[makeItem("1", { filename: "photo-1.jpg<img src=x>", taken_at: "2024-05-02T10:00:00Z" })]} />);
    const content = (markers()[0].getPopup()!.getContent() as (l: L.Layer) => HTMLElement)(markers()[0]);

    expect(content.querySelector("img")).toBeNull();
    expect(content.querySelector(".font-medium")!.textContent).toBe("photo-1.jpg<img src=x>");
    expect(content.textContent).toContain(new Date("2024-05-02T10:00:00Z").toLocaleDateString());
  });

  it("selects a clicked marker on its own, or among the markers of its spider", () => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    const onSelect = vi.fn();
    const items = [makeItem("1"), makeItem("2"), makeItem("3")];
    render(<MediaMap items={items} onSelect={onSelect} />);
    const click = (id: string) => group().fire("click", { propagatedFrom: markerFor(id) });

    click("1");
    expect(onSelect).toHaveBeenLastCalledWith(items[0], [items[0]]);

    group().fire("spiderfied", { cluster: null, markers: [markerFor("2"), markerFor("3")] });
    click("3");
    expect(onSelect).toHaveBeenLastCalledWith(items[2], [items[1], items[2]]);

    group().fire("unspiderfied", { cluster: null, markers: [] });
    vi.runAllTimers();
    click("3");
    expect(onSelect).toHaveBeenLastCalledWith(items[2], [items[2]]);
  });

  it("calls the latest onSelect", () => {
    const first = vi.fn();
    const second = vi.fn();
    const items = [makeItem("1")];
    const { rerender } = render(<MediaMap items={items} onSelect={first} />);

    rerender(<MediaMap items={items} onSelect={second} />);
    group().fire("click", { propagatedFrom: markerFor("1") });

    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledWith(items[0], [items[0]]);
  });

  // Without isolation Leaflet's controls (z-index 1000) painted over the page's filter panel.
  it("keeps Leaflet's panes and controls in the map's own stacking context", () => {
    const { container } = render(<MediaMap items={[]} />);
    expect(container.querySelector(".leaflet-container")).toHaveClass("isolate");
  });

  it("opens the map credit in a new tab, so a mis-tap does not leave the app", () => {
    const { container } = render(<MediaMap items={[]} />);
    const credit = container.querySelector<HTMLAnchorElement>('.leaflet-control-attribution a[href*="openstreetmap.org"]');
    expect(credit).not.toBeNull();
    expect(credit).toHaveAttribute("target", "_blank");
    expect(credit).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("gives markers a bigger touch target without growing the dot", () => {
    render(<MediaMap items={[makeItem("1"), makeItem("2", { media_type: "video_360" })]} />);

    for (const [id, size] of [["1", 12], ["2", 16]] as const) {
      const icon = markerFor(id).options.icon as L.DivIcon;
      expect(icon.options.className).toBe("touch:before:absolute touch:before:-inset-3");
      expect(icon.options.iconSize).toEqual([size, size]);
      expect(icon.options.html).toContain(`width:${size}px;height:${size}px`);
    }
  });

  it("gives clusters a 44 px touch target", () => {
    render(<MediaMap items={[]} />);
    const options = createGroup.mock.calls[0][0] as L.MarkerClusterGroupOptions;
    const icon = options.iconCreateFunction!({ getChildCount: () => 7 } as L.MarkerCluster) as L.DivIcon;

    expect(icon.options.className).toBe("touch:before:absolute touch:before:-inset-1");
    expect(icon.options.iconSize).toEqual([36, 36]);
    expect(icon.options.html).toContain(">7</div>");
  });

  // Their 44 px touch size comes from index.css (tailwind.config.test.ts checks the rule).
  it("puts the zoom buttons at the bottom left", () => {
    const { container } = render(<MediaMap items={[]} />);
    const zoom = container.querySelector(".leaflet-bottom.leaflet-left .leaflet-control-zoom");

    expect(zoom).not.toBeNull();
    expect(zoom!.querySelectorAll("a.leaflet-control-zoom-in, a.leaflet-control-zoom-out")).toHaveLength(2);
  });

  // removeLayers skips markers that a chunked addLayers has not reached yet, and the batch then
  // adds them anyway: MediaMap must take them out again.
  it.each([
    ["some items", [makeItem("2")], 1],
    ["every item", [], 0],
  ])("removes markers that a chunked add was still processing (%s removed)", (_, after, left) => {
    vi.useFakeTimers({ toFake: ["setTimeout"] });
    // markercluster times its chunks with Date#getTime: a jump makes the first chunk stop at once.
    let now = 0;
    let slow = true;
    vi.spyOn(Date.prototype, "getTime").mockImplementation(() => (slow ? (now += 1000) : now));

    const { rerender } = render(<MediaMap items={[makeItem("1"), makeItem("2")]} />);
    expect(markers()).toHaveLength(0); // still queued

    slow = false;
    rerender(<MediaMap items={after} />);
    vi.runAllTimers();

    expect(markers()).toHaveLength(left);
    if (left > 0) markerFor("2");
  });
});
