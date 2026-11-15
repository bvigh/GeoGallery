// The camera zone the backend names when CAMERA_TIMEZONE is unset: the server's own zone, which has
// no name.
export const SERVER_ZONE_UNSET = "server zone (unset)";

// Intl.supportedValuesOf is ES2022; the project's lib is ES2020, and older browsers lack it.
type IntlWithZones = { supportedValuesOf?: (key: "timeZone") => string[] };

// The browser's own list of IANA zones, or [] when it has none.
function browserZones(): string[] {
  const list = (Intl as IntlWithZones).supportedValuesOf;
  if (typeof list !== "function") return [];
  try {
    return list("timeZone");
  } catch {
    return [];
  }
}

// The zones a camera zone is chosen from: the browser's list, with UTC, which some browsers leave
// out, and keep (a stored setting, the browser's own zone), so that a value is always among the
// options. Sorted, each once.
export function timeZoneNames(...keep: (string | null | undefined)[]): string[] {
  const names = new Set(browserZones());
  names.add("UTC");
  for (const name of keep) if (name) names.add(name);
  return [...names].sort((a, b) => a.localeCompare(b, "en"));
}

// The server's camera zone in a sentence: its name, or what it is when CAMERA_TIMEZONE is unset.
export function serverZoneLabel(name: string): string {
  return name === SERVER_ZONE_UNSET ? "the server's own zone" : name;
}
