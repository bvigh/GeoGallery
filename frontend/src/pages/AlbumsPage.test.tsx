import type { ComponentProps } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { AlbumsPage } from "@/pages/AlbumsPage";
import { AuthContext } from "@/components/auth/AuthContext";
import type * as Gallery from "@/components/MediaGalleryOverlay";
import type { Album, AlbumMember, CurrentUser, DirectoryUser, MediaItem } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    api: {
      get: vi.fn(),
      listAlbums: vi.fn(),
      createAlbum: vi.fn(),
      renameAlbum: vi.fn(),
      deleteAlbum: vi.fn(),
      listAlbumMembers: vi.fn(),
      addAlbumMember: vi.fn(),
      removeAlbumMember: vi.fn(),
      removeFromAlbum: vi.fn(),
      listUsers: vi.fn(),
    },
  };
});

type GalleryProps = ComponentProps<typeof Gallery.MediaGalleryOverlay>;

// The real overlay needs Leaflet and media URLs; the page only decides its props.
const gallery = vi.hoisted(() => ({ props: null as GalleryProps | null }));
vi.mock("@/components/MediaGalleryOverlay", () => ({
  MediaGalleryOverlay: (props: GalleryProps) => {
    gallery.props = props;
    return <div data-testid="gallery" data-title={props.title} data-params={JSON.stringify(props.params)} />;
  },
}));

const mockApi = api as unknown as Record<
  | "get"
  | "listAlbums"
  | "createAlbum"
  | "renameAlbum"
  | "deleteAlbum"
  | "listAlbumMembers"
  | "addAlbumMember"
  | "removeAlbumMember"
  | "removeFromAlbum"
  | "listUsers",
  ReturnType<typeof vi.fn>
>;

const me: CurrentUser = { id: "u-me", email: "me@example.test", display_name: "Me", role: "user" };

const owned: Album = {
  id: "a1",
  name: "Iceland 2025",
  owner_id: "u-me",
  owner_name: "Me",
  is_owner: true,
  member_count: 2,
  media_count: 12,
  start_date: "2025-06-01T10:00:00Z",
  end_date: "2025-06-14T18:00:00Z",
  created_at: "2025-05-01T00:00:00Z",
};

const shared: Album = {
  id: "a2",
  name: "Family",
  owner_id: "u-bob",
  owner_name: "Bob",
  is_owner: false,
  member_count: 3,
  media_count: 1,
  start_date: null,
  end_date: null,
  created_at: "2025-05-02T00:00:00Z",
};

const ownedMembers: AlbumMember[] = [
  { user_id: "u-bob", display_name: "Bob", email: "bob@example.test", is_owner: false, added_at: "2025-05-03T00:00:00Z" },
  { user_id: "u-me", display_name: "Me", email: "me@example.test", is_owner: true, added_at: "2025-05-01T00:00:00Z" },
];

const sharedMembers: AlbumMember[] = [
  { user_id: "u-bob", display_name: "Bob", email: "bob@example.test", is_owner: true, added_at: "2025-05-02T00:00:00Z" },
  { user_id: "u-me", display_name: "Me", email: "me@example.test", is_owner: false, added_at: "2025-05-04T00:00:00Z" },
];

const directory: DirectoryUser[] = [
  { id: "u-me", display_name: "Me", email: "me@example.test" },
  { id: "u-bob", display_name: "Bob", email: "bob@example.test" },
  { id: "u-carol", display_name: null, email: "carol@example.test" },
  { id: "u-dave", display_name: "Dave", email: null },
];

function makeItem(id: string, ownerId: string): MediaItem {
  return {
    id,
    filename: `${id}.jpg`,
    media_type: "photo",
    lat: null,
    lng: null,
    gps_confidence: null,
    gps_source: null,
    taken_at: null,
    storage_key: `media/${ownerId}/${id}.jpg`,
    trip_id: null,
    owner_id: ownerId,
    owner_name: null,
  };
}

function renderPage() {
  return render(
    <AuthContext.Provider value={me}>
      <AlbumsPage />
    </AuthContext.Provider>
  );
}

function card(name: string): HTMLElement {
  return screen.getByRole("article", { name });
}

async function openMembers(album: Album) {
  await userEvent.click(await screen.findByRole("button", { name: `Members of ${album.name}` }));
  return screen.findByRole("dialog");
}

describe("AlbumsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    gallery.props = null;
    mockApi.listAlbums.mockResolvedValue([owned, shared]);
    mockApi.createAlbum.mockResolvedValue({ ...owned, id: "a3", name: "Tokyo" });
    mockApi.renameAlbum.mockResolvedValue(undefined);
    mockApi.deleteAlbum.mockResolvedValue(undefined);
    mockApi.listAlbumMembers.mockImplementation((id: string) =>
      Promise.resolve(id === owned.id ? ownedMembers : sharedMembers)
    );
    mockApi.addAlbumMember.mockResolvedValue(undefined);
    mockApi.removeAlbumMember.mockResolvedValue(undefined);
    mockApi.removeFromAlbum.mockResolvedValue({ removed: 1 });
    mockApi.listUsers.mockResolvedValue(directory);
    mockApi.get.mockResolvedValue([]);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows a card per album with owner, counts and date range", async () => {
    renderPage();
    await screen.findByRole("button", { name: owned.name });

    const mine = within(card(owned.name));
    expect(mine.getByText("Owner: You")).toBeInTheDocument();
    expect(mine.getByText("2 members · 12 items")).toBeInTheDocument();
    const from = new Date(owned.start_date!).toLocaleDateString();
    const to = new Date(owned.end_date!).toLocaleDateString();
    expect(mine.getByText(`${from} – ${to}`)).toBeInTheDocument();

    const theirs = within(card(shared.name));
    expect(theirs.getByText("Owner: Bob")).toBeInTheDocument();
    expect(theirs.getByText("3 members · 1 item")).toBeInTheDocument();
    expect(theirs.getByText("No dates")).toBeInTheDocument();
  });

  it.each([
    ["no owner name", { owner_name: null }, "Owner: another user"],
    ["one day", { start_date: "2025-06-01", end_date: "2025-06-01" }, new Date(2025, 5, 1).toLocaleDateString()],
  ])("formats a card with %s", async (_name, overrides, text) => {
    mockApi.listAlbums.mockResolvedValue([{ ...shared, ...overrides }]);
    renderPage();
    await screen.findByRole("button", { name: shared.name });
    expect(within(card(shared.name)).getByText(text)).toBeInTheDocument();
  });

  it("shows rename and delete only on albums the caller owns", async () => {
    renderPage();
    await screen.findByRole("button", { name: owned.name });

    expect(screen.getByRole("button", { name: `Rename ${owned.name}` })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: `Delete ${owned.name}` })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: `Rename ${shared.name}` })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: `Delete ${shared.name}` })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: `Members of ${shared.name}` })).toBeInTheDocument();
  });

  it("shows the empty state and a load error", async () => {
    mockApi.listAlbums.mockResolvedValueOnce([]);
    const { unmount } = renderPage();
    expect(await screen.findByText(/No albums yet/)).toBeInTheDocument();
    unmount();

    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.listAlbums.mockRejectedValueOnce(new Error("boom"));
    renderPage();
    expect(await screen.findByText("Could not load albums")).toBeInTheDocument();
  });

  it("creates an album with a trimmed name and refreshes the list", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "New album" }));
    const dialog = await screen.findByRole("dialog");

    mockApi.listAlbums.mockResolvedValue([owned, shared, { ...owned, id: "a3", name: "Tokyo" }]);
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "  Tokyo  ");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create album" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Tokyo");
    expect(await screen.findByRole("button", { name: "Tokyo" })).toBeInTheDocument();
    expect(mockApi.listAlbums).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("keeps the dialog open with an error when creating fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.createAlbum.mockRejectedValue(new ApiError("POST /albums failed: 400", 400));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "New album" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "Tokyo");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create album" }));

    expect(await within(dialog).findByText("Could not create album")).toBeInTheDocument();
    expect(mockApi.listAlbums).toHaveBeenCalledTimes(1);
  });

  it("opens the gallery with the album's media and refreshes counts on close", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: shared.name }));

    const el = screen.getByTestId("gallery");
    expect(el).toHaveAttribute("data-title", shared.name);
    expect(JSON.parse(el.getAttribute("data-params")!)).toEqual({ album_id: shared.id });
    expect(gallery.props!.subtitle).toBe("by Bob");

    act(() => gallery.props!.onClose());
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
    expect(mockApi.listAlbums).toHaveBeenCalledTimes(2);
  });

  it.each([
    ["owner, own item", owned, "u-me", true],
    ["owner, another member's item", owned, "u-bob", true],
    ["member, own item", shared, "u-me", true],
    ["member, another member's item", shared, "u-bob", false],
  ])("lets the gallery remove an item: %s", async (_name, album, itemOwner, allowed) => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: album.name }));
    expect(gallery.props!.canRemoveFromAlbum!(makeItem("m1", itemOwner))).toBe(allowed);
  });

  it("removes an item from the album and refreshes the counts", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: owned.name }));

    await act(() => gallery.props!.onRemoveFromAlbum!(makeItem("m1", "u-bob")));
    expect(mockApi.removeFromAlbum).toHaveBeenCalledWith(owned.id, { mediaIds: ["m1"] });
    await waitFor(() => expect(mockApi.listAlbums).toHaveBeenCalledTimes(2));
  });

  it("rejects to keep the item when removing fails, and shows why after the gallery closes", async () => {
    mockApi.removeFromAlbum.mockRejectedValue(new ApiError("POST /albums/a2/media/remove failed: 403", 403));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: shared.name }));

    await act(() => expect(gallery.props!.onRemoveFromAlbum!(makeItem("m1", "u-me"))).rejects.toThrow());
    act(() => gallery.props!.onClose());
    expect(
      await screen.findByText('Could not remove "m1.jpg" from the album: only the album owner can do that')
    ).toBeInTheDocument();
  });

  it("renames an album the caller owns", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: `Rename ${owned.name}` }));
    const input = screen.getByRole("textbox", { name: "Album name" });
    expect(input).toHaveValue(owned.name);
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();

    mockApi.listAlbums.mockResolvedValue([{ ...owned, name: "Iceland 2026" }, shared]);
    await userEvent.clear(input);
    await userEvent.type(input, "Iceland 2026{Enter}");

    expect(mockApi.renameAlbum).toHaveBeenCalledWith(owned.id, "Iceland 2026");
    expect(await screen.findByRole("button", { name: "Iceland 2026" })).toBeInTheDocument();
    await waitFor(() => expect(mockApi.listAlbums).toHaveBeenCalledTimes(2));
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });

  it("does not call the API when a rename is cancelled or unchanged", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: `Rename ${owned.name}` }));
    await userEvent.type(screen.getByRole("textbox", { name: "Album name" }), "x{Escape}");
    expect(screen.queryByRole("textbox", { name: "Album name" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: `Rename ${owned.name}` }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mockApi.renameAlbum).not.toHaveBeenCalled();
  });

  it("deletes an album after a confirm that says media are kept", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    renderPage();
    const del = await screen.findByRole("button", { name: `Delete ${owned.name}` });

    await userEvent.click(del);
    expect(mockApi.deleteAlbum).not.toHaveBeenCalled();
    expect(confirmSpy.mock.calls[0][0]).toContain("Media are not deleted");

    mockApi.listAlbums.mockResolvedValue([shared]);
    await userEvent.click(del);
    expect(mockApi.deleteAlbum).toHaveBeenCalledWith(owned.id);
    await waitFor(() => expect(screen.queryByRole("button", { name: owned.name })).not.toBeInTheDocument());
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });

  it.each([
    [403, "Could not delete album: only the album owner can do that"],
    [404, "Could not delete album: the album no longer exists, or you are no longer a member"],
    [500, "Could not delete album"],
  ])("shows why deleting failed (%i)", async (status, message) => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.deleteAlbum.mockRejectedValue(new ApiError(`DELETE /albums/a1 failed: ${status}`, status));
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: `Delete ${owned.name}` }));
    expect(await screen.findByText(message)).toBeInTheDocument();
  });

  describe("members dialog", () => {
    it("lets the owner add a user who is not a member yet", async () => {
      renderPage();
      const dialog = await openMembers(owned);

      const list = await within(dialog).findByRole("list", { name: "Members" });
      // The owner is listed first.
      expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
        "Me (you)Owner",
        "BobRemove",
      ]);
      expect(mockApi.listAlbumMembers).toHaveBeenCalledWith(owned.id);

      const users = await within(dialog).findByRole("list", { name: "Users to add" });
      // Existing members (Me, Bob) are left out; a missing display name falls back to the email.
      expect(within(users).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
        "carol@example.testAdd",
        "DaveAdd",
      ]);
      // A short list needs no search field.
      expect(within(dialog).queryByRole("searchbox")).not.toBeInTheDocument();

      mockApi.listAlbumMembers.mockResolvedValue([
        ...ownedMembers,
        { user_id: "u-carol", display_name: null, email: "carol@example.test", is_owner: false, added_at: "2025-05-05T00:00:00Z" },
      ]);
      await userEvent.click(within(users).getByRole("button", { name: "Add carol@example.test" }));

      expect(mockApi.addAlbumMember).toHaveBeenCalledWith(owned.id, "u-carol");
      await waitFor(() =>
        expect(within(users).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["DaveAdd"])
      );
      expect(within(list).getByText("carol@example.test")).toBeInTheDocument();
      // Counts on the card are refreshed.
      expect(mockApi.listAlbums).toHaveBeenCalledTimes(2);
    });

    it("says so when everyone is already a member", async () => {
      mockApi.listUsers.mockResolvedValue(directory.slice(0, 2));
      renderPage();
      const dialog = await openMembers(owned);
      expect(await within(dialog).findByText("Everyone in the user list is already a member.")).toBeInTheDocument();
      expect(within(dialog).queryByRole("list", { name: "Users to add" })).not.toBeInTheDocument();
    });

    it("searches a long user list by name or email", async () => {
      mockApi.listUsers.mockResolvedValue([
        ...directory,
        ...Array.from({ length: 8 }, (_, i) => ({ id: `u-${i}`, display_name: `User ${i}`, email: `user${i}@example.test` })),
        { id: "u-balazs", display_name: "Balázs", email: "b@example.test" },
      ]);
      renderPage();
      const dialog = await openMembers(owned);
      const search = await within(dialog).findByRole("searchbox", { name: "Search users" });
      const names = () =>
        within(dialog)
          .queryAllByRole("button", { name: /^Add / })
          .map((b) => b.getAttribute("aria-label"));

      // Accents are ignored, and the email is searched too.
      await userEvent.type(search, "balazs");
      expect(names()).toEqual(["Add Balázs"]);
      await userEvent.clear(search);
      await userEvent.type(search, "carol@");
      expect(names()).toEqual(["Add carol@example.test"]);
      // Enter does not add anyone.
      await userEvent.type(search, "{Enter}");
      expect(mockApi.addAlbumMember).not.toHaveBeenCalled();

      await userEvent.type(search, "nobody");
      expect(within(dialog).getByText("No matching users.")).toBeInTheDocument();
      await userEvent.click(within(dialog).getByRole("button", { name: "Clear search" }));
      expect(names()).toHaveLength(11);
    });

    it("shows why adding a member failed", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.addAlbumMember.mockRejectedValue(new ApiError("POST /albums/a1/members failed: 404", 404));
      renderPage();
      const dialog = await openMembers(owned);
      await userEvent.click(await within(dialog).findByRole("button", { name: "Add Dave" }));

      expect(
        await within(dialog).findByText(
          "Could not add member: the user is not available, or the album no longer exists"
        )
      ).toBeInTheDocument();
    });

    it("lets the owner remove a member after a confirm", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
      renderPage();
      const dialog = await openMembers(owned);
      const remove = await within(dialog).findByRole("button", { name: "Remove Bob" });
      // The owner's own row has no remove button.
      expect(within(dialog).queryByRole("button", { name: "Remove Me" })).not.toBeInTheDocument();

      await userEvent.click(remove);
      expect(mockApi.removeAlbumMember).not.toHaveBeenCalled();
      expect(confirmSpy.mock.calls[0][0]).toContain("Their items will be removed from the album");

      mockApi.listAlbumMembers.mockResolvedValue(ownedMembers.filter((m) => m.user_id !== "u-bob"));
      await userEvent.click(remove);
      expect(mockApi.removeAlbumMember).toHaveBeenCalledWith(owned.id, "u-bob");
      const list = within(dialog).getByRole("list", { name: "Members" });
      await waitFor(() => expect(within(list).queryByText("Bob")).not.toBeInTheDocument());
      // Bob can be added again.
      const users = within(dialog).getByRole("list", { name: "Users to add" });
      expect(within(users).getByRole("button", { name: "Add Bob" })).toBeInTheDocument();
      expect(mockApi.listAlbums).toHaveBeenCalledTimes(2);
    });

    it("gives a member a read-only list and a leave button, without the user list", async () => {
      renderPage();
      const dialog = await openMembers(shared);

      const list = await within(dialog).findByRole("list", { name: "Members" });
      expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["BobOwner", "Me (you)"]);
      expect(within(dialog).getByRole("button", { name: "Leave album" })).toBeInTheDocument();
      expect(within(dialog).queryByRole("button", { name: /^Remove/ })).not.toBeInTheDocument();
      expect(within(dialog).queryByRole("list", { name: "Users to add" })).not.toBeInTheDocument();
      expect(mockApi.listUsers).not.toHaveBeenCalled();
    });

    it("has no leave button for the album owner", async () => {
      renderPage();
      const dialog = await openMembers(owned);
      await within(dialog).findByRole("list", { name: "Members" });
      expect(within(dialog).queryByRole("button", { name: "Leave album" })).not.toBeInTheDocument();
    });

    it("leaves after a confirm that counts the caller's own items, then closes and refreshes", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
      mockApi.get.mockResolvedValue([makeItem("m1", "u-me"), makeItem("m2", "u-me")]);
      renderPage();
      const dialog = await openMembers(shared);

      mockApi.listAlbums.mockResolvedValue([owned]);
      await userEvent.click(await within(dialog).findByRole("button", { name: "Leave album" }));

      expect(mockApi.get).toHaveBeenCalledWith("/media", { album_id: shared.id, owner: "mine", offset: "0" });
      expect(confirmSpy).toHaveBeenCalledWith(
        'Leave "Family"? Your 2 items will be removed from the album, and you will lose access to it.'
      );
      expect(mockApi.removeAlbumMember).toHaveBeenCalledWith(shared.id, me.id);
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(await screen.findByRole("button", { name: owned.name })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: shared.name })).not.toBeInTheDocument();
    });

    it.each([
      ["more than one page", () => Promise.resolve(Array.from({ length: 500 }, (_, i) => makeItem(`m${i}`, "u-me"))), "Your 1000 items will be removed"],
      ["no own items", () => Promise.resolve([]), "You will lose access to its items."],
      ["a failed count", () => Promise.reject(new Error("boom")), "Your items will be removed"],
    ])("words the leave confirm for %s", async (_name, page, text) => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
      mockApi.get.mockImplementation((_path: string, params: Record<string, string>) =>
        params.offset === "1000" ? Promise.resolve([]) : page()
      );
      renderPage();
      const dialog = await openMembers(shared);
      await userEvent.click(await within(dialog).findByRole("button", { name: "Leave album" }));

      await waitFor(() => expect(confirmSpy).toHaveBeenCalledTimes(1));
      expect(confirmSpy.mock.calls[0][0]).toContain(text);
      // Cancelling keeps the membership and the dialog.
      expect(mockApi.removeAlbumMember).not.toHaveBeenCalled();
      await waitFor(() => expect(within(dialog).getByRole("button", { name: "Leave album" })).toBeEnabled());
    });

    it("does not ask to leave once the dialog was closed while counting", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
      let resolveCount!: (items: MediaItem[]) => void;
      mockApi.get.mockReturnValue(new Promise<MediaItem[]>((r) => (resolveCount = r)));
      renderPage();
      const dialog = await openMembers(shared);
      await userEvent.click(await within(dialog).findByRole("button", { name: "Leave album" }));
      expect(mockApi.get).toHaveBeenCalledWith("/media", { album_id: shared.id, owner: "mine", offset: "0" });

      await userEvent.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      await act(async () => resolveCount([makeItem("m1", "u-me")]));

      expect(confirmSpy).not.toHaveBeenCalled();
      expect(mockApi.removeAlbumMember).not.toHaveBeenCalled();
    });

    it("does not ask to leave once another album's members were opened while counting", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
      let resolveCount!: (items: MediaItem[]) => void;
      mockApi.get.mockReturnValue(new Promise<MediaItem[]>((r) => (resolveCount = r)));
      renderPage();
      const dialog = await openMembers(shared);
      await userEvent.click(await within(dialog).findByRole("button", { name: "Leave album" }));
      await userEvent.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

      const other = await openMembers(owned);
      await act(async () => resolveCount([]));

      expect(confirmSpy).not.toHaveBeenCalled();
      expect(mockApi.removeAlbumMember).not.toHaveBeenCalled();
      expect(within(other).getByRole("heading", { name: `Members of "${owned.name}"` })).toBeInTheDocument();
    });

    it("keeps another album's members open when an earlier leave finishes", async () => {
      vi.spyOn(window, "confirm").mockReturnValue(true);
      let finishLeave!: () => void;
      mockApi.removeAlbumMember.mockReturnValue(new Promise<void>((r) => (finishLeave = r)));
      renderPage();
      const dialog = await openMembers(shared);
      await userEvent.click(await within(dialog).findByRole("button", { name: "Leave album" }));
      await waitFor(() => expect(mockApi.removeAlbumMember).toHaveBeenCalledWith(shared.id, me.id));
      await userEvent.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

      const other = await openMembers(owned);
      expect(screen.getByRole("article", { name: shared.name, hidden: true })).toBeInTheDocument();
      mockApi.listAlbums.mockResolvedValue([owned]);
      await act(async () => finishLeave());

      // The left album drops off the page (hidden behind the open dialog), and the
      // dialog the user opened since stays.
      await waitFor(() =>
        expect(screen.queryByRole("article", { name: shared.name, hidden: true })).not.toBeInTheDocument()
      );
      expect(mockApi.listAlbums).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("dialog")).toBe(other);
      expect(within(other).getByRole("heading", { name: `Members of "${owned.name}"` })).toBeInTheDocument();
    });

    it("keeps the dialog open with an error when leaving fails", async () => {
      vi.spyOn(window, "confirm").mockReturnValue(true);
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.removeAlbumMember.mockRejectedValue(new ApiError("DELETE /albums/a2/members/u-me failed: 404", 404));
      renderPage();
      const dialog = await openMembers(shared);
      await userEvent.click(await within(dialog).findByRole("button", { name: "Leave album" }));

      expect(
        await within(dialog).findByText(
          "Could not leave album: the album no longer exists, or you are no longer a member"
        )
      ).toBeInTheDocument();
      expect(screen.getByRole("dialog")).toBeInTheDocument();
    });

    it("shows why the members could not be loaded", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listAlbumMembers.mockRejectedValue(new ApiError("GET /albums/a2/members failed: 404", 404));
      renderPage();
      const dialog = await openMembers(shared);
      expect(
        await within(dialog).findByText(
          "Could not load members: the album no longer exists, or you are no longer a member"
        )
      ).toBeInTheDocument();
    });
  });
});
