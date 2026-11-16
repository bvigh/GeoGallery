// The Tab trap of the fullscreen views (the Checks view, the evidence view, a challenge and a
// round's item full size). The page behind a view is covered but still in the tab order, so Tab
// and Shift+Tab wrap round inside the view, and a Tab from outside it (the focus fell to <body>)
// comes into it.

// A video's native controls take the focus too: a keyboard user seeks on its timeline.
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), video[controls], [tabindex]:not([tabindex="-1"])';

// Call from the view's keydown listener for a Tab. A view with nothing to focus keeps the focus
// where it is.
export function keepTabInside(e: KeyboardEvent, root: HTMLElement | null) {
  if (!root) return;
  const items = root.querySelectorAll<HTMLElement>(FOCUSABLE);
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement;
  if (!first || !last) {
    e.preventDefault();
  } else if (!root.contains(active)) {
    e.preventDefault();
    first.focus();
  } else if (e.shiftKey && (active === first || active === root)) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && active === last) {
    e.preventDefault();
    first.focus();
  }
}

// Whether a key goes to a field being filled in, where Escape and the views' other keys stay out.
export function isTextField(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null;
  return (
    element?.tagName === "INPUT" ||
    element?.tagName === "TEXTAREA" ||
    element?.tagName === "SELECT" ||
    element?.isContentEditable === true
  );
}
