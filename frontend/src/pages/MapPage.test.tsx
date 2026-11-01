import { render, screen, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "../api/client";
import { MapPage, MediaPreview } from "./MapPage";
import type { MediaItem, Trip } from "../types";

vi.mock("../api/client", () => ({
  api: { get: vi.fn(), getMediaUrl: vi.fn() },
}));

vi.mock("../components/map/MediaMap", () => ({
  MediaMap: ({ items }: { items: MediaItem[] }) => (
    <div data-testid="media-map" data-count={items.length} />
  ),
}));

vi.mock("../components/map/FilterPanel", () => ({
  FilterPanel: () => <div data-testid="filter-panel" />,
}));

vi.mock("../components/viewer/Panorama360", () => ({ default: () => null }));
vi.mock("../components/viewer/Video360", () => ({ default: () => null }));

const mockApi = api as unknown as { get: ReturnType<typeof vi.fn>; getMediaUrl: ReturnType<typeof vi.fn> };

function makeItem(id: string): MediaItem {
  return {
    id,
    filename: `photo-${id}.jpg`,
    media_type: "photo",
    lat: 51.5,
    lng: -0.1,
    gps_confidence: null,
    gps_source: null,
    taken_at: null,
    storage_key: `key-${id}`,
    trip_id: null,
  };
}

const sampleTrips: Trip[] = [
  { id: "t1", name: "Iceland 2024", start_date: "2024-07-01", end_date: "2024-07-14", media_count: 10, cover_key: null, user_edited: false },
];

describe("MapPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.get.mockImplementation((path: string) => {
      if (path === "/trips") return Promise.resolve(sampleTrips);
      return Promise.resolve([]);
    });
  });

  it("fetches media and trips on mount", async () => {
    render(<MapPage />);
    await waitFor(() => {
      const calls = mockApi.get.mock.calls.map((c) => c[0]);
      expect(calls).toContain("/media");
      expect(calls).toContain("/trips");
    });
  });

  it("fetches next page when first page returns 500 items", async () => {
    const page1 = Array.from({ length: 500 }, (_, i) => makeItem(`p1-${i}`));
    mockApi.get.mockImplementation((path: string, params?: Record<string, string | string[]>) => {
      if (path === "/trips") return Promise.resolve(sampleTrips);
      const offset = params?.offset ?? "0";
      if (offset === "0") return Promise.resolve(page1);
      return Promise.resolve([]);
    });

    render(<MapPage />);

    await waitFor(() => {
      const mediaCalls = mockApi.get.mock.calls.filter((c) => c[0] === "/media");
      expect(mediaCalls).toHaveLength(2);
      expect(mediaCalls[1][1]).toMatchObject({ offset: "500" });
    });
  });

  it("stops fetching when page returns fewer than 500 items", async () => {
    const partialPage = [makeItem("a"), makeItem("b"), makeItem("c")];
    mockApi.get.mockImplementation((path: string) => {
      if (path === "/trips") return Promise.resolve(sampleTrips);
      return Promise.resolve(partialPage);
    });

    render(<MapPage />);

    await waitFor(() => {
      const mediaCalls = mockApi.get.mock.calls.filter((c) => c[0] === "/media");
      expect(mediaCalls).toHaveLength(1);
    });
  });

  it("accumulates items across pages", async () => {
    const page1 = Array.from({ length: 500 }, (_, i) => makeItem(`p1-${i}`));
    const page2 = Array.from({ length: 3 }, (_, i) => makeItem(`p2-${i}`));
    mockApi.get.mockImplementation((path: string, params?: Record<string, string | string[]>) => {
      if (path === "/trips") return Promise.resolve(sampleTrips);
      const offset = params?.offset ?? "0";
      if (offset === "0") return Promise.resolve(page1);
      return Promise.resolve(page2);
    });

    render(<MapPage />);

    await waitFor(() => {
      const map = screen.getByTestId("media-map");
      expect(Number(map.getAttribute("data-count"))).toBe(503);
    });
  });

  it("passes fetched items to MediaMap", async () => {
    const items = [makeItem("x"), makeItem("y")];
    mockApi.get.mockImplementation((path: string) => {
      if (path === "/trips") return Promise.resolve(sampleTrips);
      return Promise.resolve(items);
    });

    render(<MapPage />);

    await waitFor(() => {
      expect(screen.getByTestId("media-map").getAttribute("data-count")).toBe("2");
    });
  });

  it("re-fetches from offset 0 when filters change (initial load triggers fetch)", async () => {
    render(<MapPage />);

    await waitFor(() => {
      const mediaCalls = mockApi.get.mock.calls.filter((c) => c[0] === "/media");
      expect(mediaCalls.length).toBeGreaterThanOrEqual(1);
      expect(mediaCalls[0][1]).toMatchObject({ offset: "0" });
    });
  });
});

describe("MediaPreview", () => {
  // The fullscreen 360° viewers re-load the same URL as a CORS request; a
  // no-CORS preview would poison the browser cache for it.
  it.each([
    ["photo_360", "img"],
    ["photo", "img"],
    ["video_360", "video"],
    ["video", "video"],
  ] as const)("loads %s preview in CORS mode", (mediaType, tag) => {
    const item = { ...makeItem("p"), media_type: mediaType };
    const { container } = render(
      <MediaPreview item={item} url="https://s3.example/x" onFullscreen={() => {}} />,
    );
    const el = container.querySelector(tag);
    expect(el).not.toBeNull();
    expect(el!.getAttribute("crossorigin")).toBe("anonymous");
  });
});
