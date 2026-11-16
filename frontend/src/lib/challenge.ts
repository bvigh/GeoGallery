// The pure parts of the Game's challenges: how a player is named and drawn, how the players rank,
// which albums a new challenge offers, and every sentence the challenge views say.

import { formatCount } from "@/lib/numbers";
import type { Album, ChallengeGuess, ChallengeMember, ChallengeSummary, FilterState } from "@/types";

// The backend's challengeItemCount: a challenge is made of this many rounds.
export const CHALLENGE_ROUNDS = 5;
// The backend's maxChallengePlayers, less the owner: how many players a challenge's owner can add.
export const MAX_OTHER_PLAYERS = 19;

export const CHALLENGE_GONE = "That challenge is not available: it was deleted, or you are not one of its players.";
export const ROUND_GONE = "That round is no longer available: its item was deleted, or is no longer shared with you.";
export const ALREADY_GUESSED = "You have already guessed that round.";
// The New challenge dialog's notes on its albums: when it could not find out which albums every
// ticked player is in, and when none of the caller's albums has all of them.
export const ALBUMS_UNCHECKED = "Could not check which albums every player is in, so all of yours are listed.";
export const NO_SHARED_ALBUM = "None of your albums has every player in it.";

// Words for a challenge nobody named: an adjective and an animal that start alike. Each row is one
// letter. Every pair is under the backend's 200 characters.
const NAME_WORDS: [adjectives: string[], animals: string[]][] = [
  [["Bold", "Bouncy", "Brave"], ["Badger", "Beaver", "Bison"]],
  [["Cheerful", "Clever", "Curious"], ["Camel", "Cheetah", "Crane"]],
  [["Dapper", "Daring", "Dizzy"], ["Deer", "Dolphin", "Duck"]],
  [["Eager", "Elegant"], ["Eagle", "Elk"]],
  [["Fancy", "Fearless", "Friendly"], ["Falcon", "Fox", "Frog"]],
  [["Gentle", "Giddy", "Graceful"], ["Gazelle", "Giraffe", "Goat"]],
  [["Happy", "Hasty", "Humble"], ["Hare", "Hedgehog", "Heron"]],
  [["Jazzy", "Jolly"], ["Jackal", "Jaguar"]],
  [["Keen", "Kind"], ["Kiwi", "Koala"]],
  [["Lively", "Loyal", "Lucky"], ["Lemur", "Llama", "Lynx"]],
  [["Mellow", "Merry", "Mighty"], ["Marmot", "Mole", "Moose"]],
  [["Nimble", "Noble"], ["Narwhal", "Newt"]],
  [["Patient", "Playful", "Proud"], ["Panda", "Pelican", "Puffin"]],
  [["Quick", "Quiet"], ["Quail", "Quokka"]],
  [["Rapid", "Restless", "Royal"], ["Rabbit", "Raccoon", "Raven"]],
  [["Silly", "Sunny", "Swift"], ["Seal", "Squirrel", "Swan"]],
  [["Tidy", "Tireless", "Trusty"], ["Tiger", "Toucan", "Turtle"]],
  [["Wandering", "Wise", "Witty"], ["Walrus", "Wolf", "Wombat"]],
];

/**
 * A name for a challenge whose maker gave none, such as "Cheerful Cheetah". `random` returns a
 * number from 0 up to, not including, 1, as Math.random does.
 */
export function randomChallengeName(random: () => number = Math.random): string {
  const pick = <T,>(list: T[]): T => list[Math.min(list.length - 1, Math.floor(random() * list.length))];
  const [adjectives, animals] = pick(NAME_WORDS);
  return `${pick(adjectives)} ${pick(animals)}`;
}

/** Every name randomChallengeName can give. */
export function challengeNames(): string[] {
  return NAME_WORDS.flatMap(([adjectives, animals]) => adjectives.flatMap((a) => animals.map((b) => `${a} ${b}`)));
}

// A guess pin's colours. A pin says who guessed with the initials inside it, never with its colour:
// with any number of players, colour only tells the viewer's own guess (the Game's orange) from
// everyone else's. The two were checked with the dataviz palette validator against each other and
// against the map's land and water; `ink` is the text colour with the better contrast on `fill`.
export interface PinColors {
  fill: string;
  ink: string;
}
export const OWN_PIN: PinColors = { fill: "#f97316", ink: "#0b0b0b" };
export const OTHER_PIN: PinColors = { fill: "#2a78d6", ink: "#ffffff" };

export function pinColors(isMe: boolean): PinColors {
  return isMe ? OWN_PIN : OTHER_PIN;
}

export function memberLabel(member: { name: string | null }): string {
  return member.name ?? "Unknown player";
}

// A name's words. An email (the name of a user without a display name) counts by the part before
// the @, and dots, dashes and underscores separate words there as spaces do.
function nameWords(name: string | null): string[][] {
  const text = (name ?? "").split("@")[0];
  return text
    .split(/[\s._+-]+/)
    .map((word) => Array.from(word).filter((ch) => /[\p{L}\p{N}]/u.test(ch)))
    .filter((word) => word.length > 0);
}

// Two letters for a name: the first of its first and last words, or a single word's first two.
// "?" for a name without a letter or digit.
export function initialsOf(name: string | null): string {
  const words = nameWords(name);
  if (words.length === 0) return "?";
  const first = words[0];
  const letters = words.length > 1 ? [first[0], words[words.length - 1][0]] : first.slice(0, 2);
  return letters.join("").toUpperCase();
}

// The first two letters of the first word, as written in a name: "Ba" for "Balázs Vigh".
function givenInitials(name: string | null): string {
  const first = nameWords(name)[0] ?? [];
  return first.length === 0 ? "?" : first[0].toUpperCase() + (first[1] ?? "").toLowerCase();
}

// Each player's initials, by user ID, different for every player: a family shares a surname. Players
// whose initials are the same get the start of their first names where those differ ("Ba" and
// "Be"), else a number after the initials, in the members' own order. Labels that differ only in
// case count as the same: "Al" and "AL" cannot be told apart on a pin.
export function memberInitials(members: { user_id: string; name: string | null }[]): Map<string, string> {
  type Player = { user_id: string; name: string | null };
  const groups = new Map<string, Player[]>();
  for (const member of members) {
    const key = initialsOf(member.name);
    groups.set(key, [...(groups.get(key) ?? []), member]);
  }
  const out = new Map<string, string>();
  const taken = new Set<string>();
  const assign = (member: Player, label: string) => {
    out.set(member.user_id, label);
    taken.add(label.toUpperCase());
  };
  for (const [key, group] of groups) {
    if (group.length === 1) assign(group[0], key);
  }
  for (const [key, group] of groups) {
    if (group.length === 1) continue;
    const given = group.map((member) => givenInitials(member.name));
    const upper = given.map((g) => g.toUpperCase());
    if (new Set(upper).size === group.length && upper.every((g) => !taken.has(g))) {
      group.forEach((member, i) => assign(member, given[i]));
      continue;
    }
    let n = 1;
    for (const member of group) {
      while (taken.has(`${key}${n}`)) n++;
      assign(member, `${key}${n}`);
    }
  }
  return out;
}

export function formatPoints(score: number): string {
  return `${formatCount(Math.round(score))} pts`;
}

// One decimal below 10 km, where it matters; whole kilometres, grouped, above.
export function formatKm(km: number): string {
  return km < 10 ? `${km.toFixed(1)} km` : `${formatCount(Math.round(km))} km`;
}

export interface Standing {
  member: ChallengeMember;
  // 1 for the best score; players level on points share a rank, and the next rank is skipped.
  // null for a player who has not guessed yet.
  rank: number | null;
}

// The high-score table: players who have guessed, best first, then those who have not, in the
// members' own order.
export function standings(members: ChallengeMember[]): Standing[] {
  const played = members
    .filter((m) => m.played > 0)
    .sort((a, b) => b.score - a.score || b.played - a.played || memberLabel(a).localeCompare(memberLabel(b)));
  const ranked: Standing[] = [];
  played.forEach((member, i) => {
    const level = i > 0 && played[i - 1].score === member.score;
    ranked.push({ member, rank: level ? ranked[i - 1].rank : i + 1 });
  });
  return [...ranked, ...members.filter((m) => m.played === 0).map((member) => ({ member, rank: null }))];
}

type Progress = Pick<ChallengeSummary, "played" | "score" | "finished" | "item_count">;

// Where the caller stands in a challenge, for its card.
export function challengeStatus(c: Progress): string {
  if (c.item_count === 0) return "No rounds left: its items were deleted";
  if (c.finished && c.played === 0) return "No round you can play: its items are no longer shared with you";
  if (c.finished) return `Finished · ${formatPoints(c.score)}`;
  if (c.played === 0) return "Not played yet";
  return `${c.played} of ${c.item_count} played · ${formatPoints(c.score)}`;
}

// What opening a challenge does next, for the button that opens it.
export function playLabel(c: Progress): string {
  if (c.finished) return "Results";
  return c.played === 0 ? "Play" : "Continue";
}

export function playersLabel(count: number): string {
  return count === 1 ? "1 player" : `${formatCount(count)} players`;
}

// How far a guess was and what it scored, for the popup on its pin and the round's list.
export function guessSummary(guess: Pick<ChallengeGuess, "distance_km" | "total_score">): string {
  return `${formatKm(guess.distance_km)} away · ${formatPoints(guess.total_score)}`;
}

// The date a player guessed and how far off it was; null when they guessed none, or the item has
// no date to compare with.
export function guessDateNote(guess: Pick<ChallengeGuess, "guess_date" | "date_diff_days">): string | null {
  if (guess.guess_date === null || guess.date_diff_days === null) return null;
  const days = Math.round(guess.date_diff_days);
  if (days === 0) return `Guessed ${guess.guess_date}, the right day`;
  return `Guessed ${guess.guess_date}, ${formatCount(days)} ${days === 1 ? "day" : "days"} off`;
}

// The New challenge dialog's own filters, and the albums it offers among them.
export interface ChallengeFilters {
  filters: FilterState;
  // The albums offered while other players are ticked: the caller's albums that every one of them
  // is in too, as the server listed them. null offers every album of the caller's.
  albums: Album[] | null;
  // Ticked albums a player was not in: unticked when that player was ticked, and named in the
  // dialog's note for as long as they are not offered again.
  unticked: Album[];
}

/**
 * The dialog's filters once it offers `albums`, the server's answer for the ticked players, or
 * null for every album of the caller's (`all`): nobody else is ticked, or the answer failed. A
 * ticked album that is no longer offered is unticked, and named until it is offered again. Nothing
 * is ticked again by itself. With every album offered, none is named.
 */
export function offerAlbums(current: ChallengeFilters, albums: Album[] | null, all: Album[]): ChallengeFilters {
  if (albums === null) return { ...current, albums: null, unticked: [] };
  const offered = new Set(albums.map((a) => a.id));
  // A ticked album was on the list offered before, or came with the Game page's filters.
  const known = new Map([...all, ...(current.albums ?? [])].map((a) => [a.id, a]));
  const dropped = current.filters.albumIds.filter((id) => !offered.has(id));
  const unticked = [...current.unticked];
  for (const id of dropped) {
    const album = known.get(id);
    if (album && !unticked.some((a) => a.id === id)) unticked.push(album);
  }
  return {
    filters:
      dropped.length === 0
        ? current.filters
        : { ...current.filters, albumIds: current.filters.albumIds.filter((id) => offered.has(id)) },
    albums,
    unticked: unticked.filter((a) => !offered.has(a.id)),
  };
}

// What the dialog says of the albums offerAlbums unticked; null when it unticked none.
export function untickedAlbumsNote(albums: Pick<Album, "name">[]): string | null {
  if (albums.length === 0) return null;
  const names = albums.map((a) => `“${a.name}”`);
  const list = names.length === 1 ? names[0] : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
  return `Unticked ${list}: not every player is in ${names.length === 1 ? "it" : "them"}.`;
}
