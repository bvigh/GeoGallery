import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  CLOCK_OFFSETS,
  CLOCK_RULE_CAP_REASON,
  formatUtcOffset,
  libraryCameraLabel,
  MAX_CLOCK_RULES,
  ruleCameraName,
  rulePeriod,
  sameCamera,
} from "@/lib/clockRules";
import type { CameraClockRule, CameraClockRuleInput, LibraryCamera } from "@/types";

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const TOUCH_INPUT_CLASS = "touch:py-2 touch:text-base";
const FIELD_CLASS = `w-full rounded-md border bg-background px-2 py-1 ${TOUCH_INPUT_CLASS}`;
const ROW_BUTTON_CLASS =
  "rounded-md border px-2 py-1 text-xs font-medium touch:min-h-11 touch:px-3 touch:text-sm";

// The days a date input offers; the backend refuses years outside 1900..9998.
const FIRST_DAY = "1900-01-01";
const LAST_DAY = "9998-12-31";

const OVERLAP_MESSAGE = "This camera already has a rule for part of that period.";

// The camera select's value for typing a make and model.
const OTHER_CAMERA = "other";

type CamerasState = { status: "loading" } | { status: "ready"; cameras: LibraryCamera[] } | { status: "failed" };

// The two 409s are told apart by the server's reason, not by the list on screen, which can be
// behind: rules added in another tab or by a script count towards the cap too.
function saveError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 400) return err.reason ?? "The server refused this rule.";
    if (err.status === 409) {
      return err.reason === CLOCK_RULE_CAP_REASON
        ? `You already have ${MAX_CLOCK_RULES} rules, the most allowed. Delete one first.`
        : OVERLAP_MESSAGE;
    }
    if (err.status === 404) return "This rule was deleted in the meantime.";
  }
  return "Could not save the rule.";
}

interface RuleDialogProps {
  rule: CameraClockRule | null; // null: a new rule
  onClose: () => void;
  onSaved: () => void;
}

// Mounted only while open, so every opening starts from the rule (or from nothing).
function ClockRuleDialog({ rule, onClose, onSaved }: RuleDialogProps) {
  const [cameras, setCameras] = useState<CamerasState>({ status: "loading" });
  // null until the user chooses: the default then follows the cameras once they have loaded.
  const [pick, setPick] = useState<string | null>(null);
  const [make, setMake] = useState(rule?.make ?? "");
  const [model, setModel] = useState(rule?.model ?? "");
  const [offset, setOffset] = useState(rule ? String(rule.utc_offset_min) : "");
  const [startsOn, setStartsOn] = useState(rule?.starts_on ?? "");
  const [endsOn, setEndsOn] = useState(rule?.ends_on ?? "");
  const [note, setNote] = useState(rule?.note ?? "");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A save that finishes after the dialog closed must not close the next one.
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    let live = true;
    api
      .listCameras()
      .then((list) => {
        if (live) setCameras({ status: "ready", cameras: list });
      })
      .catch((e) => {
        console.error(e);
        if (live) setCameras({ status: "failed" });
      });
    return () => {
      live = false;
    };
  }, []);

  const library = cameras.status === "ready" ? cameras.cameras : [];

  function defaultPick(): string {
    if (rule) {
      const index = library.findIndex((camera) => sameCamera(camera, rule));
      return index >= 0 ? String(index) : OTHER_CAMERA;
    }
    return library.length > 0 ? "" : OTHER_CAMERA;
  }

  const choice = pick ?? (cameras.status === "loading" ? "" : defaultPick());
  const libraryCamera = choice === "" || choice === OTHER_CAMERA ? null : library[Number(choice)];
  const camera = libraryCamera
    ? { make: libraryCamera.make, model: libraryCamera.model }
    : choice === OTHER_CAMERA
      ? { make: make.trim(), model: model.trim() }
      : null;
  const periodError = startsOn && endsOn && startsOn > endsOn ? "The first day is after the last day." : null;
  const ready = camera !== null && camera.make !== "" && camera.model !== "" && offset !== "" && !periodError;

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!ready || submitting) return;
    const input: CameraClockRuleInput = {
      make: camera.make,
      model: camera.model,
      utc_offset_min: Number(offset),
      starts_on: startsOn || null,
      ends_on: endsOn || null,
      note: note.trim(),
    };
    setSubmitting(true);
    setError(null);
    try {
      if (rule) await api.updateCameraClockRule(rule.id, input);
      else await api.createCameraClockRule(input);
      onSaved();
      if (mounted.current) onClose();
    } catch (err) {
      console.error(err);
      // A rule deleted elsewhere is gone from the list too, and a conflict can come from rules
      // added elsewhere: the list catches up (at the cap, Add rule turns off).
      if (err instanceof ApiError && (err.status === 404 || err.status === 409)) onSaved();
      if (!mounted.current) return;
      setError(saveError(err));
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{rule ? "Edit clock rule" : "Add clock rule"}</DialogTitle>
          <DialogDescription>
            The UTC offset this camera's clock really showed. New uploads and scans from it are read at that offset
            wherever their time comes from its EXIF clock.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="min-w-0 space-y-3 text-sm">
          <div>
            <label className="block">
              <span className="mb-1 block font-medium">Camera</span>
              <select
                value={choice}
                disabled={cameras.status === "loading"}
                onChange={(e) => setPick(e.target.value)}
                className={FIELD_CLASS}
              >
                {cameras.status === "loading" ? (
                  <option value="">Loading your cameras…</option>
                ) : (
                  <>
                    {choice === "" && (
                      <option value="" disabled>
                        Choose a camera
                      </option>
                    )}
                    {library.map((c, i) => (
                      <option key={`${c.make}\u0000${c.model}`} value={String(i)}>
                        {libraryCameraLabel(c)}
                      </option>
                    ))}
                    <option value={OTHER_CAMERA}>Other camera</option>
                  </>
                )}
              </select>
            </label>
            {cameras.status === "failed" && (
              <p className="mt-1 text-xs text-muted-foreground">
                Could not load the cameras in your library. Type the make and model instead.
              </p>
            )}
            {cameras.status === "ready" && library.length === 0 && (
              <p className="mt-1 text-xs text-muted-foreground">
                Your library lists no cameras yet. Type the make and model instead.
              </p>
            )}
          </div>

          {choice === OTHER_CAMERA && (
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="block min-w-0">
                <span className="mb-1 block font-medium">Make</span>
                <input
                  required
                  maxLength={200}
                  autoComplete="off"
                  value={make}
                  onChange={(e) => setMake(e.target.value)}
                  className={FIELD_CLASS}
                />
              </label>
              <label className="block min-w-0">
                <span className="mb-1 block font-medium">Model</span>
                <input
                  required
                  maxLength={200}
                  autoComplete="off"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  className={FIELD_CLASS}
                />
              </label>
              <p className="text-xs text-muted-foreground sm:col-span-2">
                As the camera writes them in its files (EXIF Make and Model), for example Panasonic and DC-TZ200.
                Case does not matter.
              </p>
            </div>
          )}

          <label className="block">
            <span className="mb-1 block font-medium">Clock showed</span>
            <select required value={offset} onChange={(e) => setOffset(e.target.value)} className={FIELD_CLASS}>
              {offset === "" && (
                <option value="" disabled>
                  Choose the offset
                </option>
              )}
              {CLOCK_OFFSETS.map((minutes) => (
                <option key={minutes} value={String(minutes)}>
                  {formatUtcOffset(minutes)}
                </option>
              ))}
            </select>
          </label>

          <fieldset className="min-w-0">
            <legend className="mb-1 font-medium">Period, on the camera's own clock</legend>
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="block min-w-0">
                <span className="mb-1 block text-muted-foreground">First day (optional)</span>
                <input
                  type="date"
                  min={FIRST_DAY}
                  max={LAST_DAY}
                  value={startsOn}
                  onChange={(e) => setStartsOn(e.target.value)}
                  className={FIELD_CLASS}
                />
              </label>
              <label className="block min-w-0">
                <span className="mb-1 block text-muted-foreground">Last day (optional)</span>
                <input
                  type="date"
                  min={FIRST_DAY}
                  max={LAST_DAY}
                  value={endsOn}
                  onChange={(e) => setEndsOn(e.target.value)}
                  className={FIELD_CLASS}
                />
              </label>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              Both days are included. Leave one empty for no start or no end.
            </p>
            {periodError && <p className="mt-1 text-red-500">{periodError}</p>}
          </fieldset>

          <label className="block">
            <span className="mb-1 block font-medium">Note (optional)</span>
            <textarea
              rows={2}
              maxLength={500}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              className={FIELD_CLASS}
            />
          </label>

          {error && <p className="text-red-500">{error}</p>}
          <DialogFooter>
            <button
              type="submit"
              disabled={!ready || submitting}
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
            >
              {submitting ? "Saving…" : rule ? "Save changes" : "Add rule"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// The Settings page's "Camera clocks" section: the caller's clock rules, which ingest applies to
// new files from those cameras.
export function CameraClockRules() {
  const [rules, setRules] = useState<CameraClockRule[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // undefined: closed; null: a new rule.
  const [editing, setEditing] = useState<CameraClockRule | null | undefined>(undefined);
  // Only the latest load may set the list: a save and a delete can each start one.
  const loads = useRef(0);

  const load = useCallback(() => {
    const n = ++loads.current;
    api
      .listCameraClockRules()
      .then((list) => {
        if (n !== loads.current) return;
        setRules(list);
        setLoadFailed(false);
      })
      .catch((e) => {
        console.error(e);
        if (n === loads.current) setLoadFailed(true);
      });
  }, []);

  useEffect(load, [load]);

  const atCap = (rules?.length ?? 0) >= MAX_CLOCK_RULES;

  const remove = async (rule: CameraClockRule) => {
    const name = ruleCameraName(rule);
    if (
      !confirm(
        `Delete the clock rule for ${name} (${rulePeriod(rule)})? New uploads from this camera will be read without it. Items already in your library keep their times.`
      )
    )
      return;
    setError(null);
    try {
      await api.deleteCameraClockRule(rule.id);
    } catch (e) {
      // Already deleted, for example in another tab: that is what was asked for.
      if (!(e instanceof ApiError && e.status === 404)) {
        console.error(e);
        setError("Could not delete the rule");
      }
    }
    load();
  };

  return (
    <section aria-labelledby="camera-clocks-heading">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h2 id="camera-clocks-heading" className="text-lg font-medium">
          Camera clocks
        </h2>
        <button
          type="button"
          onClick={() => setEditing(null)}
          disabled={atCap}
          className="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50 touch:min-h-11"
        >
          Add rule
        </button>
      </div>
      <div className="mb-3 max-w-prose space-y-1 text-sm text-muted-foreground">
        <p>
          If a camera's clock showed the wrong time for a while, for example because it stayed on summer time all
          year, nothing in its files reveals it. A rule says which UTC offset the clock really showed.
        </p>
        <p>
          New uploads and scans from that camera are then read at that offset, wherever their time comes from the
          camera's EXIF clock. A photo's own GPS clock still comes first. Items already in your library keep their
          times until you edit or shift them, or an administrator runs <code className="text-xs">server retime</code>.
        </p>
      </div>
      {error && <p className="mb-2 text-sm text-red-500">{error}</p>}
      {rules === null && !loadFailed && <p className="text-sm text-muted-foreground">Loading clock rules…</p>}
      {loadFailed && (
        <div className="mb-2 flex flex-wrap items-center gap-2 text-sm">
          <span className="text-red-500">Could not load your clock rules.</span>
          <button type="button" onClick={load} className={`${ROW_BUTTON_CLASS} hover:bg-muted`}>
            Try again
          </button>
        </div>
      )}
      {rules && rules.length === 0 && <p className="text-sm text-muted-foreground">No clock rules yet.</p>}
      {rules && rules.length > 0 && (
        <ul className="divide-y border-y">
          {rules.map((rule) => {
            const name = ruleCameraName(rule);
            const period = rulePeriod(rule);
            return (
              <li key={rule.id} className="flex flex-wrap items-start justify-between gap-x-3 gap-y-1 py-2">
                <div className="min-w-0 flex-1 text-sm">
                  <p className="font-medium [overflow-wrap:anywhere]">{name}</p>
                  <p className="text-muted-foreground">
                    Clock showed {formatUtcOffset(rule.utc_offset_min)} · {period}
                  </p>
                  {rule.note && (
                    <p className="whitespace-pre-line text-xs text-muted-foreground [overflow-wrap:anywhere]">
                      {rule.note}
                    </p>
                  )}
                </div>
                <div className="flex shrink-0 gap-2">
                  <button
                    type="button"
                    onClick={() => setEditing(rule)}
                    aria-label={`Edit rule for ${name}, ${period}`}
                    className={`${ROW_BUTTON_CLASS} hover:bg-muted`}
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    onClick={() => remove(rule)}
                    aria-label={`Delete rule for ${name}, ${period}`}
                    className={`${ROW_BUTTON_CLASS} text-red-600 hover:bg-red-50`}
                  >
                    Delete
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
      {atCap && (
        <p className="mt-2 text-sm text-muted-foreground">
          You have {MAX_CLOCK_RULES} rules, the most allowed. Delete one to add another.
        </p>
      )}
      {editing !== undefined && (
        <ClockRuleDialog
          key={editing?.id ?? "new"}
          rule={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setError(null);
            load();
          }}
        />
      )}
    </section>
  );
}
