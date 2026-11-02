import type { ComponentProps } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "../api/client";
import { MapPage, MediaPreview } from "./MapPage";
import type { FilterPanel } from "@/components/map/FilterPanel";
import { AuthContext } from "@/components/auth/AuthContext";
import { emptyFilters } from "@/lib/filters";
import type { Album, CurrentUser, MediaItem, Trip } from "../types";

vi.mock("../api/client", () => ({
  api: {
    get: vi.fn(),
    getMediaUrl: vi.fn(),
    getKeyframes: vi.fn(),
    listAlbums: vi.fn(),
    // The real AlbumMenu in the popup.
    mediaAlbums: vi.fn(),
    addToAlbum: vi.fn(),
    removeFromAlbum: vi.fn(),
    createAlbum: vi.fn(),
  },
}));

// The latest props MapPage rendered FilterPanel with.
const panel = vi.hoisted(() => ({ props: null as ComponentProps<typeof FilterPanel> | null }));

// One button per item stands in for clicking its marker.
vi.mock("../components/map/MediaMap", () => ({
  MediaMap: ({
    items,
    onSelect,
  }: {
    items: MediaItem[];
    onSelect: (item: MediaItem, peers: MediaItem[]) => void;
  }) => (
    <div data-testid="media-map" data-count={items.length}>
      {items.map((item) => (
        <button key={item.id} onClick={() => onSelect(item, [])}>
          select {item.id}
        </button>
      ))}
    </div>
  ),
}));

vi.mock("../components/map/FilterPanel", () => ({
  FilterPanel: (props: ComponentProps<typeof FilterPanel>) => {
    panel.props = props;
    return <div data-testid="filter-panel" />;
  },
}));

vi.mock("../components/viewer/Panorama360", () => ({ default: () => null }));
vi.mock("../components/viewer/Video360", () => ({ default: () => null }));

const mockApi = api as unknown as Record<
  | "get"
  | "getMediaUrl"
  | "getKeyframes"
  | "listAlbums"
  | "mediaAlbums"
  | "addToAlbum"
  | "removeFromAlbum"
  | "createAlbum",
  ReturnType<typeof vi.fn>
>;

const me: CurrentUser = { id: "u-me", email: "me@example.test", display_name: "Me", role: "user" };

function renderPage() {
  return render(
    <AuthContext.Provider value={me}>
      <MapPage />
    </AuthContext.Provider>
  );
}

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
    owner_id: "u-me",
    owner_name: "Me",
  };
}

const sampleTrips: Trip[] = [
  { id: "t1", name: "Iceland 2024", start_date: "2024-07-01", end_date: "2024-07-14", media_count: 10, cover_key: null, user_edited: false },
];

const sampleAlbums: Album[] = [
  {
    id: "a1",
    name: "Family",
    owner_id: "u-bob",
    owner_name: "Bob",
    is_owner: false,
    member_count: 3,
    media_count: 12,
    start_date: "2024-07-01T00:00:00Z",
    end_date: "2024-07-14T00:00:00Z",
    created_at: "2025-01-01T00:00:00Z",
  },
];

describe("MapPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    panel.props = null;
    mockApi.get.mockImplementation((path: string) => {
      if (path === "/trips") return Promise.resolve(sampleTrips);
      return Promise.resolve([]);
    });
    mockApi.listAlbums.mockResolvedValue(sampleAlbums);
    mockApi.getMediaUrl.mockResolvedValue("https://s3.example/x");
    mockApi.getKeyframes.mockResolvedValue([]);
  });

  it("passes trips and albums to FilterPanel", async () => {
    renderPage();
    await waitFor(() => {
      expect(panel.props?.trips).toEqual(sampleTrips);
      expect(panel.props?.albums).toEqual(sampleAlbums);
    });
    expect(mockApi.listAlbums).toHaveBeenCalledTimes(1);
    expect(panel.props?.filters).toEqual(emptyFilters);
    expect(panel.props?.activeCount).toBe(0);
  });

  it("still loads media when albums fail to load, and logs why", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    const err = new Error("boom");
    mockApi.listAlbums.mockRejectedValue(err);
    renderPage();
    await waitFor(() => {
      expect(mockApi.get.mock.calls.map((c) => c[0])).toContain("/media");
      expect(panel.props?.trips).toEqual(sampleTrips);
    });
    expect(panel.props?.albums).toEqual([]);
    await waitFor(() => expect(consoleError).toHaveBeenCalledWith("Failed to load albums:", err));
    consoleError.mockRestore();
  });

  it("sends no filter params with the default filters", async () => {
    renderPage();
    await waitFor(() => {
      const mediaCalls = mockApi.get.mock.calls.filter((c) => c[0] === "/media");
      expect(mediaCalls).toHaveLength(1);
      expect(mediaCalls[0][1]).toEqual({ offset: "0" });
    });
  });

  it("re-fetches with collection and owner filters from FilterPanel", async () => {
    renderPage();
    await waitFor(() => expect(panel.props).not.toBeNull());

    act(() => {
      panel.props!.onChange({ ...emptyFilters, tripIds: ["t1"], albumIds: ["a1"], owner: "shared" });
    });

    await waitFor(() => {
      const mediaCalls = mockApi.get.mock.calls.filter((c) => c[0] === "/media");
      expect(mediaCalls).toHaveLength(2);
      expect(mediaCalls[1][1]).toEqual({ trip_id: ["t1"], album_id: ["a1"], owner: "shared", offset: "0" });
    });
    // Ticked trips and albums count as one, owner ≠ all as another.
    expect(panel.props?.activeCount).toBe(2);
  });

  it("fetches media and trips on mount", async () => {
    renderPage();
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

    renderPage();

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

    renderPage();

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

    renderPage();

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

    renderPage();

    await waitFor(() => {
      expect(screen.getByTestId("media-map").getAttribute("data-count")).toBe("2");
    });
  });

  async function selectItem(item: MediaItem) {
    mockApi.get.mockImplementation((path: string) => {
      if (path === "/trips") return Promise.resolve(sampleTrips);
      return Promise.resolve([item]);
    });
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: `select ${item.id}` }));
  }

  it("shows owner-only actions for the caller's own item", async () => {
    await selectItem({ ...makeItem("own"), media_type: "photo_360" });

    expect(screen.getByRole("button", { name: "Delete media" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit GPS location" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit default view" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Albums" })).toBeInTheDocument();
    expect(screen.queryByText(/Shared by/)).not.toBeInTheDocument();
  });

  it.each([
    ["Bob", "Shared by Bob"],
    [null, "Shared by another user"],
  ])("hides owner-only actions for an item shared by %s", async (ownerName, label) => {
    await selectItem({ ...makeItem("shared"), media_type: "photo_360", owner_id: "u-bob", owner_name: ownerName });

    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete media" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit GPS location" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit default view" })).not.toBeInTheDocument();
    // The album menu stays, read-only.
    expect(screen.getByRole("button", { name: "Albums" })).toBeInTheDocument();
  });

  describe("album menu in the popup", () => {
    const mediaCalls = () => mockApi.get.mock.calls.filter((c) => c[0] === "/media");

    beforeEach(() => {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([makeItem("own")]);
      });
      mockApi.mediaAlbums.mockResolvedValue([{ id: "a1", name: "Family" }]);
      mockApi.addToAlbum.mockResolvedValue({ added: 1 });
      mockApi.removeFromAlbum.mockResolvedValue({ removed: 1 });
    });

    async function openAlbumMenu() {
      await userEvent.click(await screen.findByRole("button", { name: "select own" }));
      await userEvent.click(screen.getByRole("button", { name: "Albums" }));
      return screen.findByRole("dialog");
    }

    it("lists an album created from the popup in the filters", async () => {
      const rome: Album = { ...sampleAlbums[0], id: "a9", name: "Rome", owner_id: "u-me", owner_name: "Me", is_owner: true };
      mockApi.createAlbum.mockResolvedValue(rome);
      renderPage();
      await waitFor(() => expect(panel.props?.albums).toEqual(sampleAlbums));
      const dialog = await openAlbumMenu();

      await userEvent.click(await within(dialog).findByRole("button", { name: "New album…" }));
      await userEvent.type(within(dialog).getByRole("textbox", { name: "New album name" }), "Rome");
      await userEvent.click(within(dialog).getByRole("button", { name: "Create" }));

      await waitFor(() => expect(panel.props?.albums).toEqual([...sampleAlbums, rome]));
      await waitFor(() => expect(mockApi.addToAlbum).toHaveBeenCalledWith("a9", { mediaIds: ["own"] }));
      // No album filter is set, so the map stays as it is.
      expect(mediaCalls()).toHaveLength(1);
    });

    it("refetches the media after unticking an album the filters select", async () => {
      renderPage();
      await waitFor(() => expect(panel.props).not.toBeNull());
      act(() => panel.props!.onChange({ ...emptyFilters, albumIds: ["a1"] }));
      await waitFor(() => expect(mediaCalls()).toHaveLength(2));
      const dialog = await openAlbumMenu();

      await userEvent.click(await within(dialog).findByRole("checkbox", { name: /Family/ }));

      expect(mockApi.removeFromAlbum).toHaveBeenCalledWith("a1", { mediaIds: ["own"] });
      await waitFor(() => expect(mediaCalls()).toHaveLength(3));
      expect(mediaCalls()[2][1]).toEqual({ album_id: ["a1"], offset: "0" });
    });

    it("leaves the media alone when the changed album is not filtered on", async () => {
      renderPage();
      await waitFor(() => expect(panel.props).not.toBeNull());
      act(() => panel.props!.onChange({ ...emptyFilters, owner: "mine" }));
      await waitFor(() => expect(mediaCalls()).toHaveLength(2));
      const dialog = await openAlbumMenu();

      const box = await within(dialog).findByRole("checkbox", { name: /Family/ });
      await userEvent.click(box);

      await waitFor(() => expect(box).toBeEnabled());
      expect(mockApi.removeFromAlbum).toHaveBeenCalledWith("a1", { mediaIds: ["own"] });
      expect(mediaCalls()).toHaveLength(2);
    });
  });

  it("re-fetches from offset 0 when filters change (initial load triggers fetch)", async () => {
    renderPage();

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
