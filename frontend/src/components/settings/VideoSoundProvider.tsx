import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api } from "@/api/client";
import { VideoSoundContext, videosStartMuted, type VideoSound } from "@/lib/videoSound";
import type { UserSettings } from "@/types";

// The signed-in user's video sound setting, for every full-size player: loaded once when the app
// starts. The Settings page reports what it loads and saves, so a change applies to the next video
// at once. Until the first answer, and without one, videos start muted (useVideosStartMuted).
export function VideoSoundProvider({ children }: { children: ReactNode }) {
  const [startMuted, setStartMuted] = useState<boolean | null>(null);
  // Set once the Settings page has reported: the first load may answer after it, with older news.
  const reported = useRef(false);

  useEffect(() => {
    let live = true;
    api
      .getSettings()
      .then((settings) => {
        if (live && !reported.current) setStartMuted(videosStartMuted(settings));
      })
      .catch((e) => console.warn("Could not load the video sound setting:", e));
    return () => {
      live = false;
    };
  }, []);

  const report = useCallback((settings: UserSettings) => {
    reported.current = true;
    setStartMuted(videosStartMuted(settings));
  }, []);

  const value = useMemo<VideoSound>(() => ({ startMuted, report }), [startMuted, report]);
  return <VideoSoundContext.Provider value={value}>{children}</VideoSoundContext.Provider>;
}
