import { useEffect, useLayoutEffect, useRef } from "react";

// The history.state field that counts the fullscreen views stacked on the app's own entry.
export const BACK_DEPTH_KEY = "ggCloseOnBack";

type Owner = { close: () => void };

// One module-wide stack, so nested views and views closing together agree on history. Views only
// change the stack; flush() then brings history in line with it, one traversal at a time.
let base = 0; // depth of the entry the views sit on
const entries: (Owner | null)[] = []; // our entries above base, innermost last; null = view gone
const waiting: Owner[] = []; // open views that have no entry yet
let popping = false; // our history.go has not reached its popstate yet
let flushQueued = false;
let listening = false;

function stateDepth(): number {
  const state: unknown = window.history.state;
  if (typeof state !== "object" || state === null) return 0;
  const depth = (state as Record<string, unknown>)[BACK_DEPTH_KEY];
  return typeof depth === "number" && Number.isInteger(depth) && depth > 0 ? depth : 0;
}

function pushEntry(depth: number) {
  const state: unknown = window.history.state;
  const fields = typeof state === "object" && state !== null ? state : {};
  // No URL: the app has no routes, and an entry must not change what a reload shows.
  window.history.pushState({ ...fields, [BACK_DEPTH_KEY]: depth }, "");
}

function flush() {
  flushQueued = false;
  // A second history.go before the first lands could pop past the app's own entry.
  if (popping) return;
  let top = entries.length;
  while (top > 0 && entries[top - 1] === null) top--;
  // A view that opened in the same tick as another closed (StrictMode, a new key) takes over the
  // entry left behind, so history neither pops it nor pushes a new one.
  while (top < entries.length && waiting.length > 0) entries[top++] = waiting.shift()!;
  if (top < entries.length) {
    // Never pop more than both the stack and the real history say are ours.
    const excess = Math.min(entries.length - top, stateDepth() - (base + top));
    if (excess > 0) {
      popping = true;
      window.history.go(-excess);
      return;
    }
    entries.length = top;
  }
  for (const owner of waiting.splice(0)) {
    try {
      pushEntry(base + entries.length + 1);
    } catch {
      // Safari throws once pushState is called too often; the view then just has no entry.
      continue;
    }
    entries.push(owner);
  }
}

function scheduleFlush() {
  if (flushQueued) return;
  flushQueued = true;
  queueMicrotask(flush);
}

function onPopState() {
  popping = false;
  const depth = stateDepth();
  if (depth < base) base = depth;
  const kept = depth - base;
  const gone = entries.splice(kept);
  // Forward into entries of views already closed: treat them as released, so flush pops them.
  while (entries.length < kept) entries.push(null);
  scheduleFlush();
  for (const owner of gone.reverse()) owner?.close();
}

function acquire(owner: Owner) {
  if (!listening) {
    window.addEventListener("popstate", onPopState);
    listening = true;
  }
  // Entries below are not ours (another document's after a reload); never pop them.
  if (entries.length === 0 && waiting.length === 0 && !popping) base = stateDepth();
  waiting.push(owner);
  scheduleFlush();
}

function release(owner: Owner) {
  const waitingAt = waiting.indexOf(owner);
  if (waitingAt >= 0) waiting.splice(waitingAt, 1);
  const at = entries.indexOf(owner);
  if (at >= 0) entries[at] = null;
  scheduleFlush();
}

// Lets the phone's back button or gesture close a fullscreen view instead of leaving the app.
// While mounted and enabled, the view owns one history entry (same URL); going back past it calls
// onClose. Closing the view any other way (unmount, enabled false; never history.back() by hand)
// removes the entry again without closing anything else.
// Nested views each own an entry, so back closes the innermost. Views that mount in the same
// commit get entries in effect order (children first), so open nested views one at a time.
export function useCloseOnBack(onClose: () => void, enabled = true): void {
  const onCloseRef = useRef(onClose);
  useLayoutEffect(() => {
    onCloseRef.current = onClose;
  });

  useEffect(() => {
    if (!enabled) return;
    const owner: Owner = { close: () => onCloseRef.current() };
    acquire(owner);
    return () => release(owner);
  }, [enabled]);
}
