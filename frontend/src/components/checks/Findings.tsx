import { useId, useState } from "react";
import { AlertTriangle, Check, ChevronDown, ChevronRight, Info } from "lucide-react";
import { InstantFacts } from "@/components/evidence/PointCard";
import {
  bySeverity,
  cappedNote,
  checkTitle,
  checkTotal,
  findingText,
  NOTHING_FOUND,
  openLabel,
} from "@/lib/checks";
import { YEAR_OUT_OF_RANGE } from "@/lib/time";
import type { CheckCode, ChecksCheck, ChecksGroup } from "@/types";

const openButton =
  "rounded border border-border px-2 py-1 text-xs hover:bg-muted touch:min-h-11 touch:px-3 touch:text-sm";

interface GroupProps {
  code: CheckCode;
  group: ChecksGroup;
  onOpen: (ids: string[]) => void;
}

// One group of a check: a sentence with its evidence. Labels in it hold file names and camera
// models, so it is only ever rendered as text, and one with no space in it wraps anywhere rather
// than push the view sideways on a phone.
function FindingGroup({ code, group, onOpen }: GroupProps) {
  const text = findingText(code, group);
  const capped = cappedNote(code, group);
  // Several groups' buttons read "Open 3 estimates": the sentence says which finding each opens.
  const sentenceId = useId();
  return (
    <li className="space-y-1 [overflow-wrap:anywhere]">
      <p id={sentenceId} className="text-sm">
        {text.sentence}
      </p>
      {text.notes.map((note) => (
        <p key={note} className="text-xs text-muted-foreground">
          {note}
        </p>
      ))}
      {text.instants.map(({ label, iso }) => (
        <div key={label} className="max-w-sm">
          <p className="text-xs font-medium">{label}</p>
          {iso === null ? (
            <p className="text-xs">{YEAR_OUT_OF_RANGE}</p>
          ) : (
            <dl className="space-y-0.5 text-xs">
              <InstantFacts iso={iso} />
            </dl>
          )}
        </div>
      ))}
      {group.media_ids.length > 0 && (
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 pt-1">
          <button
            type="button"
            className={openButton}
            aria-describedby={sentenceId}
            onClick={() => onOpen(group.media_ids)}
          >
            {openLabel(code, group)}
          </button>
          {capped && <span className="text-xs text-muted-foreground">{capped}</span>}
        </div>
      )}
    </li>
  );
}

interface FindingsProps {
  checks: ChecksCheck[];
  onOpen: (ids: string[]) => void; // steps through these items in the evidence view
}

// High severity first and open; low severity collapsed until asked for. A check that found nothing
// takes one line.
export function Findings({ checks, onOpen }: FindingsProps) {
  const [expandedByUser, setExpandedByUser] = useState<ReadonlyMap<CheckCode, boolean>>(new Map());
  const headingId = useId();
  const ordered = bySeverity(checks);
  const found = ordered.filter((check) => check.groups.length > 0);
  const clear = ordered.filter((check) => check.groups.length === 0);

  return (
    <section aria-labelledby={headingId} className="space-y-2">
      <h3 id={headingId} className="text-sm font-semibold">
        Findings
      </h3>
      {found.length === 0 && <p className="text-sm">{NOTHING_FOUND}</p>}
      {found.length > 0 && (
        <ul className="divide-y divide-border border-y border-border">
          {found.map((check) => {
            const open = expandedByUser.get(check.code) ?? check.severity === "high";
            const high = check.severity === "high";
            const Severity = high ? AlertTriangle : Info;
            const Chevron = open ? ChevronDown : ChevronRight;
            const title = checkTitle(check.code);
            const total = checkTotal(check);
            return (
              <li key={check.code}>
                <button
                  type="button"
                  aria-expanded={open}
                  // On screen the icon's shape carries the severity; the label says it in words.
                  aria-label={`${high ? "High" : "Low"} severity: ${title}, ${total}`}
                  onClick={() => setExpandedByUser((m) => new Map(m).set(check.code, !open))}
                  className="flex w-full items-center gap-2 py-2 text-left text-sm hover:text-foreground touch:min-h-11"
                >
                  <Severity
                    className={`h-4 w-4 shrink-0 ${high ? "text-amber-600" : "text-muted-foreground"}`}
                    aria-hidden="true"
                  />
                  <span className="min-w-0 flex-1 font-medium">{title}</span>
                  <span className="shrink-0 text-xs text-muted-foreground">{total}</span>
                  <Chevron className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                </button>
                {open && (
                  <ul className="space-y-3 pb-3 pl-6">
                    {check.groups.map((group) => (
                      <FindingGroup key={group.key} code={check.code} group={group} onOpen={onOpen} />
                    ))}
                  </ul>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {clear.length > 0 && (
        <div className="text-xs text-muted-foreground">
          <p>Nothing found for:</p>
          <ul className="mt-1 flex flex-wrap gap-x-4 gap-y-1">
            {clear.map((check) => (
              <li key={check.code} className="flex items-center gap-1">
                <Check className="h-3 w-3 shrink-0" aria-hidden="true" />
                {checkTitle(check.code)}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
