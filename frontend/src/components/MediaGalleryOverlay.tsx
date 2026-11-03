import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { ExternalLink, FolderMinus, MapPin, Trash2 } from "lucide-react";
import { MapContainer, TileLayer, Marker, useMap, useMapEvents } from "react-leaflet";
import L from "leaflet";
import type { MediaItem } from "@/types";
import { api, ApiError } from "@/api/client";
import { describeError } from "@/lib/errors";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { AlbumMenu } from "@/components/albums/AlbumMenu";
import { EditLocationModal } from "@/components/EditLocationModal";
import Panorama360 from "@/components/viewer/Panorama360";
import Video360 from "@/components/viewer/Video360";
import VideoDecodeNotice from "@/components/viewer/VideoDecodeNotice";
import { useVideoDecodeCheck } from "@/lib/video";
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

function GalleryMapController({
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

interface GalleryMapOverlayProps {
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

function GalleryMapOverlay({
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
}: GalleryMapOverlayProps) {
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
          <GalleryMapController items={itemsWithGps} currentItem={currentItem} />
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

// ── Data helpers ─────────────────────────────────────────────────────────────

type MediaParams = Record<string, string | string[]>;

async function fetchAllMedia(params: MediaParams): Promise<MediaItem[]> {
  const all: MediaItem[] = [];
  let offset = 0;
  while (true) {
    const page = await api.get<MediaItem[]>("/media", {
      ...params,
      offset: String(offset),
    });
    all.push(...(page ?? []));
    if ((page ?? []).length < 500) break;
    offset += 500;
  }
  return all;
}

function sortByTakenAt(items: MediaItem[]): MediaItem[] {
  return [...items].sort((a, b) => {
    if (!a.taken_at && !b.taken_at) return 0;
    if (!a.taken_at) return 1;
    if (!b.taken_at) return -1;
    return a.taken_at.localeCompare(b.taken_at);
  });
}

// ── Main component ────────────────────────────────────────────────────────────

interface Props {
  title: string;
  subtitle?: string;
  // Query params for GET /api/media, e.g. { trip_id } or { album_id }.
  params: MediaParams;
  onClose: () => void;
  onMediaDeleted?: () => void;
  // Album galleries: whether the caller may take this item out of the album,
  // and how. The overlay drops the item from its list once the promise resolves.
  canRemoveFromAlbum?: (item: MediaItem) => boolean;
  onRemoveFromAlbum?: (item: MediaItem) => Promise<void>;
}

export function MediaGalleryOverlay({
  title,
  subtitle,
  params,
  onClose,
  onMediaDeleted,
  canRemoveFromAlbum,
  onRemoveFromAlbum,
}: Props) {
  const user = useCurrentUser();
  const [items, setItems] = useState<MediaItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [currentIndex, setCurrentIndex] = useState(0);
  const [currentUrl, setCurrentUrl] = useState<string | null>(null);
  const [urlLoading, setUrlLoading] = useState(false);
  const [urlError, setUrlError] = useState<string | null>(null);
  // Why the last delete or remove failed. The page behind the overlay cannot show it.
  const [actionError, setActionError] = useState<string | null>(null);
  // Items with a delete or remove request in flight.
  const [busyIds, setBusyIds] = useState<string[]>([]);
  // Set once an item was dropped, so only emptying the list by hand closes the gallery.
  const dropped = useRef(false);
  const [editingLocation, setEditingLocation] = useState(false);
  const [mapMinimized, setMapMinimized] = useState(false);
  const [mapHovered, setMapHovered] = useState(false);
  const [mapDragging, setMapDragging] = useState(false);
  const mapMouseLeft = useRef(false);
  const urlCache = useRef<Map<string, string>>(new Map());
  const videoRef = useRef<HTMLVideoElement>(null);
  const videoCheck = useVideoDecodeCheck(currentUrl ?? "");
  // Callers pass params inline; refetch only when their content changes.
  const paramsKey = JSON.stringify(params);
  const paramsRef = useRef(params);
  paramsRef.current = params;

  // Fetch all matching media, sorted chronologically
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    dropped.current = false;
    fetchAllMedia(paramsRef.current)
      .then((data) => {
        if (cancelled) return;
        setItems(sortByTakenAt(data));
        setCurrentIndex(0);
      })
      .catch(console.error)
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [paramsKey]);

  // Lazy-load URL for current item, prefetch neighbors; fetch keyframes for video_360
  useEffect(() => {
    const item = items[currentIndex];
    // Out of range for one commit after an item is dropped, until the index is clamped.
    if (!item) return;
    // A late answer for an item the user moved away from must not replace the current one.
    let cancelled = false;

    async function loadUrl(i: MediaItem): Promise<string> {
      if (urlCache.current.has(i.id)) return urlCache.current.get(i.id)!;
      const url = await api.getMediaUrl(i.id);
      urlCache.current.set(i.id, url);
      return url;
    }

    setCurrentUrl(urlCache.current.get(item.id) ?? null);
    setUrlLoading(!urlCache.current.has(item.id));
    setUrlError(null);

    loadUrl(item)
      .then((url) => {
        if (cancelled) return;
        setCurrentUrl(url);
        setUrlLoading(false);
      })
      .catch((e) => {
        if (cancelled) return;
        console.error("Failed to load media URL:", e);
        setUrlLoading(false);
        // A shared item stops being readable when its album or membership goes.
        setUrlError(
          e instanceof ApiError && e.status === 404 ? "You no longer have access to this item" : "Could not load media"
        );
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

    // Prefetch neighbors; a failure shows once the user gets there.
    if (currentIndex > 0) loadUrl(items[currentIndex - 1]).catch(() => {});
    if (currentIndex < items.length - 1) loadUrl(items[currentIndex + 1]).catch(() => {});

    return () => {
      cancelled = true;
    };
  }, [currentIndex, items]);

  // Dropping items can leave the index past the end; close once the last one is gone.
  useLayoutEffect(() => {
    if (items.length === 0) {
      if (dropped.current && !loading) {
        dropped.current = false;
        onClose();
      }
      return;
    }
    if (currentIndex > items.length - 1) setCurrentIndex(items.length - 1);
  }, [items.length, currentIndex, loading]); // eslint-disable-line react-hooks/exhaustive-deps

  function goTo(index: number) {
    setActionError(null);
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
    // Optimistically update the saved item immediately
    setItems((prev) =>
      prev.map((m) =>
        m.id === currentItem.id
          ? { ...m, lat, lng, gps_source: "manual" as const, gps_confidence: 1.0 }
          : m
      )
    );
    setEditingLocation(false);
    // Re-fetch all media so GPS-matched neighbours get their updated coordinates
    fetchAllMedia(paramsRef.current)
      .then((data) => {
        const sorted = sortByTakenAt(data);
        setItems((prev) =>
          sorted.map((fresh) => {
            const existing = prev.find((m) => m.id === fresh.id);
            return existing ? { ...fresh, keyframes: existing.keyframes } : fresh;
          })
        );
      })
      .catch(console.error);
  }

  // Runs after an await, so it filters the latest list: overlapping removals must
  // not bring back an item another one already dropped.
  function dropItem(id: string) {
    dropped.current = true;
    setItems((prev) => prev.filter((i) => i.id !== id));
  }

  async function runItemAction(item: MediaItem, action: () => Promise<void>) {
    setActionError(null);
    setBusyIds((prev) => [...prev, item.id]);
    try {
      await action();
    } finally {
      setBusyIds((prev) => prev.filter((id) => id !== item.id));
    }
  }

  async function handleDeleteMedia() {
    const item = items[currentIndex];
    if (!item) return;
    if (!confirm(`Delete "${item.filename}"? This cannot be undone.`)) return;
    await runItemAction(item, async () => {
      try {
        await api.delete(`/media/${item.id}`);
      } catch (e) {
        console.error("Failed to delete media:", e);
        // Already deleted elsewhere: it is gone either way.
        if (!(e instanceof ApiError && e.status === 404)) {
          setActionError(`Could not delete "${item.filename}"`);
          return;
        }
      }
      dropItem(item.id);
      onMediaDeleted?.();
    });
  }

  async function handleRemoveFromAlbum() {
    const item = items[currentIndex];
    if (!item || !onRemoveFromAlbum) return;
    await runItemAction(item, async () => {
      try {
        await onRemoveFromAlbum(item);
        dropItem(item.id);
      } catch (e) {
        console.error("Failed to remove media from album:", e);
        setActionError(describeError(`Could not remove "${item.filename}" from the album`, e));
      }
    });
  }

  // Unticking the album this gallery shows, in the item's album menu, takes the item out of it.
  function handleAlbumChange(item: MediaItem, albumId: string, added: boolean) {
    if (!added && paramsRef.current.album_id === albumId) dropItem(item.id);
  }

  const itemsWithGps = items.filter((m) => m.lat !== null && m.lng !== null);
  // Clamped like the layout effect above does, so the commit before it runs keeps the
  // item's menu and modals mounted.
  const currentItem = items[Math.min(currentIndex, items.length - 1)] ?? null;
  // Shared items are read-only: the backend 404s their writes, so hide the controls.
  const isOwn = currentItem?.owner_id === user.id;
  const canRemove = !!currentItem && !!onRemoveFromAlbum && !!canRemoveFromAlbum?.(currentItem);
  const busy = !!currentItem && busyIds.includes(currentItem.id);

  function renderMedia() {
    if (!currentItem) return null;
    if (!currentUrl) {
      return (
        <div className="flex h-full items-center justify-center text-white/40 text-sm">
          {urlError ?? (urlLoading ? "Loading…" : "No media")}
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
        <>
          <video
            key={currentItem.id}
            ref={videoRef}
            src={currentUrl}
            controls
            autoPlay
            className="absolute inset-0 h-full w-full object-contain"
            {...videoCheck.handlers}
          />
          {videoCheck.unsupported && <VideoDecodeNotice />}
        </>
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
          <AlbumMenu
            item={currentItem}
            isOwn={isOwn}
            variant="overlay"
            onChange={(albumId, added) => handleAlbumChange(currentItem, albumId, added)}
          />
        )}
        {currentItem && isOwn && (
          <button
            className="text-white/60 hover:text-white"
            onClick={() => setEditingLocation(true)}
            aria-label="Edit GPS location"
            title="Edit GPS location"
          >
            <MapPin className="h-5 w-5" />
          </button>
        )}
        {canRemove && (
          <button
            className="text-white/60 hover:text-white disabled:opacity-40"
            onClick={handleRemoveFromAlbum}
            disabled={busy}
            aria-label="Remove from album"
            title="Remove from album"
          >
            <FolderMinus className="h-5 w-5" />
          </button>
        )}
        {currentItem && isOwn && (
          <button
            className="text-white/60 hover:text-red-400 disabled:opacity-40"
            onClick={handleDeleteMedia}
            disabled={busy}
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

      {/* Title, and who shared the current item */}
      <div className="absolute left-4 top-4 z-10 flex flex-col items-start gap-1 pointer-events-none">
        <div className="text-white text-sm font-medium bg-black/50 px-3 py-1 rounded-full">
          {title}
          {subtitle && <span className="ml-2 font-normal text-white/60">{subtitle}</span>}
        </div>
        {currentItem && !isOwn && (
          <div className="text-white/80 text-xs bg-black/50 px-3 py-1 rounded-full">
            Shared by {currentItem.owner_name ?? "another user"}
          </div>
        )}
        {actionError && (
          <div role="alert" className="max-w-sm text-white text-xs bg-red-600/80 px-3 py-1 rounded-lg">
            {actionError}
          </div>
        )}
      </div>

      {editingLocation && currentItem && isOwn && (
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
            No media here
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
              <GalleryMapOverlay
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
