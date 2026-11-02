import type { ComponentProps, ReactNode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
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

vi.mock("react-leaflet", async () => {
  const { forwardRef } = await import("react");
  return {
    MapContainer: ({ children }: { children: ReactNode }) => <div>{children}</div>,
    TileLayer: () => null,
    Marker: forwardRef(() => null),
    Polyline: () => null,
    useMap: () => ({ invalidateSize: vi.fn(), fitBounds: vi.fn() }),
    useMapEvents: (handlers: { click?: MapClick }) => {
      if (handlers.click) guessMap.click = handlers.click;
      return null;
    },
  };
});

const mockApi = api as unknown as {
  get: ReturnType<typeof vi.fn>;
  post: ReturnType<typeof vi.fn>;
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
  },
];

function noRounds(path: string) {
  if (path === "/trips") return Promise.resolve([]);
  return Promise.reject(new ApiError("GET /game/round failed: 404", 404));
}

const round = { id: "m1", media_type: "photo", url: "https://s3.example/m1", max_dist_km: 500 };

function oneRound(path: string) {
  if (path === "/trips") return Promise.resolve([]);
  return Promise.resolve(round);
}

// Starts a round and places a guess on the map, leaving Submit enabled.
async function startAndGuess() {
  render(<GamePage />);
  await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
  const submit = await screen.findByRole("button", { name: "Submit" });
  act(() => guessMap.click!({ latlng: { lat: 64.1, lng: -21.9 } }));
  expect(submit).toBeEnabled();
  return submit;
}

describe("GamePage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    panel.props = null;
    guessMap.click = null;
    mockApi.listAlbums.mockResolvedValue(sampleAlbums);
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
    mockApi.post.mockResolvedValue({
      actual_lat: 64.2,
      actual_lng: -21.8,
      actual_date: null,
      distance_km: 12.3,
      date_diff_days: null,
      location_score: 4000,
      time_score: null,
      total_score: 4000,
    });
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
    mockApi.post.mockResolvedValueOnce({
      actual_lat: 64.2,
      actual_lng: -21.8,
      actual_date: null,
      distance_km: 12.3,
      date_diff_days: null,
      location_score: 4000,
      time_score: null,
      total_score: 4000,
    });
    await userEvent.click(screen.getByRole("button", { name: "Submit" }));
    expect(await screen.findByText("4,000 pts")).toBeInTheDocument();
  });
});
