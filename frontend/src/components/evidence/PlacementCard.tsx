import { useState } from "react";
import { AlertTriangle, Info } from "lucide-react";
import { chipClass, Fact, PointFacts } from "@/components/evidence/PointCard";
import {
  bracketKindChip,
  bracketPoints,
  confidenceNote,
  formatConfidence,
  formatDistance,
  independenceLine,
  NOT_RECORDED,
  REMATCH_HEADING,
  STANDING_LINE,
  warningSentence,
} from "@/lib/evidence";
import { formatDuration } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { Bracket, Estimate, ProvenanceResponse, Warning } from "@/types";

const chipButton = cn(chipClass, "hover:bg-muted disabled:opacity-50 touch:min-h-11 touch:min-w-11");

function BracketChip({
  point,
  disabled,
  onJump,
}: {
  point: Bracket;
  disabled: boolean;
  onJump: (id: string) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const text = bracketKindChip(point);
  const origin = point.kind === "log" ? null : point.origin;

  if (origin?.state === "linked") {
    return (
      <button type="button" className={chipButton} disabled={disabled} onClick={() => onJump(origin.media[0].id)}>
        {text}
      </button>
    );
  }
  if (origin?.state === "burst") {
    return (
      <>
        <button
          type="button"
          className={chipButton}
          disabled={disabled}
          aria-expanded={expanded}
          onClick={() => setExpanded((open) => !open)}
        >
          {text}
        </button>
        {expanded && (
          <ul className="flex w-full flex-wrap gap-1">
            {origin.media.map((media) => (
              <li key={media.id} className="min-w-0">
                <button
                  type="button"
                  className={cn(chipButton, "max-w-full truncate")}
                  disabled={disabled}
                  onClick={() => onJump(media.id)}
                >
                  {media.filename}
                </button>
              </li>
            ))}
          </ul>
        )}
      </>
    );
  }
  return <span className={chipClass}>{text}</span>;
}

function factor(value: number): string {
  return value.toFixed(3);
}

function Arithmetic({ now }: { now: Estimate }) {
  const { pair_gap_s, pair_km, t, time_factor, distance_factor, shape } = now.arithmetic;
  return (
    <dl className="space-y-0.5 text-xs">
      {pair_gap_s !== null && <Fact label="Span">{formatDuration(pair_gap_s)}</Fact>}
      {pair_km !== null && <Fact label="Distance">{formatDistance(pair_km * 1000)}</Fact>}
      {t !== null && <Fact label="Position along the pair">{`${Math.round(t * 100)}%`}</Fact>}
      <Fact label="Time factor">{factor(time_factor)}</Fact>
      {distance_factor !== null && <Fact label="Distance factor">{factor(distance_factor)}</Fact>}
      <Fact label="Confidence">{formatConfidence(now.confidence, shape)}</Fact>
    </dl>
  );
}

function PlacementWarning({ warning, now }: { warning: Warning; now: Estimate }) {
  const Icon = warning.level === "warn" ? AlertTriangle : Info;
  return (
    <li className="flex gap-2 text-xs">
      <Icon
        className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", warning.level === "warn" ? "text-amber-600" : "text-muted-foreground")}
        aria-hidden="true"
      />
      <span>{warningSentence(warning.code, now)}</span>
    </li>
  );
}

interface PlacementCardProps {
  item: ProvenanceResponse["item"];
  placement: ProvenanceResponse["placement"];
  // Showing a previous item while the next one loads.
  inert?: boolean;
  onJump: (id: string) => void;
}

export function PlacementCard({ item, placement, inert = false, onJump }: PlacementCardProps) {
  const { state, now, others, warnings } = placement;
  const independence = independenceLine({ item, placement });
  const note = now ? confidenceNote(now.confidence, now.arithmetic.shape) : null;
  // The confidence note says the same as confidence_zero, right under the number.
  const shownWarnings = now ? warnings.filter((warning) => warningSentence(warning.code, now) !== note) : [];

  if (!now && others.length === 0 && !independence) return null;

  return (
    <section aria-label="Points" className="space-y-3">
      {now && (
        <div className="space-y-2">
          <h3 className="text-sm font-semibold">{state === "stale" ? REMATCH_HEADING : "Track points"}</h3>
          {bracketPoints(now).map((point) => (
            <PointFacts
              key={`${point.source} ${point.t}`}
              point={{
                format: point.format,
                source: point.source,
                t: point.t,
                accuracyM: point.accuracy_m,
                altitudeM: point.altitude_m,
                gapS: point.gap_s,
              }}
              chip={<BracketChip point={point} disabled={inert} onJump={onJump} />}
            />
          ))}
          {shownWarnings.length > 0 && (
            <ul className="space-y-1">
              {shownWarnings.map((warning) => (
                <PlacementWarning key={warning.code} warning={warning} now={now} />
              ))}
            </ul>
          )}
          <Arithmetic now={now} />
          {note && <p className="text-xs text-muted-foreground">{note}</p>}
          <p className="text-xs text-muted-foreground">{STANDING_LINE}</p>
        </div>
      )}

      {others.length > 0 && (
        <div>
          <h3 className="text-sm font-semibold">Each format at this moment</h3>
          <table className="mt-1 w-full text-left text-xs">
            <thead className="text-muted-foreground">
              <tr>
                <th scope="col" className="font-normal">Format</th>
                <th scope="col" className="font-normal">Gap</th>
                <th scope="col" className="font-normal">Accuracy</th>
                <th scope="col" className="font-normal">Distance</th>
              </tr>
            </thead>
            <tbody>
              {others.map((other) => (
                <tr key={other.format}>
                  <td className="break-all pr-2">
                    {other.format}
                    {other.used && <span className={cn(chipClass, "ml-1")}>used</span>}
                  </td>
                  <td className="pr-2">{formatDuration(other.gap_s)}</td>
                  <td className="pr-2">{other.accuracy_m === null ? NOT_RECORDED : formatDistance(other.accuracy_m)}</td>
                  <td>{other.distance_m === null ? NOT_RECORDED : formatDistance(other.distance_m)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {independence && <p className="text-xs">{independence}</p>}
    </section>
  );
}
