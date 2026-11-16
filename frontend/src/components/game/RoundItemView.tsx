import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { api } from "@/api/client";
import Panorama360 from "@/components/viewer/Panorama360";
import Video360 from "@/components/viewer/Video360";
import VideoDecodeNotice from "@/components/viewer/VideoDecodeNotice";
import ViewerHeader, { SPHERE_HINT } from "@/components/viewer/ViewerHeader";
import { ZoomableImage } from "@/components/viewer/ZoomableImage";
import { itemKind, type RoundItemState } from "@/lib/challenge";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { keepTabInside } from "@/lib/focusTrap";
import { useCloseOnBack } from "@/lib/history";
import { useVideoDecodeCheck } from "@/lib/video";
import { useVideoKeys } from "@/lib/videoKeys";
import type { ChallengeRoundResult, OrientationKeyframe } from "@/types";

// How to move what is shown, beside the title.
function hintFor(mediaType: string | null): string | null {
  if (mediaType === "photo_360" || mediaType === "video_360") return SPHERE_HINT;
  return mediaType?.startsWith("video") ? null : "pinch or scroll to zoom";
}

// The item, filling the view, in the viewer a round shows it with (RoundPlay).
function RoundItemMedia({ result, title, onClose }: { result: ChallengeRoundResult; title: string; onClose: () => void }) {
  const url = result.url ?? "";
  const videoRef = useRef<HTMLVideoElement>(null);
  const videoCheck = useVideoDecodeCheck(url);
  const videoKeys = useVideoKeys(videoRef);
  const [keyframes, setKeyframes] = useState<OrientationKeyframe[]>([]);

  // A 360° video follows its owner's keyframes, which a player reads as they read the item. Until
  // they come, or without them, it opens on its default view.
  useEffect(() => {
    if (result.media_type !== "video_360" || !result.media_id) return;
    let current = true;
    api
      .getKeyframes(result.media_id)
      .then((list) => current && setKeyframes(list))
      .catch(() => current && setKeyframes([]));
    return () => {
      current = false;
    };
  }, [result.media_id, result.media_type]);

  if (result.media_type === "video_360") {
    return (
      <Video360
        url={url}
        onClose={onClose}
        contained
        initialYaw={result.initial_yaw}
        initialPitch={result.initial_pitch}
        initialFov={result.initial_fov}
        keyframes={keyframes}
      />
    );
  }
  if (result.media_type === "photo_360") {
    return (
      <Panorama360
        url={url}
        onClose={onClose}
        contained
        initialYaw={result.initial_yaw}
        initialPitch={result.initial_pitch}
        initialFov={result.initial_fov}
      />
    );
  }
  if (result.media_type?.startsWith("video")) {
    return (
      <>
        <video
          ref={videoRef}
          src={url}
          controls
          autoPlay
          className="absolute inset-0 h-full w-full object-contain outline-none"
          {...videoCheck.handlers}
          {...videoKeys}
        />
        {videoCheck.unsupported && <VideoDecodeNotice />}
      </>
    );
  }
  // The thumbnail's request mode, so the browser can reuse its download.
  return <ZoomableImage src={url} alt={title} crossOrigin="anonymous" />;
}

interface Props {
  // The round's number and media type as the results showed them when the view opened: the title
  // while the challenge is fetched again.
  number: number;
  mediaType: string | null;
  state: RoundItemState;
  // Fetches the challenge again after a fetch failed.
  onRetry: () => void;
  onClose: () => void;
  // Puts the focus in the host when the view closes and the button that opened it is gone: the
  // fresh results took the round's thumbnail away.
  returnFocus?: () => void;
}

// A round's item full size, over a challenge's results, in the viewer the round used. Nothing of
// the answer or the guesses is drawn over it. Escape and the back gesture close it alone.
export function RoundItemView({ number, mediaType, state, onRetry, onClose, returnFocus }: Props) {
  // The round as the server has it now, once the view shows it: a round before it may have gone.
  const shown = state.status === "shown" ? state.result : null;
  const kind = itemKind(shown?.media_type ?? mediaType);
  const title = `Round ${shown?.number ?? number}'s ${kind}`;
  const rootRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Before the viewers' own effects, so the focus goes back to the button that opened the view:
  // Video360 takes the focus as it starts. Once per opening, with the host's returnFocus of then.
  useLayoutEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const fallback = returnFocus;
    rootRef.current?.focus();
    return () => {
      if (opener && opener !== document.body && opener.isConnected) opener.focus();
      else fallback?.();
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useCloseOnBack(onClose);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (keyBelongsToDialog(e)) return;
      if (e.key === "Tab") {
        keepTabInside(e, rootRef.current);
        return;
      }
      if (e.key === "Escape") onCloseRef.current();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div
      ref={rootRef}
      role="dialog"
      aria-modal="true"
      aria-label={title}
      tabIndex={-1}
      className="fixed inset-0 z-[1100] flex flex-col bg-black text-white outline-none"
    >
      <ViewerHeader
        title={title}
        hint={shown ? hintFor(shown.media_type) : null}
        closeLabel="Close full-size view"
        onClose={onClose}
      />
      <div className="relative min-h-0 flex-1">
        {shown ? (
          // A new URL is a new viewer: none keeps another item's state.
          <RoundItemMedia key={shown.url} result={shown} title={title} onClose={onClose} />
        ) : (
          <div className="flex h-full flex-col items-center justify-center gap-3 p-4 text-center text-sm text-white/80">
            {state.status === "loading" && <p>Loading…</p>}
            {state.status === "failed" && (
              <>
                <p role="alert">Could not load the {kind}.</p>
                <button
                  type="button"
                  onClick={() => {
                    // The button goes with the failure: the focus stays in the view.
                    rootRef.current?.focus();
                    onRetry();
                  }}
                  className="rounded border border-white/40 px-3 py-1.5 hover:bg-white/10 touch:min-h-11"
                >
                  Try again
                </button>
              </>
            )}
            {state.status === "gone" && <p role="status">{state.reason}</p>}
          </div>
        )}
      </div>
    </div>
  );
}
