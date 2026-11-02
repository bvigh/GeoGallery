import { describe, it, expect } from "vitest";
import { byRecentUse } from "@/lib/albums";
import type { Album } from "@/types";

function album(id: string, overrides: Partial<Album> = {}): Album {
  return {
    id,
    name: id,
    owner_id: "u-me",
    owner_name: "Me",
    is_owner: true,
    member_count: 1,
    media_count: 0,
    start_date: null,
    end_date: null,
    created_at: "2026-01-01T00:00:00Z",
    last_added_at: null,
    ...overrides,
  };
}

const shared = { owner_id: "u-bob", owner_name: "Bob", is_owner: false };

describe("byRecentUse", () => {
  it.each([
    ["no albums", [], []],
    [
      "the latest addition first",
      [
        album("old", { last_added_at: "2026-03-01T00:00:00Z" }),
        album("new", { last_added_at: "2026-05-01T00:00:00Z" }),
      ],
      ["new", "old"],
    ],
    [
      "creation for an owned album without additions",
      [
        album("added", { last_added_at: "2026-03-01T00:00:00Z" }),
        album("created", { created_at: "2026-04-01T00:00:00Z" }),
      ],
      ["created", "added"],
    ],
    [
      "an addition over a later creation",
      [
        album("a", { created_at: "2026-06-01T00:00:00Z", last_added_at: "2026-02-01T00:00:00Z" }),
        album("b", { created_at: "2026-03-01T00:00:00Z" }),
      ],
      ["b", "a"],
    ],
    [
      "shared albums without additions last",
      [
        album("unused", { ...shared, created_at: "2026-09-01T00:00:00Z" }),
        album("mine", { created_at: "2026-01-01T00:00:00Z" }),
        album("used", { ...shared, last_added_at: "2026-02-01T00:00:00Z" }),
      ],
      ["used", "mine", "unused"],
    ],
    [
      "ties in server order",
      [album("x", shared), album("y"), album("z", shared), album("w")],
      ["y", "w", "x", "z"],
    ],
  ])("puts %s", (_name, albums, want) => {
    expect(byRecentUse(albums).map((a) => a.id)).toEqual(want);
  });

  it("leaves its input alone", () => {
    const albums = [album("a", shared), album("b")];
    byRecentUse(albums);
    expect(albums.map((a) => a.id)).toEqual(["a", "b"]);
  });
});
