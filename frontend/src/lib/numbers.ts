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
