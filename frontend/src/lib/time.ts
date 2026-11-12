// Times shown to the user. Every instant is formatted for a zone the caller names, and the output
// names that zone, so no label can be mistaken for another clock (docs/gps-debugging.md).

// The browser's IANA zone. A browser that cannot name its zone reports "Etc/Unknown", which Intl
// then refuses as an argument; UTC is shown and labelled as such instead.
export function browserTimeZone(): string {
  const zone = new Intl.DateTimeFormat().resolvedOptions().timeZone;
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: zone });
    return zone;
  } catch {
    return "UTC";
  }
}

export interface FormatInstantOptions {
  seconds?: boolean; // default true
  locale?: string; // default: the browser's
}

export function formatInstant(
  iso: string,
  timeZone: string,
  { seconds = true, locale }: FormatInstantOptions = {}
): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "Invalid date";
  const text = new Intl.DateTimeFormat(locale, {
    timeZone,
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: seconds ? "2-digit" : undefined,
  }).format(date);
  return `${text} ${timeZone}`;
}

const DURATION_UNITS: [size: number, name: string][] = [
  [86400, "d"],
  [3600, "h"],
  [60, "min"],
  [1, "s"],
];

// The two largest non-zero units, for example "1 h 30 min". A duration has no direction: the
// sentence around it says which way it goes.
export function formatDuration(seconds: number): string {
  const absolute = Math.abs(seconds);
  if (absolute > 0 && absolute < 1) return "under 1 s";
  let left = Math.round(absolute);
  if (left === 0) return "0 s";
  const parts: string[] = [];
  for (const [size, name] of DURATION_UNITS) {
    const count = Math.floor(left / size);
    if (count === 0) continue;
    parts.push(`${count} ${name}`);
    left -= count * size;
    if (parts.length === 2) break;
  }
  return parts.join(" ");
}
