import type { ReactNode } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type L from "leaflet";
import { ChallengeResults, Scoreboard } from "@/components/game/ChallengeResults";
import { OTHER_PIN, OWN_PIN } from "@/lib/challenge";
import { TOP_MARKER_Z_INDEX_OFFSET } from "@/components/map/markers";
import type { ChallengeGuess, ChallengeMember, ChallengeRoundResult } from "@/types";

// One map object for every useMap() call; getSize feeds the fit.
const leaflet = vi.hoisted(() => {
  const state = {
    size: { x: 800, y: 400 },
    map: { fitBounds: vi.fn(), getSize: () => state.size },
  };
  return state;
});

interface MarkerProps {
  position: [number, number];
  icon?: L.DivIcon;
  zIndexOffset?: number;
  title?: string;
  alt?: string;
  children?: ReactNode;
}

vi.mock("react-leaflet", () => ({
  MapContainer: ({ children, className }: { children: ReactNode; className?: string }) => (
    <div data-testid="map" className={className}>
      {children}
    </div>
  ),
  TileLayer: ({ attribution }: { attribution?: string }) => <span data-testid="tile-credit" data-attribution={attribution} />,
  // The pin's own element is put into the page, as Leaflet does with a divIcon's html.
  Marker: ({ position, icon, zIndexOffset, title, alt, children }: MarkerProps) => (
    <div
      data-testid="marker"
      data-position={position.join(",")}
      data-z-index-offset={zIndexOffset ?? 0}
      title={title}
      aria-label={alt}
      ref={(el) => {
        const html = icon?.options.html;
        if (el && html instanceof HTMLElement && !el.contains(html)) el.prepend(html);
      }}
    >
      {children}
    </div>
  ),
  Popup: ({ children }: { children: ReactNode }) => <div data-testid="popup">{children}</div>,
  Polyline: ({ positions, pathOptions }: { positions: [number, number][]; pathOptions: { dashArray?: string } }) => (
    <span data-testid="line" data-positions={positions.map((p) => p.join(",")).join(" ")} data-dash={pathOptions.dashArray} />
  ),
  useMap: () => leaflet.map,
}));

const ME = "u-me";

const members: ChallengeMember[] = [
  { user_id: "u-alice", name: "Alice Smith", is_owner: true, played: 3, score: 9000 },
  { user_id: ME, name: "Me Myself", is_owner: false, played: 3, score: 12500.4 },
  { user_id: "u-carol", name: "carol@example.test", is_owner: false, played: 1, score: 2000 },
  { user_id: "u-dave", name: "Dave Smith", is_owner: false, played: 0, score: 0 },
];

function guess(user_id: string, lat: number, lng: number, extra: Partial<ChallengeGuess> = {}): ChallengeGuess {
  return {
    user_id,
    lat,
    lng,
    guess_date: null,
    distance_km: 3.26,
    date_diff_days: null,
    location_score: 4800,
    time_score: null,
    total_score: 4812.5,
    ...extra,
  };
}

function round(number: number, guesses: ChallengeGuess[], extra: Partial<ChallengeRoundResult> = {}): ChallengeRoundResult {
  return {
    position: number,
    number,
    available: true,
    media_id: `m${number}`,
    media_type: "photo",
    url: `https://s3.example/m${number}`,
    actual_lat: 47.5,
    actual_lng: 19.04,
    actual_date: "2024-05-02 12:00:00+00",
    guesses,
    ...extra,
  };
}

const hidden: ChallengeRoundResult = {
  position: 3,
  number: 3,
  available: false,
  media_id: null,
  media_type: null,
  url: null,
  actual_lat: null,
  actual_lng: null,
  actual_date: null,
  guesses: [],
};

const results: ChallengeRoundResult[] = [
  round(1, [
    guess(ME, 47.52, 19.05, { guess_date: "2024-05-11", date_diff_days: 9 }),
    guess("u-alice", 48.2, 16.37, { distance_km: 214.4, total_score: 2100 }),
    guess("u-carol", -33.9, 151.2, { distance_km: 15990, total_score: 0 }),
  ]),
  round(2, [guess("u-alice", 47.5, 19.04, { distance_km: 0, total_score: 5000 }), guess(ME, 47.0, 19.5)], {
    media_type: "video",
    actual_lat: 46,
    actual_lng: 18,
    actual_date: null,
  }),
  hidden,
];

function markers(): HTMLElement[] {
  return screen.getAllByTestId("marker");
}

function pinOf(name: string): HTMLElement {
  return screen.getByTitle(name).firstElementChild as HTMLElement;
}

describe("ChallengeResults", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    leaflet.size = { x: 800, y: 400 };
  });

  it("shows the first round: the actual location, a pin per guess and a dashed line from each", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);

    expect(screen.getByRole("heading", { name: "Round 1 of 3" })).toBeInTheDocument();
    expect(markers().map((m) => m.dataset.position)).toEqual(["47.52,19.05", "48.2,16.37", "-33.9,151.2", "47.5,19.04"]);
    const lines = screen.getAllByTestId("line");
    expect(lines.map((l) => l.dataset.positions)).toEqual([
      "47.52,19.05 47.5,19.04",
      "48.2,16.37 47.5,19.04",
      "-33.9,151.2 47.5,19.04",
    ]);
    for (const line of lines) expect(line.dataset.dash).toBe("6 8");
  });

  it("draws the actual location above the pins, and names it", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    const actual = screen.getByTitle("Actual location");
    expect(actual.dataset.zIndexOffset).toBe(String(TOP_MARKER_Z_INDEX_OFFSET));
    for (const marker of markers().filter((m) => m !== actual)) expect(marker.dataset.zIndexOffset).toBe("0");
    expect(screen.getByText(/Actual location, taken 2024-05-02/)).toBeInTheDocument();
  });

  it("writes each player's initials in their pin: the viewer's orange, everyone else's blue", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);

    const mine = pinOf("Me Myself");
    expect(mine.textContent).toBe("MM");
    expect(mine.style.background).toBe("rgb(249, 115, 22)");
    expect(mine.style.color).toBe("rgb(11, 11, 11)");
    expect(OWN_PIN).toEqual({ fill: "#f97316", ink: "#0b0b0b" });

    const alice = pinOf("Alice Smith");
    expect(alice.textContent).toBe("AS");
    expect(alice.style.background).toBe("rgb(42, 120, 214)");
    expect(alice.style.color).toBe("rgb(255, 255, 255)");
    expect(OTHER_PIN).toEqual({ fill: "#2a78d6", ink: "#ffffff" });

    expect(pinOf("carol@example.test").textContent).toBe("CA");
    expect(screen.getByLabelText("Alice Smith's guess")).toBe(screen.getByTitle("Alice Smith"));
  });

  it("tells players with the same initials apart", () => {
    const family: ChallengeMember[] = [
      { user_id: "u1", name: "Balázs Vigh", is_owner: true, played: 1, score: 1 },
      { user_id: "u2", name: "Bea Vigh", is_owner: false, played: 1, score: 2 },
    ];
    render(
      <ChallengeResults results={[round(1, [guess("u1", 1, 2), guess("u2", 3, 4)])]} members={family} meId="u1" />,
    );
    expect(pinOf("Balázs Vigh").textContent).toBe("Ba");
    expect(pinOf("Bea Vigh").textContent).toBe("Be");
  });

  it("never reads a player's name as markup", () => {
    const hostile: ChallengeMember[] = [
      { user_id: "u1", name: '<img src=x onerror="alert(1)"> <b>Bob</b>', is_owner: true, played: 1, score: 1 },
    ];
    const { container } = render(
      <ChallengeResults results={[round(1, [guess("u1", 1, 2)])]} members={hostile} meId="other" />,
    );
    expect(container.querySelector("img[src='x']")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(screen.getAllByText(hostile[0].name!).length).toBeGreaterThan(0);
    const pin = markers()[0].firstElementChild as HTMLElement;
    expect(pin.children).toHaveLength(0);
    expect(pin.textContent).toBe("IB");
  });

  it("says in a pin's popup who guessed, how far off and for how many points", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    const popup = (name: string) => within(screen.getByTitle(name)).getByTestId("popup");
    expect(popup("Me Myself")).toHaveTextContent("Me Myself3.3 km away · 4,813 ptsGuessed 2024-05-11, 9 days off");
    expect(popup("Alice Smith")).toHaveTextContent("Alice Smith214 km away · 2,100 pts");
    expect(popup("Alice Smith")).not.toHaveTextContent("Guessed");
    expect(popup("carol@example.test")).toHaveTextContent("15,990 km away · 0 pts");
    expect(within(screen.getByTitle("Actual location")).queryByTestId("popup")).toBeNull();
  });

  it("lists the round's guesses in the order given, then the players without one", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    const rows = within(screen.getByRole("list", { name: "Guesses of round 1" })).getAllByRole("listitem");
    expect(rows.map((r) => r.textContent)).toEqual([
      "MMMe Myself (you)Guessed 2024-05-11, 9 days off3.3 km away · 4,813 pts",
      "ASAlice Smith214 km away · 2,100 pts",
      "CAcarol@example.test15,990 km away · 0 pts",
      "DSDave SmithNo guess yet",
    ]);
  });

  it("fits the map to the actual location and the guesses, once per round", async () => {
    const { rerender } = render(<ChallengeResults results={results} members={members} meId={ME} />);
    expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(1);
    const [bounds, options] = leaflet.map.fitBounds.mock.calls[0];
    expect(bounds.getSouthWest()).toMatchObject({ lat: -33.9, lng: 16.37 });
    expect(bounds.getNorthEast()).toMatchObject({ lat: 48.2, lng: 151.2 });
    expect(options).toEqual({ padding: [40, 40], maxZoom: 12, animate: false });

    // The same results again (a reload of the challenge) leave the map where the player put it.
    rerender(<ChallengeResults results={structuredClone(results)} members={members} meId={ME} />);
    expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole("button", { name: "Round 2" }));
    expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(2);
    expect(leaflet.map.fitBounds.mock.calls[1][0].getSouthWest()).toMatchObject({ lat: 46, lng: 18 });
  });

  it("does not fit a map without room", () => {
    leaflet.size = { x: 60, y: 300 };
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    expect(leaflet.map.fitBounds).not.toHaveBeenCalled();
  });

  it("steps through the rounds with the numbers and the arrows", async () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    const previous = screen.getByRole("button", { name: "Previous round" });
    const next = screen.getByRole("button", { name: "Next round" });
    expect(previous).toBeDisabled();
    expect(screen.getByRole("button", { name: "Round 1" })).toHaveAttribute("aria-pressed", "true");

    await userEvent.click(next);
    expect(screen.getByRole("heading", { name: "Round 2 of 3" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Round 2" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Round 1" })).toHaveAttribute("aria-pressed", "false");
    expect(markers().map((m) => m.dataset.position)).toEqual(["47.5,19.04", "47,19.5", "46,18"]);
    expect(previous).toBeEnabled();

    await userEvent.click(screen.getByRole("button", { name: "Round 3" }));
    expect(next).toBeDisabled();
    await userEvent.click(previous);
    expect(screen.getByRole("heading", { name: "Round 2 of 3" })).toBeInTheDocument();
  });

  it("shows nothing of a round whose item is no longer shared with the viewer", async () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    await userEvent.click(screen.getByRole("button", { name: "Round 3" }));
    expect(screen.getByRole("heading", { name: "Round 3 of 3" })).toBeInTheDocument();
    expect(
      screen.getByText("This round's item is no longer shared with you, so its location and the guesses on it are not shown."),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("map")).toBeNull();
    expect(screen.queryByRole("list")).toBeNull();
  });

  it("shows the round's item small: a photo as an image, a video as its first frame without controls", async () => {
    const { container } = render(<ChallengeResults results={results} members={members} meId={ME} />);
    expect(screen.getByRole("img", { name: "Round 1's photo" })).toHaveAttribute("src", "https://s3.example/m1");
    expect(container.querySelector("video")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Round 2" }));
    const video = container.querySelector("video")!;
    expect(video).toHaveAttribute("src", "https://s3.example/m2");
    expect(video).not.toHaveAttribute("controls");
    expect(video).toHaveAttribute("preload", "metadata");
    expect(screen.getByText("Actual location")).toBeInTheDocument();
    expect(screen.queryByText(/taken/)).toBeNull();
  });

  it("leaves out the guess of a player who has left", () => {
    const one = [round(1, [guess("u-gone", 1, 2), guess(ME, 3, 4)])];
    render(<ChallengeResults results={one} members={members} meId={ME} />);
    expect(markers().map((m) => m.dataset.position)).toEqual(["3,4", "47.5,19.04"]);
    expect(screen.getAllByTestId("line")).toHaveLength(1);
  });

  it("says so when no round is left", () => {
    render(<ChallengeResults results={[]} members={members} meId={ME} />);
    expect(screen.getByText("This challenge has no rounds left: its items were deleted.")).toBeInTheDocument();
    expect(screen.queryByTestId("map")).toBeNull();
  });

  // index.css gives every Leaflet container height: 100%, which beats a height class on the map
  // itself: in an auto-height grid row the map would be 0 px tall.
  it("sizes the map by its wrapper, which the map fills", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    const map = screen.getByTestId("map");
    expect(map.className.split(" ")).toEqual(expect.arrayContaining(["h-full", "w-full"]));
    expect(map.className).not.toMatch(/\bh-(72|96)\b/);
    expect(map.parentElement).toBe(screen.getByTestId("round-map"));
    expect(map.parentElement!.className.split(" ")).toEqual(expect.arrayContaining(["h-72", "sm:h-96"]));
  });

  it("keeps Leaflet's layers inside the map and its credit in a new tab", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    expect(screen.getByTestId("map").className.split(" ")).toContain("isolate");
    const credit = screen.getByTestId("tile-credit").dataset.attribution!;
    expect(credit).toContain('target="_blank"');
    expect(credit).toContain('rel="noopener noreferrer"');
  });

  it("gives the round buttons a 44 px target on touch", () => {
    render(<ChallengeResults results={results} members={members} meId={ME} />);
    for (const name of ["Previous round", "Round 1", "Round 3", "Next round"]) {
      const classes = screen.getByRole("button", { name }).className.split(" ");
      expect(classes).toEqual(expect.arrayContaining(["touch:h-11", "touch:w-11"]));
    }
  });
});

describe("Scoreboard", () => {
  it("ranks the players who have guessed, marks the viewer and lists the others without a rank", () => {
    render(<Scoreboard members={members} meId={ME} rounds={3} />);
    const table = screen.getByRole("table", { name: "High scores" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((r) => within(r).getAllByRole("cell").map((c) => c.textContent))).toEqual([
      ["1", "3 / 3", "12,500 pts"],
      ["2", "3 / 3", "9,000 pts"],
      ["3", "1 / 3", "2,000 pts"],
      ["–", "0 / 3", "–"],
    ]);
    expect(rows.map((r) => within(r).getByRole("rowheader").textContent)).toEqual([
      "MMMe Myself (you)",
      "ASAlice Smith",
      "CAcarol@example.test",
      "DSDave Smith",
    ]);
  });

  it("names a player without a name", () => {
    render(<Scoreboard members={[{ user_id: "u1", name: null, is_owner: true, played: 1, score: 10 }]} meId="x" rounds={5} />);
    expect(screen.getByRole("rowheader")).toHaveTextContent("?Unknown player");
  });
});
