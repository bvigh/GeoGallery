import { useEffect, useMemo, useRef, useState } from "react";
import { MediaMap } from "../components/map/MediaMap";
import { FilterPanel } from "../components/map/FilterPanel";
import type { FilterState, MediaItem, Trip } from "../types";
import { api } from "../api/client";
import Panorama360 from "../components/viewer/Panorama360";
import Video360 from "../components/viewer/Video360";

const PAGE_SIZE = 500;

function MediaPreview({
  item,
  url,
  onFullscreen,
}: {
  item: MediaItem;
  url: string;
  onFullscreen: () => void;
}) {
  const isVideo = item.media_type === "video" || item.media_type === "video_360";
  return (
    <button
      className="mt-2 w-full overflow-hidden rounded cursor-zoom-in"
      onClick={onFullscreen}
      aria-label="Open fullscreen"
    >
      {isVideo ? (
        <video
          src={url}
          className="w-full rounded object-cover"
          style={{ maxHeight: 160 }}
          muted
          playsInline
        />
      ) : (
        <img
          src={url}
          alt={item.filename}
          className="w-full rounded object-cover"
          style={{ maxHeight: 160 }}
        />
      )}
    </button>
  );
}

function FullscreenOverlay({
  item,
  url,
  onClose,
  onPrev,
  onNext,
  peerLabel,
}: {
  item: MediaItem;
  url: string;
  onClose: () => void;
  onPrev?: () => void;
  onNext?: () => void;
  peerLabel?: string;
}) {
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft") onPrev?.();
      if (e.key === "ArrowRight") onNext?.();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, onPrev, onNext]);

  if (item.media_type === "photo_360") {
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

  if (item.media_type === "video_360") {
    return (
      <Video360
        url={url}
        onClose={onClose}
        initialYaw={item.initial_yaw}
        initialPitch={item.initial_pitch}
        initialFov={item.initial_fov}
      />
    );
  }

  return (
    <div
      className="fixed inset-0 z-[1000] flex items-center justify-center bg-black/80"
      onClick={onClose}
    >
      <button
        className="absolute right-4 top-4 text-white text-2xl leading-none hover:opacity-70"
        onClick={onClose}
        aria-label="Close fullscreen"
      >
        ✕
      </button>
      {onPrev && (
        <button
          className="absolute left-4 top-1/2 -translate-y-1/2 text-white text-5xl leading-none hover:opacity-70 select-none"
          onClick={(e) => { e.stopPropagation(); onPrev(); }}
          aria-label="Previous"
        >
          ‹
        </button>
      )}
      {onNext && (
        <button
          className="absolute right-4 top-1/2 -translate-y-1/2 text-white text-5xl leading-none hover:opacity-70 select-none"
          onClick={(e) => { e.stopPropagation(); onNext(); }}
          aria-label="Next"
        >
          ›
        </button>
      )}
      {peerLabel && (
        <div className="absolute bottom-4 left-1/2 -translate-x-1/2 text-white text-sm bg-black/50 px-3 py-1 rounded-full pointer-events-none">
          {peerLabel}
        </div>
      )}
      <div onClick={(e) => e.stopPropagation()}>
        {item.media_type === "video" ? (
          <video
            src={url}
            controls
            autoPlay
            className="max-h-screen max-w-screen-lg rounded"
          />
        ) : (
          <img
            src={url}
            alt={item.filename}
            className="max-h-screen max-w-screen-lg rounded"
          />
        )}
      </div>
    </div>
  );
}

export function MapPage() {
  const [items, setItems] = useState<MediaItem[]>([]);
  const [trips, setTrips] = useState<Trip[]>([]);
  const [filters, setFilters] = useState<FilterState>({
    fromDate: "", toDate: "", tripIds: [], mediaTypes: [],
  });
  const [loadingMedia, setLoadingMedia] = useState(false);
  const [selected, setSelected] = useState<MediaItem | null>(null);
  const [mediaUrl, setMediaUrl] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [urlError, setUrlError] = useState<string | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [clusterPeers, setClusterPeers] = useState<MediaItem[]>([]);
  const [clusterIndex, setClusterIndex] = useState(0);
  const pendingId = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    setItems([]);
    setLoadingMedia(true);
    setListError(null);

    const params: Record<string, string | string[]> = {};
    if (filters.fromDate) params.from_date = filters.fromDate;
    if (filters.toDate) params.to_date = filters.toDate;
    if (filters.tripIds.length) params.trip_id = filters.tripIds;
    if (filters.mediaTypes.length) params.media_type = filters.mediaTypes;

    let offset = 0;

    function fetchPage() {
      api
        .get<MediaItem[]>("/media", { ...params, offset: String(offset) })
        .then((page) => {
          if (cancelled) return;
          setItems((prev) => [...prev, ...page]);
          if (page.length === PAGE_SIZE) {
            offset += PAGE_SIZE;
            fetchPage();
          } else {
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
  }, [filters]);

  useEffect(() => {
    api.get<Trip[]>("/trips").then(setTrips).catch(() => {});
  }, []);

  const activeCount = useMemo(() => {
    let n = 0;
    if (filters.fromDate || filters.toDate) n++;
    if (filters.tripIds.length > 0) n++;
    if (filters.mediaTypes.length > 0) n++;
    return n;
  }, [filters]);

  function fetchUrl(item: MediaItem) {
    pendingId.current = item.id;
    setLoading(true);
    api
      .getMediaUrl(item.id)
      .then((url) => { if (pendingId.current === item.id) setMediaUrl(url); })
      .catch((e) => { if (pendingId.current === item.id) setUrlError(e.message); })
      .finally(() => { if (pendingId.current === item.id) setLoading(false); });
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
    if (clusterPeers.length <= 1) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "ArrowLeft") navigatePeer(-1);
      if (e.key === "ArrowRight") navigatePeer(1);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [clusterPeers, clusterIndex]);

  return (
    <div className="relative h-full w-full">
      <FilterPanel
        trips={trips}
        filters={filters}
        onChange={setFilters}
        activeCount={activeCount}
      />

      {listError && (
        <div className="absolute left-4 top-16 z-10 rounded bg-destructive px-3 py-2 text-sm text-white">
          {listError}
        </div>
      )}

      {loadingMedia && (
        <div className="absolute bottom-4 left-4 z-[800] rounded bg-background/80 px-2 py-1 text-xs text-muted-foreground">
          Loading…
        </div>
      )}

      <MediaMap items={items} onSelect={(item, peers) => handleSelect(item, peers)} />

      {selected && (
        <div className="absolute right-4 top-4 z-[800] w-72 rounded-lg border bg-background p-3 shadow-lg">
          <button
            className="float-right text-muted-foreground hover:text-foreground"
            onClick={() => handleSelect(null)}
            aria-label="Close panel"
          >
            ✕
          </button>

          <p className="font-medium truncate pr-6">{selected.filename}</p>

          {selected.taken_at && (
            <p className="text-sm text-muted-foreground">
              {new Date(selected.taken_at).toLocaleString()}
            </p>
          )}

          {selected.gps_source && (
            <p className="mt-1 text-xs text-muted-foreground">
              GPS: {selected.gps_source}
              {selected.gps_confidence !== null &&
                ` (${Math.round(selected.gps_confidence * 100)}%)`}
            </p>
          )}

          {clusterPeers.length > 1 && (
            <div className="mt-2 flex items-center justify-between">
              <button
                onClick={() => navigatePeer(-1)}
                className="px-2 py-1 rounded text-lg leading-none hover:bg-muted"
                aria-label="Previous"
              >
                ‹
              </button>
              <span className="text-xs text-muted-foreground">
                {clusterIndex + 1} / {clusterPeers.length}
              </span>
              <button
                onClick={() => navigatePeer(1)}
                className="px-2 py-1 rounded text-lg leading-none hover:bg-muted"
                aria-label="Next"
              >
                ›
              </button>
            </div>
          )}

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
      )}

      {fullscreen && selected && mediaUrl && (
        <FullscreenOverlay
          item={selected}
          url={mediaUrl}
          onClose={() => setFullscreen(false)}
          onPrev={clusterPeers.length > 1 ? () => navigatePeer(-1) : undefined}
          onNext={clusterPeers.length > 1 ? () => navigatePeer(1) : undefined}
          peerLabel={clusterPeers.length > 1 ? `${clusterIndex + 1} / ${clusterPeers.length}` : undefined}
        />
      )}
    </div>
  );
}
