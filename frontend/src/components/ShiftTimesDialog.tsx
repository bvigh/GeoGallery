import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useCloseOnBack } from "@/lib/history";
import { formatCount } from "@/lib/numbers";
import { browserTimeZone, formatInstant } from "@/lib/time";
import {
  EMPTY_SHIFT,
  earliestItem,
  FIRST_YEAR,
  formatDayRange,
  formatShift,
  itemCount,
  LAST_YEAR,
  MAX_SHIFT_ITEMS,
  MAX_SHIFT_SECONDS,
  SHIFT_OUTCOME_UNKNOWN,
  shiftCandidates,
  shiftInstant,
  shiftSeconds,
  shiftStaysInRange,
  type ShiftAmount,
} from "@/lib/timeEdit";
import type { MediaItem, ShiftTimesResult } from "@/types";

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const FIELD_CLASS = "w-full min-w-0 rounded-md border bg-background px-2 py-1 touch:min-h-11 touch:text-base";

const AMOUNT_FIELDS: { field: Exclude<keyof ShiftAmount, "direction">; label: string }[] = [
  { field: "days", label: "Days" },
  { field: "hours", label: "Hours" },
  { field: "minutes", label: "Minutes" },
  { field: "seconds", label: "Seconds" },
];

// What a failed shift says, and whether it may have been saved all the same. The backend answers
// 400, 404 and 500 only before or at its commit, so those shifted nothing. Anything else (a
// gateway's 502, 503 or 504, a lost connection) may have come after the commit: shifting the same
// items again would move them twice.
function shiftError(err: unknown): { text: string; unknown: boolean } {
  if (err instanceof ApiError) {
    if (err.status === 400) return { text: err.reason ?? "The server refused this shift.", unknown: false };
    // One item deleted, or taken out of the caller's hands, refuses the whole request.
    if (err.status === 404) {
      return { text: "Some of these items are no longer available. Reload and try again.", unknown: false };
    }
    if (err.status === 500) return { text: "Could not shift the times. Try again.", unknown: false };
  }
  return { text: SHIFT_OUTCOME_UNKNOWN, unknown: true };
}

function isAre(n: number): string {
  return n === 1 ? "is" : "are";
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  items: MediaItem[] | null; // null while loading
  loadError?: string | null;
  currentUserId: string;
  // The times moved; the dialog then closes. Called even when the dialog was closed while
  // shifting, since the items' times, track points and trips have changed all the same. ids: the
  // items sent, those of the ticked cameras.
  onShifted: (result: ShiftTimesResult, deltaSeconds: number, ids: string[]) => void;
  // The shift failed without an answer, so it may have been saved: the host loads its items and
  // trips again. Apply stays off until the dialog is opened again, on fresh items.
  onOutcomeUnknown?: () => void;
}

// The owner moves the capture times of a gallery's or a trip's items, by camera, by one amount:
// for a camera whose clock was off. A shadcn dialog, so the gallery behind it leaves its keys alone
// (keyBelongsToDialog), and the phone's back gesture closes only the dialog.
export function ShiftTimesDialog({
  open,
  onOpenChange,
  title,
  items,
  loadError,
  currentUserId,
  onShifted,
  onOutcomeUnknown,
}: Props) {
  useCloseOnBack(() => onOpenChange(false), open);
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="break-words">{title}</DialogTitle>
          <DialogDescription>
            Moves the capture times of the cameras you tick by the same amount, for a camera whose clock was off. Their
            own track points move with them, estimated locations near them are placed again, and trips are detected
            again.
          </DialogDescription>
        </DialogHeader>
        {loadError ? (
          <p className="text-sm text-red-500">{loadError}</p>
        ) : items === null ? (
          <p className="text-sm text-muted-foreground">Loading items…</p>
        ) : (
          // Unmounted while closed: every opening starts from the default ticks and no amount.
          <ShiftForm
            items={items}
            currentUserId={currentUserId}
            onShifted={onShifted}
            onOutcomeUnknown={onOutcomeUnknown}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

interface FormProps {
  items: MediaItem[];
  currentUserId: string;
  onShifted: Props["onShifted"];
  onOutcomeUnknown: Props["onOutcomeUnknown"];
  onClose: () => void;
}

function ShiftForm({ items, currentUserId, onShifted, onOutcomeUnknown, onClose }: FormProps) {
  const radioName = useId();
  const reasonId = useId();
  const errorId = useId();
  const [browserZone] = useState(browserTimeZone);
  const candidates = useMemo(() => shiftCandidates(items, currentUserId), [items, currentUserId]);
  const { groups, ownUndated, others } = candidates;
  // One camera: it is ticked. Several: none is, because a trip often mixes a phone whose clock was
  // right with the camera that was off, and shifting both would break the phone's times.
  const [ticked, setTicked] = useState<Set<string>>(() => new Set(groups.length === 1 ? [groups[0].key] : []));
  const [amount, setAmount] = useState<ShiftAmount>(EMPTY_SHIFT);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A shift whose answer never came: it may have been saved, so this opening shifts nothing more.
  const [outcomeUnknown, setOutcomeUnknown] = useState(false);
  // A shift that finishes after the dialog closed must not close the next one.
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const tickedGroups = groups.filter((group) => ticked.has(group.key));
  const count = tickedGroups.reduce((sum, group) => sum + group.ids.length, 0);
  const delta = shiftSeconds(amount);

  let blocked: string | null = null;
  if (groups.length === 0) blocked = "None of your items here has a capture time to shift.";
  else if (count === 0) blocked = "Tick at least one camera.";
  else if (count > MAX_SHIFT_ITEMS)
    blocked = `At most ${formatCount(MAX_SHIFT_ITEMS)} items can be shifted at once, and ${itemCount(count)} are ticked.`;
  else if (delta === null) blocked = "Enter whole numbers of 0 or more.";
  else if (delta === 0) blocked = "Enter how far to shift the times.";
  else if (Math.abs(delta) > MAX_SHIFT_SECONDS) blocked = "A shift can be at most 100 years.";
  else if (!shiftStaysInRange(tickedGroups, delta))
    blocked = `This would move some times outside the years ${FIRST_YEAR} to ${LAST_YEAR}.`;

  const first = earliestItem(tickedGroups);
  const firstAfter = first !== null && delta !== null ? shiftInstant(first.taken_at, delta) : null;

  const toggle = (key: string, on: boolean) => {
    setTicked((prev) => {
      const next = new Set(prev);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });
  };

  const setField = (field: keyof ShiftAmount, value: string) => {
    setAmount((prev) => ({ ...prev, [field]: value }));
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (blocked !== null || delta === null || busy || outcomeUnknown) return;
    setBusy(true);
    setError(null);
    const ids = tickedGroups.flatMap((group) => group.ids);
    try {
      const result = await api.shiftTimes(ids, delta);
      onShifted(result, delta, ids);
      if (mounted.current) onClose();
    } catch (err) {
      console.error(err);
      const failure = shiftError(err);
      // Also when the dialog was closed meanwhile: the items may have moved all the same.
      if (failure.unknown) onOutcomeUnknown?.();
      if (!mounted.current) return;
      setError(failure.text);
      setOutcomeUnknown(failure.unknown);
      setBusy(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="min-w-0 space-y-3 text-sm">
      <fieldset disabled={busy} className="min-w-0 space-y-3">
        {groups.length > 0 && (
          <fieldset className="min-w-0">
            <legend className="mb-1 font-medium">Cameras</legend>
            {/* Phones held sideways scroll the whole dialog instead of a list nested inside it. */}
            <div className="max-h-64 space-y-1 overflow-y-auto short:max-h-none">
              {groups.map((group) => (
                <label key={group.key} className="flex cursor-pointer items-start gap-2 py-1 touch:min-h-11">
                  <input
                    type="checkbox"
                    className="mt-0.5 shrink-0 touch:h-5 touch:w-5"
                    checked={ticked.has(group.key)}
                    onChange={(e) => toggle(group.key, e.target.checked)}
                  />
                  <span className="min-w-0">
                    <span className="block break-words font-medium">{group.label}</span>{" "}
                    <span className="block text-xs text-muted-foreground">
                      {itemCount(group.ids.length)} · {formatDayRange(group.first.taken_at, group.last, browserZone)}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
        )}

        {ownUndated > 0 && (
          <p className="text-xs text-muted-foreground">
            {itemCount(ownUndated)} without a capture time {isAre(ownUndated)} left out: there is no time to shift.
          </p>
        )}
        {others > 0 && (
          <p className="text-xs text-muted-foreground">
            {itemCount(others)} shared by others {isAre(others)} left out: only their owners can change their times.
          </p>
        )}

        <fieldset className="min-w-0">
          <legend className="mb-1 font-medium">Shift by</legend>
          <div className="mb-2 flex gap-4">
            {(["later", "earlier"] as const).map((direction) => (
              <label key={direction} className="flex cursor-pointer items-center gap-2 touch:min-h-11">
                <input
                  type="radio"
                  className="shrink-0 touch:h-5 touch:w-5"
                  name={radioName}
                  value={direction}
                  checked={amount.direction === direction}
                  onChange={() => setField("direction", direction)}
                />
                {direction === "later" ? "Later" : "Earlier"}
              </label>
            ))}
          </div>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {AMOUNT_FIELDS.map(({ field, label }) => (
              <label key={field} className="block min-w-0">
                <span className="mb-1 block text-muted-foreground">{label}</span>
                <input
                  inputMode="numeric"
                  autoComplete="off"
                  placeholder="0"
                  value={amount[field]}
                  onChange={(e) => setField(field, e.target.value)}
                  className={FIELD_CLASS}
                />
              </label>
            ))}
          </div>
        </fieldset>
      </fieldset>

      {blocked === null && delta !== null ? (
        <div id={reasonId} className="space-y-1">
          <p>
            Shifts {itemCount(count)} {formatShift(delta)}.
          </p>
          {first !== null && firstAfter !== null && (
            <p className="break-words text-muted-foreground">
              The earliest, <span className="break-all">{first.filename}</span>:{" "}
              {formatInstant(first.taken_at, browserZone)} → {formatInstant(firstAfter, browserZone)}
            </p>
          )}
        </div>
      ) : (
        <p id={reasonId} className="text-muted-foreground">
          {blocked}
        </p>
      )}
      {error && (
        <p id={errorId} className="text-red-500">
          {error}
        </p>
      )}
      <DialogFooter>
        <button
          type="submit"
          disabled={blocked !== null || busy || outcomeUnknown}
          aria-describedby={outcomeUnknown ? errorId : reasonId}
          className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
        >
          {busy ? "Shifting…" : count > 0 ? `Shift ${itemCount(count)}` : "Shift items"}
        </button>
      </DialogFooter>
    </form>
  );
}
