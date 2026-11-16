import { X } from "lucide-react";

// How to look around a 360° item: the hint of the 360° viewers' header.
export const SPHERE_HINT = "drag to look around, pinch or scroll to zoom";

// Top bar of a full-screen viewer: what is shown, how to move it, and the close button. The hint
// gives way to the close button on a narrow phone. The 360° viewers use it as it comes; another
// view gives its own hint (null for none) and close label.
export default function ViewerHeader({
  title,
  onClose,
  hint = SPHERE_HINT,
  closeLabel = "Close 360 view",
}: {
  title: string;
  onClose: () => void;
  hint?: string | null;
  closeLabel?: string;
}) {
  return (
    <div className="flex shrink-0 items-center gap-2 bg-black/60 py-1 pl-4 pr-1 text-sm text-white touch:py-0">
      <p className="min-w-0 flex-1 truncate">
        <span className="font-medium">{title}</span>
        {hint && <span className="text-white/70"> · {hint}</span>}
      </p>
      <button
        type="button"
        onClick={onClose}
        aria-label={closeLabel}
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full hover:bg-white/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 touch:h-11 touch:w-11"
      >
        <X className="h-5 w-5" aria-hidden="true" />
      </button>
    </div>
  );
}
