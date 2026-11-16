import { describe, expect, it } from "vitest";
import {
  challengeNames,
  challengeStatus,
  formatKm,
  formatPoints,
  guessDateNote,
  guessSummary,
  initialsOf,
  memberInitials,
  memberLabel,
  offerAlbums,
  OTHER_PIN,
  OWN_PIN,
  pinColors,
  playersLabel,
  playLabel,
  randomChallengeName,
  standings,
  untickedAlbumsNote,
  type ChallengeFilters,
} from "@/lib/challenge";
import { emptyFilters } from "@/lib/filters";
import type { Album, ChallengeMember } from "@/types";

function member(user_id: string, name: string | null, played = 0, score = 0): ChallengeMember {
  return { user_id, name, is_owner: false, played, score };
}

describe("initialsOf", () => {
  it.each<[string | null, string]>([
    ["Balázs Vigh", "BV"],
    ["alice", "AL"],
    ["Mary Jane Watson", "MW"],
    ["  bob   the builder ", "BB"],
    ["Árpád Ödön", "ÁÖ"],
    ["x", "X"],
    ["balazs.vigh@example.test", "BV"],
    ["carol@example.test", "CA"],
    ["jean-luc_picard+photos@example.test", "JP"],
    ["Zoë (work)", "ZW"],
    ["李 小龍", "李小"],
    ["agent 47", "A4"],
    ["", "?"],
    ["  ", "?"],
    ["…", "?"],
    ["@example.test", "?"],
    [null, "?"],
  ])("reads %j as %s", (name, want) => {
    expect(initialsOf(name)).toBe(want);
  });
});

describe("memberInitials", () => {
  function initials(...names: (string | null)[]): string[] {
    const members = names.map((name, i) => ({ user_id: `u${i}`, name }));
    const out = memberInitials(members);
    return members.map((m) => out.get(m.user_id)!);
  }

  it("gives players with different initials their own", () => {
    expect(initials("Alice Smith", "Bob Jones", "carol")).toEqual(["AS", "BJ", "CA"]);
  });

  it("tells a family apart by the start of their first names", () => {
    expect(initials("Balázs Vigh", "Bea Vigh", "Alice Smith")).toEqual(["Ba", "Be", "AS"]);
  });

  it("numbers players whose first names start alike too", () => {
    expect(initials("Anna Berg", "Anne Berg", "Bob Jones")).toEqual(["AB1", "AB2", "BJ"]);
  });

  it("numbers players when a first name's start is another player's initials, in any case", () => {
    expect(initials("Al Bundy", "Ann Brown", "Al")).toEqual(["AB1", "AB2", "AL"]);
    expect(initials("Bo Vigh", "Be Vigh", "Bo")).toEqual(["BV1", "BV2", "BO"]);
  });

  it("skips a number another player's initials already use", () => {
    expect(initials("x", "x", "x1")).toEqual(["X2", "X3", "X1"]);
  });

  it("numbers players without a name", () => {
    expect(initials(null, null, "Bob Jones")).toEqual(["?1", "?2", "BJ"]);
  });

  it("gives every player different initials, whatever the names", () => {
    const names = ["Ann Lee", "Ann Lee", "ann.lee@example.test", "Al", "AL", null, "", "A L", "Anna Li", "al1", "a.l2@x"];
    const out = initials(...names);
    expect(new Set(out.map((label) => label.toUpperCase())).size).toBe(names.length);
  });

  it("is empty for no players", () => {
    expect(memberInitials([]).size).toBe(0);
  });
});

describe("memberLabel", () => {
  it("is the name, or a stand-in without one", () => {
    expect(memberLabel({ name: "Alice" })).toBe("Alice");
    expect(memberLabel({ name: null })).toBe("Unknown player");
  });
});

describe("pinColors", () => {
  it("tells the viewer's own guess from everyone else's", () => {
    expect(pinColors(true)).toBe(OWN_PIN);
    expect(pinColors(false)).toBe(OTHER_PIN);
    expect(OWN_PIN.fill).not.toBe(OTHER_PIN.fill);
  });
});

describe("formatPoints and formatKm", () => {
  it("rounds and groups points", () => {
    expect(formatPoints(0)).toBe("0 pts");
    expect(formatPoints(4999.6)).toBe("5,000 pts");
    expect(formatPoints(18240.2)).toBe("18,240 pts");
  });

  it("keeps a decimal only below 10 km", () => {
    expect(formatKm(0)).toBe("0.0 km");
    expect(formatKm(9.94)).toBe("9.9 km");
    expect(formatKm(10)).toBe("10 km");
    expect(formatKm(1234.5)).toBe("1,235 km");
  });
});

describe("standings", () => {
  it("ranks the players who have guessed, best first, and lists the rest after them", () => {
    const members = [
      member("a", "Alice", 5, 12000),
      member("b", "Bob", 0, 0),
      member("c", "Carol", 5, 21000),
      member("d", "Dave", 2, 3000),
      member("e", "Eve", 0, 0),
    ];
    expect(standings(members).map((s) => [s.member.user_id, s.rank])).toEqual([
      ["c", 1],
      ["a", 2],
      ["d", 3],
      ["b", null],
      ["e", null],
    ]);
  });

  it("gives players level on points the same rank and skips the next", () => {
    const members = [
      member("a", "Alice", 5, 9000),
      member("b", "Bob", 5, 15000),
      member("c", "Carol", 5, 15000),
      member("d", "Dave", 5, 9000),
      member("e", "Eve", 5, 100),
    ];
    expect(standings(members).map((s) => [s.member.user_id, s.rank])).toEqual([
      ["b", 1],
      ["c", 1],
      ["a", 3],
      ["d", 3],
      ["e", 5],
    ]);
  });

  it("ranks a player who guessed and scored nothing, but not one who has not guessed", () => {
    const members = [member("a", "Alice", 1, 0), member("b", "Bob", 0, 0)];
    expect(standings(members).map((s) => s.rank)).toEqual([1, null]);
  });

  it("puts more rounds played first among players level on points, then the name", () => {
    const members = [member("a", "Zed", 2, 0), member("b", "Amy", 2, 0), member("c", "Bob", 5, 0)];
    expect(standings(members).map((s) => s.member.user_id)).toEqual(["c", "b", "a"]);
  });

  it("leaves the members as they were given", () => {
    const members = [member("a", "Alice", 5, 1), member("b", "Bob", 5, 2)];
    standings(members);
    expect(members.map((m) => m.user_id)).toEqual(["a", "b"]);
  });
});

describe("challengeStatus and playLabel", () => {
  it.each([
    [{ played: 0, score: 0, finished: false, item_count: 5 }, "Not played yet", "Play"],
    [{ played: 2, score: 7800.4, finished: false, item_count: 5 }, "2 of 5 played · 7,800 pts", "Continue"],
    [{ played: 5, score: 18240, finished: true, item_count: 5 }, "Finished · 18,240 pts", "Results"],
    [{ played: 3, score: 9000, finished: true, item_count: 4 }, "Finished · 9,000 pts", "Results"],
    [
      { played: 0, score: 0, finished: true, item_count: 5 },
      "No round you can play: its items are no longer shared with you",
      "Results",
    ],
    [{ played: 0, score: 0, finished: true, item_count: 0 }, "No rounds left: its items were deleted", "Results"],
  ])("says where the player stands (%o)", (progress, status, label) => {
    expect(challengeStatus(progress)).toBe(status);
    expect(playLabel(progress)).toBe(label);
  });
});

describe("playersLabel", () => {
  it("counts the players", () => {
    expect(playersLabel(1)).toBe("1 player");
    expect(playersLabel(2)).toBe("2 players");
    expect(playersLabel(20)).toBe("20 players");
  });
});

describe("guessSummary and guessDateNote", () => {
  it("says how far a guess was and what it scored", () => {
    expect(guessSummary({ distance_km: 3.26, total_score: 4812.5 })).toBe("3.3 km away · 4,813 pts");
    expect(guessSummary({ distance_km: 2140.2, total_score: 0 })).toBe("2,140 km away · 0 pts");
  });

  it("says how far off a guessed date was", () => {
    expect(guessDateNote({ guess_date: "2024-05-11", date_diff_days: 9 })).toBe("Guessed 2024-05-11, 9 days off");
    expect(guessDateNote({ guess_date: "2024-05-03", date_diff_days: 1 })).toBe("Guessed 2024-05-03, 1 day off");
    expect(guessDateNote({ guess_date: "2024-05-02", date_diff_days: 0 })).toBe("Guessed 2024-05-02, the right day");
    expect(guessDateNote({ guess_date: "2019-01-01", date_diff_days: 1948 })).toBe("Guessed 2019-01-01, 1,948 days off");
  });

  it("says nothing without a guessed date, or without a date to compare it with", () => {
    expect(guessDateNote({ guess_date: null, date_diff_days: null })).toBeNull();
    expect(guessDateNote({ guess_date: "2024-05-02", date_diff_days: null })).toBeNull();
  });
});

describe("randomChallengeName", () => {
  it("gives the first pair for the lowest draw and the last for the highest", () => {
    expect(randomChallengeName(() => 0)).toBe("Bold Badger");
    expect(randomChallengeName(() => 0.999999)).toBe("Witty Wombat");
  });

  it("stays inside the lists for a draw of exactly 1", () => {
    expect(randomChallengeName(() => 1)).toBe("Witty Wombat");
  });

  it("draws the letter, the adjective and the animal one after the other", () => {
    // The second row, then its first adjective and its second animal.
    const draws = [1 / 18 + 0.001, 0, 0.4];
    expect(randomChallengeName(() => draws.shift()!)).toBe("Cheerful Cheetah");
  });

  it("uses Math.random when given nothing", () => {
    expect(challengeNames()).toContain(randomChallengeName());
  });
});

describe("challengeNames", () => {
  const names = challengeNames();

  it("holds over a hundred different names", () => {
    expect(names.length).toBeGreaterThan(100);
    expect(new Set(names).size).toBe(names.length);
  });

  it("pairs an adjective and an animal that start with the same letter", () => {
    for (const name of names) {
      expect(name).toMatch(/^[A-Z][a-z]+ [A-Z][a-z]+$/);
      const [adjective, animal] = name.split(" ");
      expect(adjective[0]).toBe(animal[0]);
    }
  });

  it("gives only names the backend takes", () => {
    for (const name of names) {
      expect(name.trim()).toBe(name);
      expect(name.length).toBeLessThanOrEqual(200);
    }
  });
});

describe("offerAlbums", () => {
  const album = (id: string, name: string): Album => ({
    id,
    name,
    owner_id: "u-me",
    owner_name: "Me",
    is_owner: true,
    member_count: 2,
    media_count: 1,
    start_date: null,
    end_date: null,
    created_at: "2026-01-01T00:00:00Z",
    last_added_at: null,
  });
  const family = album("a1", "Family");
  const iceland = album("a2", "Iceland 2024");
  const tatra = album("a3", "Tatra");
  const all = [family, iceland, tatra];
  const ticked = (albumIds: string[]) => ({ ...emptyFilters, albumIds, fromDate: "2024-05-01" });
  const start = (albumIds: string[]): ChallengeFilters => ({ filters: ticked(albumIds), albums: null, unticked: [] });
  const tick = (c: ChallengeFilters, id: string): ChallengeFilters => ({
    ...c,
    filters: { ...c.filters, albumIds: [...c.filters.albumIds, id] },
  });

  it("unticks a ticked album that is no longer offered, and names it", () => {
    expect(offerAlbums(start(["a1", "a2"]), [family, tatra], all)).toEqual({
      filters: ticked(["a1"]),
      albums: [family, tatra],
      unticked: [iceland],
    });
  });

  it("leaves the filters as they are when every ticked album is still offered", () => {
    const current = start(["a1"]);
    const next = offerAlbums(current, [family], all);
    expect(next.filters).toBe(current.filters);
    expect(next.unticked).toEqual([]);
  });

  it("names an album until it is offered again, and never ticks it again", () => {
    const withBob = offerAlbums(start(["a2"]), [family], all);
    const withCarolToo = offerAlbums(withBob, [], all);
    expect(withCarolToo.unticked).toEqual([iceland]);
    expect(withCarolToo.albums).toEqual([]);

    const back = offerAlbums(withCarolToo, [family, iceland], all);
    expect(back.unticked).toEqual([]);
    expect(back.filters.albumIds).toEqual([]);
  });

  it("adds what a later answer unticks to the albums still named, each once", () => {
    let c = offerAlbums(start(["a1", "a2"]), [family, tatra], all);
    c = offerAlbums(tick(c, "a3"), [family], all);
    expect(c.unticked).toEqual([iceland, tatra]);
    expect(c.filters.albumIds).toEqual(["a1"]);
  });

  it("names an album that only an earlier answer offered", () => {
    const fresh = album("a9", "Made since the Game page loaded");
    const c = offerAlbums(start([]), [family, fresh], all);
    expect(offerAlbums(tick(c, "a9"), [family], all).unticked).toEqual([fresh]);
  });

  it("unticks an album it does not know without naming it", () => {
    const next = offerAlbums(start(["a-left", "a1"]), [family], all);
    expect(next.filters.albumIds).toEqual(["a1"]);
    expect(next.unticked).toEqual([]);
  });

  it("offers every album for null, keeping the ticks and naming none", () => {
    const narrowed = offerAlbums(start(["a1", "a2"]), [family], all);
    expect(offerAlbums(narrowed, null, all)).toEqual({ filters: ticked(["a1"]), albums: null, unticked: [] });
  });
});

describe("untickedAlbumsNote", () => {
  it.each<[string[], string | null]>([
    [[], null],
    [["Family"], "Unticked “Family”: not every player is in it."],
    [["Family", "Iceland 2024"], "Unticked “Family” and “Iceland 2024”: not every player is in them."],
    [["Family", "Iceland 2024", "Tatra"], "Unticked “Family”, “Iceland 2024” and “Tatra”: not every player is in them."],
  ])("%j", (names, want) => {
    expect(untickedAlbumsNote(names.map((name) => ({ name })))).toBe(want);
  });
});
