import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import L from "leaflet";
import { useMap, useMapEvents } from "react-leaflet";
import { MiniMapOverlay, type MiniMapSize } from "./MiniMapOverlay";

// Real Leaflet in jsdom: the map has no size, so nothing is drawn, but the map instance, its
// events and its container are real. Sizes are read from data-size, since jsdom has no CSS.

const maps = vi.hoisted(() => ({ current: null as L.Map | null }));

function CaptureMap() {
  maps.current = useMap();
  return null;
}

function ClickSpy({ onClick }: { onClick: () => void }) {
  useMapEvents({ click: onClick });
  return null;
}

function renderMap(props: Partial<Parameters<typeof MiniMapOverlay>[0]> = {}) {
  const result = render(
    <MiniMapOverlay {...props}>
      <CaptureMap />
      {props.children}
    </MiniMapOverlay>,
  );
  const root = result.container.firstElementChild as HTMLElement;
  return { ...result, root };
}

const size = (root: HTMLElement) => root.dataset.size as MiniMapSize;
// The rounded box that holds the map and its corner buttons.
const box = (root: HTMLElement) => root.querySelector(".leaflet-container")!.parentElement!;
const ZOOM_HIDDEN_ON_TOUCH = "touch:[&_.leaflet-control-zoom]:hidden";
const button = (name: string) => screen.getByRole("button", { name });
const mouse = { pointerType: "mouse" };

beforeEach(() => {
  maps.current = null;
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("MiniMapOverlay", () => {
  it("starts small with enlarge and hide buttons, and an isolated map", () => {
    const { root } = renderMap();
    expect(size(root)).toBe("small");
    expect(button("Enlarge map")).toBeVisible();
    expect(button("Hide map")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Show map" })).not.toBeInTheDocument();
    expect(root.querySelector(".leaflet-container")).toHaveClass("isolate");
    expect(maps.current?.getCenter()).toEqual(L.latLng(20, 0));
    expect(maps.current?.getZoom()).toBe(2);
  });

  it("uses the given center and zoom", () => {
    renderMap({ center: [47.5, 19], zoom: 6 });
    expect(maps.current?.getCenter()).toEqual(L.latLng(47.5, 19));
    expect(maps.current?.getZoom()).toBe(6);
  });

  it("switches between small and large with the toggle button", () => {
    const onSizeChange = vi.fn();
    const { root } = renderMap({ onSizeChange });

    fireEvent.click(button("Enlarge map"));
    expect(size(root)).toBe("large");
    expect(screen.queryByRole("button", { name: "Enlarge map" })).not.toBeInTheDocument();

    fireEvent.click(button("Shrink map"));
    expect(size(root)).toBe("small");
    expect(onSizeChange.mock.calls).toEqual([["large"], ["small"]]);
  });

  it("hides behind a Map pill and comes back at the size it had", () => {
    const onSizeChange = vi.fn();
    const { root } = renderMap({ onSizeChange });
    fireEvent.click(button("Enlarge map"));

    fireEvent.click(button("Hide map"));
    expect(size(root)).toBe("hidden");
    // The corner buttons hide with the map.
    expect(screen.queryByRole("button", { name: "Shrink map" })).not.toBeInTheDocument();
    expect(root.querySelector(".leaflet-container")).not.toBeVisible();
    expect(button("Show map")).toHaveTextContent("Map");
    expect(button("Show map")).toHaveFocus();

    fireEvent.click(button("Show map"));
    expect(size(root)).toBe("large");
    expect(root.querySelector(".leaflet-container")).toBeVisible();
    expect(button("Hide map")).toHaveFocus();
    expect(onSizeChange.mock.calls).toEqual([["large"], ["hidden"], ["large"]]);
  });

  it("keeps the map, its view and its children while hidden", () => {
    renderMap({ children: <span data-testid="marker">pin</span> });
    const map = maps.current!;
    act(() => {
      map.setView([60, 25], 7);
    });

    fireEvent.click(button("Hide map"));
    fireEvent.click(button("Show map"));

    expect(maps.current).toBe(map);
    expect(map.getZoom()).toBe(7);
    expect(screen.getByTestId("marker")).toBeInTheDocument();
  });

  it("keeps the footer visible while the map is hidden", () => {
    renderMap({ footer: <button>Submit</button> });
    expect(button("Submit")).toBeVisible();

    fireEvent.click(button("Hide map"));
    expect(button("Submit")).toBeVisible();
    expect(button("Show map")).toBeVisible();
  });

  it("renders its children inside the map", () => {
    const { root } = renderMap({ children: <span data-testid="child">inside</span> });
    expect(root.querySelector(".leaflet-container")).toContainElement(screen.getByTestId("child"));
    expect(maps.current).toBeInstanceOf(L.Map);
  });

  it.each<[MiniMapSize, string]>([
    ["large", "Shrink map"],
    ["hidden", "Show map"],
  ])("starts %s with defaultSize", (defaultSize, control) => {
    const { root } = renderMap({ defaultSize });
    expect(size(root)).toBe(defaultSize);
    expect(button(control)).toBeInTheDocument();
  });

  it("shows a map that started hidden at the small size", () => {
    const { root } = renderMap({ defaultSize: "hidden" });
    fireEvent.click(button("Show map"));
    expect(size(root)).toBe("small");
  });

  it("merges className onto the outer column", () => {
    const { root } = renderMap({ className: "bottom-20" });
    expect(root).toHaveClass("bottom-20", "right-4");
    expect(root).not.toHaveClass("bottom-4");
  });

  describe("hover", () => {
    it("enlarges under a mouse and returns to the previous size on leave", () => {
      const onSizeChange = vi.fn();
      const { root } = renderMap({ onSizeChange });

      fireEvent.pointerEnter(root, mouse);
      expect(size(root)).toBe("large");
      // The toggle follows the chosen size: the map is only borrowed, so it offers to keep it.
      expect(button("Keep map large")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Shrink map" })).not.toBeInTheDocument();

      fireEvent.pointerLeave(root, mouse);
      expect(size(root)).toBe("small");
      expect(button("Enlarge map")).toBeInTheDocument();
      // Hover is transient: nothing to remember.
      expect(onSizeChange).not.toHaveBeenCalled();
    });

    it("stays large on leave when large was chosen, and offers to shrink while hovered", () => {
      const { root } = renderMap({ defaultSize: "large" });
      fireEvent.pointerEnter(root, mouse);
      expect(button("Shrink map")).toBeInTheDocument();
      fireEvent.pointerLeave(root, mouse);
      expect(size(root)).toBe("large");
    });

    it.each(["touch", "pen"])("does not enlarge on a %s pointer", (pointerType) => {
      const { root } = renderMap();
      fireEvent.pointerEnter(root, { pointerType });
      expect(size(root)).toBe("small");
      fireEvent.pointerLeave(root, { pointerType });
      expect(size(root)).toBe("small");
    });

    it("keeps a size chosen by tap when a pointer leaves", () => {
      const { root } = renderMap();
      fireEvent.pointerEnter(root, { pointerType: "touch" });
      fireEvent.click(button("Enlarge map"));
      fireEvent.pointerLeave(root, mouse);
      expect(size(root)).toBe("large");
      fireEvent.click(button("Shrink map"));
      expect(size(root)).toBe("small");
    });

    it("waits for the drag to end before shrinking", () => {
      const { root } = renderMap();
      const map = maps.current!;

      fireEvent.pointerEnter(root, mouse);
      act(() => {
        map.fire("dragstart");
      });
      fireEvent.pointerLeave(root, mouse);
      expect(size(root)).toBe("large");

      act(() => {
        map.fire("dragend");
      });
      expect(size(root)).toBe("small");
    });

    it("does not shrink at the end of a drag the mouse stayed inside", () => {
      const { root } = renderMap();
      const map = maps.current!;

      fireEvent.pointerEnter(root, mouse);
      act(() => {
        map.fire("dragstart");
        map.fire("dragend");
      });
      expect(size(root)).toBe("large");
    });

    it("ignores moves that are not drags", () => {
      const { root } = renderMap();
      const map = maps.current!;

      fireEvent.pointerEnter(root, mouse);
      act(() => {
        map.fire("dragstart");
        // A programmatic move (flyTo, invalidateSize) ending during the drag.
        map.fire("movestart");
        map.fire("moveend");
      });
      fireEvent.pointerLeave(root, mouse);
      expect(size(root)).toBe("large");
    });

    it("keeps the hover size with the pin, as a mouse reaches the corner only after enlarging it", () => {
      const onSizeChange = vi.fn();
      const { root } = renderMap({ onSizeChange });

      fireEvent.pointerEnter(root, mouse);
      fireEvent.click(button("Keep map large"));
      expect(size(root)).toBe("large");
      expect(onSizeChange.mock.calls).toEqual([["large"]]);
      expect(button("Shrink map")).toBeInTheDocument();

      fireEvent.pointerLeave(root, mouse);
      expect(size(root)).toBe("large");
    });

    it("shrinks a pinned map from the button, and hovers again after re-entering", () => {
      const onSizeChange = vi.fn();
      const { root } = renderMap({ onSizeChange });
      fireEvent.pointerEnter(root, mouse);
      fireEvent.click(button("Keep map large"));

      fireEvent.click(button("Shrink map"));
      expect(size(root)).toBe("small");
      expect(onSizeChange.mock.calls).toEqual([["large"], ["small"]]);
      // The cursor is still inside: no hover until it comes back.
      expect(button("Enlarge map")).toBeInTheDocument();

      fireEvent.pointerLeave(root, mouse);
      expect(size(root)).toBe("small");
      fireEvent.pointerEnter(root, mouse);
      expect(size(root)).toBe("large");
      expect(button("Keep map large")).toBeInTheDocument();
    });

    it("hides at once from the button while hover-expanded", () => {
      const onSizeChange = vi.fn();
      const { root } = renderMap({ onSizeChange });
      fireEvent.pointerEnter(root, mouse);
      fireEvent.click(button("Hide map"));
      expect(size(root)).toBe("hidden");
      expect(onSizeChange.mock.calls).toEqual([["hidden"]]);

      // Shown again at the chosen size, not the hover size.
      fireEvent.click(button("Show map"));
      expect(size(root)).toBe("small");
    });

    it("does not enlarge while hidden", () => {
      const { root } = renderMap({ footer: <span>footer</span> });
      fireEvent.click(button("Hide map"));
      fireEvent.pointerEnter(root, mouse);
      expect(size(root)).toBe("hidden");

      fireEvent.click(button("Show map"));
      expect(size(root)).toBe("small");
    });
  });

  it("gives the hidden state's Map pill a background of its own", () => {
    const { root } = renderMap({ defaultSize: "hidden" });
    // Over a light photo, white text alone would vanish.
    expect(button("Show map")).toHaveClass("rounded-full", "bg-black/70", "text-white", "touch:min-h-11");
    expect(size(root)).toBe("hidden");
  });

  describe("attribution", () => {
    it("credits OpenStreetMap without the Leaflet prefix, in a link that opens a new tab", () => {
      const { root } = renderMap();
      const attributions = root.querySelectorAll(".leaflet-control-attribution");
      expect(attributions).toHaveLength(1);
      expect(attributions[0]).toHaveTextContent(/^© OpenStreetMap$/);
      expect(root.querySelector('a[href*="leafletjs.com"]')).toBeNull();

      const links = attributions[0].querySelectorAll("a");
      expect(links).toHaveLength(1);
      for (const link of links) {
        // A mis-tap on the small game map must not navigate away from the round.
        expect(link).toHaveAttribute("target", "_blank");
        expect(link).toHaveAttribute("rel", "noopener noreferrer");
      }
    });

    it("keeps the prefix off when a child layer adds its own credit", () => {
      const { root } = renderMap();
      expect(maps.current?.attributionControl.options.prefix).toBe(false);
      act(() => {
        maps.current!.attributionControl.addAttribution("Track data");
      });
      expect(root.querySelector(".leaflet-control-attribution")).toHaveTextContent(
        /^© OpenStreetMap, Track data$|^Track data, © OpenStreetMap$/,
      );
    });
  });

  describe("touch zoom buttons", () => {
    // MapContainer keeps the className it was mounted with, so the class must sit on the box.
    it("hides them on the small box only, following the size", () => {
      const { root } = renderMap();
      expect(root.querySelector(".leaflet-container")).not.toHaveClass(ZOOM_HIDDEN_ON_TOUCH);
      expect(box(root)).toHaveClass(ZOOM_HIDDEN_ON_TOUCH);

      fireEvent.click(button("Enlarge map"));
      expect(box(root)).not.toHaveClass(ZOOM_HIDDEN_ON_TOUCH);

      fireEvent.click(button("Shrink map"));
      expect(box(root)).toHaveClass(ZOOM_HIDDEN_ON_TOUCH);
    });

    it("hides them after shrinking a map that started large", () => {
      const { root } = renderMap({ defaultSize: "large" });
      expect(box(root)).not.toHaveClass(ZOOM_HIDDEN_ON_TOUCH);
      fireEvent.click(button("Shrink map"));
      expect(box(root)).toHaveClass(ZOOM_HIDDEN_ON_TOUCH);
    });
  });

  // jsdom has no layout, so these pin the classes that make the browser fit the map: the root
  // gets a definite height from its parent, and of everything in the column only the box shrinks.
  describe("fitting its parent", () => {
    it("spans the positioned parent from top to bottom, letting pointers through outside the column", () => {
      const { root } = renderMap({ footer: <button>Submit</button>, className: "bottom-16" });
      expect(root).toHaveClass("absolute", "top-4", "bottom-16", "flex-col", "justify-end", "pointer-events-none");
      const column = root.firstElementChild as HTMLElement;
      expect(column).toHaveClass("pointer-events-auto", "flex-col", "max-h-full", "min-h-0");
      expect(column).toContainElement(box(root));
      expect(column).toContainElement(button("Submit"));
    });

    it("caps a statically placed overlay at its parent's height", () => {
      const { root } = renderMap({ className: "pointer-events-auto static ml-auto" });
      expect(root).toHaveClass("static", "max-h-full", "min-h-0", "pointer-events-auto");
      expect(root).not.toHaveClass("absolute");
      expect(root).not.toHaveClass("pointer-events-none");
    });

    it.each<MiniMapSize>(["small", "large"])("lets the %s box shrink, but not the footer", (defaultSize) => {
      const { root } = renderMap({ defaultSize, footer: <span data-testid="footer">date</span> });
      expect(box(root)).toHaveClass("min-h-0", "overflow-hidden");
      expect(box(root)).not.toHaveClass("shrink-0");
      expect(screen.getByTestId("footer").parentElement).toHaveClass("shrink-0");
    });

    it("keeps the Map pill at its size", () => {
      renderMap({ defaultSize: "hidden" });
      expect(button("Show map")).toHaveClass("shrink-0");
    });

    // Without dvh support a dvh class alone sets nothing, and the large box would collapse to
    // 0px. Every dvh size comes after a vh fallback, behind the supports- variant.
    it.each([
      ["small", ["short:w-[52vh]", "short:supports-[height:100dvh]:w-[52dvh]", "short:h-[35vh]", "short:supports-[height:100dvh]:h-[35dvh]"]],
      ["large", ["h-[55vh]", "supports-[height:100dvh]:h-[55dvh]", "short:h-[75vh]", "short:supports-[height:100dvh]:h-[75dvh]"]],
    ] as const)("gives the %s box a vh size where dvh is unsupported", (defaultSize, classes) => {
      const { root } = renderMap({ defaultSize });
      expect(box(root)).toHaveClass(...classes);
      const dvhOnly = Array.from(box(root).classList).filter((c) => c.includes("dvh") && !c.includes("supports-["));
      expect(dvhOnly).toEqual([]);
    });
  });

  it("keeps button clicks away from the map and the page", () => {
    const mapClick = vi.fn();
    const pageClick = vi.fn();
    render(
      <div onClick={pageClick}>
        <MiniMapOverlay>
          <ClickSpy onClick={mapClick} />
        </MiniMapOverlay>
      </div>,
    );

    fireEvent.click(button("Enlarge map"));
    fireEvent.click(button("Shrink map"));
    fireEvent.pointerEnter(button("Enlarge map"), mouse);
    fireEvent.click(button("Keep map large"));
    fireEvent.click(button("Hide map"));
    fireEvent.click(button("Show map"));

    expect(mapClick).not.toHaveBeenCalled();
    expect(pageClick).not.toHaveBeenCalled();
  });

  describe("re-measuring", () => {
    it("invalidates the map size after a size transition", () => {
      vi.useFakeTimers();
      const invalidate = vi.spyOn(L.Map.prototype, "invalidateSize");
      renderMap();
      vi.advanceTimersByTime(400);
      invalidate.mockClear();

      fireEvent.click(button("Enlarge map"));
      vi.advanceTimersByTime(300);
      expect(invalidate).not.toHaveBeenCalled();
      vi.advanceTimersByTime(20);
      expect(invalidate).toHaveBeenCalledTimes(1);
    });

    it("re-measures at once when shown again, and not while hidden", () => {
      vi.useFakeTimers();
      const invalidate = vi.spyOn(L.Map.prototype, "invalidateSize");
      renderMap();
      vi.advanceTimersByTime(400);
      invalidate.mockClear();

      fireEvent.click(button("Hide map"));
      vi.advanceTimersByTime(400);
      expect(invalidate).not.toHaveBeenCalled();

      fireEvent.click(button("Show map"));
      expect(invalidate).toHaveBeenCalledTimes(1);
      vi.advanceTimersByTime(400);
      expect(invalidate).toHaveBeenCalledTimes(2);
    });

    it("re-measures when the box resizes, ignoring a zero size", () => {
      const callbacks: ResizeObserverCallback[] = [];
      const observe = vi.fn();
      const disconnect = vi.fn();
      vi.stubGlobal(
        "ResizeObserver",
        class {
          constructor(cb: ResizeObserverCallback) {
            callbacks.push(cb);
          }
          observe = observe;
          unobserve() {}
          disconnect = disconnect;
        },
      );
      try {
        const invalidate = vi.spyOn(L.Map.prototype, "invalidateSize");
        const { unmount } = renderMap();
        expect(observe).toHaveBeenCalledWith(maps.current!.getContainer());
        invalidate.mockClear();

        const resize = (width: number, height: number) =>
          callbacks[0]([{ contentRect: { width, height } } as ResizeObserverEntry], {} as ResizeObserver);
        resize(0, 0);
        expect(invalidate).not.toHaveBeenCalled();
        resize(300, 200);
        expect(invalidate).toHaveBeenCalledTimes(1);

        unmount();
        expect(disconnect).toHaveBeenCalled();
      } finally {
        vi.unstubAllGlobals();
      }
    });
  });
});
