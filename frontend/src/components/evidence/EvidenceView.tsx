import { useEffect, useRef, useState } from "react";
import { ArrowLeft, ChevronLeft, ChevronRight, X } from "lucide-react";
import { api, ApiError } from "@/api/client";
import { EditLocationModal } from "@/components/EditLocationModal";
import { EvidenceMap } from "@/components/evidence/EvidenceMap";
import { PlacementCard } from "@/components/evidence/PlacementCard";
import { PointCard, type PickedPoint } from "@/components/evidence/PointCard";
import { TimeCard } from "@/components/evidence/TimeCard";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { ITEM_GONE, leadSentence, NOT_OWNER, REMATCH_FAILED, stateBadge } from "@/lib/evidence";
import { useCloseOnBack } from "@/lib/useCloseOnBack";
import { cn } from "@/lib/utils";
import type { MediaItem, ProvenanceResponse } from "@/types";

export interface EvidenceViewProps {
  mediaId: string;
  isOwn: boolean;
  onClose: () => void;
  // Absent when fewer than two own items can be stepped; both parents wrap.
  onPrev?: () => void;
  onNext?: () => void;
  position?: string; // "3 / 12", counted among own items
  onLocationSaved: (id: string, lat: number, lng: number) => void;
  onRematched: () => void;
}

const TITLE = "Why here?";

const headerButton =
  "flex items-center justify-center text-muted-foreground hover:text-foreground touch:min-h-11 touch:min-w-11";
const actionButton =
  "rounded border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50 touch:min-h-11";

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// The page behind the view is covered but still in the tab order, and reaching its location editor
// would let an arrow key change the item under that editor.
function keepTabInside(e: KeyboardEvent, root: HTMLElement | null) {
  if (!root) return;
  const items = root.querySelectorAll<HTMLElement>(FOCUSABLE);
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement;
  if (!first || !last) {
    e.preventDefault();
  } else if (!root.contains(active)) {
    e.preventDefault();
    first.focus();
  } else if (e.shiftKey && (active === first || active === root)) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && active === last) {
    e.preventDefault();
    first.focus();
  }
}

function isTextField(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null;
  return (
    element?.tagName === "INPUT" ||
    element?.tagName === "TEXTAREA" ||
    element?.tagName === "SELECT" ||
    element?.isContentEditable === true
  );
}

export function EvidenceView({
  mediaId,
  isOwn,
  onClose,
  onPrev,
  onNext,
  position,
  onLocationSaved,
  onRematched,
}: EvidenceViewProps) {
  // The item behind a track point, opened from a chip. One level: Back always returns to mediaId,
  // because the parent's stepping is relative to it.
  const [jump, setJump] = useState<{ id: string; backTo: string } | null>(null);
  const [jumpFor, setJumpFor] = useState(mediaId);
  if (jumpFor !== mediaId) {
    setJumpFor(mediaId);
    setJump(null);
  }
  const shownId = jump?.id ?? mediaId;

  const [response, setResponse] = useState<ProvenanceResponse | null>(null);
  const [failure, setFailure] = useState<"gone" | "failed" | null>(null);
  const [reload, setReload] = useState(0);
  // Which reload the response on screen answers: a re-match or a save is not reflected until then.
  const [answered, setAnswered] = useState(-1);
  const [point, setPoint] = useState<PickedPoint | null>(null);
  const [rematching, setRematching] = useState(false);
  const [rematchFailed, setRematchFailed] = useState(false);
  // A copy taken when the editor opens, so a late response cannot swap the item under it.
  const [editor, setEditor] = useState<Pick<MediaItem, "id" | "filename" | "lat" | "lng"> | null>(null);

  const [factsFor, setFactsFor] = useState(shownId);
  if (factsFor !== shownId) {
    setFactsFor(shownId);
    setPoint(null);
    setRematchFailed(false);
  }

  const rootRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    rootRef.current?.focus();
    return () => {
      if (opener?.isConnected) opener.focus();
    };
  }, []);

  useCloseOnBack(onClose);
  // A second entry while jumped: back returns to mediaId before it closes the view.
  useCloseOnBack(() => setJump(null), jump !== null);

  useEffect(() => {
    if (!isOwn) return;
    let stale = false;
    setFailure(null);
    api
      .mediaProvenance(shownId)
      .then((next) => {
        if (stale) return;
        setResponse(next);
        setAnswered(reload);
        setPoint(null);
      })
      .catch((e) => {
        if (stale) return;
        console.error("Failed to load the item's provenance:", e);
        setFailure(e instanceof ApiError && e.status === 404 ? "gone" : "failed");
      });
    return () => {
      stale = true;
    };
  }, [shownId, reload, isOwn]);

  const jumped = jump !== null;

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (keyBelongsToDialog(e)) return;
      if (e.key === "Tab") {
        keepTabInside(e, rootRef.current);
        return;
      }
      // The editor handles its own Escape. An arrow key here would step the item under it, and
      // Save would then write the old picked position to the new item.
      if (editor) return;
      if (isTextField(e.target)) return;
      if (e.key === "Escape") {
        if (point) setPoint(null);
        else if (jumped) setJump(null);
        else onClose();
      } else if (!jumped) {
        if (e.key === "ArrowLeft") onPrev?.();
        if (e.key === "ArrowRight") onNext?.();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [editor, point, jumped, onClose, onPrev, onNext]);

  // The previous item stays on screen, dimmed, until the next response arrives. Place by hand
  // pressed in that gap would open on the old item and remount onto the new one.
  const inert = response === null || response.item.id !== shownId || failure !== null;
  const actionsInert = inert || answered !== reload || rematching;
  // A step or jump that failed: what is still on screen belongs to the item before it.
  const leftover = failure !== null && response?.item.id !== shownId;

  function jumpTo(id: string) {
    setJump((prev) => ({ id, backTo: prev?.backTo ?? response?.item.filename ?? "" }));
  }

  function retry() {
    setReload((n) => n + 1);
  }

  async function rematch() {
    setRematching(true);
    setRematchFailed(false);
    try {
      await api.runMatcher();
    } catch (e) {
      console.error("Re-match failed:", e);
      setRematchFailed(true);
    } finally {
      // Also after a failure: the matcher has no transaction and may have written some rows.
      setRematching(false);
      setReload((n) => n + 1);
      onRematched();
    }
  }

  function openEditor() {
    if (!response) return;
    const { id, filename, lat, lng } = response.item;
    setEditor({ id, filename, lat, lng });
  }

  function locationSaved(lat: number, lng: number) {
    if (!editor) return;
    const { id } = editor;
    setEditor(null);
    setReload((n) => n + 1);
    onLocationSaved(id, lat, lng);
  }

  const state = response?.placement.state;
  const canRematch = (state === "stale" || state === "pending") && response?.item.gps_source !== "none";

  return (
    <div
      ref={rootRef}
      role="dialog"
      aria-modal="true"
      aria-label={TITLE}
      tabIndex={-1}
      className="fixed inset-0 z-[1100] flex flex-col bg-background text-foreground outline-none"
    >
      <header className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
        <h2 className="shrink-0 text-sm font-semibold">{TITLE}</h2>
        <span
          className={cn("min-w-0 flex-1 truncate text-sm text-muted-foreground", inert && "opacity-60")}
          title={leftover ? undefined : response?.item.filename}
        >
          {!leftover && response?.item.filename}
        </span>
        {jump ? (
          <button
            type="button"
            onClick={() => setJump(null)}
            className={cn(headerButton, "min-w-0 max-w-[45%] gap-1 text-sm")}
          >
            <ArrowLeft className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span className="truncate">Back to {jump.backTo}</span>
          </button>
        ) : (
          <>
            {onPrev && (
              <button type="button" onClick={onPrev} aria-label="Previous" className={headerButton}>
                <ChevronLeft className="h-5 w-5" aria-hidden="true" />
              </button>
            )}
            {position && <span className="shrink-0 text-sm tabular-nums text-muted-foreground">{position}</span>}
            {onNext && (
              <button type="button" onClick={onNext} aria-label="Next" className={headerButton}>
                <ChevronRight className="h-5 w-5" aria-hidden="true" />
              </button>
            )}
          </>
        )}
        <button type="button" onClick={onClose} aria-label="Close" className={headerButton}>
          <X className="h-5 w-5" aria-hidden="true" />
        </button>
      </header>

      {isOwn ? (
        <div className="flex min-h-0 flex-1 flex-col md:flex-row short:flex-row">
          <div
            className={cn(
              "relative h-[40vh] shrink-0 supports-[height:100dvh]:h-[40dvh] md:h-auto md:min-w-0 md:flex-1 short:h-auto short:min-w-0 short:flex-1",
              inert && "pointer-events-none opacity-60"
            )}
          >
            {response && !leftover && <EvidenceMap data={response} inert={inert} onPick={setPoint} />}
            {point && <PointCard point={point} onClose={() => setPoint(null)} />}
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto md:w-[420px] md:flex-none short:w-80 short:flex-none">
            <div className={cn("space-y-4 p-3", inert && !failure && "opacity-60")} aria-busy={inert}>
              {failure === "gone" && <p className="text-sm">{ITEM_GONE}</p>}
              {failure === "failed" && (
                <div className="space-y-2">
                  <p className="text-sm">Could not load this item.</p>
                  <button type="button" onClick={retry} className={actionButton}>
                    Try again
                  </button>
                </div>
              )}
              {/* The refetch after a failed Re-match fails too when the connection is down. */}
              {failure && rematchFailed && <p className="text-sm">{REMATCH_FAILED}</p>}
              {!failure && !response && <p className="text-sm text-muted-foreground">Loading…</p>}
              {!failure && response && (
                <>
                  <section aria-label="Placement" className="space-y-1">
                    <span className="inline-block rounded-full bg-muted px-2 py-0.5 text-xs font-medium">
                      {stateBadge(response.placement.state)}
                    </span>
                    <p className="text-sm">{leadSentence(response)}</p>
                  </section>

                  <TimeCard item={response.item} time={response.time} inert={inert} />

                  <PlacementCard
                    item={response.item}
                    placement={response.placement}
                    inert={inert}
                    onJump={jumpTo}
                  />

                  <section aria-label="Actions" className="space-y-2">
                    <div className="flex flex-wrap gap-2">
                      {canRematch && (
                        <button type="button" onClick={rematch} disabled={actionsInert} className={actionButton}>
                          {rematching ? "Re-matching…" : "Re-match my library"}
                        </button>
                      )}
                      <button type="button" onClick={openEditor} disabled={actionsInert} className={actionButton}>
                        {response.placement.state === "manual" ? "Move by hand" : "Place by hand"}
                      </button>
                    </div>
                    {rematchFailed && <p className="text-sm">{REMATCH_FAILED}</p>}
                  </section>
                </>
              )}
            </div>
          </div>
        </div>
      ) : (
        <p className="p-3 text-sm text-muted-foreground">{NOT_OWNER}</p>
      )}

      {editor && (
        <EditLocationModal key={editor.id} item={editor} onClose={() => setEditor(null)} onSaved={locationSaved} />
      )}
    </div>
  );
}
