import { VIDEO_HELP_URL } from "@/lib/video";

// Shown over a video whose picture the browser cannot decode. The parent must be positioned.
// Clicks stay inside the box so they never close or navigate the viewer underneath.
export default function VideoDecodeNotice() {
  return (
    <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center p-4">
      <div
        role="alert"
        className="pointer-events-auto max-w-sm rounded-lg bg-black/80 px-4 py-3 text-center text-sm text-white"
        onClick={(e) => e.stopPropagation()}
      >
        <p>This browser can't decode this video's picture. It is most likely HEVC (H.265).</p>
        <a
          href={VIDEO_HELP_URL}
          target="_blank"
          rel="noreferrer"
          className="mt-2 inline-block font-medium underline touch:mt-0 touch:py-3"
        >
          How to enable HEVC playback
        </a>
      </div>
    </div>
  );
}
