import { useEffect, useLayoutEffect, useRef } from "react";
import { flushSync } from "react-dom";

// The app's session history:
// - A page entry per visited tab (pushPage), so back returns to the previous tab. Its state has no
//   BACK_DEPTH_KEY.
// - A view entry per open fullscreen view (useCloseOnBack), stacked on the page entry it opened
//   from and counted in BACK_DEPTH_KEY. Back closes the innermost view; closing a view any other
//   way removes its entry again.
// A view can have a URL (a gallery: its trip or album and current item). Its entry shows it, and
// whatever entry is current shows the innermost open view's URL, so a reload or a copied link can
// open that view again (startHistory), and so can forward after back: the page reopens the view
// named in the entry's URL (onHistoryChange). A view without a URL keeps the URL under it, and
// forward onto its entry once it has closed leaves that entry again.

// The history.state field that counts the fullscreen views stacked on the app's own entry.
export const BACK_DEPTH_KEY = "ggCloseOnBack";

type Owner = { close: () => void; url: string | null };

// One module-wide stack, so nested views and views closing together agree on history. Views only
// change the stack; flush() then brings history in line with it, one traversal at a time.
let base = 0; // depth of the entry the views sit on
const entries: (Owner | null)[] = []; // our entries above base, innermost last; null = view gone
const waiting: Owner[] = []; // open views that have no entry yet
let popping = false; // our history.go has not reached its popstate yet
let flushQueued = false;
let listening = false;
// Called after each popstate, once the views it closed had their onClose.
const listeners = new Set<() => void>();
// Set by startHistory: the entry a view from the URL is to take over, and its depth.
let restoring: { url: string; depth: number } | null = null;

// Browsers cap history calls (Safari throws after 100 in 10 s, Firefox after 200), and an arrow key
// held down steps through a gallery faster than that. A view's URL is written at most every
// URL_INTERVAL ms; the last change always lands.
const URL_INTERVAL = 250;
let lastUrlWrite = -Infinity;
let urlTimer: ReturnType<typeof setTimeout> | null = null;

function stateDepth(): number {
  const state: unknown = window.history.state;
  if (typeof state !== "object" || state === null) return 0;
  const depth = (state as Record<string, unknown>)[BACK_DEPTH_KEY];
  return typeof depth === "number" && Number.isInteger(depth) && depth > 0 ? depth : 0;
}

function stateFields(): object {
  const state: unknown = window.history.state;
  return typeof state === "object" && state !== null ? state : {};
}

function currentUrl(): string {
  return window.location.pathname + window.location.search;
}

// The URL the current entry should show: the innermost open view's that has one.
function innermostUrl(): string | null {
  for (let i = entries.length - 1; i >= 0; i--) {
    const url = entries[i]?.url;
    if (url) return url;
  }
  return null;
}

function pushEntry(depth: number, owner: Owner) {
  const state = { ...stateFields(), [BACK_DEPTH_KEY]: depth };
  // A view without a URL of its own shows the URL of the view under it, which may not have been
  // written yet (syncUrl), or else the page's: a reload shows what is under it.
  const url = owner.url ?? innermostUrl();
  if (url === null) window.history.pushState(state, "");
  else window.history.pushState(state, "", url);
}

function scheduleUrlSync(delay: number) {
  if (urlTimer !== null) return;
  urlTimer = setTimeout(() => {
    urlTimer = null;
    syncUrl();
  }, delay);
}

function syncUrl() {
  // Only while history is where the stack says it is: the current entry must be our innermost
  // one, not an entry being popped or a page entry a tab switch pushed over open views.
  if (popping || waiting.length > 0 || stateDepth() !== base + entries.length) return;
  const url = innermostUrl();
  if (url === null || url === currentUrl()) return;
  const wait = lastUrlWrite + URL_INTERVAL - Date.now();
  if (wait > 0) {
    scheduleUrlSync(wait);
    return;
  }
  try {
    window.history.replaceState(window.history.state, "", url);
    lastUrlWrite = Date.now();
  } catch {
    // Over the browser's cap anyway: try again once it has had time to recover.
    lastUrlWrite = Date.now();
    scheduleUrlSync(URL_INTERVAL * 4);
  }
}

// The view startHistory made room for takes over the current entry, and with it the entries below
// down to the page entry: those belonged to views that were open before a reload. Only the first
// view to open may: any other one means the URL's view did not open.
function takeRestoredEntry(owner: Owner): boolean {
  const wanted = restoring;
  restoring = null;
  if (wanted === null || entries.length > 0 || owner.url !== wanted.url) return false;
  const { url, depth } = wanted;
  if (stateDepth() !== depth || currentUrl() !== url) return false;
  base = 0;
  for (let i = 1; i < depth; i++) entries.push(null);
  entries.push(owner);
  return true;
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
    if (takeRestoredEntry(owner)) continue;
    try {
      pushEntry(base + entries.length + 1, owner);
    } catch {
      // Safari throws once pushState is called too often; the view then just has no entry.
      continue;
    }
    entries.push(owner);
  }
  syncUrl();
}

function scheduleFlush() {
  if (flushQueued) return;
  flushQueued = true;
  queueMicrotask(flush);
}

function onPopState() {
  popping = false;
  restoring = null;
  const depth = stateDepth();
  if (depth < base) base = depth;
  const kept = depth - base;
  const gone = entries.splice(kept);
  // Forward into entries of views already closed: treat them as released, so flush pops them,
  // unless a view opens for one first and takes it over (flush fills released entries from the
  // waiting views before it pops any). A page opens a gallery for an entry whose URL names one,
  // in its onHistoryChange listener. Run synchronously, the render and its effects come before
  // flush, so the view that opens is waiting by then.
  while (entries.length < kept) entries.push(null);
  flushSync(() => {
    for (const owner of gone.reverse()) owner?.close();
    for (const listener of [...listeners]) listener();
  });
  scheduleFlush();
}

function listen() {
  if (listening) return;
  window.addEventListener("popstate", onPopState);
  listening = true;
}

function acquire(owner: Owner) {
  listen();
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
// While mounted and enabled, the view owns one history entry; going back past it calls onClose.
// Closing the view any other way (unmount, enabled false; never history.back() by hand) removes
// the entry again without closing anything else.
// Nested views each own an entry, so back closes the innermost. Views that mount in the same
// commit get entries in effect order (children first), so open nested views one at a time.
// url: the view's own URL (routes.ts), shown while it is the innermost view with one; null keeps
// the URL of what is under the view.
export function useCloseOnBack(onClose: () => void, enabled = true, url: string | null = null): void {
  const onCloseRef = useRef(onClose);
  useLayoutEffect(() => {
    onCloseRef.current = onClose;
  });
  // Read when the view opens; later changes go to the owner below.
  const urlRef = useRef(url);
  urlRef.current = url;
  const ownerRef = useRef<Owner | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const owner: Owner = { close: () => onCloseRef.current(), url: urlRef.current };
    ownerRef.current = owner;
    acquire(owner);
    return () => {
      ownerRef.current = null;
      release(owner);
    };
  }, [enabled]);

  useEffect(() => {
    const owner = ownerRef.current;
    if (owner === null || owner.url === url) return;
    owner.url = url;
    scheduleFlush();
  }, [url]);
}

// A tab switch: a page entry of its own, so back returns to the previous tab.
export function pushPage(url: string): void {
  restoring = null;
  if (url === currentUrl() && stateDepth() === 0) return;
  try {
    window.history.pushState(null, "", url);
  } catch {
    // Over the browser's cap: the tab opens, back just skips it.
  }
}

// Called with the URL the app was loaded with (canonical, from routes.ts) and the URL of its tab
// alone, before any view opens. A view in the URL gets a view entry above a page entry, as if it
// had been opened from its page, so back closes it before it leaves the tab; after a reload that
// entry is already there. The view then takes that entry over when it opens with this URL.
export function startHistory(url: string, page: string): void {
  listen();
  restoring = null;
  try {
    if (currentUrl() !== url) window.history.replaceState(window.history.state, "", url);
    if (url === page) return;
    if (stateDepth() === 0) {
      window.history.replaceState(window.history.state, "", page);
      window.history.pushState({ ...stateFields(), [BACK_DEPTH_KEY]: 1 }, "", url);
    }
  } catch {
    // Over the browser's cap: the view opens on an entry of its own.
    return;
  }
  restoring = { url, depth: stateDepth() };
}

// Calls listener after every back or forward, once the views it closed had their onClose. Returns
// the unsubscribe function. The listener runs inside flushSync: a view it opens is mounted, and
// its useCloseOnBack waiting, before history is brought in line, so a view opened for the entry
// back or forward landed on takes that entry over.
export function onHistoryChange(listener: () => void): () => void {
  listen();
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// onHistoryChange for a component, with the listener of its latest render.
export function useHistoryChange(listener: () => void): void {
  const listenerRef = useRef(listener);
  useLayoutEffect(() => {
    listenerRef.current = listener;
  });
  useEffect(() => onHistoryChange(() => listenerRef.current()), []);
}
