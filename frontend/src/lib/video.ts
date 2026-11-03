import { useState, type SyntheticEvent } from "react";

// Static guide (frontend/public) for browsers that cannot decode a video, mostly HEVC on Linux.
export const VIDEO_HELP_URL = "/video-playback.html";

const HAVE_CURRENT_DATA = 2;
const MEDIA_ERR_DECODE = 3;
const MEDIA_ERR_SRC_NOT_SUPPORTED = 4;

// True when the browser cannot show the video's picture. Chromium on Linux decodes HEVC only on
// the GPU; without that it plays the audio track alone and the picture stays 0×0. A file with no
// audio track fails with a media error instead.
export function videoDecodeFailed(video: HTMLVideoElement): boolean {
  const code = video.error?.code;
  if (code === MEDIA_ERR_DECODE || code === MEDIA_ERR_SRC_NOT_SUPPORTED) return true;
  return video.readyState >= HAVE_CURRENT_DATA && video.videoWidth === 0;
}

// Checks a <video> as it loads and plays. The result belongs to one src, so the next item starts
// unflagged.
export function useVideoDecodeCheck(src: string) {
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const check = (e: SyntheticEvent<HTMLVideoElement>) => {
    if (videoDecodeFailed(e.currentTarget)) setFailedSrc(src);
  };
  return {
    unsupported: failedSrc === src,
    handlers: { onLoadedData: check, onTimeUpdate: check, onError: check },
  };
}
