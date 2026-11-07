import type { ComponentProps, ReactNode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { MediaGalleryOverlay } from "@/components/MediaGalleryOverlay";
import { AuthContext } from "@/components/auth/AuthContext";
import { BACK_DEPTH_KEY } from "@/lib/useCloseOnBack";
import type { Album, CurrentUser, MediaItem } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    api: {
      get: vi.fn(),
      getMediaUrl: vi.fn(),
      getKeyframes: vi.fn(),
      delete: vi.fn(),
      // The real AlbumMenu in the header.
      listAlbums: vi.fn(),
      mediaAlbums: vi.fn(),
      addToAlbum: vi.fn(),
      removeFromAlbum: vi.fn(),
    },
  };
});

vi.mock("react-leaflet", () => ({
  MapContainer: ({ children }: { children: ReactNode }) => <div data-testid="map">{children}</div>,
  TileLayer: () => null,
  // A button per marker, so a test can click one; it carries the marker's stacking offset.
  Marker: ({
    position,
    eventHandlers,
    zIndexOffset,
  }: {
    position: [number, number];
    eventHandlers?: { click?: () => void };
    zIndexOffset?: number;
  }) => (
    <button
      aria-label={`Marker at ${position.join(",")}`}
      data-z-index-offset={zIndexOffset ?? 0}
      onClick={() => eventHandlers?.click?.()}
    />
  ),
  useMap: () => leafletMap,
  useMapEvents: () => null,
}));

// The one Leaflet map every useMap() gets (the gallery's map controller, the mini map's size sync
// and the location editor). Like Leaflet, it caches a size that only invalidateSize re-measures,
// and it fires "resize" when that size changes.
const leafletMap = vi.hoisted(() => {
  const onResize = new Set<() => void>();
  const shownSize = { x: 288, y: 192 };
  const map = {
    // Leaflet's cached size, and what the container measures right now.
    size: { ...shownSize },
    measured: { ...shownSize },
    flyTo: vi.fn(),
    setView: vi.fn(),
    fitBounds: vi.fn(),
    getZoom: () => 13,
    getSize: () => ({ ...map.size }),
    getContainer: () => document.createElement("div"),
    invalidateSize: vi.fn(() => {
      if (map.size.x === map.measured.x && map.size.y === map.measured.y) return;
      map.size = { ...map.measured };
      for (const fn of [...onResize]) fn();
    }),
    on: (type: string, fn: () => void) => {
      if (type === "resize") onResize.add(fn);
    },
    off: (type: string, fn: () => void) => {
      if (type === "resize") onResize.delete(fn);
    },
    reset() {
      onResize.clear();
      map.size = { ...shownSize };
      map.measured = { ...shownSize };
    },
  };
  return map;
});

vi.mock("leaflet", () => ({
  default: { divIcon: vi.fn(() => ({})), latLngBounds: vi.fn(() => ({})) },
}));

// Every URL a 360° viewer was rendered with, in order: the real ones build a WebGL renderer and
// load the texture for each.
const viewerUrls = vi.hoisted(() => [] as string[]);
vi.mock("@/components/viewer/Panorama360", () => ({
  default: ({ url }: { url: string }) => {
    viewerUrls.push(url);
    return <div data-testid="panorama" data-url={url} />;
  },
}));
vi.mock("@/components/viewer/Video360", () => ({
  default: ({ url }: { url: string }) => {
    viewerUrls.push(url);
    return <div data-testid="video360" data-url={url} />;
  },
}));

const mockApi = api as unknown as Record<
  "get" | "getMediaUrl" | "getKeyframes" | "delete" | "listAlbums" | "mediaAlbums" | "addToAlbum" | "removeFromAlbum",
  ReturnType<typeof vi.fn>
>;

// jsdom traverses history in zero-delay timer steps; enough rounds let any traversal a previous
// test's unmount started land before this one looks at history.
async function settleHistory() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

// A one-finger horizontal swipe over the photo (jsdom has no layout: client = local coordinates).
function swipe(el: Element, fromX: number, toX: number) {
  const at = (x: number) => ({ pointerId: 1, pointerType: "touch", button: 0, clientX: x, clientY: 100 });
  fireEvent.pointerDown(el, at(fromX));
  fireEvent.pointerMove(el, at((fromX + toX) / 2));
  fireEvent.pointerMove(el, at(toX));
  fireEvent.pointerUp(el, at(toX));
}

// A promise the test settles by hand.
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const iceland: Album = {
  id: "a1",
  name: "Iceland",
  owner_id: "u-me",
  owner_name: "Me",
  is_owner: true,
  member_count: 2,
  media_count: 2,
  start_date: null,
  end_date: null,
  created_at: "2025-01-01T00:00:00Z",
  last_added_at: null,
};
const family: Album = { ...iceland, id: "a2", name: "Family" };

const me: CurrentUser = { id: "u-me", email: "me@example.test", display_name: "Me", role: "user" };

function makeItem(id: string, overrides: Partial<MediaItem> = {}): MediaItem {
  return {
    id,
    filename: `${id}.jpg`,
    media_type: "photo",
    lat: 64.1,
    lng: -21.9,
    gps_confidence: 1,
    gps_source: "exif",
    taken_at: null,
    storage_key: `media/u-me/${id}.jpg`,
    trip_id: null,
    owner_id: "u-me",
    owner_name: "Me",
    ...overrides,
  };
}

const own = makeItem("own", { taken_at: "2025-06-01T10:00:00Z" });
const shared = makeItem("shared", { taken_at: "2025-06-02T10:00:00Z", owner_id: "u-bob", owner_name: "Bob" });

type Props = ComponentProps<typeof MediaGalleryOverlay>;

function renderOverlay(props: Partial<Props> = {}) {
  const all: Props = { title: "Iceland", params: { album_id: "a1" }, onClose: vi.fn(), ...props };
  const view = render(
    <AuthContext.Provider value={me}>
      <MediaGalleryOverlay {...all} />
    </AuthContext.Provider>
  );
  return { ...view, props: all };
}

describe("MediaGalleryOverlay", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    leafletMap.reset();
    viewerUrls.length = 0;
    mockApi.get.mockResolvedValue([shared, own]);
    mockApi.getMediaUrl.mockResolvedValue("https://s3.example/x");
    mockApi.getKeyframes.mockResolvedValue([]);
  });

  it("fetches media with the given params and shows the title", async () => {
    renderOverlay({ subtitle: "by Bob" });

    expect(await screen.findByText("1 / 2")).toBeInTheDocument();
    expect(mockApi.get).toHaveBeenCalledWith("/media", { album_id: "a1", offset: "0" });
    expect(screen.getByText("Iceland")).toBeInTheDocument();
    expect(screen.getByText("by Bob")).toBeInTheDocument();
  });

  it("does not refetch when re-rendered with equal params", async () => {
    const { rerender, props } = renderOverlay();
    await screen.findByText("1 / 2");

    rerender(
      <AuthContext.Provider value={me}>
        <MediaGalleryOverlay {...props} params={{ album_id: "a1" }} />
      </AuthContext.Provider>
    );
    expect(mockApi.get).toHaveBeenCalledTimes(1);
  });

  it("shows owner-only actions for the caller's own item and hides them for a shared one", async () => {
    renderOverlay();
    // Sorted by taken_at: the own item comes first.
    await screen.findByText("1 / 2");
    expect(screen.getByRole("button", { name: "Delete media" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit GPS location" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Albums" })).toBeInTheDocument();
    expect(screen.queryByText(/Shared by/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("Shared by Bob")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete media" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit GPS location" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Albums" })).toBeInTheDocument();
  });

  it.each([
    ["allowed", true, true, true],
    ["not allowed", false, true, false],
    ["without a remove handler", true, false, false],
  ])("remove-from-album action when %s", async (_name, allowed, withHandler, shown) => {
    const canRemoveFromAlbum = vi.fn(() => allowed);
    renderOverlay({
      canRemoveFromAlbum,
      onRemoveFromAlbum: withHandler ? vi.fn().mockResolvedValue(undefined) : undefined,
    });
    await screen.findByText("1 / 2");

    expect(!!screen.queryByRole("button", { name: "Remove from album" })).toBe(shown);
  });

  it("asks canRemoveFromAlbum per item", async () => {
    renderOverlay({
      canRemoveFromAlbum: (item) => item.owner_id === me.id,
      onRemoveFromAlbum: vi.fn().mockResolvedValue(undefined),
    });
    await screen.findByText("1 / 2");
    expect(screen.getByRole("button", { name: "Remove from album" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByText("2 / 2");
    expect(screen.queryByRole("button", { name: "Remove from album" })).not.toBeInTheDocument();
  });

  it("drops the item once it is removed from the album", async () => {
    const onRemoveFromAlbum = vi.fn().mockResolvedValue(undefined);
    renderOverlay({ canRemoveFromAlbum: () => true, onRemoveFromAlbum });
    await screen.findByText("1 / 2");

    await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));

    expect(onRemoveFromAlbum).toHaveBeenCalledWith(own);
    expect(await screen.findByText("1 / 1")).toBeInTheDocument();
    expect(screen.getByText("Shared by Bob")).toBeInTheDocument();
  });

  it.each([
    ["a server error", new ApiError("POST /albums/a1/media/remove failed: 500", 500), 'Could not remove "own.jpg" from the album'],
    [
      "an owner-only action",
      new ApiError("POST /albums/a1/media/remove failed: 403", 403),
      'Could not remove "own.jpg" from the album: only the album owner can do that',
    ],
    ["a network error", new TypeError("Failed to fetch"), 'Could not remove "own.jpg" from the album'],
  ])("keeps the item and says why when removing it from the album fails with %s", async (_name, err, message) => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const onRemoveFromAlbum = vi.fn().mockRejectedValue(err);
    renderOverlay({ canRemoveFromAlbum: () => true, onRemoveFromAlbum });
    await screen.findByText("1 / 2");

    await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(onRemoveFromAlbum).toHaveBeenCalledTimes(1);
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove from album" })).toBeEnabled();
  });

  it("clears the action error when moving to another item", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    renderOverlay({ canRemoveFromAlbum: () => true, onRemoveFromAlbum: vi.fn().mockRejectedValue(new Error("boom")) });
    await screen.findByText("1 / 2");
    await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));
    await screen.findByRole("alert");

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("2 / 2")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("disables remove and delete for the item while its removal is in flight", async () => {
    const removal = deferred<void>();
    renderOverlay({ canRemoveFromAlbum: () => true, onRemoveFromAlbum: vi.fn(() => removal.promise) });
    await screen.findByText("1 / 2");

    await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));
    expect(screen.getByRole("button", { name: "Remove from album" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete media" })).toBeDisabled();

    await act(async () => removal.resolve());
    expect(await screen.findByText("1 / 1")).toBeInTheDocument();
  });

  it("keeps the item and says why when deleting it fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const onMediaDeleted = vi.fn();
    const request = deferred<void>();
    mockApi.delete.mockReturnValue(request.promise);
    renderOverlay({ onMediaDeleted });
    await screen.findByText("1 / 2");

    await userEvent.click(screen.getByRole("button", { name: "Delete media" }));
    expect(screen.getByRole("button", { name: "Delete media" })).toBeDisabled();
    await act(async () => request.reject(new ApiError("DELETE /media/own failed: 500", 500)));

    expect(await screen.findByRole("alert")).toHaveTextContent('Could not delete "own.jpg"');
    expect(mockApi.delete).toHaveBeenCalledWith("/media/own");
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete media" })).toBeEnabled();
    expect(onMediaDeleted).not.toHaveBeenCalled();
  });

  it("drops an item that was already deleted elsewhere", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const onMediaDeleted = vi.fn();
    mockApi.delete.mockRejectedValue(new ApiError("DELETE /media/own failed: 404", 404));
    renderOverlay({ onMediaDeleted });
    await screen.findByText("1 / 2");

    await userEvent.click(screen.getByRole("button", { name: "Delete media" }));

    expect(await screen.findByText("1 / 1")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(onMediaDeleted).toHaveBeenCalledTimes(1);
  });

  it("closes once the last item is removed", async () => {
    mockApi.get.mockResolvedValue([own]);
    const { props } = renderOverlay({ canRemoveFromAlbum: () => true, onRemoveFromAlbum: vi.fn().mockResolvedValue(undefined) });
    await screen.findByText("1 / 1");

    await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1));
  });

  it("explains a video whose picture the browser cannot decode, for that item only", async () => {
    mockApi.get.mockResolvedValue([
      makeItem("hevc", { media_type: "video", taken_at: "2025-06-01T10:00:00Z" }),
      makeItem("next", { media_type: "video", taken_at: "2025-06-02T10:00:00Z" }),
    ]);
    mockApi.getMediaUrl.mockImplementation((id: string) => Promise.resolve(`https://s3.example/${id}`));
    const { container } = renderOverlay();
    await screen.findByText("1 / 2");
    await waitFor(() => expect(container.querySelector("video")).toHaveAttribute("src", "https://s3.example/hevc"));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    // What Chromium on Linux does without an HEVC decoder: audio data, a 0×0 picture.
    const video = container.querySelector("video")!;
    Object.defineProperty(video, "readyState", { value: 2 });
    act(() => {
      video.dispatchEvent(new Event("loadeddata"));
    });
    expect(screen.getByRole("link", { name: "How to enable HEVC playback" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(container.querySelector("video")).toHaveAttribute("src", "https://s3.example/next"));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not close an album that was empty from the start", async () => {
    mockApi.get.mockResolvedValue([]);
    const { props } = renderOverlay();

    expect(await screen.findByText("No media here")).toBeInTheDocument();
    expect(props.onClose).not.toHaveBeenCalled();
  });

  it.each([
    ["in order", [0, 1]],
    ["out of order", [1, 0]],
  ])("keeps both items out when two removals overlap and finish %s", async (_name, order) => {
    const items = [
      makeItem("first", { taken_at: "2025-06-01T10:00:00Z" }),
      makeItem("second", { taken_at: "2025-06-02T10:00:00Z" }),
      makeItem("third", { taken_at: "2025-06-03T10:00:00Z" }),
    ];
    mockApi.get.mockResolvedValue(items);
    const removals = [deferred<void>(), deferred<void>()];
    const onRemoveFromAlbum = vi.fn((item: MediaItem) => removals[item.id === "first" ? 0 : 1].promise);
    renderOverlay({ canRemoveFromAlbum: () => true, onRemoveFromAlbum });
    await screen.findByText("1 / 3");

    // Remove the first item, move on, and remove the second before either answers.
    await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByText("2 / 3");
    await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));
    expect(onRemoveFromAlbum.mock.calls.map(([item]) => item.id)).toEqual(["first", "second"]);

    for (const i of order) await act(async () => removals[i].resolve());

    expect(await screen.findByText("1 / 1")).toBeInTheDocument();
    expect(screen.getByText("· third.jpg")).toBeInTheDocument();
  });

  it("closes from the close button and from Escape", async () => {
    const { props } = renderOverlay();
    await screen.findByText("1 / 2");

    await userEvent.click(screen.getByRole("button", { name: "Close gallery" }));
    expect(props.onClose).toHaveBeenCalledTimes(1);

    await userEvent.keyboard("{Escape}");
    expect(props.onClose).toHaveBeenCalledTimes(2);
  });

  it("closes when the browser goes back", async () => {
    await settleHistory();
    const { props } = renderOverlay();
    await screen.findByText("1 / 2");
    await waitFor(() => expect((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY]).toBeGreaterThan(0));

    await act(async () => {
      const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
      window.history.back();
      await popped;
    });

    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  it("swipes between photos", async () => {
    mockApi.getMediaUrl.mockImplementation((id: string) => Promise.resolve(`https://s3.example/${id}`));
    renderOverlay();
    const photo = await screen.findByRole("img", { name: "own.jpg" });

    // Right to left is the next photo.
    swipe(photo.parentElement!, 300, 100);
    expect(await screen.findByRole("img", { name: "shared.jpg" })).toBeInTheDocument();
    expect(screen.getByText("2 / 2")).toBeInTheDocument();

    swipe(screen.getByRole("img", { name: "shared.jpg" }).parentElement!, 100, 300);
    expect(await screen.findByRole("img", { name: "own.jpg" })).toBeInTheDocument();
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
  });

  it("shows the item whose marker is clicked", async () => {
    mockApi.get.mockResolvedValue([own, { ...shared, lat: 48.2, lng: 16.4 }]);
    renderOverlay();
    await screen.findByText("1 / 2");

    await userEvent.click(screen.getByRole("button", { name: "Marker at 48.2,16.4" }));

    expect(await screen.findByText("2 / 2")).toBeInTheDocument();
    expect(screen.getByText("Shared by Bob")).toBeInTheDocument();
  });

  // Leaflet stacks markers by screen row, so without an offset a marker below would cover it.
  it("lifts the current item's marker above the others", async () => {
    mockApi.get.mockResolvedValue([own, { ...shared, lat: 48.2, lng: 16.4 }]);
    renderOverlay();
    await screen.findByText("1 / 2");
    const offset = (name: string) => screen.getByRole("button", { name }).getAttribute("data-z-index-offset");
    expect(offset("Marker at 64.1,-21.9")).toBe("1000");
    expect(offset("Marker at 48.2,16.4")).toBe("0");

    await userEvent.click(screen.getByRole("button", { name: "Marker at 48.2,16.4" }));
    expect(await screen.findByText("2 / 2")).toBeInTheDocument();
    expect(offset("Marker at 64.1,-21.9")).toBe("0");
    expect(offset("Marker at 48.2,16.4")).toBe("1000");
  });

  it("shows no map when no item has a location", async () => {
    mockApi.get.mockResolvedValue([
      makeItem("a", { lat: null, lng: null, taken_at: "2025-06-01T10:00:00Z" }),
      makeItem("b", { lat: null, lng: null, taken_at: "2025-06-02T10:00:00Z" }),
    ]);
    renderOverlay();
    await screen.findByText("1 / 2");

    expect(screen.queryByTestId("map")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Hide map" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeInTheDocument();
  });

  it("moves the arrows beside the position label while the map is large", async () => {
    renderOverlay();
    await screen.findByText("1 / 2");
    // At the screen edges, the arrows are left out at either end of the list.
    expect(screen.queryByRole("button", { name: "Previous" })).not.toBeInTheDocument();

    // A tap: user-event's mouse would first hover-expand the map, which turns this button into
    // "Keep map large" under the pointer (the next test covers that path).
    fireEvent.click(screen.getByRole("button", { name: "Enlarge map" }));

    // In the bottom row they stay, disabled at the ends, so the label does not jump.
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("2 / 2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Previous" }));
    expect(await screen.findByText("1 / 2")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Shrink map" }));
    expect(screen.queryByRole("button", { name: "Previous" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeEnabled();
  });

  it("moves the arrows beside the label when a mouse keeps the hover-enlarged map large", async () => {
    const { container } = renderOverlay();
    await screen.findByText("1 / 2");
    const map = container.querySelector("[data-size]")!;

    // Hovering only enlarges the map for a moment: the arrows stay at the screen edges.
    fireEvent.pointerEnter(map, { pointerType: "mouse" });
    expect(map).toHaveAttribute("data-size", "large");
    const sideNext = screen.getByRole("button", { name: "Next" });
    expect(sideNext).toHaveClass("absolute");

    fireEvent.click(screen.getByRole("button", { name: "Keep map large" }));
    fireEvent.pointerLeave(map, { pointerType: "mouse" });
    expect(map).toHaveAttribute("data-size", "large");
    const label = screen.getByText("1 / 2").closest(".items-center") as HTMLElement;
    expect(within(label).getByRole("button", { name: "Previous" })).toBeDisabled();
    expect(within(label).getByRole("button", { name: "Next" })).toBeEnabled();
  });

  it("keeps the chosen map size when the list reloads", async () => {
    const { rerender, props, container } = renderOverlay();
    await screen.findByText("1 / 2");
    fireEvent.click(screen.getByRole("button", { name: "Hide map" }));
    expect(container.querySelector("[data-size]")).toHaveAttribute("data-size", "hidden");

    rerender(
      <AuthContext.Provider value={me}>
        <MediaGalleryOverlay {...props} params={{ album_id: "a2" }} />
      </AuthContext.Provider>
    );
    await waitFor(() => expect(mockApi.get).toHaveBeenCalledTimes(2));
    await screen.findByText("1 / 2");

    expect(container.querySelector("[data-size]")).toHaveAttribute("data-size", "hidden");
    expect(screen.getByRole("button", { name: "Show map" })).toBeInTheDocument();
  });

  describe("mini map view", () => {
    const far = { ...shared, lat: 48.2, lng: 16.4 };

    it("flies to each item while the map has a size", async () => {
      mockApi.get.mockResolvedValue([own, far]);
      renderOverlay();
      await screen.findByText("1 / 2");
      expect(leafletMap.flyTo).toHaveBeenLastCalledWith([64.1, -21.9], 13, expect.anything());

      await userEvent.keyboard("{ArrowRight}");
      expect(await screen.findByText("2 / 2")).toBeInTheDocument();
      expect(leafletMap.flyTo).toHaveBeenLastCalledWith([48.2, 16.4], 13, expect.anything());
    });

    // In a browser flyTo on a 0×0 map throws (NaN maths) inside the effect, which blanked the app.
    it("does not fly a map hidden through a window resize, and shows the latest item once it is back", async () => {
      mockApi.get.mockResolvedValue([own, far]);
      renderOverlay();
      await screen.findByText("1 / 2");
      fireEvent.click(screen.getByRole("button", { name: "Hide map" }));
      // Leaflet's own window-resize handler measures the display: none container as 0×0.
      leafletMap.measured = { x: 0, y: 0 };
      act(() => {
        leafletMap.invalidateSize();
      });
      leafletMap.flyTo.mockClear();

      await userEvent.keyboard("{ArrowRight}");
      expect(await screen.findByText("2 / 2")).toBeInTheDocument();
      expect(leafletMap.flyTo).not.toHaveBeenCalled();
      expect(leafletMap.setView).not.toHaveBeenCalled();

      // Showing the map re-measures it; the view jumps to the current item without animating.
      leafletMap.measured = { x: 288, y: 192 };
      fireEvent.click(screen.getByRole("button", { name: "Show map" }));
      expect(leafletMap.setView).toHaveBeenCalledTimes(1);
      expect(leafletMap.setView).toHaveBeenCalledWith([48.2, 16.4], 13, { animate: false });
      expect(leafletMap.flyTo).not.toHaveBeenCalled();

      // Once only, and the next item flies again.
      leafletMap.measured = { x: 576, y: 384 };
      act(() => {
        leafletMap.invalidateSize();
      });
      expect(leafletMap.setView).toHaveBeenCalledTimes(1);
      await userEvent.keyboard("{ArrowLeft}");
      expect(await screen.findByText("1 / 2")).toBeInTheDocument();
      expect(leafletMap.flyTo).toHaveBeenCalledWith([64.1, -21.9], 13, expect.anything());
    });

    // fitBounds on a 0×0 map sets the zoom to Infinity, which breaks every later move.
    it("fits a map that has no size yet to the items only once it is measured", async () => {
      leafletMap.size = { x: 0, y: 0 };
      leafletMap.measured = { x: 0, y: 0 };
      mockApi.get.mockResolvedValue([
        makeItem("nogps", { lat: null, lng: null, taken_at: "2025-05-01T10:00:00Z" }),
        far,
      ]);
      renderOverlay();
      await screen.findByText("1 / 2");
      expect(leafletMap.fitBounds).not.toHaveBeenCalled();

      leafletMap.measured = { x: 288, y: 192 };
      act(() => {
        leafletMap.invalidateSize();
      });
      expect(leafletMap.fitBounds).toHaveBeenCalledTimes(1);
      expect(leafletMap.fitBounds).toHaveBeenCalledWith(expect.anything(), { padding: [40, 40], animate: false });
      expect(leafletMap.flyTo).not.toHaveBeenCalled();
    });
  });

  describe("location editor", () => {
    const second = makeItem("second", { lat: 48.2, lng: 16.4, taken_at: "2025-06-02T10:00:00Z" });
    const editorTitle = /^Edit GPS location —/;

    it("keeps the gallery's keys off the editor: the arrows do nothing, Escape closes only the editor", async () => {
      mockApi.get.mockResolvedValue([own, second]);
      const { props } = renderOverlay();
      await screen.findByText("1 / 2");
      await userEvent.click(screen.getByRole("button", { name: "Edit GPS location" }));
      expect(screen.getByText(editorTitle)).toHaveTextContent("own.jpg");

      // Focus is on the toolbar button behind the editor, not in a field.
      await userEvent.keyboard("{ArrowRight}");
      expect(screen.getByText(editorTitle)).toHaveTextContent("own.jpg");
      expect(screen.getByText("1 / 2")).toBeInTheDocument();

      await userEvent.keyboard("{Escape}");
      expect(screen.queryByText(editorTitle)).not.toBeInTheDocument();
      expect(props.onClose).not.toHaveBeenCalled();

      // Back in the gallery, its keys work again.
      await userEvent.keyboard("{ArrowRight}");
      expect(await screen.findByText("2 / 2")).toBeInTheDocument();
      await userEvent.keyboard("{Escape}");
      expect(props.onClose).toHaveBeenCalledTimes(1);
    });

    it("closes the editor when its item goes away instead of showing the next one", async () => {
      mockApi.get.mockResolvedValue([own, second]);
      const removal = deferred<void>();
      renderOverlay({ canRemoveFromAlbum: () => true, onRemoveFromAlbum: vi.fn(() => removal.promise) });
      await screen.findByText("1 / 2");
      await userEvent.click(screen.getByRole("button", { name: "Remove from album" }));
      await userEvent.click(screen.getByRole("button", { name: "Edit GPS location" }));
      expect(screen.getByText(editorTitle)).toHaveTextContent("own.jpg");

      // The removal lands while the editor is open, and the gallery moves on to the second item.
      await act(async () => removal.resolve());
      expect(await screen.findByText("1 / 1")).toBeInTheDocument();
      expect(screen.queryByText(editorTitle)).not.toBeInTheDocument();
    });

    it("does not reopen the editor when its item is shown again", async () => {
      mockApi.get.mockResolvedValue([own, second]);
      renderOverlay();
      await screen.findByText("1 / 2");
      await userEvent.click(screen.getByRole("button", { name: "Edit GPS location" }));
      expect(screen.getByText(editorTitle)).toBeInTheDocument();

      // On screen the editor covers the mini map; a marker click stands in for any change of item.
      fireEvent.click(screen.getByRole("button", { name: "Marker at 48.2,16.4" }));
      expect(await screen.findByText("2 / 2")).toBeInTheDocument();
      expect(screen.queryByText(editorTitle)).not.toBeInTheDocument();

      fireEvent.click(screen.getByRole("button", { name: "Marker at 64.1,-21.9" }));
      expect(await screen.findByText("1 / 2")).toBeInTheDocument();
      expect(screen.queryByText(editorTitle)).not.toBeInTheDocument();
    });
  });

  // jsdom has no layout: these pin the classes that place the controls per screen.
  describe("layout", () => {
    const labelBox = () => screen.getByText("1 / 2").closest(".items-center")!;

    it("keeps the position label in place while the mouse enlarges the map", async () => {
      const { container } = renderOverlay();
      await screen.findByText("1 / 2");
      const map = container.querySelector("[data-size]")!;
      // From sm the map is out of the flow, and the label keeps clear of its chosen width (on
      // both sides on a wide screen, so it is centred there).
      expect(map).toHaveClass("sm:absolute", "short:static");
      expect(labelBox()).toHaveClass("sm:pr-[18.5rem]", "xl:pl-[18.5rem]", "short:px-0");
      const before = labelBox().className;

      fireEvent.pointerEnter(map, { pointerType: "mouse" });
      expect(map).toHaveAttribute("data-size", "large");
      expect(labelBox().className).toBe(before);

      fireEvent.click(screen.getByRole("button", { name: "Hide map" }));
      expect(labelBox()).toHaveClass("sm:px-[5.5rem]");
    });

    it("keeps the label and its arrows beside a large map on a short screen", async () => {
      const { container } = renderOverlay();
      await screen.findByText("1 / 2");
      fireEvent.click(screen.getByRole("button", { name: "Enlarge map" }));

      // Under the map while it spans the width (below lg); beside it from lg and on any short
      // screen, where there is no height for both.
      const row = labelBox().parentElement!;
      expect(row).toHaveClass("flex-col-reverse", "lg:flex-row", "short:flex-row");
      expect(labelBox()).toHaveClass("lg:flex-1", "short:flex-1", "lg:pr-[36.5rem]");
      expect(container.querySelector("[data-size]")).toHaveClass("lg:absolute", "short:static");
      expect(within(labelBox() as HTMLElement).getByRole("button", { name: "Next" })).toBeInTheDocument();
    });

    // shrink-0 would let a large map on a short column run off the bottom of the screen; with
    // min-h-0 the row gives up height and the map's max-h-full caps it.
    it("lets the bottom row shrink, in the row and the stacked layout", async () => {
      renderOverlay();
      await screen.findByText("1 / 2");
      expect(labelBox().parentElement).toHaveClass("min-h-0", "flex-row");
      expect(labelBox().parentElement).not.toHaveClass("shrink-0");

      fireEvent.click(screen.getByRole("button", { name: "Enlarge map" }));
      expect(labelBox().parentElement).toHaveClass("min-h-0", "flex-col-reverse");
      expect(labelBox().parentElement).not.toHaveClass("shrink-0");
    });

    it("puts the side arrows on a dark disc with a 44px target", async () => {
      renderOverlay();
      await screen.findByText("1 / 2");

      expect(screen.getByRole("button", { name: "Next" })).toHaveClass("rounded-full", "bg-black/40", "h-11", "w-11");
    });
  });

  describe("album menu", () => {
    beforeEach(() => {
      mockApi.listAlbums.mockResolvedValue([iceland, family]);
      mockApi.mediaAlbums.mockResolvedValue([
        { id: "a1", name: "Iceland" },
        { id: "a2", name: "Family" },
      ]);
      mockApi.removeFromAlbum.mockResolvedValue({ removed: 1 });
    });

    it("drops an item unticked from the album the gallery shows", async () => {
      renderOverlay({ params: { album_id: "a1" } });
      await screen.findByText("1 / 2");

      await userEvent.click(screen.getByRole("button", { name: "Albums" }));
      const dialog = await screen.findByRole("dialog");
      await userEvent.click(await within(dialog).findByRole("checkbox", { name: "Iceland" }));

      expect(mockApi.removeFromAlbum).toHaveBeenCalledWith("a1", { mediaIds: ["own"] });
      expect(await screen.findByText("1 / 1")).toBeInTheDocument();
      expect(screen.getByText("Shared by Bob")).toBeInTheDocument();
    });

    it("keeps an item unticked from another album", async () => {
      renderOverlay({ params: { album_id: "a1" } });
      await screen.findByText("1 / 2");

      await userEvent.click(screen.getByRole("button", { name: "Albums" }));
      const dialog = await screen.findByRole("dialog");
      const box = await within(dialog).findByRole("checkbox", { name: "Family" });
      await userEvent.click(box);

      await waitFor(() => expect(box).toBeEnabled());
      expect(mockApi.removeFromAlbum).toHaveBeenCalledWith("a2", { mediaIds: ["own"] });
      expect(screen.getByText("1 / 2")).toBeInTheDocument();
    });
  });

  describe("media URL", () => {
    it.each([
      ["access is gone", new ApiError("GET /media/own/url failed: 404", 404), "You no longer have access to this item"],
      ["the request fails", new ApiError("GET /media/own/url failed: 500", 500), "Could not load media"],
    ])("says so when %s", async (_name, err, message) => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.getMediaUrl.mockRejectedValue(err);
      renderOverlay();
      await screen.findByText("1 / 2");

      expect(await screen.findByText(message)).toBeInTheDocument();
      expect(screen.queryByText("Loading…")).not.toBeInTheDocument();
    });

    it("shows the error only for the item whose URL failed", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.getMediaUrl.mockImplementation((id: string) =>
        id === "own" ? Promise.reject(new ApiError("failed: 404", 404)) : Promise.resolve("https://s3.example/shared")
      );
      renderOverlay();
      expect(await screen.findByText("You no longer have access to this item")).toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: "Next" }));
      expect(await screen.findByRole("img", { name: "shared.jpg" })).toHaveAttribute("src", "https://s3.example/shared");
      expect(screen.queryByText("You no longer have access to this item")).not.toBeInTheDocument();
    });

    it("keeps the current item's URL when a slower one for the previous item arrives", async () => {
      const slow = deferred<string>();
      mockApi.getMediaUrl.mockImplementation((id: string) =>
        id === "own" ? slow.promise : Promise.resolve("https://s3.example/shared")
      );
      renderOverlay();
      await screen.findByText("1 / 2");
      expect(screen.getByText("Loading…")).toBeInTheDocument();

      // The shared item was prefetched, so its URL is ready straight away.
      await userEvent.click(screen.getByRole("button", { name: "Next" }));
      const img = await screen.findByRole("img", { name: "shared.jpg" });
      await act(async () => slow.resolve("https://s3.example/own"));

      expect(img).toHaveAttribute("src", "https://s3.example/shared");
    });

    // A step must never render a 360° viewer with the previous item's URL, not even for one
    // commit: the real viewer would build a renderer and load the old texture again.
    describe("stepping between 360° items", () => {
      const first = makeItem("p1", { media_type: "photo_360", taken_at: "2025-06-01T10:00:00Z" });
      const second = makeItem("v2", { media_type: "video_360", taken_at: "2025-06-02T10:00:00Z", keyframes: [] });

      it("shows the placeholder, not the previous URL, while the next URL loads", async () => {
        const slow = deferred<string>();
        mockApi.get.mockResolvedValue([first, second]);
        mockApi.getMediaUrl.mockImplementation((id: string) =>
          id === "p1" ? Promise.resolve("https://s3.example/p1") : slow.promise
        );
        renderOverlay();
        expect(await screen.findByTestId("panorama")).toHaveAttribute("data-url", "https://s3.example/p1");

        viewerUrls.length = 0;
        await userEvent.click(screen.getByRole("button", { name: "Next" }));
        expect(await screen.findByText("2 / 2")).toBeInTheDocument();
        expect(screen.getByText("Loading…")).toBeInTheDocument();
        expect(screen.queryByTestId("panorama")).not.toBeInTheDocument();
        expect(screen.queryByTestId("video360")).not.toBeInTheDocument();

        await act(async () => slow.resolve("https://s3.example/v2"));
        expect(screen.getByTestId("video360")).toHaveAttribute("data-url", "https://s3.example/v2");
        expect(viewerUrls.length).toBeGreaterThan(0);
        expect(viewerUrls.every((url) => url === "https://s3.example/v2")).toBe(true);
      });

      it("shows a prefetched item at once, with its own URL", async () => {
        mockApi.get.mockResolvedValue([first, second]);
        mockApi.getMediaUrl.mockImplementation((id: string) => Promise.resolve(`https://s3.example/${id}`));
        renderOverlay();
        await screen.findByTestId("panorama");
        await waitFor(() => expect(mockApi.getMediaUrl).toHaveBeenCalledWith("v2"));

        viewerUrls.length = 0;
        await userEvent.click(screen.getByRole("button", { name: "Next" }));
        expect(screen.getByTestId("video360")).toHaveAttribute("data-url", "https://s3.example/v2");
        expect(viewerUrls.every((url) => url === "https://s3.example/v2")).toBe(true);

        viewerUrls.length = 0;
        await userEvent.click(screen.getByRole("button", { name: "Previous" }));
        expect(screen.getByTestId("panorama")).toHaveAttribute("data-url", "https://s3.example/p1");
        expect(viewerUrls.every((url) => url === "https://s3.example/p1")).toBe(true);
      });
    });
  });
});
