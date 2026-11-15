import { useRef, useState, type FormEvent } from "react";
import { CalendarSearch } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { datedDays, dayIn, firstOnOrAfter, positionIndex } from "@/lib/galleryJump";

// The gallery's "n / N". From two items on, n is a field: type a number and press Enter, or leave
// the field, to jump there (clamped to 1..N). Leaving it counts because the iPhone's number pad has
// no Enter key: its Done button only leaves the field. Escape leaves it without jumping.
//
// The gallery's window key listener ignores keys aimed at an input, so its arrows and Escape do
// nothing while the field has focus, and the video keys leave text fields alone.
export function PositionField({
  index,
  count,
  onJump,
}: {
  index: number;
  count: number;
  onJump: (index: number) => void;
}) {
  // What is typed while the field has focus; it starts empty, with the current number as its
  // placeholder, so typing replaces it without selecting it first (iOS ignores select() on focus).
  const [draft, setDraft] = useState<string | null>(null);
  const cancelled = useRef(false);
  if (count < 2) {
    return (
      <span className="shrink-0 tabular-nums">
        {index + 1} / {count}
      </span>
    );
  }
  const digits = String(count).length;
  return (
    <span className="flex shrink-0 items-center gap-1 tabular-nums">
      <input
        type="text"
        inputMode="numeric"
        pattern="[0-9]*"
        enterKeyHint="go"
        autoComplete="off"
        maxLength={digits}
        aria-label={`Item number, 1 to ${count}`}
        title="Type a number to go to that item"
        value={draft ?? String(index + 1)}
        placeholder={String(index + 1)}
        // Wide enough for N; at least 44px on touch, where the text is 16px (index.css).
        style={{ width: `calc(${digits}ch + 0.5rem)` }}
        className="rounded bg-white/15 px-1 text-center text-white placeholder:text-white/50 hover:bg-white/25 focus:bg-white/25 focus:outline-none focus-visible:ring-2 focus-visible:ring-white/70 touch:min-h-11 touch:min-w-11"
        onFocus={() => {
          cancelled.current = false;
          setDraft("");
        }}
        onChange={(e) => setDraft(e.target.value.replace(/\D/g, ""))}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            e.currentTarget.blur();
          } else if (e.key === "Escape") {
            cancelled.current = true;
            e.currentTarget.blur();
          }
        }}
        onBlur={(e) => {
          const target = cancelled.current ? null : positionIndex(e.currentTarget.value, count);
          cancelled.current = false;
          setDraft(null);
          if (target !== null && target !== index) onJump(target);
        }}
      />
      <span aria-hidden="true">/ {count}</span>
    </span>
  );
}

// The calendar button and its dialog: jump to the first item, in the gallery's order, taken on or
// after a day. Days are in the zone the gallery shows its times in. Undated items never match.
// Nothing to show without a dated item, or with a single item.
export function GoToDate<T extends { taken_at: string | null }>({
  items,
  index,
  timeZone,
  onJump,
}: {
  items: readonly T[];
  index: number;
  timeZone: string;
  onJump: (index: number) => void;
}) {
  const [open, setOpen] = useState(false);
  const [day, setDay] = useState("");
  // The days the dated items span, which the field's picker offers.
  const [range, setRange] = useState<{ first: string; last: string } | null>(null);
  const [notFound, setNotFound] = useState(false);
  if (items.length < 2 || !items.some((item) => item.taken_at)) return null;

  function openDialog() {
    const days = datedDays(items, timeZone);
    if (!days) return;
    const taken = items[index]?.taken_at;
    // The current item's day, so the picker opens where the user is; an undated item is at the end.
    setDay((taken && dayIn(taken, timeZone)) || days.last);
    setRange(days);
    setNotFound(false);
    setOpen(true);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!day) return;
    const target = firstOnOrAfter(items, day, timeZone);
    // Only past the last dated day, which the field's max already refuses in a browser.
    if (target < 0) {
      setNotFound(true);
      return;
    }
    setOpen(false);
    onJump(target);
  }

  return (
    <>
      <button
        type="button"
        className="flex h-6 w-6 shrink-0 items-center justify-center rounded text-white/70 hover:bg-white/15 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 touch:h-11 touch:w-11"
        onClick={openDialog}
        aria-label="Go to date"
        title="Go to date"
      >
        <CalendarSearch className="h-4 w-4" aria-hidden="true" />
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        {/* The gallery behind it ignores keys while it is open: keyBelongsToDialog. */}
        <DialogContent className="max-w-xs">
          <DialogHeader>
            <DialogTitle>Go to date</DialogTitle>
            <DialogDescription>
              Shows the first item taken on or after this day, in {timeZone} like the times in the gallery.
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={submit} className="grid gap-3 text-sm">
            <label className="block">
              <span className="mb-1 block text-muted-foreground">Day</span>
              {/* Shown in the format of the browser's language, which a page cannot choose. */}
              <input
                type="date"
                required
                min={range?.first}
                max={range?.last}
                value={day}
                onChange={(e) => {
                  setDay(e.target.value);
                  setNotFound(false);
                }}
                className="w-full rounded-md border bg-background px-2 py-1 touch:min-h-11 touch:py-2 touch:text-base"
              />
            </label>
            {notFound && (
              <p role="alert" className="text-red-500">
                No item was taken on or after that day.
              </p>
            )}
            <DialogFooter>
              <button
                type="submit"
                className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground"
              >
                Go
              </button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
