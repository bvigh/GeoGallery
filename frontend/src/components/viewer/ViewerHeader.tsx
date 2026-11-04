import { X } from "lucide-react";

// Top bar of a full-screen 360° viewer. The hint gives way to the close button on a narrow phone.
export default function ViewerHeader({ title, onClose }: { title: string; onClose: () => void }) {
  return (
    <div className="flex shrink-0 items-center gap-2 bg-black/60 py-1 pl-4 pr-1 text-sm text-white touch:py-0">
      <p className="min-w-0 flex-1 truncate">
        <span className="font-medium">{title}</span>
        <span className="text-white/70"> · drag to look around, pinch or scroll to zoom</span>
      </p>
      <button
        type="button"
        onClick={onClose}
        aria-label="Close 360 view"
        className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full hover:bg-white/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 touch:h-11 touch:w-11"
      >
        <X className="h-5 w-5" aria-hidden="true" />
      </button>
    </div>
  );
}
