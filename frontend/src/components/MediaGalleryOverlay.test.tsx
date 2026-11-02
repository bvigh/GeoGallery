import type { ComponentProps, ReactNode } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { MediaGalleryOverlay } from "@/components/MediaGalleryOverlay";
import { AuthContext } from "@/components/auth/AuthContext";
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
  Marker: () => null,
  useMap: () => ({ flyTo: vi.fn(), fitBounds: vi.fn(), invalidateSize: vi.fn() }),
  useMapEvents: () => null,
}));

vi.mock("leaflet", () => ({
  default: { divIcon: vi.fn(() => ({})), latLngBounds: vi.fn(() => ({})) },
}));

vi.mock("@/components/viewer/Panorama360", () => ({ default: () => null }));
vi.mock("@/components/viewer/Video360", () => ({ default: () => null }));

const mockApi = api as unknown as Record<
  "get" | "getMediaUrl" | "getKeyframes" | "delete" | "listAlbums" | "mediaAlbums" | "addToAlbum" | "removeFromAlbum",
  ReturnType<typeof vi.fn>
>;

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
  });
});
