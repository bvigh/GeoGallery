// Lists shorter than this fit at a glance, so they get no search field.
export const SEARCH_MIN_ITEMS = 8;

// Lowercased and without accents, so "balazs" finds "Balázs".
function fold(text: string): string {
  return text.normalize("NFD").replace(/\p{Diacritic}/gu, "").toLowerCase();
}

// True when every whitespace-separated word of the query occurs in one of the fields,
// in any order: "ice 2025" finds "Iceland 2025". An empty query matches everything.
export function matchesSearch(query: string, ...fields: (string | null | undefined)[]): boolean {
  const words = fold(query).split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const haystack = fields.filter((f): f is string => !!f).map(fold);
  return words.every((w) => haystack.some((f) => f.includes(w)));
}
