import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";
import { Map as MapIcon, Maximize2, Minimize2, Pin, X } from "lucide-react";
import { MapContainer, TileLayer, useMap, useMapEvents } from "react-leaflet";
import type { LatLngExpression } from "leaflet";
import "leaflet/dist/leaflet.css";
import { cn } from "@/lib/utils";

export type MiniMapSize = "small" | "large" | "hidden";

// Base classes are phones in portrait, sm: tablets and desktops, short: phones held sideways
// (short: comes last in the CSS, so it wins over sm:). The small phone map keeps 12.5rem of width,
// so a guess can still land beside the 88px of corner buttons. These are the preferred sizes:
// where the overlay's parent is shorter, the box shrinks to fit (see the root's classes).
// Every dvh size has a vh fallback first: without dvh support the dvh class alone sets nothing,
// and the large map would collapse to 0px. The supports- variant keeps the CSS order fixed
// (vh, then dvh, then sm:, then short:), which two plain arbitrary classes would not.
const BOX_SIZE: Record<"small" | "large", string> = {
  small:
    "w-[max(45vw,12.5rem)] h-[max(30vw,8.5rem)] sm:w-72 sm:h-48 " +
    "short:w-[52vh] short:supports-[height:100dvh]:w-[52dvh] short:h-[35vh] short:supports-[height:100dvh]:h-[35dvh]",
  large:
    "w-[calc(100vw-2rem)] h-[55vh] supports-[height:100dvh]:h-[55dvh] sm:w-[36rem] sm:h-96 " +
    "short:w-[min(36rem,60vw)] short:h-[75vh] short:supports-[height:100dvh]:h-[75dvh]",
};

// Matches the transition-all duration-300 of the map box, plus a frame.
const TRANSITION_MS = 310;

function MapSizeSync({
  shown,
  onDragChange,
}: {
  shown: MiniMapSize;
  onDragChange: (dragging: boolean) => void;
}) {
  const map = useMap();
  const wasHidden = useRef(shown === "hidden");

  useEffect(() => {
    if (shown === "hidden") {
      wasHidden.current = true;
      return;
    }
    // While display: none the map may have measured nothing, so re-measure as soon as it shows.
    if (wasHidden.current) {
      wasHidden.current = false;
      map.invalidateSize();
    }
    const id = setTimeout(() => map.invalidateSize(), TRANSITION_MS);
    return () => clearTimeout(id);
  }, [shown, map]);

  // The box also changes size without a prop change: a phone turned sideways switches to the
  // short: sizes, and dvh follows the browser's address bar.
  useEffect(() => {
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const { width, height } = entries[0].contentRect;
      if (width > 0 && height > 0) map.invalidateSize();
    });
    observer.observe(map.getContainer());
    return () => observer.disconnect();
  }, [map]);

  // Drag, not move: invalidateSize and flyTo fire movestart/moveend too, and would end a real
  // drag early.
  useMapEvents({
    dragstart: () => onDragChange(true),
    dragend: () => onDragChange(false),
  });
  return null;
}

// No "Leaflet" prefix, so the credit stays one line on a small phone map (OSM's tile policy
// requires the credit itself to stay visible). Before paint, so the prefix never flashes.
function NoAttributionPrefix() {
  const map = useMap();
  useLayoutEffect(() => {
    map.attributionControl?.setPrefix(false);
  }, [map]);
  return null;
}

function CornerButton({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      className="group flex h-7 w-7 items-center justify-center focus-visible:outline-none touch:h-11 touch:w-11"
      aria-label={label}
      title={label}
      onClick={(e) => {
        // Not the map's click: in the game that would place a guess.
        e.stopPropagation();
        onClick();
      }}
    >
      {/* The target grows to 44px on touch; the visible chip stays small. */}
      <span className="flex h-6 w-6 items-center justify-center rounded-md bg-black/70 text-white/80 backdrop-blur-sm group-hover:text-white group-focus-visible:ring-2 group-focus-visible:ring-white/70 touch:h-8 touch:w-8">
        {children}
      </span>
    </button>
  );
}

interface Props {
  // Rendered inside the MapContainer: markers, click handlers, map controllers.
  children?: ReactNode;
  // Rendered under the map in the same column; it stays when the map is hidden.
  footer?: ReactNode;
  defaultSize?: MiniMapSize;
  center?: LatLngExpression;
  zoom?: number;
  // Merged onto the outer column, e.g. to move it ("bottom-20").
  className?: string;
  // The size chosen with the buttons. Hover expansion is transient and not reported.
  onSizeChange?: (size: MiniMapSize) => void;
}

// A floating map in the bottom-right corner (game guess map, gallery map). A mouse enlarges it
// by hovering and keeps it large with the pin; everyone can enlarge, shrink or hide it with the
// corner buttons.
export function MiniMapOverlay({
  children,
  footer,
  defaultSize = "small",
  center = [20, 0],
  zoom = 2,
  className,
  onSizeChange,
}: Props) {
  const [visibleSize, setVisibleSize] = useState<"small" | "large">(
    defaultSize === "hidden" ? "small" : defaultSize,
  );
  const [hidden, setHidden] = useState(defaultSize === "hidden");
  const [hovered, setHovered] = useState(false);
  const dragging = useRef(false);
  const pointerLeft = useRef(false);
  const pillRef = useRef<HTMLButtonElement>(null);
  const cornerRef = useRef<HTMLDivElement>(null);
  // Set by the hide and show buttons, so focus moves to what replaced the clicked button.
  const moveFocus = useRef(false);

  const boxSize = hovered ? "large" : visibleSize;
  const shown: MiniMapSize = hidden ? "hidden" : boxSize;

  useEffect(() => {
    if (!moveFocus.current) return;
    moveFocus.current = false;
    if (hidden) pillRef.current?.focus();
    else cornerRef.current?.querySelector<HTMLButtonElement>("button:last-child")?.focus();
  }, [hidden]);

  function choose(size: MiniMapSize) {
    const chosen: MiniMapSize = hidden ? "hidden" : visibleSize;
    if (size === "hidden") {
      setHidden(true);
    } else {
      setHidden(false);
      setVisibleSize(size);
    }
    // A click while hover-expanded must take effect now; hover returns on the next enter.
    setHovered(false);
    if (size !== chosen) onSizeChange?.(size);
  }

  function onPointerEnter(e: ReactPointerEvent) {
    // A finger cannot hover: a tap would grow the map and nothing would shrink it again.
    if (e.pointerType !== "mouse" || hidden) return;
    pointerLeft.current = false;
    setHovered(true);
  }

  function onPointerLeave(e: ReactPointerEvent) {
    if (e.pointerType !== "mouse") return;
    pointerLeft.current = true;
    // Shrinking mid-drag would pull the map out from under the cursor; wait for dragend.
    if (!dragging.current) setHovered(false);
  }

  function onDragChange(d: boolean) {
    dragging.current = d;
    if (!d && pointerLeft.current) setHovered(false);
  }

  return (
    // Placed absolutely (the game), the root spans its parent from top-4 to the bottom offset, so
    // its height is exactly the room the overlay has; placed statically (the gallery's bottom row),
    // max-h-full caps it at the row. Either way the map box shrinks to fit and the footer and the
    // buttons stay on screen. The root itself lets pointers through to the media; only the column
    // takes them (and covers the gap, so moving from the map to the footer is no hover leave).
    <div
      className={cn(
        "pointer-events-none absolute bottom-4 right-4 top-4 z-[900] flex max-h-full min-h-0 max-w-[calc(100vw-2rem)] flex-col items-end justify-end",
        className,
      )}
      data-size={shown}
      onPointerEnter={onPointerEnter}
      onPointerLeave={onPointerLeave}
    >
      <div className="pointer-events-auto flex max-h-full min-h-0 max-w-full flex-col items-end gap-2">
        {hidden && (
          <button
            ref={pillRef}
            type="button"
            // A pill of its own: over a light photo, white text alone would vanish.
            className="flex shrink-0 items-center gap-1.5 rounded-full bg-black/70 px-3 py-2 text-sm text-white shadow-lg ring-1 ring-white/20 backdrop-blur-sm hover:bg-black/90 touch:min-h-11"
            aria-label="Show map"
            title="Show map"
            onClick={(e) => {
              e.stopPropagation();
              moveFocus.current = true;
              choose(visibleSize);
            }}
          >
            <MapIcon className="h-4 w-4" aria-hidden="true" />
            Map
          </button>
        )}

        {/* Hidden, not unmounted: the viewport and the markers survive. The hidden attribute
            (display: none) needs a box without a display utility. On a small touch map pinch
            zooms, so the +/- buttons only crowd it: the class sits here, because MapContainer
            keeps the className it was mounted with. */}
        <div
          hidden={hidden}
          className={cn(
            "relative min-h-0 max-w-full overflow-hidden rounded-xl shadow-2xl ring-1 ring-white/20 transition-all duration-300 motion-reduce:transition-none",
            BOX_SIZE[boxSize],
            boxSize === "small" && "touch:[&_.leaflet-control-zoom]:hidden",
          )}
        >
          <MapContainer
            center={center}
            zoom={zoom}
            // isolate keeps Leaflet's z-index 1000 controls under the corner buttons and the
            // page's panels.
            className="isolate h-full w-full"
          >
            <NoAttributionPrefix />
            {/* A new tab: a mis-tap on the credit must not navigate away from a game round or the
                gallery. */}
            <TileLayer
              attribution='&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">OpenStreetMap</a>'
              url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
            />
            <MapSizeSync shown={shown} onDragChange={onDragChange} />
            {children}
          </MapContainer>

          {/* Keyed off the chosen size, not the hover size: a mouse enlarges the map on its way to
              the corner, and must still find a button that keeps it large. */}
          <div ref={cornerRef} className="absolute right-1 top-1 z-10 flex touch:right-0 touch:top-0">
            {visibleSize === "large" ? (
              <CornerButton label="Shrink map" onClick={() => choose("small")}>
                <Minimize2 className="h-3.5 w-3.5" aria-hidden="true" />
              </CornerButton>
            ) : hovered ? (
              <CornerButton label="Keep map large" onClick={() => choose("large")}>
                <Pin className="h-3.5 w-3.5" aria-hidden="true" />
              </CornerButton>
            ) : (
              <CornerButton label="Enlarge map" onClick={() => choose("large")}>
                <Maximize2 className="h-3.5 w-3.5" aria-hidden="true" />
              </CornerButton>
            )}
            <CornerButton
              label="Hide map"
              onClick={() => {
                moveFocus.current = true;
                choose("hidden");
              }}
            >
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </CornerButton>
          </div>
        </div>

        {footer && (
          // At least 18rem on phones, where the small map is narrower than a date field and a
          // button; as wide as the map otherwise.
          <div
            className={cn(
              "max-w-full shrink-0 transition-all duration-300 motion-reduce:transition-none",
              boxSize === "large" && !hidden ? "w-full" : "w-72",
            )}
          >
            {footer}
          </div>
        )}
      </div>
    </div>
  );
}
