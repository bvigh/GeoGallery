import { useEffect, useState } from "react";
import { AlertTriangle, Info } from "lucide-react";
import { api } from "@/api/client";
import { chipClass, Fact, InstantFacts } from "@/components/evidence/PointCard";
import {
  cameraClock,
  checkSentence,
  NOT_RECORDED,
  rereadSentence,
  REREAD_NOT_RECORDED,
  timeOriginChip,
  VIDEO_TIME_MAY_BE_END,
  zoneChip,
} from "@/lib/evidence";
import { browserTimeZone, formatDuration, formatInstant } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { FileReread, ProvenanceResponse } from "@/types";

// A video re-read starts an ffprobe process, so stepping through items must not start one per item.
const REREAD_DELAY_MS = 400;

const smallButton = "rounded border border-border px-2 py-1 text-xs hover:bg-muted disabled:opacity-50 touch:min-h-11";

type RereadState =
  | { id: string; status: "loading" }
  | { id: string; status: "done"; data: FileReread }
  | { id: string; status: "failed" };

interface TimeCardProps {
  item: ProvenanceResponse["item"];
  time: ProvenanceResponse["time"];
  // Showing a previous item while the next one loads: that item is no longer current.
  inert?: boolean;
}

export function TimeCard({ item, time, inert = false }: TimeCardProps) {
  const [reread, setReread] = useState<RereadState | null>(null);
  // The button (or Try again) for one item, counted so that pressing it again runs the effect.
  const [asked, setAsked] = useState<{ id: string; attempt: number } | null>(null);

  const evidence = time.evidence;
  const automatic = time.recorded === "not_recorded";
  const askedAttempt = asked?.id === item.id ? asked.attempt : 0;
  const wanted = automatic || askedAttempt > 0;
  const current = reread?.id === item.id ? reread : null;
  const loaded = current?.status === "done";
  const id = item.id;

  useEffect(() => {
    if (inert || !wanted || loaded) return;
    const controller = new AbortController();

    function start() {
      setReread({ id, status: "loading" });
      api
        .mediaProvenanceFile(id, controller.signal)
        .then((data) => {
          if (!controller.signal.aborted) setReread({ id, status: "done", data });
        })
        .catch((e) => {
          if (controller.signal.aborted) return;
          console.error("Failed to re-read the file:", e);
          setReread({ id, status: "failed" });
        });
    }

    // Only the automatic read waits; a press of the button means this item.
    if (askedAttempt > 0) start();
    const timer = askedAttempt > 0 ? undefined : setTimeout(start, REREAD_DELAY_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [id, inert, wanted, loaded, askedAttempt]);

  function askReread() {
    setAsked({ id, attempt: askedAttempt + 1 });
  }

  const file = current?.status === "done" ? current.data : null;
  const clock = cameraClock(evidence, file);
  const origin = timeOriginChip(evidence, file);
  const zone = zoneChip(evidence, file, time.server_camera_zone);
  const isVideo = item.media_type === "video" || item.media_type === "video_360";
  const duration = item.duration_sec ?? evidence?.duration_s ?? file?.file.video?.duration_s ?? null;
  // Only a container time can be the end of the clip; a modified date cannot.
  const timeFromContainer = evidence ? evidence.src === "container" : file?.rule_today?.source === "container";
  const offsetTag = file?.file.offset_tag ?? evidence?.offset_tag ?? null;

  const checks = (file?.checks ?? []).flatMap((check) => {
    // Decided below, from where the stored time came from, and shown beside the duration.
    if (check.id === "video_time_may_be_end") return [];
    const text = checkSentence(check, offsetTag);
    return text && !zone?.notes.includes(text) ? [{ id: check.id, level: check.level, text }] : [];
  });

  // Every other instant: UTC and the browser zone, never a camera clock.
  const otherInstants: [label: string, iso: string | null][] = [
    ["GPS clock", evidence?.gps_utc ?? file?.file.gps_stamp ?? null],
    ["Container time", evidence?.creation_time ?? file?.file.video?.creation_time ?? null],
    ["File modified", evidence?.file_mtime ?? null],
    ["Uploaded", item.created_at],
  ];

  return (
    <section aria-label="Time" className="space-y-2">
      <h3 className="text-sm font-semibold">Time</h3>

      <dl className="space-y-0.5 text-xs">
        <Fact label="UTC">{item.taken_at ? formatInstant(item.taken_at, "UTC") : NOT_RECORDED}</Fact>
        <Fact label="Camera">
          {clock.text}
          {clock.word && <span className="text-muted-foreground"> · {clock.word}</span>}
        </Fact>
        <Fact label="Your browser">
          {item.taken_at ? formatInstant(item.taken_at, browserTimeZone()) : NOT_RECORDED}
        </Fact>
      </dl>

      <div className="flex flex-wrap gap-1">
        <span className={chipClass}>{origin.text}</span>
        {zone && <span className={chipClass}>{zone.text}</span>}
      </div>
      {origin.note && <p className="text-xs text-muted-foreground">{origin.note}</p>}
      {zone?.notes.map((note) => (
        <p key={note} className="text-xs text-muted-foreground">
          {note}
        </p>
      ))}
      {zone?.warning && (
        <p className="flex gap-2 text-xs">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-600" aria-hidden="true" />
          <span>{zone.warning}</span>
        </p>
      )}

      {isVideo && (duration !== null || timeFromContainer) && (
        <p className="text-xs text-muted-foreground">
          {duration !== null && `Duration ${formatDuration(duration)}. `}
          {timeFromContainer && VIDEO_TIME_MAY_BE_END}
        </p>
      )}

      {automatic && <p className="text-xs text-muted-foreground">{REREAD_NOT_RECORDED}</p>}
      {!automatic && !current && (
        <button type="button" className={smallButton} disabled={inert} onClick={askReread}>
          Check against the file
        </button>
      )}
      {current?.status === "loading" && <p className="text-xs text-muted-foreground">Reading the file…</p>}
      {current?.status === "failed" && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span>Could not read the file.</span>
          <button type="button" className={smallButton} disabled={inert} onClick={askReread}>
            Try again
          </button>
        </div>
      )}
      {file && <p className="text-xs">{rereadSentence(file, item.media_type, evidence)}</p>}

      {checks.length > 0 && (
        <ul className="space-y-1">
          {checks.map((check) => {
            const Icon = check.level === "warn" ? AlertTriangle : Info;
            return (
              <li key={check.id} className="flex gap-2 text-xs">
                <Icon
                  className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", check.level === "warn" ? "text-amber-600" : "text-muted-foreground")}
                  aria-hidden="true"
                />
                <span>{check.text}</span>
              </li>
            );
          })}
        </ul>
      )}

      {otherInstants.map(
        ([label, iso]) =>
          iso && (
            <div key={label}>
              <p className="text-xs font-medium">{label}</p>
              <dl className="space-y-0.5 text-xs">
                <InstantFacts iso={iso} />
              </dl>
            </div>
          )
      )}

      {file && file.tags.length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer touch:flex touch:min-h-11 touch:items-center">
            Tags in the file ({file.tags.length})
          </summary>
          <dl className="mt-1 space-y-0.5">
            {file.tags.map((tag) => (
              <Fact key={tag.name} label={tag.name}>
                {tag.value}
              </Fact>
            ))}
          </dl>
        </details>
      )}
    </section>
  );
}
