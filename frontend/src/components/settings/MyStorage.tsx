import { useCallback, useEffect, useRef, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { api } from "@/api/client";
import type { MyStorage as MyStorageReport, StorageAlbum } from "@/types";
import { formatCount } from "@/lib/numbers";
import { albumOwnerLabel, itemsLabel, ownUnknownSizesNote, sizeLabel, tripDays } from "@/lib/storage";

export const MY_STORAGE_FAILED = "Could not load your storage use.";

const ROW_BUTTON_CLASS =
  "rounded-md border px-2 py-1 text-xs font-medium touch:min-h-11 touch:px-3 touch:text-sm";
// The columns a phone has no room for: their values are folded into the first cell there.
const WIDE_CELL = "hidden py-1 pr-3 sm:table-cell";
const NUMBER_CELL = "hidden py-1 pr-3 text-right tabular-nums sm:table-cell";
const TRIPS_ID = "my-storage-trips";
const ALBUMS_ID = "my-storage-albums";

// The Settings page's "Storage": how much the caller's own items take, in all, per trip and per
// album (GET /api/me/storage). Items others share with the caller are their owners' storage and
// never count here. It loads with the page: these are aggregates over the caller's own rows.
export function MyStorage() {
  const [storage, setStorage] = useState<MyStorageReport | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  // One breakdown at a time, right below its button: the trips can run to hundreds of rows.
  const [view, setView] = useState<"trips" | "albums" | null>(null);
  const toggle = (v: "trips" | "albums") => setView((open) => (open === v ? null : v));
  const request = useRef<AbortController | null>(null);

  const load = useCallback(() => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoadFailed(false);
    api
      .myStorage(controller.signal)
      .then((s) => {
        if (!controller.signal.aborted) setStorage(s);
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        console.error(e);
        setLoadFailed(true);
      });
  }, []);

  useEffect(() => {
    load();
    return () => request.current?.abort();
  }, [load]);

  const note = storage && ownUnknownSizesNote(storage);
  return (
    <section aria-labelledby="my-storage-heading">
      <h2 id="my-storage-heading" className="mb-2 text-lg font-medium">
        Storage
      </h2>
      <p className="mb-3 max-w-prose text-sm text-muted-foreground">
        How much storage your items take, in all, per trip and per album. Items others share with you count for them,
        not for you.
      </p>
      {storage === null && !loadFailed && <p className="text-sm text-muted-foreground">Loading your storage use…</p>}
      {loadFailed && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-red-500">{MY_STORAGE_FAILED}</span>
          <button type="button" onClick={load} className={`${ROW_BUTTON_CLASS} hover:bg-muted`}>
            Try again
          </button>
        </div>
      )}
      {storage && storage.items === 0 && <p className="text-sm text-muted-foreground">You have no items yet.</p>}
      {storage && storage.items > 0 && (
        <>
          <p className="text-sm font-medium">
            Your library: {itemsLabel(storage.items)}, {sizeLabel(storage)}
          </p>
          {note && (
            <p className="mt-2 flex max-w-prose gap-2 text-sm">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" aria-hidden="true" />
              <span>{note}</span>
            </p>
          )}
          <div className="mt-3 flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => toggle("trips")}
              aria-expanded={view === "trips"}
              aria-controls={view === "trips" ? TRIPS_ID : undefined}
              className={`${ROW_BUTTON_CLASS} hover:bg-muted`}
            >
              By trip ({formatCount(storage.trips.length)})
            </button>
            <button
              type="button"
              onClick={() => toggle("albums")}
              aria-expanded={view === "albums"}
              aria-controls={view === "albums" ? ALBUMS_ID : undefined}
              className={`${ROW_BUTTON_CLASS} hover:bg-muted`}
            >
              By album ({formatCount(storage.albums.length)})
            </button>
          </div>
          {view === "trips" && <TripsTable storage={storage} />}
          {view === "albums" && <AlbumsTable albums={storage.albums} />}
        </>
      )}
    </section>
  );
}

function TripsTable({ storage }: { storage: MyStorageReport }) {
  return (
    <div id={TRIPS_ID} className="mt-3 overflow-x-auto">
      <table className="w-full text-left text-sm" aria-label="Your storage by trip">
        <thead className="hidden border-b text-muted-foreground sm:table-header-group">
          <tr>
            <th className="py-1 pr-3 font-medium">Trip</th>
            <th className="py-1 pr-3 font-medium">Days</th>
            <th className="py-1 pr-3 text-right font-medium">Items</th>
            <th className="py-1 pr-3 text-right font-medium">Size</th>
          </tr>
        </thead>
        <tbody>
          {storage.trips.map((t) => (
            <tr key={t.id} className="border-b align-top">
              <td className="py-1 pr-3 [overflow-wrap:anywhere]">
                {t.name}
                <p className="mt-0.5 text-xs text-muted-foreground sm:hidden">
                  {tripDays(t)} · {itemsLabel(t.items)} · {sizeLabel(t)}
                </p>
              </td>
              <td className={`${WIDE_CELL} whitespace-nowrap`}>{tripDays(t)}</td>
              <td className={NUMBER_CELL}>{formatCount(t.items)}</td>
              <td className={NUMBER_CELL}>{sizeLabel(t)}</td>
            </tr>
          ))}
          <tr className="border-b align-top italic">
            <td className="py-1 pr-3">
              Not in a trip
              <p className="mt-0.5 text-xs not-italic text-muted-foreground sm:hidden">
                {itemsLabel(storage.no_trip.items)} · {sizeLabel(storage.no_trip)}
              </p>
            </td>
            <td className={WIDE_CELL} />
            <td className={NUMBER_CELL}>{formatCount(storage.no_trip.items)}</td>
            <td className={NUMBER_CELL}>{sizeLabel(storage.no_trip)}</td>
          </tr>
        </tbody>
      </table>
    </div>
  );
}

function AlbumsTable({ albums }: { albums: StorageAlbum[] }) {
  return (
    <div id={ALBUMS_ID} className="mt-3">
      <p className="mb-2 max-w-prose text-sm text-muted-foreground">
        Only your own items in each album count here. An item in several albums counts in each: albums hold no copies,
        so they take no space of their own.
      </p>
      {albums.length === 0 ? (
        <p className="text-sm text-muted-foreground">None of your items is in an album.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm" aria-label="Your storage by album">
            <thead className="hidden border-b text-muted-foreground sm:table-header-group">
              <tr>
                <th className="py-1 pr-3 font-medium">Album</th>
                <th className="py-1 pr-3 font-medium">Owner</th>
                <th className="py-1 pr-3 text-right font-medium">Your items</th>
                <th className="py-1 pr-3 text-right font-medium">Size</th>
              </tr>
            </thead>
            <tbody>
              {albums.map((a) => (
                <tr key={a.id} className="border-b align-top">
                  <td className="py-1 pr-3 [overflow-wrap:anywhere]">
                    {a.name}
                    <p className="mt-0.5 text-xs text-muted-foreground sm:hidden">
                      {albumOwnerLabel(a)} · {itemsLabel(a.items)} · {sizeLabel(a)}
                    </p>
                  </td>
                  <td className={`${WIDE_CELL} [overflow-wrap:anywhere]`}>{albumOwnerLabel(a)}</td>
                  <td className={NUMBER_CELL}>{formatCount(a.items)}</td>
                  <td className={NUMBER_CELL}>{sizeLabel(a)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
