// Counts shown to the user. Point counts run into millions, so they are grouped by the browser's
// locale ("27,558" in English). toLocaleString is banned on the evidence surfaces, which only
// restricts times, so counts go through one cached Intl.NumberFormat per locale instead.

const formatters = new Map<string, Intl.NumberFormat>();

export function formatCount(n: number, locale?: string): string {
  const key = locale ?? "";
  let formatter = formatters.get(key);
  if (!formatter) {
    formatter = new Intl.NumberFormat(locale, { maximumFractionDigits: 0 });
    formatters.set(key, formatter);
  }
  return formatter.format(n);
}

// Decimal units, as file managers on macOS and phones show sizes: 1 kB is 1,000 bytes.
const BYTE_UNITS = ["B", "kB", "MB", "GB", "TB", "PB"];

const sizeFormatters = new Map<string, Intl.NumberFormat>();

function sizeFormatter(locale: string | undefined, fractionDigits: number): Intl.NumberFormat {
  const key = `${locale ?? ""}|${fractionDigits}`;
  let formatter = sizeFormatters.get(key);
  if (!formatter) {
    formatter = new Intl.NumberFormat(locale, { maximumFractionDigits: fractionDigits });
    sizeFormatters.set(key, formatter);
  }
  return formatter;
}

// A size in bytes, with one decimal below 100 of a unit: "512 B", "1.2 kB", "34.5 MB", "123 GB".
// The number follows the browser's locale ("1,2 GB" in German) and the unit symbols do not, since
// Intl's own byte units differ by locale ("512 byte", "512B" and "512 o" among them).
export function formatBytes(n: number, locale?: string): string {
  let unit = 0;
  let value = n;
  while (unit < BYTE_UNITS.length - 1 && Math.abs(value) >= 1000) {
    value /= 1000;
    unit++;
  }
  let digits = unit === 0 || Math.abs(value) >= 100 ? 0 : 1;
  // Rounding can carry into the next unit: 999,960 bytes would read "1,000 kB".
  if (unit > 0 && unit < BYTE_UNITS.length - 1 && Number(Math.abs(value).toFixed(digits)) >= 1000) {
    value /= 1000;
    unit++;
    digits = 1;
  }
  return `${sizeFormatter(locale, digits).format(value)} ${BYTE_UNITS[unit]}`;
}
