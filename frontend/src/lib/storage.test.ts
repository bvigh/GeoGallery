import { describe, expect, it } from "vitest";
import {
  UNNAMED_USER,
  albumOwnerLabel,
  itemsLabel,
  sizeLabel,
  trackPointsLabel,
  storageUserName,
  storageUserStatus,
  ownUnknownSizesNote,
  tripDays,
  unknownSizesNote,
} from "@/lib/storage";

describe("storageUserName", () => {
  it.each([
    [{ display_name: "Alice", email: "alice@example.test" }, "Alice"],
    [{ display_name: null, email: "bob@example.test" }, "bob@example.test"],
    [{ display_name: null, email: null }, UNNAMED_USER],
  ])("names %o %s", (user, want) => {
    expect(storageUserName(user)).toBe(want);
  });
});

describe("storageUserStatus", () => {
  it.each([
    [{ claimed: true, disabled: false }, null],
    [{ claimed: true, disabled: true }, "disabled"],
    [{ claimed: false, disabled: false }, "unclaimed"],
    [{ claimed: false, disabled: true }, "unclaimed"],
  ])("%o is %s", (user, want) => {
    expect(storageUserStatus(user)).toBe(want);
  });
});

describe("itemsLabel", () => {
  it.each([
    [0, "0 items"],
    [1, "1 item"],
    [2, "2 items"],
    [27558, "27,558 items"],
  ])("%d is %s", (n, want) => {
    expect(itemsLabel(n)).toBe(want);
  });
});

describe("trackPointsLabel", () => {
  it.each([
    [0, "0 track points"],
    [1, "1 track point"],
    [6246825, "6,246,825 track points"],
  ])("%d is %s", (n, want) => {
    expect(trackPointsLabel(n)).toBe(want);
  });
});

describe("sizeLabel", () => {
  it.each([
    [{ items: 0, bytes: 0, unknown_items: 0 }, "0 B"],
    [{ items: 3, bytes: 0, unknown_items: 0 }, "0 B"],
    [{ items: 2, bytes: 1234, unknown_items: 0 }, "1.2 kB"],
    [{ items: 5, bytes: 5_368_709_120, unknown_items: 2 }, "5.4 GB + 2 unknown"],
    [{ items: 1500, bytes: 1000, unknown_items: 1499 }, "1 kB + 1,499 unknown"],
    [{ items: 3, bytes: 0, unknown_items: 3 }, "Unknown"],
  ])("%o reads %s", (use, want) => {
    expect(sizeLabel(use)).toBe(want);
  });
});

describe("tripDays", () => {
  it.each([
    [{ start_date: "2024-05-01", end_date: "2024-05-03" }, "2024-05-01 – 2024-05-03"],
    [{ start_date: "2024-05-01", end_date: "2024-05-01" }, "2024-05-01"],
    [{ start_date: "2024-05-01", end_date: null }, "from 2024-05-01"],
    [{ start_date: null, end_date: "2024-05-03" }, "until 2024-05-03"],
    [{ start_date: null, end_date: null }, "no dates"],
  ])("%o reads %s", (trip, want) => {
    expect(tripDays(trip)).toBe(want);
  });
});

describe("albumOwnerLabel", () => {
  it.each([
    [{ is_owner: true, owner_name: "Alice" }, "You"],
    [{ is_owner: false, owner_name: "Bob" }, "Bob"],
    [{ is_owner: false, owner_name: null }, UNNAMED_USER],
  ])("%o is %s", (album, want) => {
    expect(albumOwnerLabel(album)).toBe(want);
  });
});

describe.each([
  ["unknownSizesNote, for the admin", unknownSizesNote, "Run server backfill-sizes on the server"],
  ["ownUnknownSizesNote, for the owner", ownUnknownSizesNote, "until an administrator reads their sizes."],
])("%s", (_name, note, howToRead) => {
  it("says nothing when every size is known", () => {
    expect(note({ items: 4, bytes: 10, unknown_items: 0 })).toBeNull();
  });

  it("counts the items and says how their sizes are read", () => {
    const text = note({ items: 4, bytes: 10, unknown_items: 1234 });
    expect(text).toContain("The size of 1,234 items is unknown: they were stored before sizes were recorded.");
    expect(text).toContain(howToRead);
    expect(note({ items: 4, bytes: 10, unknown_items: 1 })).toContain("The size of 1 item is unknown: it was");
  });
});

it("tells only the admin to run the backfill", () => {
  expect(ownUnknownSizesNote({ items: 1, bytes: 0, unknown_items: 1 })).not.toContain("backfill");
});
