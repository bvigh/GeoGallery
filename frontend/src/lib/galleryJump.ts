// Jumping inside a trip or album gallery (MediaGalleryOverlay): to an item number, or to the first
// item taken on or after a day.

// The index for an item number typed into the gallery's position field, clamped to the gallery:
// 0 goes to the first item and a number past the end to the last. null when no number was typed.
export function positionIndex(typed: string, count: number): number | null {
  const text = typed.trim();
  if (count < 1 || !/^\d+$/.test(text)) return null;
  // A number too long for a double is Infinity, which clamps like any other.
  return Math.min(Math.max(Number(text), 1), count) - 1;
}

// A day as a date field writes it: YYYY-MM-DD. Stored capture times have years 1-9998, so their days
// always have four digits and compare as text.
const DAY = /^\d{4}-\d{2}-\d{2}$/;

function dayFormatter(timeZone: string): (date: Date) => string {
  const format = new Intl.DateTimeFormat("en-US-u-ca-gregory-nu-latn", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
  return (date) => {
    const parts = format.formatToParts(date);
    const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((p) => p.type === type)?.value ?? "";
    return `${part("year").padStart(4, "0")}-${part("month")}-${part("day")}`;
  };
}

function dayOf(iso: string, format: (date: Date) => string): string | null {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? null : format(date);
}

// The calendar day an instant falls on in a zone (the zone the gallery shows its times in), as a
// date field writes it.
export function dayIn(iso: string, timeZone: string): string | null {
  return dayOf(iso, dayFormatter(timeZone));
}

// The first and last days the dated items fall on in a zone; null when no item is dated.
export function datedDays(
  items: readonly { taken_at: string | null }[],
  timeZone: string
): { first: string; last: string } | null {
  const format = dayFormatter(timeZone);
  let first: string | null = null;
  let last: string | null = null;
  for (const item of items) {
    const day = item.taken_at ? dayOf(item.taken_at, format) : null;
    if (day === null) continue;
    if (first === null || day < first) first = day;
    if (last === null || day > last) last = day;
  }
  return first === null || last === null ? null : { first, last };
}

// The index of the first item, in the gallery's own order, taken on or after a day in a zone.
// Undated items never match. -1 when no item does, or the day is not one a date field writes
// with a four-digit year (a later year, which no stored time reaches).
export function firstOnOrAfter(
  items: readonly { taken_at: string | null }[],
  day: string,
  timeZone: string
): number {
  if (!DAY.test(day)) return -1;
  const format = dayFormatter(timeZone);
  return items.findIndex((item) => {
    const taken = item.taken_at ? dayOf(item.taken_at, format) : null;
    return taken !== null && taken >= day;
  });
}
