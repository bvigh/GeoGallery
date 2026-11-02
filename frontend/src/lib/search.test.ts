import { describe, it, expect } from "vitest";
import { matchesSearch } from "@/lib/search";

describe("matchesSearch", () => {
  it.each([
    ["an empty query", "", ["Anything"], true],
    ["a blank query", "   ", ["Anything"], true],
    ["a substring", "land", ["Iceland 2025"], true],
    ["any case", "ICE", ["Iceland 2025"], true],
    ["words in any order", "2025 ice", ["Iceland 2025"], true],
    ["a word missing", "ice 2024", ["Iceland 2025"], false],
    ["words across fields", "family bob", ["Family", "Bob"], true],
    ["no accents in the query", "balazs", ["Balázs"], true],
    ["accents in the query", "árvíztűrő", ["Arvizturo tukorfurogep"], true],
    ["null fields", "bob", [null, undefined, "Bob"], true],
    ["no fields", "bob", [], false],
  ])("handles %s", (_name, query, fields, want) => {
    expect(matchesSearch(query, ...fields)).toBe(want);
  });
});
