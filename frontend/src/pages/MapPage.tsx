import { useEffect, useRef, useState } from "react";
import { MediaMap } from "../components/map/MediaMap";
import type { MediaItem } from "../types";
import { api } from "../api/client";

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
}: {
  item: MediaItem;
  url: string;
  onClose: () => void;
}) {
  const isVideo = item.media_type === "video" || item.media_type === "video_360";

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

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
      <div onClick={(e) => e.stopPropagation()}>
        {isVideo ? (
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
  const [selected, setSelected] = useState<MediaItem | null>(null);
  const [mediaUrl, setMediaUrl] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [urlError, setUrlError] = useState<string | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const pendingId = useRef<string | null>(null);

  useEffect(() => {
    api
      .get<MediaItem[]>("/media")
      .then(setItems)
      .catch((e) => setListError(e.message));
  }, []);

  function handleSelect(item: MediaItem | null) {
    setSelected(item);
    setMediaUrl(null);
    setUrlError(null);
    setFullscreen(false);
    if (!item) return;

    pendingId.current = item.id;
    setLoading(true);
    api
      .getMediaUrl(item.id)
      .then((url) => {
        if (pendingId.current === item.id) setMediaUrl(url);
      })
      .catch((e) => {
        if (pendingId.current === item.id) setUrlError(e.message);
      })
      .finally(() => {
        if (pendingId.current === item.id) setLoading(false);
      });
  }

  return (
    <div className="relative h-full w-full">
      {listError && (
        <div className="absolute left-4 top-4 z-10 rounded bg-destructive px-3 py-2 text-sm text-white">
          {listError}
        </div>
      )}

      <MediaMap items={items} onSelect={handleSelect} />

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
        />
      )}
    </div>
  );
}
