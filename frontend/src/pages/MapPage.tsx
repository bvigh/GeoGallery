import { useEffect, useMemo, useRef, useState } from "react";
import { Trash2 } from "lucide-react";
import { MediaMap } from "../components/map/MediaMap";
import { FilterPanel } from "../components/map/FilterPanel";
import { EditLocationModal } from "../components/EditLocationModal";
import type { FilterState, MediaItem, OrientationKeyframe, Trip } from "../types";
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
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft") onPrev?.();
      if (e.key === "ArrowRight") onNext?.();
      if (e.code === "Space" && item.media_type === "video") {
        const target = e.target as HTMLElement;
        if (target === videoRef.current) return; // native controls handle it
        const tag = target?.tagName;
        const editable = target?.isContentEditable;
        if (tag !== "INPUT" && tag !== "TEXTAREA" && tag !== "SELECT" && !editable) {
          e.preventDefault();
          const v = videoRef.current;
          if (v) v.paused ? v.play() : v.pause();
        }
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, onPrev, onNext, item.media_type]);

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
        keyframes={item.keyframes}
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
            ref={videoRef}
            src={url}
            controls
            autoPlay
            className="max-h-[90vh] max-w-[90vw] rounded object-contain outline-none"
          />
        ) : (
          <img
            src={url}
            alt={item.filename}
            className="max-h-[100vh] max-w-[100vw] rounded object-contain"
          />
        )}
      </div>
    </div>
  );
}

function EditViewModal({
  item,
  url,
  onClose,
  onSaved,
}: {
  item: MediaItem;
  url: string;
  onClose: () => void;
  onSaved: (updated: Partial<MediaItem>) => void;
}) {
  const captureRef = useRef<(() => { yaw: number; pitch: number; fov: number }) | null>(null);
  const captureTimeRef = useRef<(() => number) | null>(null);
  const controlRef = useRef<{ pause: () => void; seek: (t: number) => void; seekAndApply: (t: number) => void } | null>(null);
  const [keyframes, setKeyframes] = useState<OrientationKeyframe[]>(item.keyframes ?? []);
  const [saving, setSaving] = useState(false);

  async function handleSetDefaultView() {
    if (!captureRef.current) return;
    const { yaw, pitch, fov } = captureRef.current();
    setSaving(true);
    await api.updateViewSettings(item.id, { initial_yaw: yaw, initial_pitch: pitch, initial_fov: fov });
    setSaving(false);
    onSaved({ initial_yaw: yaw, initial_pitch: pitch, initial_fov: fov });
  }

  function handleAddKeyframe() {
    if (!captureRef.current || !captureTimeRef.current) return;
    controlRef.current?.pause();
    const { yaw, pitch, fov } = captureRef.current();
    const time = captureTimeRef.current();
    setKeyframes((prev) => {
      const without = prev.filter((kf) => Math.abs(kf.time - time) > 0.05);
      return [...without, { time, yaw, pitch, fov }].sort((a, b) => a.time - b.time);
    });
  }

  function handleSeekToKeyframe(time: number) {
    controlRef.current?.seekAndApply(time);
  }

  async function handleSaveKeyframes() {
    setSaving(true);
    await api.replaceKeyframes(item.id, keyframes);
    setSaving(false);
    onSaved({ keyframes });
  }

  function handleDeleteKeyframe(time: number) {
    setKeyframes((prev) => prev.filter((kf) => kf.time !== time));
  }

  const isVideo = item.media_type === "video_360";

  return (
    <div className="fixed inset-0 z-[1100] flex flex-col bg-black">
      <div className="flex items-center justify-between px-4 py-2 bg-black/80 text-white text-sm shrink-0">
        <span className="font-medium">Edit default view — {item.filename}</span>
        <button onClick={onClose} className="text-white hover:text-gray-300 text-xl leading-none">✕</button>
      </div>

      <div className="flex flex-1 overflow-hidden">
        <div className="flex-1 relative">
          {isVideo ? (
            <Video360
              url={url}
              onClose={() => {}}
              contained
              initialYaw={item.initial_yaw}
              initialPitch={item.initial_pitch}
              initialFov={item.initial_fov}
              keyframes={keyframes}
              captureRef={captureRef}
              captureTimeRef={captureTimeRef}
              controlRef={controlRef}
            />
          ) : (
            <Panorama360
              url={url}
              onClose={() => {}}
              contained
              initialYaw={item.initial_yaw}
              initialPitch={item.initial_pitch}
              initialFov={item.initial_fov}
              captureRef={captureRef}
            />
          )}
        </div>

        <div className="w-64 shrink-0 bg-black/90 text-white flex flex-col gap-3 p-4 overflow-y-auto">
          <div>
            <p className="text-xs text-white/60 mb-1">Default view (yaw / pitch / zoom)</p>
            <button
              onClick={handleSetDefaultView}
              disabled={saving}
              className="w-full rounded bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
            >
              Set current view as default
            </button>
          </div>

          {isVideo && (
            <div>
              <p className="text-xs text-white/60 mb-1">Keyframes</p>
              <button
                onClick={handleAddKeyframe}
                className="w-full rounded bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20"
              >
                + Add keyframe at current time
              </button>
              {keyframes.length > 0 && (
                <ul className="mt-2 space-y-1 text-xs">
                  {keyframes.map((kf) => (
                    <li key={kf.time} className="rounded bg-white/5 px-2 py-1">
                      <div className="flex items-center justify-between">
                        <button
                          onClick={() => handleSeekToKeyframe(kf.time)}
                          className="tabular-nums text-left hover:text-primary"
                          title="Jump to this keyframe"
                        >
                          {kf.time.toFixed(1)}s &nbsp;·&nbsp;
                          y:{(kf.yaw * 180 / Math.PI).toFixed(0)}°&nbsp;
                          p:{(kf.pitch * 180 / Math.PI).toFixed(0)}°&nbsp;
                          z:{kf.fov.toFixed(0)}°
                        </button>
                        <div className="flex items-center gap-1 ml-2 shrink-0">
                          <button
                            onClick={() => handleAddKeyframe()}
                            className="text-white/40 hover:text-white text-xs px-1"
                            title="Update this keyframe with current view"
                            aria-label="Update keyframe"
                          >
                            ↺
                          </button>
                          <button
                            onClick={() => handleDeleteKeyframe(kf.time)}
                            className="text-white/40 hover:text-white"
                            aria-label="Delete keyframe"
                          >
                            ✕
                          </button>
                        </div>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
              {keyframes.length > 0 && (
                <button
                  onClick={handleSaveKeyframes}
                  disabled={saving}
                  className="mt-2 w-full rounded bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50"
                >
                  Save keyframes
                </button>
              )}
            </div>
          )}
        </div>
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
  const [editingView, setEditingView] = useState(false);
  const [editingLocation, setEditingLocation] = useState(false);
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
          <div className="flex items-start justify-between gap-2 mb-1">
            <p className="font-medium truncate">{selected.filename}</p>
            <div className="flex items-center gap-1 shrink-0">
              <button
                className="text-muted-foreground hover:text-destructive"
                onClick={() => handleDeleteMedia(selected)}
                aria-label="Delete media"
                title="Delete media"
              >
                <Trash2 className="h-4 w-4" />
              </button>
              <button
                className="text-muted-foreground hover:text-foreground"
                onClick={() => handleSelect(null)}
                aria-label="Close panel"
              >
                ✕
              </button>
            </div>
          </div>

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

          <button
            className="mt-1 w-full rounded border border-border px-2 py-1 text-xs hover:bg-muted"
            onClick={() => setEditingLocation(true)}
          >
            Edit GPS location
          </button>

          {(selected.media_type === "photo_360" || selected.media_type === "video_360") && (
            <button
              className="mt-2 w-full rounded border border-border px-2 py-1 text-xs hover:bg-muted"
              onClick={() => setEditingView(true)}
            >
              Edit default view
            </button>
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

      {editingLocation && selected && (
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

      {editingView && selected && mediaUrl && (
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
