import { useEffect, useMemo, useState } from "react";
import { MapContainer, Marker, Polyline, Popup, TileLayer, useMap } from "react-leaflet";
import L from "leaflet";
import "leaflet/dist/leaflet.css";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { actualIcon, GUESS_LINE } from "@/components/game/gameMarkers";
import { TOP_MARKER_Z_INDEX_OFFSET } from "@/components/map/markers";
import {
  formatPoints,
  guessDateNote,
  guessSummary,
  memberInitials,
  memberLabel,
  pinColors,
  standings,
  type PinColors,
} from "@/lib/challenge";
import { cn } from "@/lib/utils";
import type { ChallengeGuess, ChallengeMember, ChallengeRoundResult } from "@/types";

const PIN_SIZE = 30;

// A player's guess on the map: a circle with their initials. Built as an element, never as markup,
// so nothing from a name is ever parsed as HTML.
function pinIcon(initials: string, colors: PinColors): L.DivIcon {
  const el = document.createElement("div");
  el.textContent = initials;
  Object.assign(el.style, {
    width: `${PIN_SIZE}px`,
    height: `${PIN_SIZE}px`,
    borderRadius: "50%",
    background: colors.fill,
    color: colors.ink,
    border: "2px solid white",
    boxShadow: "0 2px 6px rgba(0,0,0,0.5)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    // Three characters ("BV2") still fit the circle.
    font: `600 ${initials.length > 2 ? 10 : 12}px/1 system-ui, sans-serif`,
  });
  return L.divIcon({ className: "", html: el, iconSize: [PIN_SIZE, PIN_SIZE], iconAnchor: [PIN_SIZE / 2, PIN_SIZE / 2] });
}

// The same circle beside a player's name, in the tables.
export function PlayerChip({ initials, isMe }: { initials: string; isMe: boolean }) {
  const colors = pinColors(isMe);
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-full font-semibold leading-none",
        initials.length > 2 ? "text-[10px]" : "text-xs",
      )}
      style={{ background: colors.fill, color: colors.ink }}
    >
      {initials}
    </span>
  );
}

const FIT_PADDING = 40;

// Shows the answer and every guess of the round, and again when another round is chosen. A round
// where every guess sits on the answer would otherwise open at street level.
function FitRound({ points }: { points: [number, number][] }) {
  const map = useMap();
  const key = points.map((p) => p.join(",")).join(";");
  useEffect(() => {
    if (points.length === 0) return;
    const { x, y } = map.getSize();
    // Leaflet throws on the NaN zoom of a map with no room left inside the padding.
    if (x <= 2 * FIT_PADDING || y <= 2 * FIT_PADDING) return;
    map.fitBounds(L.latLngBounds(points), { padding: [FIT_PADDING, FIT_PADDING], maxZoom: 12, animate: false });
    // key stands for points: the same places in a new array must not move a map the player panned.
  }, [map, key]); // eslint-disable-line react-hooks/exhaustive-deps
  return null;
}

export function Scoreboard({
  members,
  meId,
  rounds,
}: {
  members: ChallengeMember[];
  meId: string;
  // How many rounds the challenge has now.
  rounds: number;
}) {
  const initials = useMemo(() => memberInitials(members), [members]);
  return (
    <table className="w-full text-sm">
      <caption className="sr-only">High scores</caption>
      <thead>
        <tr className="border-b text-left text-xs text-muted-foreground">
          <th scope="col" className="w-8 py-1.5 pr-2 font-medium">
            #
          </th>
          <th scope="col" className="py-1.5 pr-2 font-medium">
            Player
          </th>
          <th scope="col" className="py-1.5 pr-2 text-right font-medium">
            Rounds
          </th>
          <th scope="col" className="py-1.5 text-right font-medium">
            Score
          </th>
        </tr>
      </thead>
      <tbody>
        {standings(members).map(({ member, rank }) => {
          const isMe = member.user_id === meId;
          return (
            <tr key={member.user_id} className={cn("border-b last:border-b-0", isMe && "bg-muted/60")}>
              <td className="py-1.5 pr-2 tabular-nums text-muted-foreground">{rank ?? "–"}</td>
              <th scope="row" className="py-1.5 pr-2 text-left font-normal">
                <span className="flex min-w-0 items-center gap-2">
                  <PlayerChip initials={initials.get(member.user_id) ?? "?"} isMe={isMe} />
                  <span className="min-w-0 [overflow-wrap:anywhere]">
                    {memberLabel(member)}
                    {isMe && <span className="text-muted-foreground"> (you)</span>}
                  </span>
                </span>
              </th>
              <td className="py-1.5 pr-2 text-right tabular-nums text-muted-foreground">
                {member.played} / {rounds}
              </td>
              <td className="py-1.5 text-right font-medium tabular-nums">
                {member.played > 0 ? formatPoints(member.score) : "–"}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

// The item of a round, small, beside its map: a reminder of which one it was.
function RoundPreview({ result }: { result: ChallengeRoundResult }) {
  if (!result.url || !result.media_type) return null;
  const className = "h-full w-full rounded-md bg-black object-cover";
  return result.media_type.startsWith("video") ? (
    // The first frame only: no controls, so it is not a player and takes no keys.
    <video src={result.url} preload="metadata" muted playsInline aria-label={`Round ${result.number}'s video`} className={className} />
  ) : (
    <img src={result.url} alt={`Round ${result.number}'s photo`} loading="lazy" className={className} />
  );
}

interface Props {
  results: ChallengeRoundResult[];
  members: ChallengeMember[];
  meId: string;
}

// What a finished player sees of the rounds: one round at a time, the map with the actual location
// and every player's guess (a pin with their initials, joined to the location by a dashed line;
// a click says how far off it was), and the same guesses as a list, best first.
export function ChallengeResults({ results, members, meId }: Props) {
  const [index, setIndex] = useState(0);
  const initials = useMemo(() => memberInitials(members), [members]);
  const icons = useMemo(() => {
    const out = new Map<string, L.DivIcon>();
    for (const member of members) {
      out.set(member.user_id, pinIcon(initials.get(member.user_id) ?? "?", pinColors(member.user_id === meId)));
    }
    return out;
  }, [members, initials, meId]);

  if (results.length === 0) {
    return <p className="text-sm text-muted-foreground">This challenge has no rounds left: its items were deleted.</p>;
  }
  const result = results[Math.min(index, results.length - 1)];
  const byId = new Map(members.map((m) => [m.user_id, m]));
  // A guess of a player who has since left has no member row, and is not shown.
  const guesses = result.guesses.filter((g) => byId.has(g.user_id));
  const waiting = members.filter((m) => !guesses.some((g) => g.user_id === m.user_id));
  const actual: [number, number] | null =
    result.available && result.actual_lat !== null && result.actual_lng !== null
      ? [result.actual_lat, result.actual_lng]
      : null;
  const name = (g: ChallengeGuess) => memberLabel(byId.get(g.user_id) ?? { name: null });

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="mr-auto text-sm font-semibold">
          Round {result.number} of {results.length}
        </h3>
        <div role="group" aria-label="Round" className="flex items-center gap-1">
          <button
            type="button"
            aria-label="Previous round"
            disabled={index === 0}
            onClick={() => setIndex((i) => Math.max(0, i - 1))}
            className="flex h-8 w-8 items-center justify-center rounded-md border hover:bg-muted disabled:opacity-40 touch:h-11 touch:w-11"
          >
            <ChevronLeft className="h-4 w-4" aria-hidden="true" />
          </button>
          {results.map((r, i) => (
            <button
              key={r.position}
              type="button"
              aria-label={`Round ${r.number}`}
              aria-pressed={i === index}
              onClick={() => setIndex(i)}
              className={cn(
                "flex h-8 w-8 items-center justify-center rounded-md border text-sm tabular-nums touch:h-11 touch:w-11",
                i === index ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted",
              )}
            >
              {r.number}
            </button>
          ))}
          <button
            type="button"
            aria-label="Next round"
            disabled={index >= results.length - 1}
            onClick={() => setIndex((i) => Math.min(results.length - 1, i + 1))}
            className="flex h-8 w-8 items-center justify-center rounded-md border hover:bg-muted disabled:opacity-40 touch:h-11 touch:w-11"
          >
            <ChevronRight className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </div>

      {actual === null ? (
        <p className="rounded-md border border-dashed p-4 text-sm text-muted-foreground">
          This round's item is no longer shared with you, so its location and the guesses on it are not shown.
        </p>
      ) : (
        <>
          <div className="grid gap-3 sm:grid-cols-[10rem_1fr]">
            <div className="h-28">
              <RoundPreview result={result} />
            </div>
            {/* The height is the wrapper's: index.css makes every Leaflet container fill its parent,
                which beats a height class on the map itself and would leave it 0 px tall here. */}
            <div data-testid="round-map" className="h-72 sm:h-96">
              <MapContainer
                center={actual}
                zoom={5}
                // isolate: Leaflet's z-index 1000 panes and controls stay inside the map.
                className="isolate h-full w-full rounded-md"
              >
                <TileLayer
                  attribution='&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">OpenStreetMap</a>'
                  url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
                />
                <FitRound points={[actual, ...guesses.map((g): [number, number] => [g.lat, g.lng])]} />
                {guesses.map((g) => (
                  <Polyline key={`line-${g.user_id}`} positions={[[g.lat, g.lng], actual]} pathOptions={GUESS_LINE} />
                ))}
                {guesses.map((g) => (
                  <Marker
                    key={g.user_id}
                    position={[g.lat, g.lng]}
                    icon={icons.get(g.user_id)}
                    title={name(g)}
                    alt={`${name(g)}'s guess`}
                  >
                    <Popup>
                      <strong>{name(g)}</strong>
                      <br />
                      {guessSummary(g)}
                      {guessDateNote(g) && (
                        <>
                          <br />
                          {guessDateNote(g)}
                        </>
                      )}
                    </Popup>
                  </Marker>
                ))}
                <Marker
                  position={actual}
                  icon={actualIcon}
                  zIndexOffset={TOP_MARKER_Z_INDEX_OFFSET}
                  title="Actual location"
                  alt="Actual location"
                />
              </MapContainer>
            </div>
          </div>

          <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <span className="inline-block h-3 w-3 rounded-full bg-green-500" aria-hidden="true" /> Actual location
              {result.actual_date && `, taken ${result.actual_date.slice(0, 10)}`}
            </span>
            <span>A pin is a player's guess. Select one to see how far off it was.</span>
          </p>

          <ul aria-label={`Guesses of round ${result.number}`} className="divide-y rounded-md border text-sm">
            {guesses.map((g) => (
              <li key={g.user_id} className="flex items-center gap-2 px-3 py-2">
                <PlayerChip initials={initials.get(g.user_id) ?? "?"} isMe={g.user_id === meId} />
                <span className="min-w-0 flex-1">
                  <span className="[overflow-wrap:anywhere]">
                    {name(g)}
                    {g.user_id === meId && <span className="text-muted-foreground"> (you)</span>}
                  </span>
                  {guessDateNote(g) && <span className="block text-xs text-muted-foreground">{guessDateNote(g)}</span>}
                </span>
                <span className="shrink-0 text-right tabular-nums">{guessSummary(g)}</span>
              </li>
            ))}
            {waiting.map((m) => (
              <li key={m.user_id} className="flex items-center gap-2 px-3 py-2 text-muted-foreground">
                <PlayerChip initials={initials.get(m.user_id) ?? "?"} isMe={m.user_id === meId} />
                <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{memberLabel(m)}</span>
                <span className="shrink-0">No guess yet</span>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
