import { createContext, useContext } from "react";
import type { UserSettings } from "@/types";

// How a video opened full size starts: muted or with sound, as the user's setting says (Settings →
// Video sound). Every full-size player reads it as a video starts; after that M and the player's own
// button turn the sound on and off, whatever the setting.

export interface VideoSound {
  // Whether a video starts muted: the user's choice, else the server's default. null until the
  // settings have loaded, or when they could not be.
  startMuted: boolean | null;
  // The Settings page reports the settings it loaded or saved, so the next video starts as they say.
  report: (settings: UserSettings) => void;
}

// Outside VideoSoundProvider (a test) nothing is known.
export const VideoSoundContext = createContext<VideoSound>({ startMuted: null, report: () => {} });

// Whether the settings start videos muted: the user's choice, else the server's default. A backend
// without the setting starts them with sound.
export function videosStartMuted(settings: UserSettings): boolean {
  return settings.videos_start_muted ?? settings.defaults?.videos_start_muted ?? false;
}

// Whether a video starting now starts muted. Until the setting is known it does: a video that
// starts silent bothers nobody, and M gives it sound.
export function useVideosStartMuted(): boolean {
  return useContext(VideoSoundContext).startMuted ?? true;
}

// Plays a video muted or with sound. A browser refuses sound until the page has had a click or a
// key (a video opened from a link, or after a reload): the video then plays muted, and M or the
// player's own button turns the sound on. Resolves, once the video plays or cannot, to whether it
// is muted.
export async function startVideo(video: HTMLVideoElement, muted: boolean): Promise<boolean> {
  video.muted = muted;
  try {
    await video.play();
  } catch (e) {
    if (!muted && isNotAllowed(e)) {
      video.muted = true;
      try {
        await video.play();
      } catch {
        // Refused even muted (a browser can block every autoplay): it waits for its play button.
      }
    }
    // Any other failure (a pause or a new source before it played, a file it cannot play) is the
    // player's own to show.
  }
  return video.muted;
}

function isNotAllowed(e: unknown): boolean {
  return typeof e === "object" && e !== null && (e as { name?: unknown }).name === "NotAllowedError";
}
