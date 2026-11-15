import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import { CameraZoneSelect } from "@/components/CameraZoneSelect";
import { browserTimeZone } from "@/lib/time";
import { serverZoneLabel } from "@/lib/timeZones";
import type { UserSettings } from "@/types";

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const FIELD_CLASS = "min-w-0 max-w-full rounded-md border bg-background px-2 py-1 touch:min-h-11 touch:text-base";
const ROW_BUTTON_CLASS =
  "rounded-md border px-2 py-1 text-xs font-medium touch:min-h-11 touch:px-3 touch:text-sm";

// The Settings page's "Camera time zone": the caller's users.camera_timezone, the zone a capture
// time is read in when the file gives no offset of its own.
export function CameraTimeZone() {
  const [settings, setSettings] = useState<UserSettings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [choice, setChoice] = useState(""); // "" is the server's zone
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  // Only the latest load may fill the form.
  const loads = useRef(0);

  const load = useCallback(() => {
    const n = ++loads.current;
    setLoadFailed(false);
    api
      .getSettings()
      .then((s) => {
        if (n !== loads.current) return;
        setSettings(s);
        setChoice(s.camera_timezone ?? "");
      })
      .catch((e) => {
        console.error(e);
        if (n === loads.current) setLoadFailed(true);
      });
  }, []);

  useEffect(load, [load]);

  const changed = settings !== null && choice !== (settings.camera_timezone ?? "");

  // The button stays focusable while it cannot save (aria-disabled), so the focus never falls to
  // <body> when a save ends.
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!changed || saving) return;
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      const next = await api.updateSettings({ camera_timezone: choice === "" ? null : choice });
      setSettings(next);
      setChoice(next.camera_timezone ?? "");
      setSaved(true);
    } catch (err) {
      console.error(err);
      setError(
        err instanceof ApiError && err.status === 400 && err.reason ? err.reason : "Could not save your camera time zone."
      );
    } finally {
      setSaving(false);
    }
  };

  const browserZone = browserTimeZone();

  return (
    <section aria-labelledby="camera-zone-heading">
      <h2 id="camera-zone-heading" className="mb-2 text-lg font-medium">
        Camera time zone
      </h2>
      <div className="mb-3 max-w-prose space-y-1 text-sm text-muted-foreground">
        <p>
          The zone your cameras' clocks are set to. A capture time is read in it only when the file does not say
          its own offset: no usable GPS time, no camera clock rule below, and no offset tag the camera can be
          trusted for. An upload can choose another zone for its files on the Upload page.
        </p>
        <p>
          A new zone dates new uploads and scans. Items already in your library keep their times until an
          administrator runs <code className="text-xs">server retime</code>, which then reads them in this zone,
          apart from files uploaded with a zone of their own.
        </p>
      </div>
      {settings === null && !loadFailed && <p className="text-sm text-muted-foreground">Loading your settings…</p>}
      {loadFailed && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-red-500">Could not load your camera time zone.</span>
          <button type="button" onClick={load} className={`${ROW_BUTTON_CLASS} hover:bg-muted`}>
            Try again
          </button>
        </div>
      )}
      {settings && (
        <form onSubmit={save} className="flex flex-wrap items-center gap-2 text-sm">
          <label htmlFor="camera-zone" className="font-medium">
            Zone
          </label>
          <CameraZoneSelect
            id="camera-zone"
            value={choice}
            onChange={(zone) => {
              setChoice(zone);
              setSaved(false);
              setError(null);
            }}
            defaultLabel={`Server default (${serverZoneLabel(settings.defaults.camera_timezone)})`}
            className={FIELD_CLASS}
          />
          <button
            type="submit"
            aria-disabled={!changed || saving}
            className="rounded-lg bg-primary px-3 py-1.5 font-medium text-primary-foreground aria-disabled:cursor-default aria-disabled:opacity-50 touch:min-h-11"
          >
            {saving ? "Saving…" : "Save"}
          </button>
          <span role="status" className="text-muted-foreground">
            {saved ? "Saved." : ""}
          </span>
        </form>
      )}
      {error && <p className="mt-2 text-sm text-red-500">{error}</p>}
      {settings && <p className="mt-1 text-xs text-muted-foreground">Your browser is in {browserZone}.</p>}
    </section>
  );
}
