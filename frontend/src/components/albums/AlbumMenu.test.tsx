import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "@/api/client";
import { AlbumMenu } from "@/components/albums/AlbumMenu";
import type { Album, MediaItem } from "@/types";

vi.mock("@/api/client", () => ({
  api: {
    listAlbums: vi.fn(),
    mediaAlbums: vi.fn(),
    addToAlbum: vi.fn(),
    removeFromAlbum: vi.fn(),
    createAlbum: vi.fn(),
  },
}));

const mockApi = api as unknown as {
  listAlbums: ReturnType<typeof vi.fn>;
  mediaAlbums: ReturnType<typeof vi.fn>;
  addToAlbum: ReturnType<typeof vi.fn>;
  removeFromAlbum: ReturnType<typeof vi.fn>;
  createAlbum: ReturnType<typeof vi.fn>;
};

const item: MediaItem = {
  id: "m1",
  filename: "geysir.jpg",
  media_type: "photo",
  lat: 64.3,
  lng: -20.3,
  gps_confidence: 1,
  gps_source: "exif",
  taken_at: null,
  storage_key: "media/u-me/abc.jpg",
  trip_id: null,
  owner_id: "u-me",
  owner_name: "Me",
};

function makeAlbum(overrides: Partial<Album>): Album {
  return {
    id: "a1",
    name: "Family",
    owner_id: "u-me",
    owner_name: "Me",
    is_owner: true,
    member_count: 1,
    media_count: 0,
    start_date: null,
    end_date: null,
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const family = makeAlbum({ id: "a1", name: "Family" });
const iceland = makeAlbum({ id: "a2", name: "Iceland", owner_id: "u-bob", owner_name: "Bob", is_owner: false });

async function openMenu() {
  await userEvent.click(screen.getByRole("button", { name: "Albums" }));
  return screen.findByRole("dialog");
}

describe("AlbumMenu", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.listAlbums.mockResolvedValue([family, iceland]);
    mockApi.mediaAlbums.mockResolvedValue([{ id: "a2", name: "Iceland" }]);
    mockApi.addToAlbum.mockResolvedValue({ added: 1 });
    mockApi.removeFromAlbum.mockResolvedValue({ removed: 1 });
  });

  it("lists the caller's albums, ticking the ones that contain the item", async () => {
    render(<AlbumMenu item={item} isOwn />);
    const dialog = await openMenu();

    const familyBox = await within(dialog).findByRole("checkbox", { name: /Family/ });
    const icelandBox = within(dialog).getByRole("checkbox", { name: /Iceland/ });
    expect(familyBox).not.toBeChecked();
    expect(icelandBox).toBeChecked();
    expect(within(dialog).getByText("by Bob")).toBeInTheDocument();
    expect(mockApi.mediaAlbums).toHaveBeenCalledWith("m1");
  });

  it.each([
    ["ticking adds the item", /Family/, "addToAlbum", "a1", true],
    ["unticking removes the item", /Iceland/, "removeFromAlbum", "a2", false],
  ] as const)("%s", async (_name, label, helper, albumId, checkedAfter) => {
    render(<AlbumMenu item={item} isOwn />);
    const dialog = await openMenu();

    const box = await within(dialog).findByRole("checkbox", { name: label });
    await userEvent.click(box);

    expect(mockApi[helper]).toHaveBeenCalledWith(albumId, { mediaIds: ["m1"] });
    const other = helper === "addToAlbum" ? mockApi.removeFromAlbum : mockApi.addToAlbum;
    expect(other).not.toHaveBeenCalled();
    await waitFor(() => expect(box).not.toBeDisabled());
    expect((box as HTMLInputElement).checked).toBe(checkedAfter);
  });

  it("rolls the checkbox back and shows an error when adding fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.addToAlbum.mockRejectedValue(new Error("boom"));
    render(<AlbumMenu item={item} isOwn />);
    const dialog = await openMenu();

    const box = await within(dialog).findByRole("checkbox", { name: /Family/ });
    await userEvent.click(box);

    expect(await within(dialog).findByText('Could not add to "Family"')).toBeInTheDocument();
    expect(box).not.toBeChecked();
  });

  it("creates a new album and adds the item to it", async () => {
    const trip = makeAlbum({ id: "a3", name: "Road trip" });
    mockApi.createAlbum.mockResolvedValue(trip);
    render(<AlbumMenu item={item} isOwn />);
    const dialog = await openMenu();

    await userEvent.click(await within(dialog).findByRole("button", { name: "New album…" }));
    await userEvent.type(within(dialog).getByRole("textbox", { name: "New album name" }), "  Road trip ");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Road trip");
    await waitFor(() => expect(mockApi.addToAlbum).toHaveBeenCalledWith("a3", { mediaIds: ["m1"] }));
    expect(await within(dialog).findByRole("checkbox", { name: /Road trip/ })).toBeChecked();
  });

  it.each([
    ["an add", /Family/, "a1", true],
    ["a removal", /Iceland/, "a2", false],
  ] as const)("reports %s to onChange once the server applied it", async (_name, label, albumId, added) => {
    const onChange = vi.fn();
    render(<AlbumMenu item={item} isOwn onChange={onChange} />);
    const dialog = await openMenu();

    await userEvent.click(await within(dialog).findByRole("checkbox", { name: label }));

    await waitFor(() => expect(onChange).toHaveBeenCalledTimes(1));
    expect(onChange).toHaveBeenCalledWith(albumId, added);
  });

  it.each([
    ["add", /Family/, "addToAlbum"],
    ["removal", /Iceland/, "removeFromAlbum"],
  ] as const)("does not report a failed %s to onChange", async (_name, label, helper) => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi[helper].mockRejectedValue(new Error("boom"));
    const onChange = vi.fn();
    render(<AlbumMenu item={item} isOwn onChange={onChange} />);
    const dialog = await openMenu();

    await userEvent.click(await within(dialog).findByRole("checkbox", { name: label }));

    expect(await within(dialog).findByText(/^Could not/)).toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("reports a created album and the add to it", async () => {
    const trip = makeAlbum({ id: "a3", name: "Road trip" });
    mockApi.createAlbum.mockResolvedValue(trip);
    const onChange = vi.fn();
    const onAlbumCreated = vi.fn();
    render(<AlbumMenu item={item} isOwn onChange={onChange} onAlbumCreated={onAlbumCreated} />);
    const dialog = await openMenu();

    await userEvent.click(await within(dialog).findByRole("button", { name: "New album…" }));
    await userEvent.type(within(dialog).getByRole("textbox", { name: "New album name" }), "Road trip");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    await waitFor(() => expect(onChange).toHaveBeenCalledWith("a3", true));
    expect(onAlbumCreated).toHaveBeenCalledTimes(1);
    expect(onAlbumCreated).toHaveBeenCalledWith(trip);
  });

  it("reports a created album even when adding to it fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const trip = makeAlbum({ id: "a3", name: "Road trip" });
    mockApi.createAlbum.mockResolvedValue(trip);
    mockApi.addToAlbum.mockRejectedValue(new Error("boom"));
    const onChange = vi.fn();
    const onAlbumCreated = vi.fn();
    render(<AlbumMenu item={item} isOwn onChange={onChange} onAlbumCreated={onAlbumCreated} />);
    const dialog = await openMenu();

    await userEvent.click(await within(dialog).findByRole("button", { name: "New album…" }));
    await userEvent.type(within(dialog).getByRole("textbox", { name: "New album name" }), "Road trip");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    expect(await within(dialog).findByText('Could not add to "Road trip"')).toBeInTheDocument();
    expect(onAlbumCreated).toHaveBeenCalledWith(trip);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("shows only an error when the albums fail to load", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.mediaAlbums.mockRejectedValue(new Error("boom"));
    render(<AlbumMenu item={item} isOwn />);
    const dialog = await openMenu();

    expect(await within(dialog).findByText("Could not load albums")).toBeInTheDocument();
    expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("No albums yet.")).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "New album…" })).not.toBeInTheDocument();
  });

  it("shows a shared item's albums read-only", async () => {
    render(<AlbumMenu item={{ ...item, owner_id: "u-bob", owner_name: "Bob" }} isOwn={false} />);
    const dialog = await openMenu();

    const list = await within(dialog).findByRole("list", { name: "Shared through" });
    expect(within(list).getByText("Iceland")).toBeInTheDocument();
    expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "New album…" })).not.toBeInTheDocument();
    expect(mockApi.listAlbums).not.toHaveBeenCalled();
  });

  it("keeps keys inside the dialog from reaching the viewer behind it", async () => {
    const onWindowKey = vi.fn();
    window.addEventListener("keydown", onWindowKey);
    try {
      render(<AlbumMenu item={item} isOwn />);
      const dialog = await openMenu();
      await within(dialog).findByRole("checkbox", { name: /Family/ });

      await userEvent.keyboard("{ArrowRight}");
      expect(onWindowKey).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", onWindowKey);
    }
  });

  describe("with many albums", () => {
    const many = Array.from({ length: 8 }, (_, i) => makeAlbum({ id: `a${i + 10}`, name: `Album ${i}` }));

    beforeEach(() => {
      mockApi.listAlbums.mockResolvedValue([...many, family, iceland]);
    });

    const names = (dialog: HTMLElement) =>
      within(within(dialog).getByRole("group", { name: "Your albums" }))
        .getAllByRole("checkbox")
        .map((c) => c.closest("label")?.textContent);

    it("lists the albums the item is in first", async () => {
      render(<AlbumMenu item={item} isOwn />);
      const dialog = await openMenu();
      await within(dialog).findByRole("checkbox", { name: /^Iceland/ });
      expect(names(dialog)[0]).toBe("Icelandby Bob");
      // Ticking does not reorder the list under the pointer.
      await userEvent.click(within(dialog).getByRole("checkbox", { name: "Album 5" }));
      expect(names(dialog)[6]).toBe("Album 5");
    });

    it("searches by album name and owner, keeping ticked albums listed", async () => {
      render(<AlbumMenu item={item} isOwn />);
      const dialog = await openMenu();
      const search = await within(dialog).findByRole("searchbox", { name: "Search albums" });

      await userEvent.type(search, "fam");
      expect(names(dialog)).toEqual(["Icelandby Bob", "Family"]);
      await userEvent.clear(search);
      await userEvent.type(search, "album 3");
      expect(names(dialog)).toEqual(["Icelandby Bob", "Album 3"]);

      await userEvent.click(within(dialog).getByRole("checkbox", { name: /^Iceland/ }));
      await waitFor(() => expect(mockApi.removeFromAlbum).toHaveBeenCalled());
      expect(names(dialog)).toEqual(["Album 3"]);

      await userEvent.clear(search);
      await userEvent.type(search, "nothing like it");
      expect(within(dialog).getByText("No matching albums.")).toBeInTheDocument();
    });

    it("names a new album after the search", async () => {
      render(<AlbumMenu item={item} isOwn />);
      const dialog = await openMenu();
      await userEvent.type(await within(dialog).findByRole("searchbox", { name: "Search albums" }), " Summer ");
      await userEvent.click(within(dialog).getByRole("button", { name: "New album…" }));
      expect(within(dialog).getByRole("textbox", { name: "New album name" })).toHaveValue("Summer");
    });
  });
});
