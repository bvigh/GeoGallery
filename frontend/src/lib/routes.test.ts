import { describe, expect, it } from "vitest";
import { galleryRoute, parseRoute, routeUrl, tabRoute, type Route } from "@/lib/routes";

const TRIP = "0b6f3c1e-5d1a-4c47-9a8e-2f0d7b9c4e11";
const ITEM = "7c2e9d40-1b3a-4f6e-8d5c-a9e1f0b2c3d4";

describe("parseRoute", () => {
  it.each<[string, string, Route]>([
    ["/", "", tabRoute("map")],
    ["/trips", "", tabRoute("trips")],
    ["/albums", "", tabRoute("albums")],
    ["/game", "", tabRoute("game")],
    ["/upload", "", tabRoute("upload")],
    ["/settings", "", tabRoute("settings")],
    ["/admin", "", tabRoute("admin")],
    [`/trips/${TRIP}`, "", galleryRoute("trips", TRIP)],
    [`/trips/${TRIP}`, `?item=${ITEM}`, galleryRoute("trips", TRIP, ITEM)],
    [`/albums/${TRIP}`, `?item=${ITEM}`, galleryRoute("albums", TRIP, ITEM)],
  ])("reads %s%s", (pathname, search, route) => {
    expect(parseRoute(pathname, search)).toEqual(route);
  });

  it.each<[string, string, string, Route]>([
    ["an unknown path", "/photos", "", tabRoute("map")],
    ["a tab in another case", "/Trips", "", tabRoute("map")],
    ["a trailing slash", "/albums/", "", tabRoute("albums")],
    ["doubled slashes", `//albums//${TRIP}`, "", galleryRoute("albums", TRIP)],
    ["a malformed ID", "/albums/42", `?item=${ITEM}`, tabRoute("albums")],
    ["a malformed item", `/albums/${TRIP}`, "?item=42", galleryRoute("albums", TRIP)],
    ["upper-case IDs", `/albums/${TRIP.toUpperCase()}`, `?item=${ITEM.toUpperCase()}`, galleryRoute("albums", TRIP, ITEM)],
    ["parts after the ID", `/trips/${TRIP}/more`, `?x=1&item=${ITEM}`, galleryRoute("trips", TRIP, ITEM)],
    ["an ID on a tab without galleries", `/game/${TRIP}`, `?item=${ITEM}`, tabRoute("game")],
    ["an item without a gallery", "/albums", `?item=${ITEM}`, tabRoute("albums")],
  ])("reads %s leniently", (_name, pathname, search, route) => {
    expect(parseRoute(pathname, search)).toEqual(route);
  });
});

describe("routeUrl", () => {
  it.each<[Route, string]>([
    [tabRoute("map"), "/"],
    [tabRoute("trips"), "/trips"],
    [tabRoute("admin"), "/admin"],
    [galleryRoute("trips", TRIP), `/trips/${TRIP}`],
    [galleryRoute("albums", TRIP, ITEM), `/albums/${TRIP}?item=${ITEM}`],
  ])("writes %o as %s", (route, url) => {
    expect(routeUrl(route)).toBe(url);
  });

  it("reads back what it writes", () => {
    for (const route of [tabRoute("upload"), galleryRoute("albums", TRIP), galleryRoute("trips", TRIP, ITEM)]) {
      const url = new URL(routeUrl(route), "http://localhost");
      expect(parseRoute(url.pathname, url.search)).toEqual(route);
    }
  });
});
