import { describe, expect, it } from "vitest";
import {
  challengeStatus,
  formatKm,
  formatPoints,
  guessDateNote,
  guessSummary,
  initialsOf,
  memberInitials,
  memberLabel,
  OTHER_PIN,
  OWN_PIN,
  pinColors,
  playersLabel,
  playLabel,
  standings,
} from "@/lib/challenge";
import type { ChallengeMember } from "@/types";

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
