import type { ComponentProps } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "@/api/client";
import { AlbumPickerDialog } from "@/components/albums/AlbumPickerDialog";
import type { Album } from "@/types";

vi.mock("@/api/client", () => ({
  api: {
    listAlbums: vi.fn(),
    createAlbum: vi.fn(),
  },
}));

const mockApi = api as unknown as {
  listAlbums: ReturnType<typeof vi.fn>;
  createAlbum: ReturnType<typeof vi.fn>;
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
const shared = makeAlbum({ id: "a2", name: "Road trip", owner_id: "u-bob", owner_name: "Bob", is_owner: false });

type Props = ComponentProps<typeof AlbumPickerDialog>;

function renderPicker(overrides: Partial<Props> = {}) {
  const props: Props = {
    open: true,
    onOpenChange: vi.fn(),
    title: 'Add "Iceland 2024" to an album',
    description: "Adds the trip's current media.",
    defaultName: "Iceland 2024",
    onPick: vi.fn(),
    ...overrides,
  };
  const view = render(<AlbumPickerDialog {...props} />);
  return { ...view, props };
}

// The list replaces the loading line once listAlbums settles.
async function loadedDialog() {
  const dialog = await screen.findByRole("dialog");
  await waitFor(() => expect(within(dialog).queryByText("Loading albums…")).not.toBeInTheDocument());
  return dialog;
}

describe("AlbumPickerDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.listAlbums.mockResolvedValue([family, shared]);
    mockApi.createAlbum.mockResolvedValue(makeAlbum({ id: "a-new", name: "Iceland 2024" }));
  });

  it("lists the caller's albums, naming the owner of shared ones", async () => {
    renderPicker();
    const dialog = await loadedDialog();

    expect(within(dialog).getByRole("heading", { name: 'Add "Iceland 2024" to an album' })).toBeInTheDocument();
    expect(within(dialog).getByRole("radio", { name: "Family" })).toBeInTheDocument();
    expect(within(dialog).getByRole("radio", { name: /Road trip/ })).toBeInTheDocument();
    expect(within(dialog).getByText("by Bob")).toBeInTheDocument();
  });

  it("returns the chosen existing album without creating one", async () => {
    const { props } = renderPicker();
    const dialog = await loadedDialog();

    await userEvent.click(within(dialog).getByRole("radio", { name: /Road trip/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(props.onPick).toHaveBeenCalledWith(shared);
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
    expect(mockApi.createAlbum).not.toHaveBeenCalled();
  });

  it("creates a new album named after the default, and returns it", async () => {
    const { props } = renderPicker();
    const dialog = await loadedDialog();

    expect(within(dialog).getByRole("radio", { name: "New album" })).toBeChecked();
    expect(within(dialog).getByRole("textbox", { name: "New album name" })).toHaveValue("Iceland 2024");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Iceland 2024");
    expect(props.onPick).toHaveBeenCalledWith(expect.objectContaining({ id: "a-new" }));
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
  });

  it("creates the album under the edited, trimmed name", async () => {
    const { props } = renderPicker();
    const dialog = await loadedDialog();

    // Choosing an existing album first: typing a name switches back to a new one.
    await userEvent.click(within(dialog).getByRole("radio", { name: "Family" }));
    const input = within(dialog).getByRole("textbox", { name: "New album name" });
    await userEvent.clear(input);
    await userEvent.type(input, "  Ring road  ");
    expect(within(dialog).getByRole("radio", { name: "New album" })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Ring road");
    expect(props.onPick).toHaveBeenCalledTimes(1);
  });

  it("preselects an album that already has the default name", async () => {
    mockApi.listAlbums.mockResolvedValue([family, makeAlbum({ id: "a3", name: "Iceland 2024" })]);
    const { props } = renderPicker();
    const dialog = await loadedDialog();

    expect(within(dialog).getByRole("radio", { name: "Iceland 2024" })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(props.onPick).toHaveBeenCalledWith(expect.objectContaining({ id: "a3" }));
    expect(mockApi.createAlbum).not.toHaveBeenCalled();
  });

  it("cannot create an album without a name", async () => {
    renderPicker({ defaultName: "" });
    const dialog = await loadedDialog();

    expect(within(dialog).getByRole("button", { name: "Add" })).toBeDisabled();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "New album name" }), "   ");
    expect(within(dialog).getByRole("button", { name: "Add" })).toBeDisabled();
  });

  it("caps a long default name at the album name limit", async () => {
    renderPicker({ defaultName: "x".repeat(250) });
    const dialog = await loadedDialog();

    expect(within(dialog).getByRole("textbox", { name: "New album name" })).toHaveValue("x".repeat(200));
  });

  it("stays open with an error when creating fails", async () => {
    mockApi.createAlbum.mockRejectedValue(new Error("POST /albums failed: 500"));
    const { props } = renderPicker();
    const dialog = await loadedDialog();

    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(await within(dialog).findByText("Could not create album")).toBeInTheDocument();
    expect(props.onPick).not.toHaveBeenCalled();
    expect(props.onOpenChange).not.toHaveBeenCalled();
    expect(within(dialog).getByRole("button", { name: "Add" })).toBeEnabled();
  });

  it("finishes the add when the dialog closes while the new album is being created", async () => {
    const created = makeAlbum({ id: "a-new", name: "Iceland 2024" });
    let resolveCreate!: (album: Album) => void;
    mockApi.createAlbum.mockReturnValue(new Promise<Album>((r) => (resolveCreate = r)));
    const { props, rerender } = renderPicker();
    const dialog = await loadedDialog();

    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));
    expect(within(dialog).getByRole("button", { name: "Creating…" })).toBeDisabled();
    rerender(<AlbumPickerDialog {...props} open={false} />);
    await act(async () => resolveCreate(created));

    expect(props.onPick).toHaveBeenCalledTimes(1);
    expect(props.onPick).toHaveBeenCalledWith(created);
    // The parent already closed it; a late close would hit a later opening.
    expect(props.onOpenChange).not.toHaveBeenCalled();
  });

  it("does not close a reopened dialog when an earlier creation finishes", async () => {
    const created = makeAlbum({ id: "a-new", name: "Iceland 2024" });
    let resolveCreate!: (album: Album) => void;
    mockApi.createAlbum.mockReturnValueOnce(new Promise<Album>((r) => (resolveCreate = r)));
    const { props, rerender } = renderPicker();
    await userEvent.click(within(await loadedDialog()).getByRole("button", { name: "Add" }));
    rerender(<AlbumPickerDialog {...props} open={false} />);
    rerender(<AlbumPickerDialog {...props} open defaultName="Tokyo Winter" />);
    const dialog = await loadedDialog();

    await act(async () => resolveCreate(created));

    expect(props.onPick).toHaveBeenCalledWith(created);
    expect(props.onOpenChange).not.toHaveBeenCalled();
    expect(within(dialog).getByRole("textbox", { name: "New album name" })).toHaveValue("Tokyo Winter");
    expect(within(dialog).getByRole("button", { name: "Add" })).toBeEnabled();
  });

  it("still offers a new album when the list fails to load", async () => {
    mockApi.listAlbums.mockRejectedValue(new Error("GET /albums failed: 500"));
    const { props } = renderPicker();
    const dialog = await screen.findByRole("dialog");

    expect(await within(dialog).findByText("Could not load albums")).toBeInTheDocument();
    expect(within(dialog).getByRole("radio", { name: "New album" })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Iceland 2024");
    expect(props.onPick).toHaveBeenCalledTimes(1);
  });

  it("reloads the albums and resets the name each time it opens", async () => {
    const { props, rerender } = renderPicker();
    await loadedDialog();
    expect(mockApi.listAlbums).toHaveBeenCalledTimes(1);

    rerender(<AlbumPickerDialog {...props} open={false} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    rerender(<AlbumPickerDialog {...props} open defaultName="Tokyo Winter" />);
    const dialog = await loadedDialog();

    expect(mockApi.listAlbums).toHaveBeenCalledTimes(2);
    expect(within(dialog).getByRole("textbox", { name: "New album name" })).toHaveValue("Tokyo Winter");
  });

  it("loads nothing while closed", () => {
    renderPicker({ open: false });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mockApi.listAlbums).not.toHaveBeenCalled();
  });

  describe("with many albums", () => {
    const many = Array.from({ length: 8 }, (_, i) => makeAlbum({ id: `a${i + 10}`, name: `Album ${i}` }));

    beforeEach(() => {
      mockApi.listAlbums.mockResolvedValue([...many, family, shared]);
    });

    const names = (dialog: HTMLElement) =>
      within(dialog)
        .getAllByRole("radio")
        .map((r) => r.closest("label")?.textContent);

    it("searches by album name and owner", async () => {
      renderPicker();
      const dialog = await loadedDialog();
      const search = within(dialog).getByRole("searchbox", { name: "Search albums" });

      await userEvent.type(search, "bob");
      expect(names(dialog)).toEqual(["Road tripby Bob", "New album"]);
      await userEvent.clear(search);
      await userEvent.type(search, "xyz");
      expect(within(dialog).getByText("No matching albums")).toBeInTheDocument();
      expect(names(dialog)).toEqual(["New album"]);
    });

    it("keeps the chosen album listed, and Enter in the search does not submit", async () => {
      const { props } = renderPicker({ initialAlbumId: "a1" });
      const dialog = await loadedDialog();
      expect(within(dialog).getByRole("radio", { name: "Family" })).toBeChecked();

      await userEvent.type(within(dialog).getByRole("searchbox", { name: "Search albums" }), "album 2{Enter}");
      expect(names(dialog)).toEqual(["Album 2", "Family", "New album"]);
      expect(props.onPick).not.toHaveBeenCalled();
      expect(mockApi.createAlbum).not.toHaveBeenCalled();
    });
  });

  it("prefers the initial album over a name match", async () => {
    mockApi.listAlbums.mockResolvedValue([family, makeAlbum({ id: "a3", name: "Iceland 2024" })]);
    renderPicker({ initialAlbumId: "a1" });
    const dialog = await loadedDialog();
    expect(within(dialog).getByRole("radio", { name: "Family" })).toBeChecked();
  });
});
