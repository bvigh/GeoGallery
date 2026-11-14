import { useCallback, useEffect, useState, type RefObject } from "react";

// The trip or album whose gallery the user closed last, so its page can mark that card after the
// gallery closes, after a tab switch and after a reload. It lives in sessionStorage, so it ends
// with the browser tab, and is kept per user: an album two users share must not show one user's
// choice to the other after a sign-out in the same tab. Storage can be missing or throw (a private
// window, blocked site data, a full quota); then the page remembers the ID only while it is mounted.

export type LastViewedPage = "trips" | "albums";

// The marked card: a ring around it, and room for that ring when it is scrolled to the edge.
export const LAST_VIEWED_CLASS = "ring-2 ring-primary ring-offset-2 ring-offset-background scroll-m-4";

function storageKey(userId: string, page: LastViewedPage): string {
  return `geogallery.lastViewed.${page}.${userId}`;
}

export function readLastViewed(userId: string, page: LastViewedPage): string | null {
  try {
    return window.sessionStorage.getItem(storageKey(userId, page));
  } catch {
    return null;
  }
}

export function writeLastViewed(userId: string, page: LastViewedPage, id: string): void {
  try {
    window.sessionStorage.setItem(storageKey(userId, page), id);
  } catch {
    // Not kept beyond the page's own state.
  }
}

// The page's last viewed ID, and the function that remembers a new one.
export function useLastViewed(userId: string, page: LastViewedPage): [string | null, (id: string) => void] {
  const [id, setId] = useState(() => readLastViewed(userId, page));
  const remember = useCallback(
    (next: string) => {
      setId(next);
      writeLastViewed(userId, page, next);
    },
    [userId, page]
  );
  return [id, remember];
}

// Scrolls the marked card into view whenever the list comes into sight: once it has loaded, and
// again each time a gallery over it closes. "nearest" leaves a card that is already in view alone.
export function useRevealLastViewed(card: RefObject<HTMLElement | null>, listInSight: boolean): void {
  useEffect(() => {
    if (listInSight) card.current?.scrollIntoView({ block: "nearest" });
  }, [card, listInSight]);
}
