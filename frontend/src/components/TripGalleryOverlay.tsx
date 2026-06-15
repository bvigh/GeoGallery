import { useEffect, useRef, useState } from "react";
import { ExternalLink, MapPin, Trash2 } from "lucide-react";
import { MapContainer, TileLayer, Marker, useMap, useMapEvents } from "react-leaflet";
import L from "leaflet";
import type { MediaItem, Trip } from "../types";
import { api } from "../api/client";
import { EditLocationModal } from "./EditLocationModal";
import Panorama360 from "./viewer/Panorama360";
import Video360 from "./viewer/Video360";
import "leaflet/dist/leaflet.css";

// ── Leaflet helper components ────────────────────────────────────────────────

function MapResizer({
  expanded,
  onDragChange,
}: {
  expanded: boolean;
  onDragChange: (dragging: boolean) => void;
}) {
  const map = useMap();
  useEffect(() => {
    const id = setTimeout(() => map.invalidateSize(), 310);
    return () => clearTimeout(id);
  }, [expanded, map]);
  useMapEvents({
    movestart: () => onDragChange(true),
    moveend: () => onDragChange(false),
  });
  return null;
}

function TripMapController({
  items,
  currentItem,
}: {
  items: MediaItem[];
  currentItem: MediaItem;
}) {
  const map = useMap();
  useEffect(() => {
    if (currentItem.lat !== null && currentItem.lng !== null) {
      map.flyTo([currentItem.lat, currentItem.lng], 13, { animate: true, duration: 0.8 });
    } else if (items.length > 0) {
      const bounds = L.latLngBounds(items.map((m) => [m.lat!, m.lng!] as [number, number]));
      map.fitBounds(bounds, { padding: [40, 40] });
    }
  }, [currentItem.id]); // eslint-disable-line react-hooks/exhaustive-deps
  return null;
}

// ── Marker icons ─────────────────────────────────────────────────────────────

const currentMarkerIcon = L.divIcon({
  className: "",
  html: `<div style="width:16px;height:16px;border-radius:50%;background:#22c55e;border:2px solid white;box-shadow:0 2px 6px rgba(0,0,0,0.5)"></div>`,
  iconSize: [16, 16],
  iconAnchor: [8, 8],
});

function markerIcon(item: MediaItem) {
  const color =
    item.gps_confidence !== null && item.gps_confidence < 0.9 ? "#f97316" : "#3b82f6";
  return L.divIcon({
    className: "",
    html: `<div style="width:12px;height:12px;border-radius:50%;background:${color};border:2px solid white;box-shadow:0 1px 3px rgba(0,0,0,.4)"></div>`,
    iconSize: [12, 12],
    iconAnchor: [6, 6],
  });
}

// ── Map overlay sub-component ────────────────────────────────────────────────

interface TripMapOverlayProps {
  itemsWithGps: MediaItem[];
  currentItem: MediaItem;
  minimized: boolean;
  mapHovered: boolean;
  mapDragging: boolean;
  mapMouseLeft: React.MutableRefObject<boolean>;
  onMinimize: () => void;
  onExpand: () => void;
  onHoverChange: (hovered: boolean) => void;
  onDragChange: (dragging: boolean) => void;
  onMarkerClick: (item: MediaItem) => void;
}

function TripMapOverlay({
  itemsWithGps,
  currentItem,
  minimized,
  mapHovered,
  mapDragging,
  mapMouseLeft,
  onMinimize,
  onExpand,
  onHoverChange,
  onDragChange,
  onMarkerClick,
}: TripMapOverlayProps) {
  if (minimized) {
    return (
      <button
        className="absolute bottom-4 right-4 z-[900] rounded-xl bg-black/70 px-3 py-2 text-white text-sm backdrop-blur-sm hover:bg-black/90"
        onClick={onExpand}
      >
        🗺 Map
      </button>
    );
  }

  return (
    <div
      className="absolute bottom-4 right-4 z-[900] transition-all duration-300"
      style={{ width: mapHovered ? "576px" : "288px" }}
      onMouseEnter={() => {
        mapMouseLeft.current = false;
        onHoverChange(true);
      }}
      onMouseLeave={() => {
        mapMouseLeft.current = true;
        if (!mapDragging) onHoverChange(false);
      }}
    >
      <div
        className="relative overflow-hidden rounded-xl shadow-2xl ring-1 ring-white/20 transition-all duration-300"
        style={{ height: mapHovered ? "384px" : "192px" }}
      >
        <button
          className="absolute right-2 top-2 z-[1000] rounded bg-black/70 px-2 py-0.5 text-xs text-white/70 hover:text-white backdrop-blur-sm"
          onClick={onMinimize}
        >
          minimize
        </button>
        <MapContainer center={[20, 0]} zoom={2} className="h-full w-full">
          <TileLayer
            attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>'
            url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
          />
          <MapResizer
            expanded={mapHovered}
            onDragChange={(dragging) => {
              onDragChange(dragging);
              if (!dragging && mapMouseLeft.current) onHoverChange(false);
            }}
          />
          <TripMapController items={itemsWithGps} currentItem={currentItem} />
          {itemsWithGps.map((m) => (
            <Marker
              key={m.id}
              position={[m.lat!, m.lng!]}
              icon={m.id === currentItem.id ? currentMarkerIcon : markerIcon(m)}
              eventHandlers={{ click: () => onMarkerClick(m) }}
            />
          ))}
        </MapContainer>
      </div>
    </div>
  );
}

// ── Main component ────────────────────────────────────────────────────────────

interface Props {
  trip: Trip;
  onClose: () => void;
  onMediaDeleted?: () => void;
}

export function TripGalleryOverlay({ trip, onClose, onMediaDeleted }: Props) {
  const [items, setItems] = useState<MediaItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [currentIndex, setCurrentIndex] = useState(0);
  const [currentUrl, setCurrentUrl] = useState<string | null>(null);
  const [urlLoading, setUrlLoading] = useState(false);
  const [editingLocation, setEditingLocation] = useState(false);
  const [mapMinimized, setMapMinimized] = useState(false);
  const [mapHovered, setMapHovered] = useState(false);
  const [mapDragging, setMapDragging] = useState(false);
  const mapMouseLeft = useRef(false);
  const urlCache = useRef<Map<string, string>>(new Map());
  const videoRef = useRef<HTMLVideoElement>(null);

  // Fetch all media for this trip, sorted chronologically
  useEffect(() => {
    setLoading(true);
    api
      .get<MediaItem[]>("/media", { trip_id: trip.id })
      .then((data) => {
        const sorted = (data ?? []).sort((a, b) => {
          if (!a.taken_at && !b.taken_at) return 0;
          if (!a.taken_at) return 1;
          if (!b.taken_at) return -1;
          return a.taken_at.localeCompare(b.taken_at);
        });
        setItems(sorted);
        setCurrentIndex(0);
      })
      .catch(console.error)
      .finally(() => setLoading(false));
  }, [trip.id]);

  // Lazy-load URL for current item, prefetch neighbors; fetch keyframes for video_360
  useEffect(() => {
    if (items.length === 0) return;
    const item = items[currentIndex];

    async function loadUrl(i: MediaItem): Promise<string> {
      if (urlCache.current.has(i.id)) return urlCache.current.get(i.id)!;
      const url = await api.getMediaUrl(i.id);
      urlCache.current.set(i.id, url);
      return url;
    }

    setCurrentUrl(urlCache.current.get(item.id) ?? null);
    setUrlLoading(!urlCache.current.has(item.id));

    loadUrl(item).then((url) => {
      setCurrentUrl(url);
      setUrlLoading(false);
    });

    // Fetch keyframes for video_360 if not already loaded
    if (item.media_type === "video_360" && !item.keyframes) {
      api.getKeyframes(item.id)
        .then((keyframes) => {
          setItems((prev) =>
            prev.map((m) => (m.id === item.id ? { ...m, keyframes } : m))
          );
        })
        .catch(() => {});
    }

    // Prefetch neighbors
    if (currentIndex > 0) loadUrl(items[currentIndex - 1]);
    if (currentIndex < items.length - 1) loadUrl(items[currentIndex + 1]);
  }, [currentIndex, items]);

  function goTo(index: number) {
    setCurrentIndex(Math.max(0, Math.min(index, items.length - 1)));
  }
  function goPrev() { goTo(currentIndex - 1); }
  function goNext() { goTo(currentIndex + 1); }

  // Keyboard navigation
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const tag = (e.target as HTMLElement)?.tagName;
      const editable = (e.target as HTMLElement)?.isContentEditable;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || editable) return;
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft") goPrev();
      if (e.key === "ArrowRight") goNext();
      if (e.code === "Space") {
        const v = videoRef.current;
        if (v) {
          e.preventDefault();
          v.paused ? v.play() : v.pause();
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, currentIndex, items.length]); // eslint-disable-line react-hooks/exhaustive-deps

  function handleMarkerClick(item: MediaItem) {
    const index = items.findIndex((m) => m.id === item.id);
    if (index !== -1) goTo(index);
  }

  function handleLocationSaved(lat: number, lng: number) {
    if (!currentItem) return;
    setItems((prev) =>
      prev.map((m) =>
        m.id === currentItem.id
          ? { ...m, lat, lng, gps_source: "manual" as const, gps_confidence: 1.0 }
          : m
      )
    );
    setEditingLocation(false);
  }

  async function handleDeleteMedia() {
    const item = items[currentIndex];
    if (!item) return;
    if (!confirm(`Delete "${item.filename}"? This cannot be undone.`)) return;
    try {
      await api.delete(`/media/${item.id}`);
      const newItems = items.filter((i) => i.id !== item.id);
      setItems(newItems);
      setCurrentIndex((prev) => Math.min(prev, Math.max(0, newItems.length - 1)));
      if (newItems.length === 0) onClose();
      onMediaDeleted?.();
    } catch (e) {
      console.error("Failed to delete media:", e);
    }
  }

  const itemsWithGps = items.filter((m) => m.lat !== null && m.lng !== null);
  const currentItem = items[currentIndex] ?? null;

  function renderMedia() {
    if (!currentItem) return null;
    if (!currentUrl) {
      return (
        <div className="flex h-full items-center justify-center text-white/40 text-sm">
          {urlLoading ? "Loading…" : "No media"}
        </div>
      );
    }
    if (currentItem.media_type === "photo_360") {
      return (
        <Panorama360
          url={currentUrl}
          onClose={onClose}
          contained
          initialYaw={currentItem.initial_yaw}
          initialPitch={currentItem.initial_pitch}
          initialFov={currentItem.initial_fov}
        />
      );
    }
    if (currentItem.media_type === "video_360") {
      return (
        <Video360
          url={currentUrl}
          onClose={onClose}
          contained
          initialYaw={currentItem.initial_yaw}
          initialPitch={currentItem.initial_pitch}
          initialFov={currentItem.initial_fov}
          keyframes={currentItem.keyframes}
        />
      );
    }
    if (currentItem.media_type === "video") {
      return (
        <video
          key={currentItem.id}
          ref={videoRef}
          src={currentUrl}
          controls
          autoPlay
          className="absolute inset-0 h-full w-full object-contain"
        />
      );
    }
    return (
      <img
        key={currentItem.id}
        src={currentUrl}
        alt={currentItem.filename}
        className="absolute inset-0 h-full w-full object-contain"
      />
    );
  }

  return (
    <div className="fixed inset-0 z-[1000] flex flex-col bg-black">
      {/* Header controls */}
      <div className="absolute right-4 top-4 z-10 flex items-center gap-2 rounded-xl bg-black/40 px-2 py-1.5 backdrop-blur-sm">
        {currentItem?.lat !== null && currentItem?.lng !== null && currentItem && (
          <a
            href={`https://www.google.com/maps/@?api=1&map_action=pano&viewpoint=${currentItem.lat},${currentItem.lng}`}
            target="_blank"
            rel="noopener noreferrer"
            className="text-white/60 hover:text-white"
            aria-label="Open in Street View"
            title="Open in Street View"
          >
            <ExternalLink className="h-5 w-5" />
          </a>
        )}
        {currentItem && (
          <button
            className="text-white/60 hover:text-white"
            onClick={() => setEditingLocation(true)}
            aria-label="Edit GPS location"
            title="Edit GPS location"
          >
            <MapPin className="h-5 w-5" />
          </button>
        )}
        {currentItem && (
          <button
            className="text-white/60 hover:text-red-400"
            onClick={handleDeleteMedia}
            aria-label="Delete media"
            title="Delete media"
          >
            <Trash2 className="h-5 w-5" />
          </button>
        )}
        <button
          className="text-white text-2xl leading-none hover:opacity-70"
          onClick={onClose}
          aria-label="Close gallery"
        >
          ✕
        </button>
      </div>

      {/* Trip name */}
      <div className="absolute left-4 top-4 z-10 text-white text-sm font-medium bg-black/50 px-3 py-1 rounded-full pointer-events-none">
        {trip.name}
      </div>

      {editingLocation && currentItem && (
        <EditLocationModal
          item={currentItem}
          onClose={() => setEditingLocation(false)}
          onSaved={handleLocationSaved}
        />
      )}

      {/* Media area */}
      <div className="relative flex-1">
        {loading && (
          <div className="flex h-full items-center justify-center text-white/40">
            Loading…
          </div>
        )}
        {!loading && items.length === 0 && (
          <div className="flex h-full items-center justify-center text-white/40">
            No media in this trip
          </div>
        )}
        {!loading && items.length > 0 && (
          <>
            {renderMedia()}

            {/* Left arrow */}
            {currentIndex > 0 && (
              <button
                className="absolute left-4 top-1/2 -translate-y-1/2 z-10 text-white text-5xl leading-none hover:opacity-70 select-none"
                onClick={(e) => { e.stopPropagation(); goPrev(); }}
                aria-label="Previous"
              >
                ‹
              </button>
            )}

            {/* Right arrow */}
            {currentIndex < items.length - 1 && (
              <button
                className="absolute right-4 top-1/2 -translate-y-1/2 z-10 text-white text-5xl leading-none hover:opacity-70 select-none"
                onClick={(e) => { e.stopPropagation(); goNext(); }}
                aria-label="Next"
              >
                ›
              </button>
            )}

            {/* Position label */}
            <div className="absolute bottom-4 left-1/2 -translate-x-1/2 z-10 text-white text-sm bg-black/50 px-3 py-1 rounded-full pointer-events-none">
              {currentIndex + 1} / {items.length}
              {currentItem?.filename && (
                <span className="ml-2 text-white/80">· {currentItem.filename}</span>
              )}
              {currentItem?.taken_at && (
                <span className="ml-2 text-white/60">
                  · {new Date(currentItem.taken_at).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}
                </span>
              )}
            </div>

            {/* Map overlay */}
            {itemsWithGps.length > 0 && currentItem && (
              <TripMapOverlay
                itemsWithGps={itemsWithGps}
                currentItem={currentItem}
                minimized={mapMinimized}
                mapHovered={mapHovered}
                mapDragging={mapDragging}
                mapMouseLeft={mapMouseLeft}
                onMinimize={() => setMapMinimized(true)}
                onExpand={() => setMapMinimized(false)}
                onHoverChange={setMapHovered}
                onDragChange={(d) => {
                  setMapDragging(d);
                  if (!d && mapMouseLeft.current) setMapHovered(false);
                }}
                onMarkerClick={handleMarkerClick}
              />
            )}
          </>
        )}
      </div>
    </div>
  );
}
