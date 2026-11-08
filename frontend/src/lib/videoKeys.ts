import { useEffect, useRef, type RefObject, type SyntheticEvent } from "react";
import { keyBelongsToDialog } from "@/lib/dialogKeys";

// Space and M in the full-size video players: the gallery, the Map page's view, the Game round and
// Video360.

export interface VideoKeyActions {
  togglePlayback: () => void;
  toggleMute: () => void;
}

// Keys go to a field being filled in. A range slider takes no text, so a seek bar that was just
// dragged keeps the shortcuts.
function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target instanceof HTMLInputElement) return target.type !== "range";
  return target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement || target.isContentEditable;
}

// Space plays or pauses and M mutes, through whatever getActions returns. With null (no video on
// screen) the keys stay with the page. Returns the cleanup.
//
// A capture listener that also stops the event: with focus on a <video controls>, Chromium's own
// controls toggle on Space even after a bubble listener on window called preventDefault, so the
// two toggles cancelled out.
export function listenVideoKeys(getActions: () => VideoKeyActions | null): () => void {
  function onKey(e: KeyboardEvent) {
    const space = e.code === "Space";
    if ((!space && e.key !== "m" && e.key !== "M") || e.ctrlKey || e.metaKey || e.altKey) return;
    if (isTyping(e.target) || keyBelongsToDialog(e)) return;
    const actions = getActions();
    if (!actions) return;
    e.preventDefault();
    e.stopPropagation();
    // A held key repeats, and each repeat would toggle again.
    if (e.repeat) return;
    if (space) actions.togglePlayback();
    else actions.toggleMute();
  }
  window.addEventListener("keydown", onKey, true);
  return () => window.removeEventListener("keydown", onKey, true);
}

// The keys for a <video controls>, while one is mounted and `enabled` holds. Spread the returned
// handlers on the element.
//
// Chromium keeps key events inside its native controls: with focus on the play button, mute button
// or timeline, not even a capture listener on window hears them. So once a mouse has used a
// control, focus leaves it. The handlers run on the media's own events because a click inside the
// controls never reaches the page either, and because a control that changes nothing must keep
// focus: blurring the ⋮ button or its menu would close the menu.
export function useVideoKeys(videoRef: RefObject<HTMLVideoElement>, enabled = true) {
  // From a Tab until the mouse moves: focus reached by keyboard stays, or arrow keys on the
  // timeline would seek once and then go to the page.
  const tabbing = useRef(false);

  useEffect(() => {
    if (!enabled) return;
    return listenVideoKeys(() => {
      const video = videoRef.current;
      if (!video) return null;
      return {
        togglePlayback: () => {
          if (video.paused) video.play().catch(() => {});
          else video.pause();
        },
        toggleMute: () => {
          video.muted = !video.muted;
        },
      };
    });
  }, [videoRef, enabled]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Tab") tabbing.current = true;
    };
    const onPointerMove = () => {
      tabbing.current = false;
    };
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("pointermove", onPointerMove, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("pointermove", onPointerMove, true);
    };
  }, []);

  const releaseFocus = (e: SyntheticEvent<HTMLVideoElement>) => {
    const video = e.currentTarget;
    if (!tabbing.current && document.activeElement === video) video.blur();
  };

  return { onPlay: releaseFocus, onPause: releaseFocus, onSeeked: releaseFocus, onVolumeChange: releaseFocus };
}
