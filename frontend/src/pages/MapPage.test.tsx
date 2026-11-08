import type { ComponentProps } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api } from "../api/client";
import { MapPage, MediaPreview } from "./MapPage";
import type { FilterPanel } from "@/components/map/FilterPanel";
import { AuthContext } from "@/components/auth/AuthContext";
import { stubMediaPlayback } from "@/components/viewer/viewerTestUtils";
import { emptyFilters } from "@/lib/filters";
import { BACK_DEPTH_KEY } from "@/lib/useCloseOnBack";
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

// One button per item stands in for clicking its marker, and "select cluster" for clicking the
// first marker of a spider holding every item.
vi.mock("../components/map/MediaMap", () => ({
  MediaMap: ({
    items,
    onSelect,
    visible,
  }: {
    items: MediaItem[];
    onSelect: (item: MediaItem, peers: MediaItem[]) => void;
    visible?: boolean;
  }) => (
    <div data-testid="media-map" data-count={items.length} data-visible={String(visible)}>
      {items.map((item) => (
        <button key={item.id} onClick={() => onSelect(item, [])}>
          select {item.id}
        </button>
      ))}
      <button onClick={() => onSelect(items[0], items)}>select cluster</button>
    </div>
  ),
}));

vi.mock("../components/map/FilterPanel", () => ({
  FilterPanel: (props: ComponentProps<typeof FilterPanel>) => {
    panel.props = props;
    return <div data-testid="filter-panel" />;
  },
}));

// Stand-ins that show which viewer is open, contained or full screen, and can close it.
vi.mock("../components/viewer/Panorama360", () => ({
  default: ({ onClose, contained }: { onClose: () => void; contained?: boolean }) => (
    <div data-testid="panorama" data-contained={String(Boolean(contained))}>
      <button onClick={onClose}>Close 360 view</button>
    </div>
  ),
}));
// data-tap-to-play shows the prop as Video360 resolves it (default true). Video360.test.tsx
// checks that tapToPlay={false} leaves play and pause alone on a click or tap.
vi.mock("../components/viewer/Video360", () => ({
  default: ({ onClose, contained, tapToPlay = true }: { onClose: () => void; contained?: boolean; tapToPlay?: boolean }) => (
    <div data-testid="video360" data-contained={String(Boolean(contained))} data-tap-to-play={String(tapToPlay)}>
      <button onClick={onClose}>Close 360 view</button>
    </div>
  ),
}));

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

function page(active?: boolean) {
  return (
    <AuthContext.Provider value={me}>
      <MapPage active={active} />
    </AuthContext.Provider>
  );
}

function renderPage(active?: boolean) {
  const result = render(page(active));
  return { ...result, setActive: (a: boolean) => result.rerender(page(a)) };
}

const PAGE_SIZE = 5000;

// What every /media request carries besides the filters.
function pageParams(offset: number) {
  return { has_location: "true", limit: String(PAGE_SIZE), offset: String(offset) };
}

// How many fullscreen views hold a history entry (useCloseOnBack).
const depth = () => ((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY] as number) ?? 0;

// jsdom traverses history in setTimeout(0) steps; this lets every pending step land.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

// The phone's back gesture. Only call it over an entry of the view (check depth() first):
// without one, jsdom never fires popstate.
async function goBack() {
  await act(async () => {
    const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
    window.history.back();
    await popped;
  });
  await settle();
}

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
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
    last_added_at: null,
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
      expect(mediaCalls[0][1]).toEqual(pageParams(0));
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
      expect(mediaCalls[1][1]).toEqual({ trip_id: ["t1"], album_id: ["a1"], owner: "shared", ...pageParams(0) });
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

  it("fetches next page when first page is full", async () => {
    const page1 = Array.from({ length: PAGE_SIZE }, (_, i) => makeItem(`p1-${i}`));
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
      expect(mediaCalls[1][1]).toEqual(pageParams(PAGE_SIZE));
    });
  });

  it("stops fetching when a page is not full", async () => {
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
    const page1 = Array.from({ length: PAGE_SIZE }, (_, i) => makeItem(`p1-${i}`));
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
      expect(Number(map.getAttribute("data-count"))).toBe(PAGE_SIZE + 3);
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
      expect(mediaCalls()[2][1]).toEqual({ album_id: ["a1"], ...pageParams(0) });
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

    // In a browser focus falls back to <body> when a ticked checkbox is disabled while its request
    // runs; then the arrows reach window, where they would switch the item and close the menu.
    it("keeps the cluster arrows off the album menu, even with focus on <body>", async () => {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([makeItem("a"), makeItem("b"), makeItem("c")]);
      });
      renderPage(true);
      await userEvent.click(await screen.findByRole("button", { name: "select cluster" }));
      await userEvent.click(screen.getByRole("button", { name: "Albums" }));
      const dialog = await screen.findByRole("dialog");
      await within(dialog).findByRole("checkbox", { name: /Family/ });

      await userEvent.keyboard("{ArrowRight}");
      act(() => (document.activeElement as HTMLElement).blur());
      await userEvent.keyboard("{ArrowRight}");
      expect(screen.getByText("1 / 3")).toBeInTheDocument();
      expect(screen.getByRole("dialog")).toBeInTheDocument();

      await userEvent.keyboard("{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      await userEvent.keyboard("{ArrowRight}");
      expect(screen.getByText("2 / 3")).toBeInTheDocument();
    });
  });

  describe("while another tab is open", () => {
    const mediaCalls = () => mockApi.get.mock.calls.filter((c) => c[0] === "/media");
    const tripCalls = () => mockApi.get.mock.calls.filter((c) => c[0] === "/trips");
    const count = () => screen.getByTestId("media-map").getAttribute("data-count");

    it("keeps the markers, then reloads in the background when shown again", async () => {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([makeItem("x")]);
      });
      const { setActive } = renderPage(true);
      await waitFor(() => expect(count()).toBe("1"));
      expect(screen.getByTestId("media-map").getAttribute("data-visible")).toBe("true");

      setActive(false);
      expect(screen.getByTestId("media-map").getAttribute("data-visible")).toBe("false");
      expect(count()).toBe("1");
      expect(mediaCalls()).toHaveLength(1);

      const reload = deferred<MediaItem[]>();
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return reload.promise;
      });
      setActive(true);
      await waitFor(() => expect(mediaCalls()).toHaveLength(2));
      expect(mediaCalls()[1][1]).toEqual(pageParams(0));
      expect(tripCalls()).toHaveLength(2);
      expect(mockApi.listAlbums).toHaveBeenCalledTimes(2);
      // The old markers stay until the reload is complete.
      expect(count()).toBe("1");
      expect(screen.getByText("Loading…")).toBeInTheDocument();

      await act(async () => reload.resolve([makeItem("x"), makeItem("y")]));
      expect(count()).toBe("2");
      expect(screen.queryByText("Loading…")).not.toBeInTheDocument();
    });

    it("swaps in a multi-page reload only once its last page arrived", async () => {
      const full = Array.from({ length: PAGE_SIZE }, (_, i) => makeItem(`p-${i}`));
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([makeItem("old")]);
      });
      const { setActive } = renderPage(true);
      await waitFor(() => expect(count()).toBe("1"));

      const last = deferred<MediaItem[]>();
      mockApi.get.mockImplementation((path: string, params?: Record<string, string | string[]>) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return params?.offset === "0" ? Promise.resolve(full) : last.promise;
      });
      setActive(false);
      setActive(true);
      await waitFor(() => expect(mediaCalls()).toHaveLength(3));
      expect(count()).toBe("1");

      await act(async () => last.resolve([makeItem("new")]));
      expect(count()).toBe(String(PAGE_SIZE + 1));
    });

    it("keeps the markers when the background reload fails", async () => {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([makeItem("x")]);
      });
      const { setActive } = renderPage(true);
      await waitFor(() => expect(count()).toBe("1"));

      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.reject(new Error("GET /media failed: 500"));
      });
      setActive(false);
      setActive(true);

      expect(await screen.findByText("GET /media failed: 500")).toBeInTheDocument();
      expect(count()).toBe("1");
    });

    it("clears the map when the filters change", async () => {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([makeItem("x")]);
      });
      renderPage(true);
      await waitFor(() => expect(count()).toBe("1"));

      const filtered = deferred<MediaItem[]>();
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return filtered.promise;
      });
      act(() => panel.props!.onChange({ ...emptyFilters, owner: "mine" }));
      expect(count()).toBe("0");

      await act(async () => filtered.resolve([makeItem("y")]));
      expect(count()).toBe("1");
    });

    it("does not reload when mounted hidden or when hidden", async () => {
      const { setActive } = renderPage(false);
      await waitFor(() => expect(mediaCalls()).toHaveLength(1));
      setActive(false);
      await act(async () => {});
      expect(mediaCalls()).toHaveLength(1);
    });

    it("leaves the arrow keys alone", async () => {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([makeItem("a"), makeItem("b"), makeItem("c")]);
      });
      const { setActive } = renderPage(true);
      await userEvent.click(await screen.findByRole("button", { name: "select cluster" }));
      expect(screen.getByText("1 / 3")).toBeInTheDocument();

      await userEvent.keyboard("{ArrowRight}");
      expect(screen.getByText("2 / 3")).toBeInTheDocument();

      setActive(false);
      await userEvent.keyboard("{ArrowRight}");
      expect(screen.getByText("2 / 3")).toBeInTheDocument();
    });
  });

  describe("on a phone", () => {
    const cluster = [
      { ...makeItem("a"), taken_at: "2024-07-02T10:00:00Z", gps_source: "exif" as const, gps_confidence: 1 },
      makeItem("b"),
      makeItem("c"),
    ];

    async function selectCluster() {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve(cluster);
      });
      renderPage();
      await userEvent.click(await screen.findByRole("button", { name: "select cluster" }));
      await screen.findByRole("button", { name: "Open fullscreen" });
      return screen.getByRole("region", { name: "Selected media" });
    }

    // The order a flex column shows the children in: by their -order-N class, then source order.
    function phoneOrder(container: Element): Element[] {
      const order = (el: Element) => -Number(/(?:^| )-order-(\d+)(?: |$)/.exec(el.getAttribute("class") ?? "")?.[1] ?? 0);
      return [...container.children]
        .map((el, i) => ({ el, i }))
        .sort((x, y) => order(x.el) - order(y.el) || x.i - y.i)
        .map(({ el }) => el);
    }

    it("shows the selected item in a sheet across the bottom, and as a card from sm up", async () => {
      const sheet = await selectCluster();

      expect(sheet).toHaveClass("absolute", "inset-x-2", "bottom-2", "max-h-[55%]", "overflow-y-auto");
      expect(sheet).toHaveClass("sm:inset-x-auto", "sm:bottom-auto", "sm:right-4", "sm:top-4", "sm:w-72");
      // A phone held sideways gets the card, which must not run off the bottom either.
      expect(sheet).toHaveClass("sm:max-h-[calc(100%-2rem)]");
      expect(sheet.firstElementChild).toHaveClass("flex", "flex-col", "sm:block");
    });

    it("keeps the title row with Delete and Close stuck to the top while the panel scrolls", async () => {
      const sheet = await selectCluster();
      const titleRow = within(sheet).getByText("photo-a.jpg").parentElement!;

      expect(within(titleRow).getByRole("button", { name: "Delete media" })).toBeInTheDocument();
      expect(within(titleRow).getByRole("button", { name: "Close panel" })).toBeInTheDocument();
      // Opaque, above the content scrolling under it, and holding the panel's top padding, so
      // nothing shows above it once stuck.
      expect(titleRow).toHaveClass("sticky", "top-0", "z-10", "bg-background", "pt-3");
      expect(sheet).toHaveClass("px-3", "pb-3");
      expect(sheet).not.toHaveClass("p-3");
    });

    it("puts the details, preview and peers before the actions on a phone, upright or sideways", async () => {
      const sheet = await selectCluster();
      const column = sheet.firstElementChild!;
      // A sideways phone gets the card, but too short to reach a preview below the actions.
      expect(column).toHaveClass("sm:block", "short:flex");
      const title = within(sheet).getByText("photo-a.jpg");
      const date = within(sheet).getByText(new Date("2024-07-02T10:00:00Z").toLocaleString());
      const preview = within(sheet).getByRole("button", { name: "Open fullscreen" });
      const peers = within(sheet).getByText("1 / 3");
      const streetView = within(sheet).getByRole("link", { name: /Street View/ });
      const albums = within(sheet).getByRole("button", { name: "Albums" });

      const shown = phoneOrder(column);
      const at = (el: Element) => shown.findIndex((child) => child.contains(el));
      expect(at(title)).toBe(0);
      expect(at(date)).toBeLessThan(at(preview));
      expect(at(preview)).toBeLessThan(at(peers));
      expect(at(peers)).toBeLessThan(at(streetView));
      expect(at(streetView)).toBeLessThan(at(albums));

      // The desktop card is a block: the source order, peers between the actions and the preview.
      const dom = [...column.children];
      const inDom = (el: Element) => dom.findIndex((child) => child.contains(el));
      expect(inDom(albums)).toBeLessThan(inDom(peers));
      expect(inDom(peers)).toBeLessThan(inDom(preview));
    });

    it("gives the sheet's buttons 44 px touch targets", async () => {
      const sheet = await selectCluster();

      for (const name of ["Delete media", "Close panel", "Previous", "Next"]) {
        expect(within(sheet).getByRole("button", { name })).toHaveClass("touch:min-h-11", "touch:min-w-11");
      }
      expect(within(sheet).getByRole("button", { name: "Edit GPS location" })).toHaveClass("touch:min-h-11");
      expect(within(sheet).getByRole("link", { name: /Street View/ })).toHaveClass("touch:min-h-11");
    });

    it("keeps the Loading label beside the zoom control", async () => {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return new Promise(() => {});
      });
      renderPage();
      expect(await screen.findByText("Loading…")).toHaveClass("bottom-2.5", "left-14", "touch:left-[4.25rem]");
    });
  });

  describe("fullscreen view", () => {
    const cluster = [makeItem("a"), makeItem("b"), makeItem("c")];

    beforeEach(() => {
      mockApi.getMediaUrl.mockImplementation((id: string) => Promise.resolve(`https://s3.example/${id}`));
    });

    // An overlay left open would pop its history entry after the next test began.
    afterEach(async () => {
      cleanup();
      await settle();
    });

    async function select(items: MediaItem[], button = "select cluster") {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve(items);
      });
      renderPage();
      await userEvent.click(await screen.findByRole("button", { name: button }));
    }

    async function openFullscreen() {
      await userEvent.click(await screen.findByRole("button", { name: "Open fullscreen" }));
      await settle();
    }

    // jsdom has no layout: a 400 x 300 viewer at the origin holding an 800 x 400 photo, which
    // leaves letterboxes at y 0..50 and 250..300.
    function layoutPhoto(dialog: HTMLElement, name: string) {
      const img = within(dialog).getByRole("img", { name }) as HTMLImageElement;
      const box = img.parentElement!;
      box.getBoundingClientRect = () =>
        ({ left: 0, top: 0, x: 0, y: 0, width: 400, height: 300, right: 400, bottom: 300 }) as DOMRect;
      Object.defineProperty(img, "naturalWidth", { value: 800, configurable: true });
      Object.defineProperty(img, "naturalHeight", { value: 400, configurable: true });
      return box;
    }

    const touch = (x: number, y: number) => ({ pointerId: 1, pointerType: "touch", button: 0, clientX: x, clientY: y });

    function tap(el: Element, x: number, y: number) {
      fireEvent.pointerDown(el, touch(x, y));
      fireEvent.pointerUp(el, touch(x, y));
      fireEvent.click(el, { clientX: x, clientY: y });
    }

    function swipe(el: Element, fromX: number, toX: number) {
      fireEvent.pointerDown(el, touch(fromX, 150));
      fireEvent.pointerMove(el, touch((fromX + toX) / 2, 150));
      fireEvent.pointerMove(el, touch(toX, 150));
      fireEvent.pointerUp(el, touch(toX, 150));
    }

    it("shows a photo in a zoomable viewer with large controls", async () => {
      await select(cluster);
      await openFullscreen();
      const dialog = screen.getByRole("dialog", { name: "photo-a.jpg" });
      const img = within(dialog).getByRole("img", { name: "photo-a.jpg" });

      expect(img).toHaveAttribute("src", "https://s3.example/a");
      expect(img.parentElement).toHaveAttribute("data-zoomed", "false");
      expect(within(dialog).getByRole("button", { name: "Close fullscreen" })).toHaveClass("h-11", "w-11");
      expect(within(dialog).getByRole("button", { name: "Previous" })).toHaveClass("h-12", "w-12");
      expect(within(dialog).getByRole("button", { name: "Next" })).toHaveClass("h-12", "w-12");
      // Top centre, clear of the close button and of a video's controls.
      expect(within(dialog).getByText("1 / 3")).toHaveClass("top-4", "left-1/2", "pointer-events-none");
    });

    it("hides the page behind it completely on touch, and dims it with a mouse", async () => {
      await select(cluster);
      await openFullscreen();

      expect(screen.getByRole("dialog", { name: "photo-a.jpg" })).toHaveClass("bg-black/80", "touch:bg-black");
    });

    it("closes on a tap beside the photo, but not on one on it", async () => {
      await select(cluster);
      await openFullscreen();
      const box = layoutPhoto(screen.getByRole("dialog"), "photo-a.jpg");

      tap(box, 200, 150);
      // A single tap on the photo waits out the double-tap window, then does nothing.
      await act(() => new Promise((resolve) => setTimeout(resolve, 350)));
      expect(screen.getByRole("dialog")).toBeInTheDocument();

      tap(box, 200, 20);
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.getByRole("region", { name: "Selected media" })).toBeInTheDocument();
    });

    it("steps through the cluster with a swipe", async () => {
      await select(cluster);
      await openFullscreen();
      const dialog = screen.getByRole("dialog");

      swipe(layoutPhoto(dialog, "photo-a.jpg"), 300, 100);
      expect(await within(dialog).findByRole("img", { name: "photo-b.jpg" })).toBeInTheDocument();
      expect(within(dialog).getByText("2 / 3")).toBeInTheDocument();

      swipe(layoutPhoto(dialog, "photo-b.jpg"), 100, 300);
      expect(await within(dialog).findByRole("img", { name: "photo-a.jpg" })).toBeInTheDocument();
      expect(within(dialog).getByText("1 / 3")).toBeInTheDocument();
    });

    it("stays open while the next item's URL loads, and says when it fails", async () => {
      await select(cluster);
      await openFullscreen();
      const dialog = screen.getByRole("dialog");
      let resolveB!: (url: string) => void;
      mockApi.getMediaUrl.mockImplementation((id: string) =>
        id === "b"
          ? new Promise<string>((resolve) => { resolveB = resolve; })
          : Promise.reject(new Error("GET /media/c/url failed: 404")),
      );

      await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
      expect(screen.getByRole("dialog")).toBe(dialog);
      expect(within(dialog).getByText("Loading media…")).toBeInTheDocument();
      await act(async () => resolveB("https://s3.example/b"));
      expect(within(dialog).getByRole("img", { name: "photo-b.jpg" })).toBeInTheDocument();

      await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
      expect(await within(dialog).findByText("Failed to load media")).toBeInTheDocument();
    });

    it("keeps its keyboard shortcuts, stepping once per arrow key", async () => {
      await select(cluster);
      await openFullscreen();
      const calls = mockApi.getMediaUrl.mock.calls.length;

      await userEvent.keyboard("{ArrowRight}");
      expect(await within(screen.getByRole("dialog")).findByRole("img", { name: "photo-b.jpg" })).toBeInTheDocument();
      // The page's own arrow keys stand down while the view handles them.
      expect(mockApi.getMediaUrl.mock.calls.length).toBe(calls + 1);

      await userEvent.keyboard("{ArrowLeft}");
      expect(await within(screen.getByRole("dialog")).findByRole("img", { name: "photo-a.jpg" })).toBeInTheDocument();
      expect(mockApi.getMediaUrl.mock.calls.length).toBe(calls + 2);

      await userEvent.keyboard("{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });

    it("closes from its close button and takes its history entry along", async () => {
      await select(cluster);
      const before = depth();
      await openFullscreen();
      expect(depth()).toBe(before + 1);

      await userEvent.click(screen.getByRole("button", { name: "Close fullscreen" }));
      await settle();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(depth()).toBe(before);
    });

    it("closes on the back gesture, leaving the item selected", async () => {
      await select(cluster);
      const before = depth();
      await openFullscreen();
      expect(depth()).toBe(before + 1);

      await goBack();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(screen.getByRole("region", { name: "Selected media" })).toBeInTheDocument();
    });

    it("fits a video into the visible height and closes on a click beside it", async () => {
      await select([{ ...makeItem("v"), media_type: "video" }], "select v");
      await openFullscreen();
      const dialog = screen.getByRole("dialog");
      const video = dialog.querySelector("video")!;

      expect(video).toHaveClass("max-h-[90vh]", "supports-[height:100dvh]:max-h-[90dvh]", "max-w-[90vw]");
      await userEvent.click(video);
      expect(screen.getByRole("dialog")).toBeInTheDocument();
      await userEvent.click(dialog);
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });

    it("plays and pauses a video on Space and mutes it on M, with focus on it or not", async () => {
      const { play, pause } = stubMediaPlayback();
      await select([{ ...makeItem("v"), media_type: "video" }], "select v");
      await openFullscreen();
      const video = screen.getByRole("dialog").querySelector("video")!;

      fireEvent.keyDown(video, { code: "Space", key: " " });
      expect(play).toHaveBeenCalledOnce();
      fireEvent.keyDown(document.body, { code: "Space", key: " " });
      expect(pause).toHaveBeenCalledOnce();
      expect(play).toHaveBeenCalledOnce();

      fireEvent.keyDown(video, { key: "m" });
      expect(video.muted).toBe(true);
      fireEvent.keyDown(document.body, { key: "m" });
      expect(video.muted).toBe(false);
      play.mockRestore();
      pause.mockRestore();
    });

    it.each([
      ["photo_360", "panorama"],
      ["video_360", "video360"],
    ] as const)("leaves a %s view on the back gesture", async (mediaType, testId) => {
      await select([{ ...makeItem("s"), media_type: mediaType }], "select s");
      const before = depth();
      await openFullscreen();
      expect(screen.getByTestId(testId)).toHaveAttribute("data-contained", "false");
      expect(depth()).toBe(before + 1);

      await goBack();
      expect(screen.queryByTestId(testId)).not.toBeInTheDocument();
      expect(depth()).toBe(before);
    });
  });

  describe("default view editor", () => {
    afterEach(async () => {
      cleanup();
      await settle();
    });

    async function openEditor(mediaType: "photo_360" | "video_360" = "photo_360") {
      mockApi.get.mockImplementation((path: string) => {
        if (path === "/trips") return Promise.resolve(sampleTrips);
        return Promise.resolve([{ ...makeItem("s"), media_type: mediaType }]);
      });
      renderPage();
      await userEvent.click(await screen.findByRole("button", { name: "select s" }));
      await screen.findByRole("button", { name: "Open fullscreen" });
      await userEvent.click(screen.getByRole("button", { name: "Edit default view" }));
      return screen.getByRole("button", { name: "Close view editor" });
    }

    it("stacks the viewer over the controls below md, side by side from md or sideways", async () => {
      await openEditor();
      const controls = screen.getByRole("button", { name: "Set current view as default" }).parentElement!.parentElement!;

      expect(screen.getByTestId("panorama")).toHaveAttribute("data-contained", "true");
      expect(controls.parentElement).toHaveClass("flex-col", "md:flex-row", "short:flex-row");
      expect(controls).toHaveClass("max-h-[40%]", "overflow-y-auto", "md:max-h-none", "md:w-64", "short:max-h-none");
      expect(screen.getByRole("button", { name: "Set current view as default" })).toHaveClass("touch:min-h-11");
    });

    // In the editor a click, or a 1-4 px nudge of the view, would restart playback and change
    // the time the next keyframe stores.
    it("turns off tap-to-play for a 360° video in the editor, but not in the fullscreen view", async () => {
      await openEditor("video_360");
      expect(screen.getByTestId("video360")).toHaveAttribute("data-contained", "true");
      expect(screen.getByTestId("video360")).toHaveAttribute("data-tap-to-play", "false");

      await userEvent.click(screen.getByRole("button", { name: "Close view editor" }));
      await settle();
      await userEvent.click(screen.getByRole("button", { name: "Open fullscreen" }));
      await settle();
      expect(screen.getByTestId("video360")).toHaveAttribute("data-contained", "false");
      expect(screen.getByTestId("video360")).toHaveAttribute("data-tap-to-play", "true");
    });

    it("closes from a 44 px close button", async () => {
      const close = await openEditor();
      expect(close).toHaveClass("touch:h-11", "touch:w-11");

      await userEvent.click(close);
      expect(screen.queryByRole("button", { name: "Close view editor" })).not.toBeInTheDocument();
    });

    it("closes on the back gesture", async () => {
      const before = depth();
      await openEditor();
      await settle();
      expect(depth()).toBe(before + 1);

      await goBack();
      expect(screen.queryByRole("button", { name: "Close view editor" })).not.toBeInTheDocument();
      expect(depth()).toBe(before);
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

  it.each(["photo", "video"] as const)("shows a %s preview taller on a phone, smaller held sideways", (mediaType) => {
    const { container } = render(
      <MediaPreview item={{ ...makeItem("p"), media_type: mediaType }} url="https://s3.example/x" onFullscreen={() => {}} />,
    );
    expect(container.querySelector(mediaType === "photo" ? "img" : "video")).toHaveClass(
      // vh first: without dvh support the dvh class alone would leave no cap.
      "max-h-[30vh]",
      "supports-[height:100dvh]:max-h-[30dvh]",
      "sm:max-h-40",
      "short:max-h-24",
    );
  });
});
