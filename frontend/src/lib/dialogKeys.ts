// Fullscreen views (the gallery, the Map page's view, Video360) listen for keys on window. A dialog
// (components/ui/dialog) over them owns the keyboard, but stopping propagation in the dialog is not
// enough: focus falls back to <body> when the focused control is disabled or re-rendered away, and
// then the keys go straight to window.
//
// Escape needs its own check. Radix closes the dialog from a capture listener on document and
// marks the event handled (preventDefault). In a browser, React commits that close in the
// microtask right after the listener returns, before the event reaches window, so by then the
// dialog is gone from the page and the view's own state may already say it is closed.

// Set on every DialogContent by components/ui/dialog.tsx.
const OPEN_DIALOG = '[data-app-dialog][data-state="open"]';

// Whether a key event belongs to a dialog (open now, or just closed by this very Escape) and
// should be ignored by the view behind it.
export function keyBelongsToDialog(e: KeyboardEvent): boolean {
  return e.defaultPrevented || document.querySelector(OPEN_DIALOG) !== null;
}
