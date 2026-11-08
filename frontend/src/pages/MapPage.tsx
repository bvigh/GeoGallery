import { useEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, ExternalLink, Trash2, X } from "lucide-react";
import { MediaMap } from "../components/map/MediaMap";
import { FilterPanel } from "../components/map/FilterPanel";
import { EditLocationModal } from "../components/EditLocationModal";
import type { Album, FilterState, MediaItem, Trip } from "../types";
import { api } from "../api/client";
import { activeFilterCount, emptyFilters, filterParams } from "@/lib/filters";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { AlbumMenu } from "@/components/albums/AlbumMenu";
import Panorama360 from "../components/viewer/Panorama360";
import Video360 from "../components/viewer/Video360";
import VideoDecodeNotice from "../components/viewer/VideoDecodeNotice";
import { useVideoDecodeCheck } from "@/lib/video";
import { useCloseOnBack } from "@/lib/useCloseOnBack";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { useVideoKeys } from "@/lib/videoKeys";
import { ZoomableImage } from "@/components/viewer/ZoomableImage";
import { EditViewModal } from "@/components/viewer/EditViewModal";

// The backend's maximum page: every page re-runs the whole filtered query, so fewer is faster.
const PAGE_SIZE = 5000;

// crossOrigin matters: the fullscreen 360° viewers load this same presigned URL
// as a CORS request (WebGL texture). A plain no-CORS preview fetch gets cached
// without Access-Control-Allow-Origin, and the browser would reuse it and fail.
// Taller on a phone, where the panel spans the screen and the preview is its main content, and
// smaller on a phone held sideways, where the card has only ~300 px to show it in.
export function MediaPreview({
  item,
  url,
  onFullscreen,
}: {
  item: MediaItem;
  url: string;
  onFullscreen: () => void;
}) {
  const isVideo = item.media_type === "video" || item.media_type === "video_360";
  // The vh value is the fallback for browsers without dvh, where the dvh class alone sets no cap.
  const size =
    "max-h-[30vh] supports-[height:100dvh]:max-h-[30dvh] w-full rounded object-cover sm:max-h-40 short:max-h-24";
  return (
    <button
      className="mt-2 w-full overflow-hidden rounded cursor-zoom-in"
      onClick={onFullscreen}
      aria-label="Open fullscreen"
    >
      {isVideo ? (
        <video src={url} crossOrigin="anonymous" className={size} muted playsInline />
      ) : (
        <img src={url} crossOrigin="anonymous" alt={item.filename} className={size} />
      )}
    </button>
  );
}

// Round, translucent and at least 44 px: readable over any picture and easy to hit with a finger.
const overlayButton =
  "absolute flex items-center justify-center rounded-full bg-black/40 text-white hover:bg-black/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70";

// Stays open while a step to a cluster peer loads that peer's URL (url null), so it does not
// flash the map and keeps its history entry.
function FullscreenOverlay({
  item,
  url,
  failed = false,
  onClose,
  onPrev,
  onNext,
  peerLabel,
}: {
  item: MediaItem;
  url: string | null;
  failed?: boolean;
  onClose: () => void;
  onPrev?: () => void;
  onNext?: () => void;
  peerLabel?: string;
}) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const videoCheck = useVideoDecodeCheck(url ?? "");
  const videoKeys = useVideoKeys(videoRef);
  // The phone's back gesture closes the view instead of leaving the app.
  useCloseOnBack(onClose);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (keyBelongsToDialog(e)) return;
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft") onPrev?.();
      if (e.key === "ArrowRight") onNext?.();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, onPrev, onNext]);

  if (url && item.media_type === "photo_360") {
    return (
      <Panorama360
        url={url}
        onClose={onClose}
        initialYaw={item.initial_yaw}
        initialPitch={item.initial_pitch}
        initialFov={item.initial_fov}
      />
    );
  }

  if (url && item.media_type === "video_360") {
    return (
      <Video360
        url={url}
        onClose={onClose}
        initialYaw={item.initial_yaw}
        initialPitch={item.initial_pitch}
        initialFov={item.initial_fov}
        keyframes={item.keyframes}
      />
    );
  }

  const photo = url !== null && item.media_type === "photo";

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={item.filename}
      // Opaque on touch: on a phone the map, the header and the selected-item panel showing
      // through around the photo look like part of it.
      className="fixed inset-0 z-[1000] flex items-center justify-center bg-black/80 touch:bg-black"
      // A photo closes on a tap beside the picture (ZoomableImage's onTap): the backdrop also
      // gets a click after every pan and double tap.
      onClick={photo ? undefined : onClose}
    >
      {url === null ? (
        <p className="text-sm text-white/80">{failed ? "Failed to load media" : "Loading media…"}</p>
      ) : photo ? (
        // Same request mode as the preview, so the browser can reuse its download.
        <ZoomableImage
          src={url}
          alt={item.filename}
          crossOrigin="anonymous"
          onTap={(inside) => {
            if (!inside) onClose();
          }}
          onSwipe={(direction) => (direction === "next" ? onNext?.() : onPrev?.())}
        />
      ) : (
        <div className="relative" onClick={(e) => e.stopPropagation()}>
          <video
            ref={videoRef}
            src={url}
            controls
            autoPlay
            className="max-h-[90vh] supports-[height:100dvh]:max-h-[90dvh] max-w-[90vw] rounded object-contain outline-none"
            {...videoCheck.handlers}
            {...videoKeys}
          />
          {videoCheck.unsupported && <VideoDecodeNotice />}
        </div>
      )}
      {/* After the picture, so they paint over it. Top centre: clear of the close button and of
          a video's controls. */}
      {peerLabel && (
        <div className="pointer-events-none absolute left-1/2 top-4 -translate-x-1/2 rounded-full bg-black/50 px-3 py-1 text-sm text-white">
          {peerLabel}
        </div>
      )}
      <button
        className={`${overlayButton} right-2 top-2 h-11 w-11`}
        onClick={(e) => {
          e.stopPropagation();
          onClose();
        }}
        aria-label="Close fullscreen"
      >
        <X className="h-6 w-6" aria-hidden="true" />
      </button>
      {onPrev && (
        <button
          className={`${overlayButton} left-2 top-1/2 h-12 w-12 -translate-y-1/2`}
          onClick={(e) => {
            e.stopPropagation();
            onPrev();
          }}
          aria-label="Previous"
        >
          <ChevronLeft className="h-8 w-8" aria-hidden="true" />
        </button>
      )}
      {onNext && (
        <button
          className={`${overlayButton} right-2 top-1/2 h-12 w-12 -translate-y-1/2`}
          onClick={(e) => {
            e.stopPropagation();
            onNext();
          }}
          aria-label="Next"
        >
          <ChevronRight className="h-8 w-8" aria-hidden="true" />
        </button>
      )}
    </div>
  );
}

// Icon buttons: small on a desktop, 44 px for a finger.
const touchIcon = "touch:flex touch:min-h-11 touch:min-w-11 touch:items-center touch:justify-center";
// Full-width actions: as tall as a fingertip needs, with text to match.
const touchRow = "touch:min-h-11 touch:text-sm";

// App keeps this page mounted while another tab is open (active false), so the map and its
// markers survive tab switches.
export function MapPage({ active = true }: { active?: boolean }) {
  const user = useCurrentUser();
  const [items, setItems] = useState<MediaItem[]>([]);
  const [trips, setTrips] = useState<Trip[]>([]);
  const [albums, setAlbums] = useState<Album[]>([]);
  const [filters, setFilters] = useState<FilterState>(emptyFilters);
  const [loadingMedia, setLoadingMedia] = useState(false);
  const [selected, setSelected] = useState<MediaItem | null>(null);
  const [mediaUrl, setMediaUrl] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [urlError, setUrlError] = useState<string | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [clusterPeers, setClusterPeers] = useState<MediaItem[]>([]);
  const [clusterIndex, setClusterIndex] = useState(0);
  const [editingView, setEditingView] = useState(false);
  const [editingLocation, setEditingLocation] = useState(false);
  // Bumped to refetch the media list under the same filters.
  const [mediaReload, setMediaReload] = useState(0);
  // Bumped when the tab is shown again: other tabs may have uploaded, deleted or shared media.
  const [revisit, setRevisit] = useState(0);
  const wasActive = useRef(active);
  // The filter params of the list on the map.
  const shownQuery = useRef<string | null>(null);
  const pendingId = useRef<string | null>(null);

  useEffect(() => {
    if (active && !wasActive.current) setRevisit((n) => n + 1);
    wasActive.current = active;
  }, [active]);

  useEffect(() => {
    let cancelled = false;

    const params = filterParams(filters);
    // Under the same filters the current markers stay until the new list is complete, so a
    // reload never empties the map. New filters clear it and fill it page by page.
    const query = JSON.stringify(params);
    const inBackground = query === shownQuery.current;
    shownQuery.current = query;

    if (!inBackground) setItems([]);
    setLoadingMedia(true);
    setListError(null);

    const loaded: MediaItem[] = [];
    let offset = 0;

    function fetchPage() {
      api
        .get<MediaItem[]>("/media", {
          ...params,
          has_location: "true",
          limit: String(PAGE_SIZE),
          offset: String(offset),
        })
        .then((page) => {
          if (cancelled) return;
          loaded.push(...page);
          if (!inBackground) setItems([...loaded]);
          if (page.length === PAGE_SIZE) {
            offset += PAGE_SIZE;
            fetchPage();
          } else {
            if (inBackground) setItems(loaded);
            setLoadingMedia(false);
          }
        })
        .catch((e) => {
          if (cancelled) return;
          setListError(e.message);
          setLoadingMedia(false);
        });
    }

    fetchPage();

    return () => { cancelled = true; };
  }, [filters, mediaReload, revisit]);

  useEffect(() => {
    api.get<Trip[]>("/trips").then(setTrips).catch(() => {});
    // The filter panel then just lists no albums.
    api.listAlbums().then(setAlbums).catch((e) => console.error("Failed to load albums:", e));
  }, [revisit]);

  // An album menu change only alters the map when the filters select that album.
  function handleAlbumChange(albumId: string) {
    if (filters.albumIds.includes(albumId)) setMediaReload((n) => n + 1);
  }

  function handleAlbumCreated(album: Album) {
    setAlbums((prev) => (prev.some((a) => a.id === album.id) ? prev : [...prev, album]));
  }

  const activeCount = activeFilterCount(filters);
  // Shared items are read-only: the backend 404s their writes, so hide the controls.
  const isOwn = selected?.owner_id === user.id;

  function fetchUrl(item: MediaItem) {
    pendingId.current = item.id;
    setLoading(true);
    api
      .getMediaUrl(item.id)
      .then((url) => { if (pendingId.current === item.id) setMediaUrl(url); })
      .catch((e) => { if (pendingId.current === item.id) setUrlError(e.message); })
      .finally(() => { if (pendingId.current === item.id) setLoading(false); });
  }

  async function handleDeleteMedia(item: MediaItem) {
    if (!confirm(`Delete "${item.filename}"? This cannot be undone.`)) return;
    try {
      await api.delete(`/media/${item.id}`);
      setItems((prev) => prev.filter((i) => i.id !== item.id));
      setSelected(null);
      setMediaUrl(null);
      setClusterPeers((prev) => {
        const next = prev.filter((i) => i.id !== item.id);
        setClusterIndex((idx) => Math.min(idx, Math.max(0, next.length - 1)));
        return next;
      });
    } catch (e) {
      console.error("Failed to delete media:", e);
    }
  }

  function handleSelect(item: MediaItem | null, peers: MediaItem[] = []) {
    setSelected(item);
    setClusterPeers(peers);
    setClusterIndex(peers.findIndex((p) => p.id === item?.id));
    setMediaUrl(null);
    setUrlError(null);
    setFullscreen(false);
    if (!item) return;
    fetchUrl(item);
    if (item.media_type === "video_360") {
      api.getKeyframes(item.id).then((kf) => {
        setSelected((prev) => prev?.id === item.id ? { ...prev, keyframes: kf } : prev);
      }).catch(() => {});
    }
  }

  function navigatePeer(delta: number) {
    if (clusterPeers.length <= 1) return;
    const newIndex = (clusterIndex + delta + clusterPeers.length) % clusterPeers.length;
    const newItem = clusterPeers[newIndex];
    setClusterIndex(newIndex);
    setSelected(newItem);
    setMediaUrl(null);
    setUrlError(null);
    fetchUrl(newItem);
  }

  useEffect(() => {
    // Hidden behind another tab, the arrow keys are not ours. The fullscreen view handles them
    // itself, and behind an editor they would switch the item being edited.
    const covered = fullscreen || editingView || editingLocation;
    if (!active || covered || clusterPeers.length <= 1) return;
    function onKey(e: KeyboardEvent) {
      // Behind the item panel's album menu, they would switch the item (and close the menu).
      if (keyBelongsToDialog(e)) return;
      if (e.key === "ArrowLeft") navigatePeer(-1);
      if (e.key === "ArrowRight") navigatePeer(1);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // navigatePeer reads only clusterPeers and clusterIndex, which are listed.
  }, [active, fullscreen, editingView, editingLocation, clusterPeers, clusterIndex]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="relative h-full w-full">
      <FilterPanel
        trips={trips}
        albums={albums}
        filters={filters}
        onChange={setFilters}
        activeCount={activeCount}
      />

      {listError && (
        <div className="absolute left-4 top-16 z-10 rounded bg-destructive px-3 py-2 text-sm text-white">
          {listError}
        </div>
      )}

      {/* Beside the zoom control, not over it: the control ends 44 px from the left, 58 px with
          its touch-sized buttons. */}
      {loadingMedia && (
        <div className="absolute bottom-2.5 left-14 z-[800] rounded bg-background/80 px-2 py-1 text-xs text-muted-foreground touch:left-[4.25rem]">
          Loading…
        </div>
      )}

      <MediaMap items={items} visible={active} onSelect={(item, peers) => handleSelect(item, peers)} />

      {/* A sheet across the bottom of a phone, a card at the top right otherwise; both scroll
          within the map. They show only their top before scrolling, so on a phone (upright or
          sideways) the details, the preview and the peer navigation come first (negative order)
          and the actions after; on a desktop the card is a plain block and keeps the source
          order. The title row sticks to the top, so Delete and Close stay in reach; it carries
          the top padding itself, so nothing scrolls past above it. */}
      {selected && (
        <div
          aria-label="Selected media"
          role="region"
          className="absolute inset-x-2 bottom-2 z-[800] max-h-[55%] overflow-y-auto overscroll-contain rounded-lg border bg-background px-3 pb-3 shadow-lg sm:inset-x-auto sm:bottom-auto sm:right-4 sm:top-4 sm:max-h-[calc(100%-2rem)] sm:w-72"
        >
          <div className="flex flex-col sm:block short:flex">
            <div className="sticky top-0 z-10 -order-3 flex items-start justify-between gap-2 bg-background pb-1 pt-3">
              <p className="min-w-0 font-medium truncate touch:self-center">{selected.filename}</p>
              <div className="flex items-center gap-1 shrink-0">
                {isOwn && (
                  <button
                    className={`text-muted-foreground hover:text-destructive ${touchIcon}`}
                    onClick={() => handleDeleteMedia(selected)}
                    aria-label="Delete media"
                    title="Delete media"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                )}
                <button
                  className={`text-muted-foreground hover:text-foreground ${touchIcon}`}
                  onClick={() => handleSelect(null)}
                  aria-label="Close panel"
                >
                  ✕
                </button>
              </div>
            </div>

            {!isOwn && (
              <p className="-order-3 text-xs text-muted-foreground">
                Shared by {selected.owner_name ?? "another user"}
              </p>
            )}

            {selected.taken_at && (
              <p className="-order-3 text-sm text-muted-foreground">
                {new Date(selected.taken_at).toLocaleString()}
              </p>
            )}

            {selected.gps_source && (
              <p className="-order-3 mt-1 text-xs text-muted-foreground">
                GPS: {selected.gps_source}
                {selected.gps_confidence !== null &&
                  ` (${Math.round(selected.gps_confidence * 100)}%)`}
              </p>
            )}

            {selected.lat !== null && selected.lng !== null && (
              <a
                href={`https://www.google.com/maps/@?api=1&map_action=pano&viewpoint=${selected.lat},${selected.lng}`}
                target="_blank"
                rel="noopener noreferrer"
                className={`mt-1 flex items-center justify-center gap-1 w-full rounded border border-border px-2 py-1 text-xs hover:bg-muted ${touchRow}`}
              >
                <ExternalLink className="h-3 w-3" />
                Open in Street View
              </a>
            )}

            {isOwn && (
              <button
                className={`mt-1 w-full rounded border border-border px-2 py-1 text-xs hover:bg-muted ${touchRow}`}
                onClick={() => setEditingLocation(true)}
              >
                Edit GPS location
              </button>
            )}

            <AlbumMenu
              item={selected}
              isOwn={isOwn}
              onChange={handleAlbumChange}
              onAlbumCreated={handleAlbumCreated}
            />

            {isOwn && (selected.media_type === "photo_360" || selected.media_type === "video_360") && (
              <button
                className={`mt-2 w-full rounded border border-border px-2 py-1 text-xs hover:bg-muted ${touchRow}`}
                onClick={() => setEditingView(true)}
              >
                Edit default view
              </button>
            )}

            {clusterPeers.length > 1 && (
              <div className="-order-1 mt-2 flex items-center justify-between">
                <button
                  onClick={() => navigatePeer(-1)}
                  className={`px-2 py-1 rounded text-lg leading-none hover:bg-muted ${touchIcon}`}
                  aria-label="Previous"
                >
                  ‹
                </button>
                <span className="text-xs text-muted-foreground">
                  {clusterIndex + 1} / {clusterPeers.length}
                </span>
                <button
                  onClick={() => navigatePeer(1)}
                  className={`px-2 py-1 rounded text-lg leading-none hover:bg-muted ${touchIcon}`}
                  aria-label="Next"
                >
                  ›
                </button>
              </div>
            )}

            <div className="-order-2">
              {loading && (
                <p className="mt-2 text-xs text-muted-foreground animate-pulse">
                  Loading preview…
                </p>
              )}

              {urlError && (
                <p className="mt-2 text-xs text-destructive">
                  Failed to load preview
                </p>
              )}

              {mediaUrl && !loading && (
                <MediaPreview
                  item={selected}
                  url={mediaUrl}
                  onFullscreen={() => setFullscreen(true)}
                />
              )}
            </div>
          </div>
        </div>
      )}

      {fullscreen && selected && (
        <FullscreenOverlay
          item={selected}
          url={mediaUrl}
          failed={urlError !== null}
          onClose={() => setFullscreen(false)}
          onPrev={clusterPeers.length > 1 ? () => navigatePeer(-1) : undefined}
          onNext={clusterPeers.length > 1 ? () => navigatePeer(1) : undefined}
          peerLabel={clusterPeers.length > 1 ? `${clusterIndex + 1} / ${clusterPeers.length}` : undefined}
        />
      )}

      {editingLocation && selected && isOwn && (
        <EditLocationModal
          item={selected}
          onClose={() => setEditingLocation(false)}
          onSaved={(lat, lng) => {
            const updated = { lat, lng, gps_source: "manual" as const, gps_confidence: 1.0 };
            setSelected((prev) => (prev ? { ...prev, ...updated } : prev));
            setItems((prev) => prev.map((i) => (i.id === selected.id ? { ...i, ...updated } : i)));
            setEditingLocation(false);
          }}
        />
      )}

      {editingView && selected && mediaUrl && isOwn && (
        <EditViewModal
          item={selected}
          url={mediaUrl}
          onClose={() => setEditingView(false)}
          onSaved={(updated) => {
            setSelected((prev) => prev ? { ...prev, ...updated } : prev);
            setEditingView(false);
          }}
        />
      )}
    </div>
  );
}
