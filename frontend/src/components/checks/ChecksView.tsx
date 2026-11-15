import { useEffect, useRef, useState, type ReactNode } from "react";
import { RefreshCw, X } from "lucide-react";
import { api, ApiError } from "@/api/client";
import { Findings } from "@/components/checks/Findings";
import { FormatResiduals } from "@/components/checks/FormatResiduals";
import { TrackFiles } from "@/components/checks/TrackFiles";
import { EvidenceView } from "@/components/evidence/EvidenceView";
import { MediaGalleryOverlay } from "@/components/MediaGalleryOverlay";
import {
  CHECKS_TIMEOUT,
  madeProgress,
  mergeComparison,
  NO_COMPARISON,
  type AgreementState,
  type Comparison,
} from "@/lib/checks";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { useCloseOnBack } from "@/lib/history";
import { browserTimeZone, formatDuration, formatInstant } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { AgreementResponse, ChecksResponse, ProvenanceTrip } from "@/types";

const TITLE = "Checks";

// Refresh is busy while the checks run, never disabled: a disabled button would drop the keyboard
// focus to <body>.
const headerButton =
  "flex items-center justify-center gap-1 text-sm text-muted-foreground hover:text-foreground aria-disabled:cursor-default aria-disabled:opacity-50 aria-disabled:hover:text-muted-foreground touch:min-h-11 touch:min-w-11";
const actionButton = "rounded border border-border px-3 py-1.5 text-sm hover:bg-muted touch:min-h-11";

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// As in EvidenceView: the page behind the view is covered but still in the tab order.
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

function Pair({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap gap-x-1.5">
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

// The items of one finding, stepped through in the evidence view.
interface OpenFinding {
  ids: string[];
  index: number;
}

// The trip gallery a finding's item is shown in, in place of the evidence view.
interface TripView {
  trip: ProvenanceTrip;
  itemId: string;
}

// A comparison asked for: from the first log file, or on from where `resume` stopped.
interface CompareRun {
  id: number;
  resume: Comparison | null;
}

// The library-wide checks (docs/gps-debugging.md, Step 2), full screen over the Trips page. Nothing
// here writes: a finding opens its items in the evidence view, which places them by hand or
// re-matches through the existing routes.
export function ChecksView({ onClose }: { onClose: () => void }) {
  const [data, setData] = useState<ChecksResponse | null>(null);
  const [failure, setFailure] = useState<"timeout" | "failed" | null>(null);
  const [reload, setReload] = useState(0);
  // Which reload the checks on screen answer; until the latest does, they stay, dimmed.
  const [answered, setAnswered] = useState(-1);
  const [agreement, setAgreement] = useState<AgreementState>(NO_COMPARISON);
  const [compareRun, setCompareRun] = useState<CompareRun | null>(null);
  const [finding, setFinding] = useState<OpenFinding | null>(null);
  // A finding's item shown in its trip, among the photos around it. It replaces the evidence view,
  // which comes back on the same item when the gallery closes: both change in one commit, so the
  // gallery takes over the evidence view's history entry and gives it back (useCloseOnBack).
  const [tripView, setTripView] = useState<TripView | null>(null);
  // A location saved or a re-match run in the evidence view: the checks are out of date once it
  // closes. A ref, not the state: the evidence view reports a re-match when the matcher answers,
  // through the callback of the render that started it, which can be after it closed.
  const findingOpen = useRef(false);
  const findingChanged = useRef(false);
  const [timeZone] = useState(browserTimeZone);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    rootRef.current?.focus();
    return () => {
      if (opener?.isConnected) opener.focus();
    };
  }, []);

  useCloseOnBack(onClose);

  // Closing the view or loading again cancels the request in flight.
  useEffect(() => {
    const controller = new AbortController();
    setFailure(null);
    api
      .checks(controller.signal)
      .then((next) => {
        if (controller.signal.aborted) return;
        setData(next);
        setAnswered(reload);
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        console.error("Failed to run the checks:", e);
        setFailure(e instanceof ApiError && e.status === 504 ? "timeout" : "failed");
      });
    return () => controller.abort();
  }, [reload]);

  // A comparison takes as many requests as the server needs: each answers as far as its time budget
  // goes, and the next goes on from there. It stops, keeping what it has, when an answer compares
  // nothing or a request fails. Closing the view or starting over cancels the request in flight.
  useEffect(() => {
    if (compareRun === null) return;
    const controller = new AbortController();
    const { signal } = controller;
    const { resume } = compareRun;
    // A new comparison leaves the last one on screen until its first answer.
    setAgreement((previous) => ({ status: "loading", data: resume ?? previous.data, answered: resume !== null }));

    async function compare(comparison: Comparison | null) {
      let from = comparison?.next ?? 0;
      for (;;) {
        let answer: AgreementResponse;
        try {
          answer = await api.checksAgreement(from, signal);
        } catch (e) {
          if (signal.aborted) return;
          console.error("Failed to compare the track files:", e);
          const status = e instanceof ApiError && e.status === 503 ? "busy" : "failed";
          setAgreement((previous) => (comparison ? { status, data: comparison, answered: true } : { ...previous, status }));
          return;
        }
        if (signal.aborted) return;
        const progressed = madeProgress(from, answer);
        comparison = mergeComparison(comparison, answer);
        if (comparison.next === null) {
          setAgreement({ status: "done", data: comparison, answered: true });
          return;
        }
        if (!progressed) {
          setAgreement({ status: "stopped", data: comparison, answered: true });
          return;
        }
        setAgreement({ status: "loading", data: comparison, answered: true });
        from = comparison.next;
      }
    }
    void compare(resume);
    return () => controller.abort();
  }, [compareRun]);

  const evidenceOpen = finding !== null;

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (keyBelongsToDialog(e)) return;
      // The evidence view, or the trip gallery in its place, handles every key itself, Tab included.
      if (evidenceOpen) return;
      if (e.key === "Tab") {
        keepTabInside(e, rootRef.current);
        return;
      }
      if (isTextField(e.target)) return;
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [evidenceOpen, onClose]);

  const loading = failure === null && answered !== reload;

  function load() {
    setReload((n) => n + 1);
  }

  function tryAgain() {
    // The button goes with the failure: the focus moves to the view rather than fall to <body>.
    rootRef.current?.focus();
    load();
  }

  function startComparison(resume: Comparison | null) {
    setCompareRun((run) => ({ id: (run?.id ?? 0) + 1, resume }));
  }

  function openFinding(ids: string[]) {
    findingOpen.current = true;
    findingChanged.current = false;
    setFinding({ ids, index: 0 });
  }

  function stepFinding(delta: number) {
    setFinding((open) => open && { ...open, index: (open.index + delta + open.ids.length) % open.ids.length });
  }

  function closeFinding() {
    findingOpen.current = false;
    setFinding(null);
    setTripView(null);
    if (findingChanged.current) {
      findingChanged.current = false;
      load();
    }
  }

  // While the evidence view is open, its changes wait for it to close, so stepping through a
  // finding and placing items one by one does not run the checks after each. A re-match that
  // answers after it closed runs them at once.
  function markFindingChanged() {
    if (findingOpen.current) findingChanged.current = true;
    else load();
  }

  function showInTrip(trip: ProvenanceTrip, itemId: string) {
    setTripView({ trip, itemId });
  }

  const steps = finding !== null && finding.ids.length > 1;

  return (
    <div
      ref={rootRef}
      role="dialog"
      aria-modal="true"
      aria-label={TITLE}
      tabIndex={-1}
      className="fixed inset-0 z-[1000] flex flex-col bg-background text-foreground outline-none"
    >
      <header className="shrink-0 border-b border-border px-3 py-2">
        <div className="flex items-center gap-2">
          <h2 className="min-w-0 flex-1 text-sm font-semibold">{TITLE}</h2>
          <button type="button" onClick={loading ? undefined : load} aria-disabled={loading} className={headerButton}>
            <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} aria-hidden="true" />
            Refresh
          </button>
          <button type="button" onClick={onClose} aria-label="Close" className={headerButton}>
            <X className="h-5 w-5" aria-hidden="true" />
          </button>
        </div>
        {data && !failure && (
          <dl className={cn("mt-1 flex flex-wrap gap-x-4 gap-y-0.5 text-xs", loading && "opacity-60")}>
            <Pair label="Checked, UTC">{formatInstant(data.generated_at, "UTC")}</Pair>
            <Pair label="Checked, your browser">{formatInstant(data.generated_at, timeZone)}</Pair>
            <Pair label="Match window">{formatDuration(data.window_s)}</Pair>
          </dl>
        )}
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {/* The track files table takes a wide screen; the prose above it keeps a readable width. */}
        <div className={cn("mx-auto max-w-screen-2xl space-y-6 p-3 sm:p-4", loading && "opacity-60")} aria-busy={loading}>
          {failure && (
            <div className="space-y-2">
              <p className="text-sm">{failure === "timeout" ? CHECKS_TIMEOUT : "Could not run the checks."}</p>
              <button type="button" onClick={tryAgain} className={actionButton}>
                Try again
              </button>
            </div>
          )}
          {!failure && !data && <p className="text-sm text-muted-foreground">Running the checks… This takes a few seconds.</p>}
          {!failure && data && (
            <>
              <div className="max-w-6xl space-y-6">
                <Findings checks={data.checks} onOpen={openFinding} />
                <FormatResiduals residuals={data.residuals} />
              </div>
              <TrackFiles
                files={data.files}
                agreement={agreement}
                onCompare={() => startComparison(null)}
                onResume={() => startComparison(agreement.data)}
                timeZone={timeZone}
              />
            </>
          )}
        </div>
      </div>

      {finding && tripView && (
        <MediaGalleryOverlay
          title={tripView.trip.name}
          params={{ trip_id: tripView.trip.id }}
          route={{ tab: "trips", collectionId: tripView.trip.id }}
          // The Checks view has no URL, so neither has its gallery: the evidence view takes its
          // history entry back and would keep the gallery's URL.
          inUrl={false}
          initialItemId={tripView.itemId}
          onClose={() => setTripView(null)}
          onItemsChanged={markFindingChanged}
          onMediaDeleted={markFindingChanged}
        />
      )}
      {finding && !tripView && (
        <EvidenceView
          mediaId={finding.ids[finding.index]}
          isOwn
          onClose={closeFinding}
          onPrev={steps ? () => stepFinding(-1) : undefined}
          onNext={steps ? () => stepFinding(1) : undefined}
          position={steps ? `${finding.index + 1} / ${finding.ids.length}` : undefined}
          onLocationSaved={markFindingChanged}
          onRematched={markFindingChanged}
          onShowInTrip={showInTrip}
        />
      )}
    </div>
  );
}
