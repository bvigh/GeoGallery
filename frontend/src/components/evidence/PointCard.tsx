import type { ReactNode } from "react";
import { X } from "lucide-react";
import { formatDistance, formatTimeNote, NOT_RECORDED } from "@/lib/evidence";
import { browserTimeZone, formatDuration, formatInstant } from "@/lib/time";

// One point as the evidence view shows it, whichever part of the response it came from.
export interface PointDetails {
  format: string;
  source: string;
  t: string;
  // Omitted where the response does not carry the field, which is different from "not recorded".
  accuracyM?: number | null;
  altitudeM?: number | null;
  gapS: number | null; // from the item's capture time; null when the item has none
  side?: "before" | "after" | null; // which side of the capture time; null at the same instant
}

// A point picked on the map, with its one-word kind.
export interface PickedPoint extends PointDetails {
  lat: number;
  lng: number;
  kind: string;
}

export function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-3">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-right">{children}</dd>
    </div>
  );
}

// An instant with no camera clock: UTC and the browser zone, each labelled.
export function InstantFacts({ iso }: { iso: string }) {
  return (
    <>
      <Fact label="UTC">{formatInstant(iso, "UTC")}</Fact>
      <Fact label="Your browser">{formatInstant(iso, browserTimeZone())}</Fact>
    </>
  );
}

function fileOf(source: string): string | null {
  const colon = source.indexOf(":");
  if (colon >= 0) return source.slice(colon + 1);
  return source === "exif" || source === "manual" ? null : source;
}

function formatMeters(meters: number | null): string {
  return meters === null ? NOT_RECORDED : `${Math.round(meters)} m`;
}

// The facts of one point. The file name is the owner's upload and only ever rendered as text.
export function PointFacts({ point, chip }: { point: PointDetails; chip: ReactNode }) {
  const file = fileOf(point.source);
  const note = formatTimeNote(point.format);
  return (
    <div>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{point.format}</span>
        {chip}
      </div>
      <dl className="mt-1 space-y-0.5 text-xs">
        {file && <Fact label="File">{file}</Fact>}
        <InstantFacts iso={point.t} />
        {point.gapS !== null && (
          <Fact label="Gap">{point.side ? `${formatDuration(point.gapS)} ${point.side}` : formatDuration(point.gapS)}</Fact>
        )}
        {point.accuracyM !== undefined && (
          <Fact label="Accuracy">{point.accuracyM === null ? NOT_RECORDED : formatDistance(point.accuracyM)}</Fact>
        )}
        {point.altitudeM !== undefined && <Fact label="Altitude">{formatMeters(point.altitudeM)}</Fact>}
      </dl>
      {note && <p className="mt-1 text-xs text-muted-foreground">{note}</p>}
    </div>
  );
}

export const chipClass = "inline-block rounded-full border border-border px-2 py-0.5 text-xs";

// Over the map, as a sibling of its container: inside Leaflet's stacking context it would sit under
// the panes, and in the facts column it would move them.
export function PointCard({ point, onClose }: { point: PickedPoint; onClose: () => void }) {
  return (
    <div
      role="region"
      aria-label="Track point"
      className="absolute inset-x-0 bottom-0 z-10 max-h-full overflow-y-auto border-t border-border bg-background px-3 pb-3 text-sm shadow-lg"
    >
      <div className="flex justify-end">
        <button
          type="button"
          onClick={onClose}
          aria-label="Close point"
          className="-mr-3 flex h-11 w-11 items-center justify-center text-muted-foreground hover:text-foreground"
        >
          <X className="h-4 w-4" aria-hidden="true" />
        </button>
      </div>
      <PointFacts point={point} chip={<span className={chipClass}>{point.kind}</span>} />
    </div>
  );
}
