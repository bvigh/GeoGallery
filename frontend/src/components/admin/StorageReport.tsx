import { useCallback, useEffect, useRef, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { ApiError, api } from "@/api/client";
import type { AdminStorage } from "@/types";
import { formatCount } from "@/lib/numbers";
import {
  itemsLabel,
  sizeLabel,
  storageUserName,
  storageUserStatus,
  trackPointsLabel,
  unknownSizesNote,
} from "@/lib/storage";

export const STORAGE_FAILED = "Could not count the storage use.";
export const STORAGE_TIMEOUT = "Counting the storage use took too long. Try again later.";

// The columns a phone has no room for: their values are folded into the name cell there.
const NUMBER_CELL = "hidden py-2 pr-3 text-right tabular-nums sm:table-cell";

// The Admin page's storage section: each user's totals, never what is inside their library. Each
// user sees how theirs split by trip and album in Settings. Nothing is counted until the admin asks:
// counting the track points reads the whole track table, which takes seconds on millions of points.
export function StorageReport() {
  const [report, setReport] = useState<AdminStorage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const request = useRef<AbortController | null>(null);

  useEffect(() => () => request.current?.abort(), []);

  const load = useCallback(() => {
    if (request.current) return; // one count at a time; the button only looks disabled
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setError(null);
    api
      .adminStorage(controller.signal)
      .then(setReport)
      .catch((e) => {
        if (controller.signal.aborted) return;
        console.error(e);
        setError(e instanceof ApiError && e.status === 504 ? STORAGE_TIMEOUT : STORAGE_FAILED);
      })
      .finally(() => {
        if (controller.signal.aborted) return;
        request.current = null;
        setLoading(false);
      });
  }, []);

  const note = report && unknownSizesNote(report);
  return (
    <section className="mt-8" aria-labelledby="storage-heading">
      <h2 id="storage-heading" className="mb-2 text-lg font-medium">
        Storage
      </h2>
      <p className="mb-3 max-w-2xl text-sm text-muted-foreground">
        How much storage each user's items take, and how many track points each user has. Each user sees how theirs
        split by trip and album in their Settings. It adds up the files of the items in the library, so a file in
        storage that no item uses is not counted. Counting reads every track point, which can take a few seconds, so
        it runs only when you ask.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        {/* One button throughout, focusable while busy, so the focus never falls to the page. */}
        <button
          onClick={load}
          aria-disabled={loading}
          className="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground aria-disabled:cursor-default aria-disabled:opacity-50 touch:min-h-11"
        >
          {loading ? "Counting…" : report ? "Count again" : "Show storage use"}
        </button>
        <p role="status" className="text-sm text-muted-foreground">
          {loading ? "Counting the storage use…" : ""}
        </p>
      </div>
      {error && (
        <p role="alert" className="mt-2 text-sm text-red-500">
          {error}
        </p>
      )}

      {report && (
        <div className={loading ? "opacity-60" : undefined}>
          <p className="mt-4 text-sm font-medium">
            In all: {itemsLabel(report.items)}, {sizeLabel(report)}, {trackPointsLabel(report.track_points)}
          </p>
          {note && (
            <p className="mt-2 flex max-w-2xl gap-2 text-sm">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" aria-hidden="true" />
              <span>{note}</span>
            </p>
          )}
          <div className="mt-3 overflow-x-auto">
            <table className="w-full text-left text-sm" aria-label="Storage per user">
              <thead className="hidden border-b text-muted-foreground sm:table-header-group">
                <tr>
                  <th className="py-2 pr-3 font-medium">User</th>
                  <th className="py-2 pr-3 text-right font-medium">Items</th>
                  <th className="py-2 pr-3 text-right font-medium">Size</th>
                  <th className="py-2 pr-3 text-right font-medium">Track points</th>
                </tr>
              </thead>
              <tbody>
                {report.users.map((u) => {
                  const status = storageUserStatus(u);
                  return (
                    <tr key={u.id} className="border-b align-top">
                      <td className="py-2 pr-3 [overflow-wrap:anywhere]">
                        {storageUserName(u)}
                        {status && <span className="text-muted-foreground"> ({status})</span>}
                        <p className="mt-0.5 text-xs text-muted-foreground sm:hidden">
                          {itemsLabel(u.items)} · {sizeLabel(u)} · {trackPointsLabel(u.track_points)}
                        </p>
                      </td>
                      <td className={NUMBER_CELL}>{formatCount(u.items)}</td>
                      <td className={NUMBER_CELL}>{sizeLabel(u)}</td>
                      <td className={NUMBER_CELL}>{formatCount(u.track_points)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </section>
  );
}
