// Why a 360° viewer has no picture: the device could not create a WebGL context at all, or the
// browser took it away later (GPU reset, too many contexts).
export type SphereFailure = "unavailable" | "lost";

const MESSAGES: Record<SphereFailure, string> = {
  unavailable: "This device cannot show 360° views (WebGL unavailable).",
  lost: "The 360° view stopped: the browser lost its graphics context. Close it and open it again.",
};

// Shown over a 360° viewer that cannot draw. The parent must be positioned. Pointer events pass
// through, so the viewer's close button and the gallery's swipes and taps keep working.
export default function WebGLNotice({ failure }: { failure: SphereFailure }) {
  return (
    <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center p-4">
      <p role="alert" className="max-w-sm rounded-lg bg-black/80 px-4 py-3 text-center text-sm text-white">
        {MESSAGES[failure]}
      </p>
    </div>
  );
}
