import L from "leaflet";

// The Game's two dots: the player's own guess (orange) and the item's actual location (green, as
// the marker a map is about everywhere in the app).
function dot(color: string): L.DivIcon {
  return L.divIcon({
    className: "",
    html: `<div style="width:16px;height:16px;border-radius:50%;background:${color};border:2px solid white;box-shadow:0 2px 6px rgba(0,0,0,0.5)"></div>`,
    iconSize: [16, 16],
    iconAnchor: [8, 8],
  });
}

export const guessIcon = dot("#f97316");
export const actualIcon = dot("#22c55e");

// The dashed line from a guess to the actual location.
export const GUESS_LINE = { color: "#000000", weight: 2, dashArray: "6 8", opacity: 0.85 };
