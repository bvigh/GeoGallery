import type { ComponentProps, ReactNode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { GamePage } from "@/pages/GamePage";
import type { FilterPanel } from "@/components/map/FilterPanel";
import { emptyFilters } from "@/lib/filters";
import type { Album } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), getKeyframes: vi.fn(), listAlbums: vi.fn() } };
});

// The latest props GamePage rendered FilterPanel with.
const panel = vi.hoisted(() => ({ props: null as ComponentProps<typeof FilterPanel> | null }));

vi.mock("@/components/map/FilterPanel", () => ({
  FilterPanel: (props: ComponentProps<typeof FilterPanel>) => {
    panel.props = props;
    return null;
  },
}));
vi.mock("@/components/viewer/Panorama360", () => ({ default: () => null }));
vi.mock("@/components/viewer/Video360", () => ({ default: () => null }));

type MapClick = (e: { latlng: { lat: number; lng: number } }) => void;
// The guess map's click handler, so a test can place a guess.
const guessMap = vi.hoisted(() => ({ click: null as MapClick | null }));

// One map object for every useMap() call, so effects depending on it run once. on/fire keep
// Leaflet's event registry, and getSize/getContainer feed the result map's fit.
const leaflet = vi.hoisted(() => {
  const handlers = new Map<string, Set<() => void>>();
  const state = {
    size: { x: 1200, y: 800 },
    container: null as HTMLElement | null,
    map: {
      invalidateSize: vi.fn(),
      fitBounds: vi.fn(),
      getSize: () => state.size,
      getContainer: () => state.container!,
      on: (type: string, fn: () => void) => {
        if (!handlers.has(type)) handlers.set(type, new Set());
        handlers.get(type)!.add(fn);
      },
      off: (type: string, fn: () => void) => handlers.get(type)?.delete(fn),
    },
    fire: (type: string) => handlers.get(type)?.forEach((fn) => fn()),
    reset: () => handlers.clear(),
  };
  return state;
});

vi.mock("react-leaflet", async () => {
  const { forwardRef } = await import("react");
  return {
    MapContainer: ({ children, className }: { children: ReactNode; className?: string }) => (
      <div data-testid="map" className={className}>
        {children}
      </div>
    ),
    TileLayer: ({ attribution }: { attribution?: string }) => (
      <span data-testid="tile-credit" data-attribution={attribution} />
    ),
    Marker: forwardRef(
      ({ position, zIndexOffset }: { position: [number, number]; zIndexOffset?: number }, _ref) => (
        <span data-testid="marker" data-position={position.join(",")} data-z-index-offset={zIndexOffset ?? 0} />
      ),
    ),
    Polyline: () => null,
    useMap: () => leaflet.map,
    useMapEvents: (handlers: { click?: MapClick }) => {
      if (handlers.click) guessMap.click = handlers.click;
      return null;
    },
  };
});

const mockApi = api as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
  getKeyframes: ReturnType<typeof vi.fn>;
  listAlbums: ReturnType<typeof vi.fn>;
};

const sampleAlbums: Album[] = [
  {
    id: "a1",
    name: "Family",
    owner_id: "u-me",
    owner_name: "Me",
    is_owner: true,
    member_count: 2,
    media_count: 4,
    start_date: null,
    end_date: null,
    created_at: "2025-01-01T00:00:00Z",
    last_added_at: null,
  },
];

const guessResult = {
  actual_lat: 64.2,
  actual_lng: -21.8,
  actual_date: null,
  distance_km: 12.3,
  date_diff_days: null,
  location_score: 4000,
  time_score: null,
  total_score: 4000,
};

function noRounds(path: string) {
  if (path === "/trips") return Promise.resolve([]);
  return Promise.reject(new ApiError("GET /game/round failed: 404", 404));
}

const round = { id: "m1", media_type: "photo", url: "https://s3.example/m1", max_dist_km: 500 };

function roundOf(mediaType: string) {
  return (path: string) => {
    if (path === "/trips") return Promise.resolve([]);
    return Promise.resolve({ ...round, media_type: mediaType });
  };
}

const oneRound = roundOf("photo");

// Starts a round and places a guess on the map, leaving Submit enabled.
async function startAndGuess() {
  render(<GamePage />);
  await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
  const submit = await screen.findByRole("button", { name: "Submit" });
  act(() => guessMap.click!({ latlng: { lat: 64.1, lng: -21.9 } }));
  expect(submit).toBeEnabled();
  return submit;
}

// The guess map overlay: its outer column carries the shown size.
function guessOverlay() {
  return screen.getByRole("button", { name: /^(Hide|Show) map$/ }).closest("[data-size]") as HTMLElement;
}

describe("GamePage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    panel.props = null;
    guessMap.click = null;
    leaflet.reset();
    leaflet.size = { x: 1200, y: 800 };
    leaflet.container = document.createElement("div");
    mockApi.listAlbums.mockResolvedValue(sampleAlbums);
    mockApi.getKeyframes.mockResolvedValue([]);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("passes albums to FilterPanel", async () => {
    mockApi.get.mockImplementation(noRounds);
    render(<GamePage />);
    await waitFor(() => expect(panel.props?.albums).toEqual(sampleAlbums));
    expect(panel.props?.activeCount).toBe(0);
  });

  it("sends no filter params with the default filters", async () => {
    mockApi.get.mockImplementation(noRounds);
    render(<GamePage />);
    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    await screen.findByText("No geolocated media yet");
    expect(mockApi.get).toHaveBeenCalledWith("/game/round", {});
  });

  it("sends collection, owner and media type filters to /game/round", async () => {
    mockApi.get.mockImplementation(noRounds);
    render(<GamePage />);
    await waitFor(() => expect(panel.props).not.toBeNull());

    act(() => {
      panel.props!.onChange({
        ...emptyFilters,
        tripIds: ["unassigned"],
        albumIds: ["a1"],
        mediaTypes: ["photo"],
        owner: "mine",
      });
    });
    // Collections count once, owner once, media type once.
    expect(panel.props?.activeCount).toBe(3);

    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    // The library may have plenty: only the filters matched nothing.
    expect(await screen.findByText("No geolocated media match these filters")).toBeInTheDocument();
    expect(screen.queryByText("No geolocated media yet")).not.toBeInTheDocument();
    expect(mockApi.get).toHaveBeenCalledWith("/game/round", {
      trip_id: ["unassigned"],
      album_id: ["a1"],
      media_type: ["photo"],
      owner: "mine",
    });
  });

  it("renders the empty state when /game/round returns 404", async () => {
    mockApi.get.mockImplementation((path: string) => {
      if (path === "/trips") return Promise.resolve([]);
      return Promise.reject(new ApiError("GET /game/round failed: 404", 404));
    });
    render(<GamePage />);
    expect(screen.queryByText("No geolocated media yet")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    expect(await screen.findByText("No geolocated media yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start Round" })).toBeInTheDocument();
  });

  it("drops the empty-state notice once the filters change", async () => {
    mockApi.get.mockImplementation(noRounds);
    render(<GamePage />);
    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    await screen.findByText("No geolocated media yet");

    act(() => panel.props!.onChange({ ...emptyFilters, owner: "shared" }));
    expect(screen.queryByText(/^No geolocated media/)).not.toBeInTheDocument();
  });

  it("submits the guess and shows the score", async () => {
    mockApi.get.mockImplementation(oneRound);
    mockApi.post.mockResolvedValue(guessResult);
    const submit = await startAndGuess();
    await userEvent.click(submit);

    expect(await screen.findByText("4,000 pts")).toBeInTheDocument();
    expect(mockApi.post).toHaveBeenCalledWith(
      "/game/guess",
      expect.objectContaining({ media_id: "m1", guess_lat: 64.1, guess_lng: -21.9, max_dist_km: 500 })
    );
  });

  it("ends the round when its photo is no longer available", async () => {
    mockApi.get.mockImplementation(oneRound);
    mockApi.post.mockRejectedValue(new ApiError("POST /game/guess failed: 404", 404));
    const submit = await startAndGuess();
    await userEvent.click(submit);

    expect(await screen.findByText("This photo is no longer available")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("img", { name: "guess this" })).not.toBeInTheDocument();

    // The next round starts as usual and clears the notice.
    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    expect(await screen.findByRole("button", { name: "Submit" })).toBeInTheDocument();
    expect(screen.queryByText("This photo is no longer available")).not.toBeInTheDocument();
  });

  it("keeps the round open with an error when submitting fails otherwise", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.get.mockImplementation(oneRound);
    mockApi.post.mockRejectedValueOnce(new ApiError("POST /game/guess failed: 500", 500));
    const submit = await startAndGuess();
    await userEvent.click(submit);

    expect(await screen.findByRole("alert")).toHaveTextContent("Could not submit guess");
    expect(screen.getByRole("button", { name: "Submit" })).toBeEnabled();
    expect(screen.getByRole("img", { name: "guess this" })).toBeInTheDocument();

    // Retrying works and clears the error.
    mockApi.post.mockResolvedValueOnce(guessResult);
    await userEvent.click(screen.getByRole("button", { name: "Submit" }));
    expect(await screen.findByText("4,000 pts")).toBeInTheDocument();
  });

  describe("guess map", () => {
    it("can be hidden and shown again, keeping the date field and Submit", async () => {
      mockApi.get.mockImplementation(oneRound);
      await startAndGuess();
      expect(guessOverlay()).toHaveAttribute("data-size", "small");

      await userEvent.click(screen.getByRole("button", { name: "Hide map" }));
      expect(guessOverlay()).toHaveAttribute("data-size", "hidden");
      expect(screen.getByTestId("map")).not.toBeVisible();
      expect(screen.getByRole("button", { name: "Submit" })).toBeVisible();
      expect(screen.getByRole("textbox", { name: "Date taken (dd-mm-yyyy)" })).toBeVisible();

      await userEvent.click(screen.getByRole("button", { name: "Show map" }));
      expect(guessOverlay()).toHaveAttribute("data-size", "small");
      expect(screen.getByTestId("map")).toBeVisible();
    });

    it("does not grow on a tap, only under a mouse", async () => {
      mockApi.get.mockImplementation(oneRound);
      await startAndGuess();
      const overlay = guessOverlay();

      fireEvent.pointerEnter(overlay, { pointerType: "touch" });
      expect(overlay).toHaveAttribute("data-size", "small");

      fireEvent.pointerEnter(overlay, { pointerType: "mouse" });
      expect(overlay).toHaveAttribute("data-size", "large");
      fireEvent.pointerLeave(overlay, { pointerType: "mouse" });
      expect(overlay).toHaveAttribute("data-size", "small");
    });

    it("starts every round small, whatever size the last round ended with", async () => {
      mockApi.get.mockImplementation(oneRound);
      mockApi.post.mockResolvedValue(guessResult);
      const submit = await startAndGuess();
      // A tap: userEvent would hover with a mouse first, which already enlarges the map.
      fireEvent.click(screen.getByRole("button", { name: "Enlarge map" }));
      expect(guessOverlay()).toHaveAttribute("data-size", "large");

      await userEvent.click(submit);
      await userEvent.click(await screen.findByRole("button", { name: "Next Round" }));
      await screen.findByRole("button", { name: "Submit" });
      expect(guessOverlay()).toHaveAttribute("data-size", "small");
    });

    it.each([
      ["photo", "bottom-4"],
      ["photo_360", "bottom-4"],
      // Above the 360 player's control bar, and above the native video controls.
      ["video_360", "bottom-12"],
      ["video", "bottom-16"],
    ])("sits clear of the controls of a %s round", async (mediaType, bottom) => {
      mockApi.get.mockImplementation(roundOf(mediaType));
      render(<GamePage />);
      await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
      await screen.findByRole("button", { name: "Submit" });
      expect(guessOverlay()).toHaveClass(bottom);
      // Replaced, not added beside it: the CSS order of two bottom-* classes is not theirs to pick.
      if (bottom !== "bottom-4") expect(guessOverlay()).not.toHaveClass("bottom-4");
    });
  });

  it("shows a photo round in a zoomable image", async () => {
    mockApi.get.mockImplementation(oneRound);
    render(<GamePage />);
    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    const img = await screen.findByRole("img", { name: "guess this" });
    expect(img).toHaveAttribute("src", round.url);
    expect(img.closest("[data-zoomed]")).toHaveAttribute("data-zoomed", "false");
  });

  it("shows the whole frame of a video round", async () => {
    mockApi.get.mockImplementation(roundOf("video"));
    const { container } = render(<GamePage />);
    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    await screen.findByRole("button", { name: "Submit" });
    const video = container.querySelector("video");
    expect(video).toHaveClass("object-contain");
    expect(video).not.toHaveClass("object-cover");
  });

  describe("date field", () => {
    async function dateField() {
      mockApi.get.mockImplementation(oneRound);
      await startAndGuess();
      return screen.getByRole("textbox", { name: "Date taken (dd-mm-yyyy)" });
    }

    it("asks for a number pad and inserts the dashes", async () => {
      mockApi.post.mockResolvedValue(guessResult);
      const field = await dateField();
      expect(field).toHaveAttribute("inputmode", "numeric");
      expect(field).toHaveAttribute("placeholder", "dd-mm-yyyy");

      await userEvent.type(field, "12032020");
      expect(field).toHaveValue("12-03-2020");

      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      expect(mockApi.post).toHaveBeenCalledWith(
        "/game/guess",
        expect.objectContaining({ guess_date: "2020-03-12" })
      );
    });

    it.each([
      ["12", "12-"],
      ["1203", "12-03-"],
      // Typed dashes are not doubled, and letters are dropped.
      ["12-03-2020", "12-03-2020"],
      ["1a2", "12-"],
      ["120320201", "12-03-2020"],
    ])("formats %j as %j", async (typed, want) => {
      const field = await dateField();
      await userEvent.type(field, typed);
      expect(field).toHaveValue(want);
    });

    it("takes a pasted date", async () => {
      const field = await dateField();
      await userEvent.click(field);
      await userEvent.paste("12/03/2020");
      expect(field).toHaveValue("12-03-2020");
    });

    it("lets Backspace delete past a dash", async () => {
      const field = await dateField();
      await userEvent.type(field, "1203");
      expect(field).toHaveValue("12-03-");
      await userEvent.type(field, "{Backspace}");
      expect(field).toHaveValue("12-03");
      await userEvent.type(field, "{Backspace}{Backspace}");
      expect(field).toHaveValue("12");
      await userEvent.type(field, "{Backspace}");
      expect(field).toHaveValue("1");
    });

    // A date typed in full, then edited inside the text with the keyboard and the caret.
    async function filledDateField() {
      const field = await dateField();
      await userEvent.type(field, "12032020");
      expect(field).toHaveValue("12-03-2020");
      return field;
    }

    function expectCaret(field: HTMLElement, at: number) {
      const input = field as HTMLInputElement;
      expect([input.selectionStart, input.selectionEnd]).toEqual([at, at]);
    }

    async function expectSubmittedDate(guessDate: string) {
      mockApi.post.mockResolvedValue(guessResult);
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      expect(mockApi.post).toHaveBeenCalledWith("/game/guess", expect.objectContaining({ guess_date: guessDate }));
    }

    it("keeps the other parts in place on a Backspace inside the text", async () => {
      const field = await filledDateField();
      // The caret after the "3" of the month.
      await userEvent.type(field, "{Backspace}", { initialSelectionStart: 5, initialSelectionEnd: 5 });
      expect(field).toHaveValue("12-0-2020");
      expectCaret(field, 4);

      await userEvent.type(field, "4", { initialSelectionStart: 4, initialSelectionEnd: 4 });
      expect(field).toHaveValue("12-04-2020");
      expectCaret(field, 5);
      await expectSubmittedDate("2020-04-12");
    });

    // Selects start..end and types text. user-event will not type over a selection in a field at
    // its maxLength (it ignores the selection), so the selection is deleted first.
    async function typeOver(field: HTMLElement, start: number, end: number, text: string) {
      await userEvent.type(field, `{Backspace}${text}`, { initialSelectionStart: start, initialSelectionEnd: end });
    }

    it.each([
      ["day", 0, 2, "15", "15-03-2020", "2020-03-15"],
      ["day with one digit", 0, 2, "1", "1-03-2020", "2020-03-01"],
      ["month", 3, 5, "04", "12-04-2020", "2020-04-12"],
      ["year", 6, 10, "2019", "12-03-2019", "2019-03-12"],
    ])("replaces a selected %s", async (_part, start, end, typed, want, sent) => {
      const field = await filledDateField();
      await typeOver(field, start, end, typed);
      expect(field).toHaveValue(want);
      expectCaret(field, start + typed.length);
      await expectSubmittedDate(sent);
    });

    it("keeps the text when a browser types one digit over the selected day", async () => {
      const field = (await filledDateField()) as HTMLInputElement;
      // What a browser does in one input event: the new text with the caret after the digit.
      // The prototype setter bypasses React's value tracker, so React sees the change.
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, "1-03-2020");
      field.setSelectionRange(1, 1);
      fireEvent.input(field);
      expect(field).toHaveValue("1-03-2020");
      expectCaret(field, 1);
      await expectSubmittedDate("2020-03-01");
    });

    it("keeps the parts typed inside the text when typing on at the end", async () => {
      const field = await filledDateField();
      await typeOver(field, 0, 2, "1");
      expect(field).toHaveValue("1-03-2020");
      // The year is full, so the digit is dropped instead of shifting every part.
      await userEvent.type(field, "5");
      expect(field).toHaveValue("1-03-2020");
    });

    it("turns a separator typed inside the text into a dash, keeping the caret", async () => {
      const field = await filledDateField();
      await userEvent.type(field, "{Delete}", { initialSelectionStart: 2, initialSelectionEnd: 2 });
      expect(field).toHaveValue("1203-2020");
      await userEvent.type(field, "/", { initialSelectionStart: 2, initialSelectionEnd: 2 });
      expect(field).toHaveValue("12-03-2020");
      expectCaret(field, 3);
      // A letter is dropped, and the caret stays where it was.
      await userEvent.type(field, "x", { initialSelectionStart: 3, initialSelectionEnd: 3 });
      expect(field).toHaveValue("12-03-2020");
      expectCaret(field, 3);
    });

    it("reads a one-digit day and month typed with dashes", async () => {
      const field = await dateField();
      await userEvent.type(field, "1-3-2020");
      expect(field).toHaveValue("1-3-2020");
      await expectSubmittedDate("2020-03-01");
    });

    it.each([["31-02-2020"], ["12-03-20"], ["12-13-2020"]])(
      "asks again for an unreadable date %j instead of scoring it without time",
      async (typed) => {
        mockApi.post.mockResolvedValue(guessResult);
        const field = await dateField();
        await userEvent.type(field, typed);
        await userEvent.click(screen.getByRole("button", { name: "Submit" }));
        expect(mockApi.post).not.toHaveBeenCalled();
        const hint = screen.getByRole("alert");
        expect(hint).toHaveTextContent("Enter the date as dd-mm-yyyy, or leave it empty");
        expect(field).toHaveAttribute("aria-invalid", "true");
        expect(field).toHaveAccessibleDescription("Enter the date as dd-mm-yyyy, or leave it empty");

        // The next edit drops the hint; an empty date is sent as none.
        await userEvent.clear(field);
        expect(screen.queryByRole("alert")).not.toBeInTheDocument();
        expect(field).not.toHaveAttribute("aria-invalid");
        await expectSubmittedDate("");
      }
    );
  });

  describe("result", () => {
    // Fakes the layout: the map fills size, the score card sits cardHeight px tall, 24px above
    // the map's bottom edge.
    function layout(size: { x: number; y: number }, cardHeight: number) {
      leaflet.size = size;
      vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
        let top = 0;
        let height = 0;
        if (this === leaflet.container) {
          height = size.y;
        } else if (this.getAttribute("aria-label") === "Round result") {
          top = size.y - 24 - cardHeight;
          height = cardHeight;
        }
        return { top, height, bottom: top + height, left: 0, right: 0, width: 0, x: 0, y: top, toJSON: () => ({}) };
      });
    }

    async function showResult() {
      mockApi.get.mockImplementation(oneRound);
      mockApi.post.mockResolvedValue(guessResult);
      const submit = await startAndGuess();
      await userEvent.click(submit);
      return screen.findByRole("region", { name: "Round result" });
    }

    it("isolates the map, so Leaflet's controls stay under the score card", async () => {
      await showResult();
      expect(screen.getByTestId("map")).toHaveClass("isolate");
    });

    // Leaflet stacks markers by screen row: a guess just below the spot would cover it.
    it("draws the actual location above the guess", async () => {
      await showResult();
      const offsets = Object.fromEntries(
        within(screen.getByTestId("map"))
          .getAllByTestId("marker")
          .map((marker) => [marker.dataset.position, marker.dataset.zIndexOffset]),
      );
      expect(offsets["64.2,-21.8"]).toBe("1000");
      expect(Object.values(offsets).filter((offset) => offset === "0")).toHaveLength(1);
    });

    it("opens the map credit in a new tab, so a mis-tap does not leave the round", async () => {
      await showResult();
      const credit = within(screen.getByTestId("map")).getByTestId("tile-credit").dataset.attribution;
      expect(credit).toContain('href="https://www.openstreetmap.org/copyright"');
      expect(credit).toContain('target="_blank"');
      expect(credit).toContain('rel="noopener noreferrer"');
    });

    it.each([
      // Desktop: 80px around the markers, plus the card and its margin at the bottom.
      { name: "desktop", size: { x: 1200, y: 800 }, card: 200, pad: 80, bottom: 80 + 224 },
      // Phone: 10% of the short side, so most of the map stays for the markers.
      { name: "phone", size: { x: 375, y: 600 }, card: 200, pad: 38, bottom: 38 + 224 },
      // A sideways phone with a tall card: the bottom padding stops at 60% of the map.
      { name: "short", size: { x: 800, y: 300 }, card: 200, pad: 30, bottom: 180 },
      // No card measured (no layout): just the padding.
      { name: "no layout", size: { x: 200, y: 150 }, card: 0, pad: 24, bottom: 24 },
    ])("fits both markers above the score card ($name)", async ({ size, card, pad, bottom }) => {
      layout(size, card);
      await showResult();
      expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(1);
      const [bounds, options] = leaflet.map.fitBounds.mock.calls[0];
      expect(bounds.getSouthWest()).toMatchObject({ lat: 64.1, lng: -21.9 });
      expect(bounds.getNorthEast()).toMatchObject({ lat: 64.2, lng: -21.8 });
      expect(options).toEqual({ paddingTopLeft: [pad, pad], paddingBottomRight: [pad, bottom] });
    });

    it("fits again after a resize until the player moves the map", async () => {
      layout({ x: 800, y: 600 }, 200);
      await showResult();
      expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(1);

      // Turned sideways: a different size and a lower card.
      layout({ x: 600, y: 400 }, 120);
      act(() => leaflet.fire("resize"));
      expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(2);
      expect(leaflet.map.fitBounds.mock.calls[1][1]).toEqual({
        paddingTopLeft: [40, 40],
        paddingBottomRight: [40, 40 + 144],
      });

      fireEvent.pointerDown(leaflet.container!);
      act(() => leaflet.fire("resize"));
      expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(2);
    });

    it("starts the next round from the score card", async () => {
      await showResult();
      await userEvent.click(screen.getByRole("button", { name: "Next Round" }));
      expect(await screen.findByRole("button", { name: "Submit" })).toBeDisabled();
      expect(screen.queryByRole("region", { name: "Round result" })).not.toBeInTheDocument();
      // The resize listener left with the result map.
      act(() => leaflet.fire("resize"));
      expect(leaflet.map.fitBounds).toHaveBeenCalledTimes(1);
    });
  });
});
