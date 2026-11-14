// The app's URLs. Each tab has a path, and the Trips and Albums tabs add the trip or album whose
// gallery is open and the gallery's current item:
//
//   /                                   Map
//   /trips    /trips/<trip id>?item=<media id>
//   /albums   /albums/<album id>?item=<media id>
//   /game  /upload  /settings  /admin
//
// A page reads its route when it mounts; src/lib/history.ts keeps the URL in step with what is
// open after that. Filters stay out of the URL: ticked trips and albums would make it longer than
// nginx takes in a request line (8 KB) and than the login cookie that carries return_to can hold.

export type Tab = "map" | "trips" | "albums" | "game" | "upload" | "settings" | "admin";

// The tabs whose galleries have a URL.
export type GalleryTab = "trips" | "albums";

export interface Route {
  tab: Tab;
  // The trip (Trips) or album (Albums) whose gallery is open; null without one.
  collectionId: string | null;
  // The gallery's current item; null without a gallery, or when it is not known yet.
  itemId: string | null;
}

const PATHS: Record<Tab, string> = {
  map: "/",
  trips: "/trips",
  albums: "/albums",
  game: "/game",
  upload: "/upload",
  settings: "/settings",
  admin: "/admin",
};

const TABS = Object.keys(PATHS) as Tab[];

// Every ID the API hands out is a UUID. Anything else in an ID's place is dropped, so a mangled
// link opens the tab without asking the API about it.
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function uuid(value: string | null | undefined): string | null {
  return value && UUID.test(value) ? value.toLowerCase() : null;
}

export function tabRoute(tab: Tab): Route {
  return { tab, collectionId: null, itemId: null };
}

export function galleryRoute(tab: GalleryTab, collectionId: string, itemId: string | null = null): Route {
  return { tab, collectionId, itemId };
}

// Lenient: an unknown path is the map, and parts that do not fit are ignored. routeUrl gives the
// canonical form.
export function parseRoute(pathname: string, search: string): Route {
  const [first = "", second] = pathname.split("/").filter((part) => part !== "");
  const tab = TABS.find((t) => PATHS[t] === `/${first}`) ?? "map";
  if (tab !== "trips" && tab !== "albums") return tabRoute(tab);
  const collectionId = uuid(second);
  if (collectionId === null) return tabRoute(tab);
  return galleryRoute(tab, collectionId, uuid(new URLSearchParams(search).get("item")));
}

export function routeUrl(route: Route): string {
  const path = PATHS[route.tab];
  if (route.collectionId === null) return path;
  const gallery = `${path}/${route.collectionId}`;
  return route.itemId === null ? gallery : `${gallery}?item=${route.itemId}`;
}

export function currentRoute(): Route {
  return parseRoute(window.location.pathname, window.location.search);
}
