import { useEffect, useState } from "react";
import { MediaMap } from "../components/map/MediaMap";
import type { MediaItem } from "../types";
import { api } from "../api/client";

export function MapPage() {
  const [items, setItems] = useState<MediaItem[]>([]);
  const [selected, setSelected] = useState<MediaItem | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .get<MediaItem[]>("/media")
      .then(setItems)
      .catch((e) => setError(e.message));
  }, []);

  return (
    <div className="relative h-full w-full">
      {error && (
        <div className="absolute left-4 top-4 z-10 rounded bg-destructive px-3 py-2 text-sm text-white">
          {error}
        </div>
      )}
      <MediaMap items={items} onSelect={setSelected} />
      {selected && (
        <div className="absolute right-4 top-4 z-10 w-64 rounded-lg border bg-background p-3 shadow-lg">
          <button
            className="float-right text-muted-foreground hover:text-foreground"
            onClick={() => setSelected(null)}
          >
            ✕
          </button>
          <p className="font-medium">{selected.filename}</p>
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
        </div>
      )}
    </div>
  );
}
