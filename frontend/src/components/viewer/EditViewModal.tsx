import { useRef, useState } from "react";
import { X } from "lucide-react";
import type { MediaItem, OrientationKeyframe } from "@/types";
import { api } from "@/api/client";
import { useCloseOnBack } from "@/lib/useCloseOnBack";
import Panorama360 from "@/components/viewer/Panorama360";
import Video360 from "@/components/viewer/Video360";

// The editor for a 360° item's default view and, for a video, its orientation keyframes. Owner
// only: the caller hides it on shared items (the backend 404s the writes).
//
// It starts from item.keyframes, so a caller opens it on a video only once they are loaded:
// saving from an empty list would delete the stored ones.
//
// Viewer and controls side by side, or stacked on a phone held upright, where a 16rem sidebar
// would leave the viewer a sliver.
export function EditViewModal({
  item,
  url,
  onClose,
  onSaved,
}: {
  item: MediaItem;
  url: string;
  onClose: () => void;
  onSaved: (updated: Partial<MediaItem>) => void;
}) {
  const captureRef = useRef<(() => { yaw: number; pitch: number; fov: number }) | null>(null);
  const captureTimeRef = useRef<(() => number) | null>(null);
  const controlRef = useRef<{ pause: () => void; seek: (t: number) => void; seekAndApply: (t: number) => void } | null>(null);
  const [keyframes, setKeyframes] = useState<OrientationKeyframe[]>(item.keyframes ?? []);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useCloseOnBack(onClose);

  // Keeps the editor open with a message when the request fails, so nothing set here is lost.
  async function save(what: string, request: () => Promise<void>, updated: Partial<MediaItem>) {
    setSaving(true);
    setError(null);
    try {
      await request();
    } catch (e) {
      console.error(`Failed to save ${what}:`, e);
      setError(`Could not save the ${what}`);
      setSaving(false);
      return;
    }
    setSaving(false);
    onSaved(updated);
  }

  async function handleSetDefaultView() {
    if (!captureRef.current) return;
    const { yaw, pitch, fov } = captureRef.current();
    const settings = { initial_yaw: yaw, initial_pitch: pitch, initial_fov: fov };
    await save("default view", () => api.updateViewSettings(item.id, settings), settings);
  }

  function handleAddKeyframe() {
    if (!captureRef.current || !captureTimeRef.current) return;
    controlRef.current?.pause();
    const { yaw, pitch, fov } = captureRef.current();
    const time = captureTimeRef.current();
    setKeyframes((prev) => {
      const without = prev.filter((kf) => Math.abs(kf.time - time) > 0.05);
      return [...without, { time, yaw, pitch, fov }].sort((a, b) => a.time - b.time);
    });
  }

  function handleSeekToKeyframe(time: number) {
    controlRef.current?.seekAndApply(time);
  }

  async function handleSaveKeyframes() {
    await save("keyframes", () => api.replaceKeyframes(item.id, keyframes), { keyframes });
  }

  function handleDeleteKeyframe(time: number) {
    setKeyframes((prev) => prev.filter((kf) => kf.time !== time));
  }

  const isVideo = item.media_type === "video_360";

  return (
    <div className="fixed inset-0 z-[1100] flex flex-col bg-black">
      <div className="flex shrink-0 items-center justify-between gap-2 bg-black/80 py-1 pl-4 pr-1 text-sm text-white">
        <span className="min-w-0 truncate font-medium">Edit default view — {item.filename}</span>
        <button
          onClick={onClose}
          aria-label="Close view editor"
          className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full hover:bg-white/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 touch:h-11 touch:w-11"
        >
          <X className="h-5 w-5" aria-hidden="true" />
        </button>
      </div>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row short:flex-row">
        <div className="relative min-h-0 flex-1">
          {isVideo ? (
            <Video360
              url={url}
              onClose={() => {}}
              contained
              // A click or a small nudge of the view must not restart playback and move the
              // time the next keyframe stores.
              tapToPlay={false}
              initialYaw={item.initial_yaw}
              initialPitch={item.initial_pitch}
              initialFov={item.initial_fov}
              keyframes={keyframes}
              captureRef={captureRef}
              captureTimeRef={captureTimeRef}
              controlRef={controlRef}
            />
          ) : (
            <Panorama360
              url={url}
              onClose={() => {}}
              contained
              initialYaw={item.initial_yaw}
              initialPitch={item.initial_pitch}
              initialFov={item.initial_fov}
              captureRef={captureRef}
            />
          )}
        </div>

        <div className="flex max-h-[40%] shrink-0 flex-col gap-3 overflow-y-auto overscroll-contain bg-black/90 p-4 text-white md:max-h-none md:w-64 short:max-h-none short:w-56">
          <div>
            <p className="text-xs text-white/60 mb-1">Default view (yaw / pitch / zoom)</p>
            <button
              onClick={handleSetDefaultView}
              disabled={saving}
              className="w-full rounded bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50 touch:min-h-11"
            >
              Set current view as default
            </button>
          </div>

          {isVideo && (
            <div>
              <p className="text-xs text-white/60 mb-1">Keyframes</p>
              <button
                onClick={handleAddKeyframe}
                className="w-full rounded bg-white/10 px-3 py-1.5 text-sm hover:bg-white/20 touch:min-h-11"
              >
                + Add keyframe at current time
              </button>
              {keyframes.length > 0 && (
                <ul className="mt-2 space-y-1 text-xs">
                  {keyframes.map((kf) => (
                    <li key={kf.time} className="rounded bg-white/5 px-2 py-1 touch:py-0">
                      <div className="flex items-center justify-between">
                        <button
                          onClick={() => handleSeekToKeyframe(kf.time)}
                          className="tabular-nums text-left hover:text-primary touch:min-h-11"
                          title="Jump to this keyframe"
                        >
                          {kf.time.toFixed(1)}s &nbsp;·&nbsp;
                          y:{(kf.yaw * 180 / Math.PI).toFixed(0)}°&nbsp;
                          p:{(kf.pitch * 180 / Math.PI).toFixed(0)}°&nbsp;
                          z:{kf.fov.toFixed(0)}°
                        </button>
                        <div className="flex items-center gap-1 ml-2 shrink-0">
                          <button
                            onClick={() => handleAddKeyframe()}
                            className="text-white/40 hover:text-white text-xs px-1 touch:flex touch:min-h-11 touch:min-w-11 touch:items-center touch:justify-center"
                            title="Update this keyframe with current view"
                            aria-label="Update keyframe"
                          >
                            ↺
                          </button>
                          <button
                            onClick={() => handleDeleteKeyframe(kf.time)}
                            className="text-white/40 hover:text-white touch:flex touch:min-h-11 touch:min-w-11 touch:items-center touch:justify-center"
                            aria-label="Delete keyframe"
                          >
                            ✕
                          </button>
                        </div>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
              {keyframes.length > 0 && (
                <button
                  onClick={handleSaveKeyframes}
                  disabled={saving}
                  className="mt-2 w-full rounded bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50 touch:min-h-11"
                >
                  Save keyframes
                </button>
              )}
            </div>
          )}

          {error && (
            <p role="alert" className="text-xs text-red-400">
              {error}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
