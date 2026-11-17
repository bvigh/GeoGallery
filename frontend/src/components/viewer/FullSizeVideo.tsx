import { forwardRef, useState, type ComponentPropsWithoutRef } from "react";
import { useVideosStartMuted } from "@/lib/videoSound";

// The <video> of a full-size player without a sphere: a gallery's, the Map page's view, a Game
// round's and a challenge round's item full size. It starts muted or with sound as the user's
// setting says, read once as it mounts: were the setting to load or change while the video is
// open, React would set `muted` over what M or the player's own button chose. Hosts key it per
// item, so each one starts as the setting says.
//
// With sound, a browser that allows none yet (no click or key on the page since it loaded, as from
// a link) leaves an autoPlay video paused until its play button.
export const FullSizeVideo = forwardRef<HTMLVideoElement, ComponentPropsWithoutRef<"video">>(function FullSizeVideo(
  props,
  ref
) {
  const startMuted = useVideosStartMuted();
  const [muted] = useState(startMuted);
  return <video ref={ref} {...props} muted={muted} />;
});
