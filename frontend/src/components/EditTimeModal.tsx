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
import { CLOCK_OFFSETS, formatUtcOffset } from "@/lib/clockRules";
import { useCloseOnBack } from "@/lib/history";
import { browserTimeZone, formatInstant } from "@/lib/time";
import {
  FIRST_YEAR,
  joinWall,
  LAST_YEAR,
  parseZoneChoice,
  repeatedWallClock,
  splitWall,
  wallClockIn,
  zoneChoiceValue,
} from "@/lib/timeEdit";
import { timeZoneNames } from "@/lib/timeZones";
import type { MediaItem, SetTakenAtResult } from "@/types";

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const FIELD_CLASS = "w-full min-w-0 rounded-md border bg-background px-2 py-1 touch:min-h-11 touch:text-base";

// The days a date input offers: the years the backend stores.
const FIRST_DAY = `${String(FIRST_YEAR).padStart(4, "0")}-01-01`;
const LAST_DAY = `${LAST_YEAR}-12-31`;

function saveError(err: unknown): string {
  if (err instanceof ApiError) {
    // The backend's reasons are written for the user: a wall clock the zone skipped, a year it
    // cannot store.
    if (err.status === 400) return err.reason ?? "The server refused this time.";
    if (err.status === 404) return "This item is no longer available.";
  }
  return "Could not save the time. Try again.";
}

type Item = Pick<MediaItem, "id" | "filename" | "taken_at">;

interface Props {
  open: boolean;
  item: Item | null;
  onClose: () => void;
  // The time is saved; the dialog then closes. Called even when the dialog was closed while saving,
  // since the item's time, track point and trip have changed all the same.
  onSaved: (result: SetTakenAtResult) => void;
}

// The owner sets one item's capture time by hand: a date, a time and the zone that wall clock was
// in. A shadcn dialog, so the gallery and the evidence view behind it leave its keys alone
// (keyBelongsToDialog), and the phone's back gesture closes only the dialog.
export function EditTimeModal({ open, item, onClose, onSaved }: Props) {
  const shown = open && item !== null;
  useCloseOnBack(onClose, shown);
  return (
    <Dialog
      open={shown}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit capture time</DialogTitle>
          <DialogDescription>
            The item's own track point moves with it, estimated locations near the old and new times are placed
            again, and trips are detected again. A time set here stays when an administrator runs server retime.
          </DialogDescription>
        </DialogHeader>
        {/* Keyed by item, and unmounted while closed: every opening starts from the stored time. */}
        {item && <EditTimeForm key={item.id} item={item} onClose={onClose} onSaved={onSaved} />}
      </DialogContent>
    </Dialog>
  );
}

// The fields an item's stored time starts the form from: its wall clock in the browser's zone, and
// that zone. A wall clock the zone showed twice (the hour repeated when daylight saving time ends)
// would be read back as the earlier instant, so the stored instant's own offset is chosen instead,
// and repeated says so.
interface Prefill {
  date: string;
  time: string;
  wall: string | null; // date and time joined, null for an undated item
  zoneValue: string;
  repeated: { zone: string; offsetMin: number } | null;
}

function prefill(takenAt: string | null, browserZone: string): Prefill {
  const zoneValue = zoneChoiceValue({ zone: browserZone });
  if (takenAt === null) return { ...splitWall(null), wall: null, zoneValue, repeated: null };
  const wall = wallClockIn(takenAt, browserZone);
  const fields = { ...splitWall(wall), wall };
  const repeat = repeatedWallClock(takenAt, browserZone);
  // Only an offset the select offers; another (a zone's local mean time) keeps the zone.
  if (repeat?.repeated && CLOCK_OFFSETS.includes(repeat.offsetMin)) {
    return {
      ...fields,
      zoneValue: zoneChoiceValue({ utcOffsetMin: repeat.offsetMin }),
      repeated: { zone: browserZone, offsetMin: repeat.offsetMin },
    };
  }
  return { ...fields, zoneValue, repeated: null };
}

function EditTimeForm({ item, onClose, onSaved }: { item: Item; onClose: () => void; onSaved: Props["onSaved"] }) {
  const zoneHintId = useId();
  const repeatHintId = useId();
  const [browserZone] = useState(browserTimeZone);
  const [initial] = useState(() => prefill(item.taken_at, browserZone));
  const [date, setDate] = useState(initial.date);
  const [time, setTime] = useState(initial.time);
  const [zoneValue, setZoneValue] = useState(initial.zoneValue);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A save that finishes after the dialog closed must not close the next one.
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const zoneNames = useMemo(() => timeZoneNames(browserZone), [browserZone]);
  const wall = joinWall(date, time);
  const zone = parseZoneChoice(zoneValue);
  const incomplete = date === "" || time === "";
  // The fields as they started name the stored time (to the second): saving them would write a
  // change that changes nothing, or drop a stored fraction of a second.
  const unchanged = initial.wall !== null && wall === initial.wall && zoneValue === initial.zoneValue;
  const ready = wall !== null && zone !== null && !unchanged;

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (wall === null || zone === null || unchanged || saving) return;
    setSaving(true);
    setError(null);
    try {
      const result = await api.setTakenAt(item.id, wall, zone);
      onSaved(result);
      if (mounted.current) onClose();
    } catch (err) {
      console.error(err);
      if (!mounted.current) return;
      setError(saveError(err));
      setSaving(false);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="min-w-0 space-y-3 text-sm">
      <div className="space-y-1">
        <p className="break-all font-medium">{item.filename}</p>
        {item.taken_at === null ? (
          <p className="text-muted-foreground">No capture time stored.</p>
        ) : (
          <p className="text-muted-foreground">
            Stored: {formatInstant(item.taken_at, "UTC")}
            {browserZone !== "UTC" && <> · {formatInstant(item.taken_at, browserZone)}</>}
          </p>
        )}
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block min-w-0">
          <span className="mb-1 block font-medium">Date</span>
          <input
            type="date"
            required
            min={FIRST_DAY}
            max={LAST_DAY}
            value={date}
            onChange={(e) => setDate(e.target.value)}
            className={FIELD_CLASS}
          />
        </label>
        <label className="block min-w-0">
          <span className="mb-1 block font-medium">Time</span>
          {/* step 1: the seconds are part of the time and stay as they were. */}
          <input
            type="time"
            required
            step={1}
            value={time}
            onChange={(e) => setTime(e.target.value)}
            className={FIELD_CLASS}
          />
        </label>
      </div>

      <div>
        <label className="block">
          <span className="mb-1 block font-medium">Time zone</span>
          <select
            value={zoneValue}
            onChange={(e) => setZoneValue(e.target.value)}
            aria-describedby={initial.repeated ? `${repeatHintId} ${zoneHintId}` : zoneHintId}
            className={FIELD_CLASS}
          >
            <optgroup label="Time zones">
              {zoneNames.map((name) => (
                <option key={name} value={zoneChoiceValue({ zone: name })}>
                  {name}
                </option>
              ))}
            </optgroup>
            <optgroup label="Fixed UTC offsets">
              {CLOCK_OFFSETS.map((minutes) => (
                <option key={minutes} value={zoneChoiceValue({ utcOffsetMin: minutes })}>
                  {formatUtcOffset(minutes)}
                </option>
              ))}
            </optgroup>
          </select>
        </label>
        {initial.repeated && (
          <p id={repeatHintId} className="mt-1 text-xs">
            {initial.repeated.zone} showed this time twice, as its clocks went back, so the zone starts as the stored
            time's own offset, {formatUtcOffset(initial.repeated.offsetMin)}.
          </p>
        )}
        <p id={zoneHintId} className="mt-1 text-xs text-muted-foreground">
          The zone the date and time above are in. A time zone applies its daylight saving time on that date; a
          fixed offset does not.
        </p>
      </div>

      {!incomplete && wall === null && (
        <p className="text-red-500">
          Enter a real date and time in the years {FIRST_YEAR} to {LAST_YEAR}.
        </p>
      )}
      {error && <p className="text-red-500">{error}</p>}
      <DialogFooter>
        <button
          type="submit"
          disabled={!ready || saving}
          className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save time"}
        </button>
      </DialogFooter>
    </form>
  );
}
