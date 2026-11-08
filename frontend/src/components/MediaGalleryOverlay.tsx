import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, ExternalLink, FolderMinus, MapPin, Rotate3d, Trash2 } from "lucide-react";
import { Marker, useMap } from "react-leaflet";
import L from "leaflet";
import type { MediaItem } from "@/types";
import { api, ApiError } from "@/api/client";
import { describeError } from "@/lib/errors";
import { useCloseOnBack } from "@/lib/useCloseOnBack";
import { cn } from "@/lib/utils";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { AlbumMenu } from "@/components/albums/AlbumMenu";
import { EditLocationModal } from "@/components/EditLocationModal";
import { EditViewModal } from "@/components/viewer/EditViewModal";
import { MiniMapOverlay, type MiniMapSize } from "@/components/map/MiniMapOverlay";
import { TOP_MARKER_Z_INDEX_OFFSET } from "@/components/map/markers";
import Panorama360 from "@/components/viewer/Panorama360";
import Video360 from "@/components/viewer/Video360";
import VideoDecodeNotice from "@/components/viewer/VideoDecodeNotice";
import { ZoomableImage } from "@/components/viewer/ZoomableImage";
import { useVideoDecodeCheck } from "@/lib/video";

// ── Map helpers ──────────────────────────────────────────────────────────────

function GalleryMapController({
  items,
  currentItem,
}: {
  items: MediaItem[];
  currentItem: MediaItem;
}) {
  const map = useMap();
  useEffect(() => {
    function show(animate: boolean) {
      if (currentItem.lat !== null && currentItem.lng !== null) {
        if (animate) map.flyTo([currentItem.lat, currentItem.lng], 13, { animate: true, duration: 0.8 });
        else map.setView([currentItem.lat, currentItem.lng], 13, { animate: false });
      } else if (items.length > 0) {
        const bounds = L.latLngBounds(items.map((m) => [m.lat!, m.lng!] as [number, number]));
        // animate: true would force an animation Leaflet otherwise skips for a far jump.
        map.fitBounds(bounds, animate ? { padding: [40, 40] } : { padding: [40, 40], animate: false });
      }
    }
    // Leaflet's cached size, which flyTo and fitBounds use: a window resize while the map is hidden
    // (display: none) caches 0×0. flyTo then throws on NaN maths and fitBounds sets zoom to Infinity.
    const sized = () => {
      const size = map.getSize();
      return size.x > 0 && size.y > 0;
    };
    if (sized()) {
      show(true);
      return;
    }
    // Hidden, or mounted hidden: invalidateSize fires 'resize' once the map is shown and measured.
    // Moving to another item replaces the listener, so only the latest target applies.
    const onResize = () => {
      if (!sized()) return;
      map.off("resize", onResize);
      show(false);
    };
    map.on("resize", onResize);
    return () => {
      map.off("resize", onResize);
    };
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

// ── Controls ─────────────────────────────────────────────────────────────────

// Room kept beside the position label for the map in the corner (its chosen width, from sm; the
// large map from lg) plus the gap, so the label never moves while the map hover-enlarges. Both
// sides where it still leaves the label enough room, so it sits in the middle of the screen. On a
// short screen the map is back in the row and needs none.
const LABEL_CLEARANCE: Record<MiniMapSize, string> = {
  // The "Map" pill.
  hidden: "sm:px-[5.5rem] short:px-0",
  small: "sm:pr-[18.5rem] xl:pl-[18.5rem] short:px-0",
  large: "lg:pr-[36.5rem] short:px-0",
};

// The visible icon stays small; on touch the target grows to 44px.
const toolButton =
  "flex items-center justify-center text-white/60 hover:text-white disabled:opacity-40 touch:min-h-11 touch:min-w-11";

function NavButton({
  direction,
  placement,
  disabled = false,
  onClick,
}: {
  direction: "prev" | "next";
  // side: at the screen edge, vertically centred; row: beside the position label, used while
  // the map is large and would cover the right edge.
  placement: "side" | "row";
  disabled?: boolean;
  onClick: () => void;
}) {
  const label = direction === "prev" ? "Previous" : "Next";
  const Icon = direction === "prev" ? ChevronLeft : ChevronRight;
  if (placement === "row") {
    return (
      <button
        type="button"
        className="pointer-events-auto flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-black/50 text-white hover:bg-black/70 disabled:opacity-30 touch:h-11 touch:w-11"
        onClick={onClick}
        disabled={disabled}
        aria-label={label}
      >
        <Icon className="h-6 w-6" aria-hidden="true" />
      </button>
    );
  }
  return (
    <button
      type="button"
      className={cn(
        // A dark disc keeps the arrow visible on a light photo; at least 44px everywhere.
        "pointer-events-auto absolute top-1/2 flex h-11 w-11 -translate-y-1/2 items-center justify-center rounded-full bg-black/40 text-white hover:bg-black/60 sm:h-12 sm:w-12",
        direction === "prev" ? "left-0 sm:left-4 short:left-0" : "right-0 sm:right-4 short:right-0",
      )}
      onClick={onClick}
      aria-label={label}
    >
      <Icon className="h-8 w-8" aria-hidden="true" />
    </button>
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
  // The URL (or why it failed) together with the item it belongs to. A separate URL state would
  // still hold the previous item's URL for the commit after a step, and a 360° viewer would
  // build a renderer and load that old texture again before the effect below replaced it.
  const [urlState, setUrlState] = useState<{ id: string; url: string | null; error: string | null } | null>(null);
  // Why the last delete or remove failed. The page behind the overlay cannot show it.
  const [actionError, setActionError] = useState<string | null>(null);
  // Items with a delete or remove request in flight.
  const [busyIds, setBusyIds] = useState<string[]>([]);
  // Set once an item was dropped, so only emptying the list by hand closes the gallery.
  const dropped = useRef(false);
  // The open editor (location, or the 360° default view and keyframes) and its item. Tied to the
  // item, so a change of item under the editor (a removal finishing) closes it instead of showing
  // the next one with the old values, and coming back to the item does not reopen it.
  const [editing, setEditing] = useState<{ id: string; kind: "location" | "view" } | null>(null);
  // The size chosen with the map's buttons; it outlives the map, which unmounts while loading.
  const [mapSize, setMapSize] = useState<MiniMapSize>("small");
  const urlCache = useRef<Map<string, string>>(new Map());
  const videoRef = useRef<HTMLVideoElement>(null);
  // Clamped like the layout effect below does, so the commit before it runs keeps the
  // item's menu and modals mounted.
  const currentItem = items[Math.min(currentIndex, items.length - 1)] ?? null;
  // Only a URL of the current item: its state, else the cache (a prefetched neighbour shows at
  // once, without a placeholder for the commit before the effect below runs).
  const urlEntry = currentItem && urlState?.id === currentItem.id ? urlState : null;
  const currentUrl = currentItem ? (urlEntry?.url ?? urlCache.current.get(currentItem.id) ?? null) : null;
  const urlError = urlEntry?.error ?? null;
  const urlLoading = !currentUrl && (urlEntry === null || (urlEntry.url === null && urlEntry.error === null));
  const videoCheck = useVideoDecodeCheck(currentUrl ?? "");
  // Callers pass params inline; refetch only when their content changes.
  const paramsKey = JSON.stringify(params);
  const paramsRef = useRef(params);
  paramsRef.current = params;
  // The phone's back gesture closes the gallery instead of leaving the app.
  useCloseOnBack(onClose);

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

    setUrlState({ id: item.id, url: urlCache.current.get(item.id) ?? null, error: null });

    loadUrl(item)
      .then((url) => {
        if (cancelled) return;
        setUrlState((prev) =>
          prev?.id === item.id && prev.url === url && prev.error === null ? prev : { id: item.id, url, error: null }
        );
      })
      .catch((e) => {
        if (cancelled) return;
        console.error("Failed to load media URL:", e);
        // A shared item stops being readable when its album or membership goes.
        setUrlState({
          id: item.id,
          url: null,
          error:
            e instanceof ApiError && e.status === 404 ? "You no longer have access to this item" : "Could not load media",
        });
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
    setEditing(null);
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

  // The viewer remounts with these once the editor closes.
  function handleViewSaved(id: string, updated: Partial<MediaItem>) {
    setItems((prev) => prev.map((m) => (m.id === id ? { ...m, ...updated } : m)));
    setEditing(null);
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
  // Shared items are read-only: the backend 404s their writes, so hide the controls.
  const isOwn = currentItem?.owner_id === user.id;
  const canRemove = !!currentItem && !!onRemoveFromAlbum && !!canRemoveFromAlbum?.(currentItem);
  const busy = !!currentItem && busyIds.includes(currentItem.id);
  const showItems = !loading && !!currentItem;
  const hasPrev = currentIndex > 0;
  const hasNext = currentIndex < items.length - 1;
  const hasMap = showItems && itemsWithGps.length > 0;
  // A large map covers the right edge (all of the bottom on a phone), so the arrows move into
  // the bottom row beside the position label.
  const mapLarge = hasMap && mapSize === "large";
  const isVideo = currentItem?.media_type === "video" || currentItem?.media_type === "video_360";
  const takenAt = currentItem?.taken_at
    ? new Date(currentItem.taken_at).toLocaleString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : null;
  // Only while its item is current; the effect below forgets an editor whose item went away.
  const editorOpen = editing !== null && editing.id === currentItem?.id;
  const editingLocation = editorOpen && editing.kind === "location";
  const editingView = editorOpen && editing.kind === "view";
  const is360 = currentItem?.media_type === "photo_360" || currentItem?.media_type === "video_360";
  // The view editor starts from the item's keyframes, so it waits for them: saving from an empty
  // list would delete the stored ones.
  const viewEditorReady =
    !!currentUrl && (currentItem?.media_type !== "video_360" || currentItem.keyframes !== undefined);

  useEffect(() => {
    if (editing !== null && editing.id !== currentItem?.id) setEditing(null);
  }, [editing, currentItem?.id]);

  // Keyboard navigation
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const tag = (e.target as HTMLElement)?.tagName;
      const editable = (e.target as HTMLElement)?.isContentEditable;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || editable) return;
      // An editor covers the gallery: Escape closes only the editor, as back does, and the other
      // keys must not change or play the item under it.
      if (editorOpen) {
        if (e.key === "Escape") setEditing(null);
        return;
      }
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft") goPrev();
      if (e.key === "ArrowRight") goNext();
      if (e.code === "Space") {
        const v = videoRef.current;
        if (v) {
          e.preventDefault();
          if (v.paused) v.play();
          else v.pause();
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, currentIndex, items.length, editorOpen]); // eslint-disable-line react-hooks/exhaustive-deps

  function renderMedia() {
    if (!currentItem) return null;
    if (!currentUrl) {
      return (
        <div className="flex h-full items-center justify-center text-white/40 text-sm">
          {urlError ?? (urlLoading ? "Loading…" : "No media")}
        </div>
      );
    }
    // The view editor brings its own viewer. One underneath would hold a second WebGL context,
    // play a video's sound and toggle it on Space; it remounts with the saved view afterwards.
    if (editingView) return null;
    // currentUrl always belongs to currentItem; the key also gives each item a fresh viewer state.
    if (currentItem.media_type === "photo_360") {
      return (
        <Panorama360
          key={currentItem.id}
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
          key={currentItem.id}
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
      <ZoomableImage
        key={currentItem.id}
        src={currentUrl}
        alt={currentItem.filename}
        onSwipe={(direction) => (direction === "next" ? goNext() : goPrev())}
      />
    );
  }

  return (
    <div className="fixed inset-0 z-[1000] overflow-hidden bg-black">
      {/* The media fills the screen; the controls float above it. */}
      <div className="absolute inset-0">
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
        {showItems && renderMedia()}
      </div>

      {/* One column: the header, a free middle for the side arrows, and the bottom row, so on a
          phone none of them can cover another. Only the controls take pointer events; the rest
          passes through to the media (pan, zoom, swipe, 360 drag). */}
      <div className="pointer-events-none absolute inset-0 z-10 flex flex-col p-2 sm:p-4 short:p-2">
        {/* Title on the left, truncated; the toolbar keeps its size on the right. */}
        <div className="relative z-20 flex shrink-0 items-start gap-2 touch:items-center">
          <div className="min-w-0 truncate rounded-full bg-black/50 px-3 py-1 text-sm font-medium text-white">
            {title}
            {subtitle && <span className="ml-2 font-normal text-white/60">{subtitle}</span>}
          </div>

          <div className="pointer-events-auto ml-auto flex shrink-0 items-center gap-2 rounded-xl bg-black/40 px-2 py-1.5 backdrop-blur-sm touch:gap-0 touch:px-0.5 touch:py-0">
            {currentItem?.lat !== null && currentItem?.lng !== null && currentItem && (
              <a
                href={`https://www.google.com/maps/@?api=1&map_action=pano&viewpoint=${currentItem.lat},${currentItem.lng}`}
                target="_blank"
                rel="noopener noreferrer"
                className={toolButton}
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
                className={toolButton}
                onClick={() => setEditing({ id: currentItem.id, kind: "location" })}
                aria-label="Edit GPS location"
                title="Edit GPS location"
              >
                <MapPin className="h-5 w-5" />
              </button>
            )}
            {currentItem && isOwn && is360 && (
              <button
                className={toolButton}
                onClick={() => setEditing({ id: currentItem.id, kind: "view" })}
                disabled={!viewEditorReady}
                aria-label="Edit default view"
                title="Edit default view"
              >
                <Rotate3d className="h-5 w-5" />
              </button>
            )}
            {canRemove && (
              <button
                className={toolButton}
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
                className={cn(toolButton, "hover:text-red-400")}
                onClick={handleDeleteMedia}
                disabled={busy}
                aria-label="Delete media"
                title="Delete media"
              >
                <Trash2 className="h-5 w-5" />
              </button>
            )}
            <button
              className="flex items-center justify-center text-2xl leading-none text-white hover:opacity-70 touch:min-h-11 touch:min-w-11"
              onClick={onClose}
              aria-label="Close gallery"
            >
              ✕
            </button>
          </div>

          {/* Below the header row, so on a phone they get the full width, not the strip left
              of the toolbar. */}
          {((currentItem && !isOwn) || actionError) && (
            <div className="absolute left-0 top-full mt-1 flex max-w-full flex-col items-start gap-1">
              {currentItem && !isOwn && (
                <div className="max-w-full truncate rounded-full bg-black/50 px-3 py-1 text-xs text-white/80">
                  Shared by {currentItem.owner_name ?? "another user"}
                </div>
              )}
              {actionError && (
                <div role="alert" className="max-w-full rounded-lg bg-red-600/80 px-3 py-1 text-xs text-white sm:max-w-sm">
                  {actionError}
                </div>
              )}
            </div>
          )}
        </div>

        {/* The side arrows centre in the space between the header and the bottom row on a phone,
            and on the whole screen (static: positioned by the column) on a larger one. */}
        <div className="relative min-h-0 flex-1 sm:static short:relative">
          {showItems && !mapLarge && hasPrev && <NavButton direction="prev" placement="side" onClick={goPrev} />}
          {showItems && !mapLarge && hasNext && <NavButton direction="next" placement="side" onClick={goNext} />}
        </div>

        {showItems && currentItem && (
          <div
            className={cn(
              // min-h-0, not shrink-0: on a short column the row gives up height, so the mini
              // map's max-h-full caps it at the space below the header instead of letting it run
              // off the bottom. The middle spacer has no basis, so this only happens when space
              // runs out.
              "flex min-h-0 gap-2",
              // Below lg the large map leaves no room beside it (it spans a phone's width), so the
              // label goes under it. A short screen has no height for that: it stays beside.
              mapLarge
                ? "flex-col-reverse lg:flex-row lg:items-end short:flex-row short:items-end"
                : "flex-row items-end",
              // Clear of the video's controls bar. A large map on a phone held sideways would
              // then reach the toolbar; it covers most of the video anyway.
              isVideo && (mapLarge ? "mb-11 sm:mb-10 short:mb-0" : "mb-11 sm:mb-10 short:mb-11"),
            )}
          >
            <div
              className={cn(
                "flex min-w-0 items-center gap-2",
                mapLarge ? "lg:flex-1 short:flex-1" : "flex-1",
                hasMap ? "justify-start sm:justify-center" : "justify-center",
                hasMap && LABEL_CLEARANCE[mapSize],
              )}
            >
              {mapLarge && <NavButton direction="prev" placement="row" disabled={!hasPrev} onClick={goPrev} />}

              {/* Position label. A portrait phone has room for two short lines beside the map:
                  the date, then the position and the truncated file name. */}
              <div className="min-w-0 rounded-xl bg-black/50 px-3 py-1 text-sm text-white sm:flex sm:items-baseline sm:gap-2 sm:rounded-full">
                {takenAt && (
                  <div className="truncate text-white/60 sm:order-last sm:shrink-0">
                    <span className="hidden sm:inline">· </span>
                    {takenAt}
                  </div>
                )}
                <div className="flex min-w-0 items-baseline gap-2 sm:contents">
                  <span className="shrink-0 tabular-nums">
                    {currentIndex + 1} / {items.length}
                  </span>
                  {currentItem.filename && (
                    <span className="min-w-0 truncate text-white/80">· {currentItem.filename}</span>
                  )}
                </div>
              </div>

              {mapLarge && <NavButton direction="next" placement="row" disabled={!hasNext} onClick={goNext} />}
            </div>

            {hasMap && (
              <MiniMapOverlay
                // In the row on a phone, so the label stays beside it. From sm (lg for the large
                // map) out of the flow, in the column's corner as in the game, so a hover
                // enlargement covers the label for a moment rather than sliding it. top-16 keeps
                // it under the toolbar; bottom-14 matches the row's margin over a video's controls.
                className={cn(
                  "static ml-auto short:static",
                  mapLarge ? "lg:absolute lg:top-16" : "sm:absolute sm:top-16",
                  isVideo && (mapLarge ? "lg:bottom-14" : "sm:bottom-14"),
                )}
                defaultSize={mapSize}
                onSizeChange={setMapSize}
              >
                <GalleryMapController items={itemsWithGps} currentItem={currentItem} />
                {itemsWithGps.map((m) => (
                  <Marker
                    key={m.id}
                    position={[m.lat!, m.lng!]}
                    icon={m.id === currentItem.id ? currentMarkerIcon : markerIcon(m)}
                    zIndexOffset={m.id === currentItem.id ? TOP_MARKER_Z_INDEX_OFFSET : 0}
                    eventHandlers={{ click: () => handleMarkerClick(m) }}
                  />
                ))}
              </MiniMapOverlay>
            )}
          </div>
        )}
      </div>

      {editingLocation && currentItem && isOwn && (
        <EditLocationModal
          item={currentItem}
          onClose={() => setEditing(null)}
          onSaved={handleLocationSaved}
        />
      )}

      {editingView && currentItem && currentUrl && isOwn && (
        <EditViewModal
          item={currentItem}
          url={currentUrl}
          onClose={() => setEditing(null)}
          onSaved={(updated) => handleViewSaved(currentItem.id, updated)}
        />
      )}
    </div>
  );
}
