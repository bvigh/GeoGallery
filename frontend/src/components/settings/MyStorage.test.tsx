import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { MY_STORAGE_FAILED, MyStorage } from "@/components/settings/MyStorage";
import type { MyStorage as MyStorageReport } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ApiError: actual.ApiError, api: { myStorage: vi.fn() } };
});

const mockApi = api as unknown as { myStorage: ReturnType<typeof vi.fn> };

const storage: MyStorageReport = {
  items: 5,
  bytes: 4_500_000,
  unknown_items: 2,
  trips: [
    {
      id: "t-lisbon",
      name: "Lisbon",
      start_date: "2024-05-01",
      end_date: "2024-05-03",
      user_edited: true,
      items: 2,
      bytes: 4_000_000,
      unknown_items: 0,
    },
    {
      id: "t-june",
      name: "Trip 2024-06-01",
      start_date: "2024-06-01",
      end_date: "2024-06-01",
      user_edited: false,
      items: 1,
      bytes: 0,
      unknown_items: 1,
    },
  ],
  no_trip: { items: 2, bytes: 500_000, unknown_items: 1 },
  albums: [
    { id: "a-family", name: "Family", owner_id: "me", owner_name: "Me", is_owner: true, items: 2, bytes: 1_000_000, unknown_items: 1 },
    { id: "a-hikes", name: "Hikes", owner_id: "u-bob", owner_name: "Bob", is_owner: false, items: 1, bytes: 1_000_000, unknown_items: 0 },
    { id: "a-old", name: "Old", owner_id: "u-gone", owner_name: null, is_owner: false, items: 1, bytes: 10, unknown_items: 0 },
  ],
};

// What each cell of a table's body rows starts with: its own text, before what a phone folds into it.
function cellTexts(table: HTMLElement): (string | null | undefined)[][] {
  const rows = within(table).getAllByRole("row").slice(1) as HTMLTableRowElement[];
  return rows.map((r) => Array.from(r.cells).map((c) => c.firstChild?.textContent));
}

async function renderLoaded(s: MyStorageReport = storage) {
  mockApi.myStorage.mockResolvedValue(s);
  render(<MyStorage />);
  await screen.findByText(/^Your library:/);
}

describe("MyStorage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  it("loads at once and says that shared items count for their owners", async () => {
    mockApi.myStorage.mockReturnValue(new Promise(() => {}));
    render(<MyStorage />);
    expect(screen.getByRole("heading", { name: "Storage" })).toBeInTheDocument();
    expect(screen.getByText(/How much storage your items take, in all, per trip and per album./)).toHaveTextContent(
      "Items others share with you count for them, not for you."
    );
    expect(screen.getByText("Loading your storage use…")).toBeInTheDocument();
    expect(mockApi.myStorage).toHaveBeenCalledTimes(1);
  });

  it("gives the caller's totals and says how many sizes are unknown", async () => {
    await renderLoaded();
    expect(screen.getByText("Your library: 5 items, 4.5 MB + 2 unknown")).toBeInTheDocument();
    const note = screen.getByText(/The size of 2 items is unknown: they were stored before sizes were recorded./);
    expect(note).toHaveTextContent("until an administrator reads their sizes.");
    expect(note).not.toHaveTextContent("backfill");
  });

  it("leaves the notice out when every size is known", async () => {
    await renderLoaded({ ...storage, unknown_items: 0 });
    expect(screen.queryByText(/is unknown/)).not.toBeInTheDocument();
  });

  it("opens and closes the trips, with the items in none", async () => {
    await renderLoaded();
    const button = screen.getByRole("button", { name: "By trip (2)" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).not.toHaveAttribute("aria-controls");
    expect(button).toHaveClass("touch:min-h-11");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();

    await userEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    const table = screen.getByRole("table", { name: "Your storage by trip" });
    expect(button).toHaveAttribute("aria-controls", table.parentElement!.id);
    expect(cellTexts(table)).toEqual([
      ["Lisbon", "2024-05-01 – 2024-05-03", "2", "4 MB"],
      ["Trip 2024-06-01", "2024-06-01", "1", "Unknown"],
      ["Not in a trip", undefined, "2", "500 kB + 1 unknown"],
    ]);
    expect(within(table).getByText("2024-05-01 – 2024-05-03 · 2 items · 4 MB")).toHaveClass("sm:hidden");

    await userEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("opens the albums with the caller's own items in each, and whose each album is", async () => {
    await renderLoaded();
    const button = screen.getByRole("button", { name: "By album (3)" });
    await userEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/Only your own items in each album count here./)).toHaveTextContent(
      "albums hold no copies, so they take no space of their own."
    );
    const table = screen.getByRole("table", { name: "Your storage by album" });
    expect(cellTexts(table)).toEqual([
      ["Family", "You", "2", "1 MB + 1 unknown"],
      ["Hikes", "Bob", "1", "1 MB"],
      ["Old", "Unnamed user", "1", "10 B"],
    ]);
    expect(within(table).getByText("Bob · 1 item · 1 MB")).toHaveClass("sm:hidden");
    expect(screen.getByRole("button", { name: "By trip (2)" })).toHaveAttribute("aria-expanded", "false");
  });

  it("shows one breakdown at a time, right below its button", async () => {
    await renderLoaded();
    const byTrip = screen.getByRole("button", { name: "By trip (2)" });
    const byAlbum = screen.getByRole("button", { name: "By album (3)" });
    await userEvent.click(byTrip);
    await userEvent.click(byAlbum);
    expect(byAlbum).toHaveAttribute("aria-expanded", "true");
    expect(byTrip).toHaveAttribute("aria-expanded", "false");
    expect(byTrip).not.toHaveAttribute("aria-controls");
    expect(screen.getAllByRole("table").map((t) => t.getAttribute("aria-label"))).toEqual(["Your storage by album"]);

    await userEvent.click(byTrip);
    expect(screen.getAllByRole("table").map((t) => t.getAttribute("aria-label"))).toEqual(["Your storage by trip"]);
    expect(byAlbum).toHaveAttribute("aria-expanded", "false");
  });

  it("says when none of the caller's items is in an album", async () => {
    await renderLoaded({ ...storage, albums: [] });
    await userEvent.click(screen.getByRole("button", { name: "By album (0)" }));
    expect(screen.getByText("None of your items is in an album.")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("says so for an empty library, without the breakdown", async () => {
    mockApi.myStorage.mockResolvedValue({
      items: 0,
      bytes: 0,
      unknown_items: 0,
      trips: [],
      no_trip: { items: 0, bytes: 0, unknown_items: 0 },
      albums: [],
    });
    render(<MyStorage />);
    expect(await screen.findByText("You have no items yet.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("says when it cannot load, and tries again", async () => {
    mockApi.myStorage.mockRejectedValueOnce(new ApiError("GET /me/storage failed: 500", 500));
    render(<MyStorage />);
    expect(await screen.findByText(MY_STORAGE_FAILED)).toBeInTheDocument();

    mockApi.myStorage.mockResolvedValueOnce(storage);
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText(/^Your library:/)).toBeInTheDocument();
    expect(screen.queryByText(MY_STORAGE_FAILED)).not.toBeInTheDocument();
    expect(mockApi.myStorage).toHaveBeenCalledTimes(2);
  });

  it("cancels the request when the page goes", () => {
    mockApi.myStorage.mockReturnValue(new Promise(() => {}));
    const { unmount } = render(<MyStorage />);
    const signal: AbortSignal = mockApi.myStorage.mock.calls[0][0];
    expect(signal.aborted).toBe(false);
    unmount();
    expect(signal.aborted).toBe(true);
  });
});
