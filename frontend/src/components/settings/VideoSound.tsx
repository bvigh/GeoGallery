import { useCallback, useContext, useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import { VideoSoundContext, videosStartMuted } from "@/lib/videoSound";
import type { UserSettings } from "@/types";

const ROW_BUTTON_CLASS =
  "rounded-md border px-2 py-1 text-xs font-medium touch:min-h-11 touch:px-3 touch:text-sm";

const CHOICES = [
  { muted: false, label: "With sound" },
  { muted: true, label: "Muted" },
];

// The Settings page's "Video sound": the caller's users.videos_start_muted, whether a video opened
// full size starts muted or with sound. What it loads and saves goes to VideoSoundProvider, so the
// next video starts as it says without a reload.
export function VideoSound() {
  const { report } = useContext(VideoSoundContext);
  const [settings, setSettings] = useState<UserSettings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [muted, setMuted] = useState(false); // the choice on screen
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
        setMuted(videosStartMuted(s));
        report(s);
      })
      .catch((e) => {
        console.error(e);
        if (n === loads.current) setLoadFailed(true);
      });
  }, [report]);

  useEffect(load, [load]);

  const changed = settings !== null && muted !== videosStartMuted(settings);

  // The button stays focusable while it cannot save (aria-disabled), so the focus never falls to
  // <body> when a save ends.
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!changed || saving) return;
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      const next = await api.updateSettings({ videos_start_muted: muted });
      setSettings(next);
      setMuted(videosStartMuted(next));
      report(next);
      setSaved(true);
    } catch (err) {
      console.error(err);
      setError(
        err instanceof ApiError && err.status === 400 && err.reason ? err.reason : "Could not save your video sound setting."
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <section aria-labelledby="video-sound-heading">
      <h2 id="video-sound-heading" className="mb-2 text-lg font-medium">
        Video sound
      </h2>
      <div className="mb-3 max-w-prose space-y-1 text-sm text-muted-foreground">
        <p>
          Whether a video starts with sound when you open it full size: in a gallery, on the map, in a Game round and
          in a challenge's results. M, or the player's own button, turns the sound on and off while it plays.
        </p>
        <p>
          Browsers allow sound only once you have clicked or pressed a key on the page, so a video opened from a link or
          after a reload may start muted, or wait for its play button.
        </p>
      </div>
      {settings === null && !loadFailed && <p className="text-sm text-muted-foreground">Loading your settings…</p>}
      {loadFailed && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-red-500">Could not load your video sound setting.</span>
          <button type="button" onClick={load} className={`${ROW_BUTTON_CLASS} hover:bg-muted`}>
            Try again
          </button>
        </div>
      )}
      {settings && (
        <form onSubmit={save} className="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
          <div role="radiogroup" aria-labelledby="video-sound-label" className="flex flex-wrap items-center gap-x-4">
            <span id="video-sound-label" className="font-medium">
              Videos start
            </span>
            {CHOICES.map((choice) => (
              <label key={choice.label} className="flex items-center gap-2 touch:min-h-11">
                <input
                  type="radio"
                  name="video-sound"
                  checked={muted === choice.muted}
                  onChange={() => {
                    setMuted(choice.muted);
                    setSaved(false);
                    setError(null);
                  }}
                  className="touch:h-5 touch:w-5"
                />
                {choice.muted === settings.defaults.videos_start_muted ? `${choice.label} (default)` : choice.label}
              </label>
            ))}
          </div>
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
    </section>
  );
}
