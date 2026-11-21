import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { STORAGE_FAILED, STORAGE_TIMEOUT, StorageReport } from "@/components/admin/StorageReport";
import type { AdminStorage, AdminStorageUser } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ApiError: actual.ApiError, api: { adminStorage: vi.fn() } };
});

const mockApi = api as unknown as { adminStorage: ReturnType<typeof vi.fn> };

const bob: AdminStorageUser = {
  id: "u-bob",
  display_name: null,
  email: "bob@example.test",
  disabled: true,
  claimed: true,
  items: 2,
  bytes: 30_000_000_000,
  unknown_items: 0,
  track_points: 1,
};

const alice: AdminStorageUser = {
  id: "u-alice",
  display_name: "Alice",
  email: "alice@example.test",
  disabled: false,
  claimed: true,
  items: 5,
  bytes: 4_500_000,
  unknown_items: 2,
  track_points: 6_246_825,
};

const owner: AdminStorageUser = {
  id: "00000000-0000-0000-0000-000000000001",
  display_name: "Owner",
  email: null,
  disabled: false,
  claimed: false,
  items: 0,
  bytes: 0,
  unknown_items: 0,
  track_points: 0,
};

const report: AdminStorage = {
  items: 7,
  bytes: 30_004_500_000,
  unknown_items: 2,
  track_points: 6_246_826,
  users: [bob, alice, owner],
};

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const showButton = () => screen.getByRole("button", { name: "Show storage use" });

async function renderLoaded(r: AdminStorage = report) {
  mockApi.adminStorage.mockResolvedValue(r);
  render(<StorageReport />);
  await userEvent.click(showButton());
  return screen.findByRole("table", { name: "Storage per user" });
}

describe("StorageReport", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  it("counts nothing until asked, and says what it shows and why", () => {
    render(<StorageReport />);
    expect(screen.getByRole("heading", { name: "Storage" })).toBeInTheDocument();
    const intro = screen.getByText(/How much storage each user's items take/);
    expect(intro).toHaveTextContent("Each user sees how theirs split by trip and album in their Settings.");
    expect(intro).toHaveTextContent("Counting reads every track point, which can take a few seconds");
    expect(showButton()).toHaveClass("touch:min-h-11");
    expect(mockApi.adminStorage).not.toHaveBeenCalled();
  });

  it("counts once however often the button is pressed, keeping the focus on it", async () => {
    const pending = deferred<AdminStorage>();
    mockApi.adminStorage.mockReturnValue(pending.promise);
    render(<StorageReport />);
    const button = showButton();
    await userEvent.click(button);

    expect(button).toHaveTextContent("Counting…");
    expect(button).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("status")).toHaveTextContent("Counting the storage use…");
    await userEvent.click(button);
    expect(mockApi.adminStorage).toHaveBeenCalledTimes(1);

    await act(async () => pending.resolve(report));
    expect(button).toHaveTextContent("Count again");
    expect(button).toHaveAttribute("aria-disabled", "false");
    expect(button).toHaveFocus();
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("gives the totals and says how many sizes are unknown and how to read them", async () => {
    await renderLoaded();
    expect(screen.getByText("In all: 7 items, 30 GB + 2 unknown, 6,246,826 track points")).toBeInTheDocument();
    expect(screen.getByText(/The size of 2 items is unknown: they were stored before sizes were recorded./)).toHaveTextContent(
      "Run server backfill-sizes on the server"
    );
  });

  it("leaves the notice out when every size is known", async () => {
    await renderLoaded({ ...report, unknown_items: 0, users: [bob] });
    expect(screen.queryByText(/is unknown/)).not.toBeInTheDocument();
  });

  it("lists every user's totals, largest first, with what a phone folds into the name cell", async () => {
    const table = await renderLoaded();
    const rows = within(table).getAllByRole("row").slice(1) as HTMLTableRowElement[];
    expect(rows.map((r) => r.cells[0].firstChild?.textContent)).toEqual(["bob@example.test", "Alice", "Owner"]);

    const [b, a, o] = rows;
    expect(within(b.cells[0]).getByText("(disabled)")).toBeInTheDocument();
    expect(within(b.cells[0]).getByText("2 items · 30 GB · 1 track point")).toHaveClass("sm:hidden");
    expect(Array.from(b.cells).slice(1).map((c) => c.textContent)).toEqual(["2", "30 GB", "1"]);
    for (const cell of Array.from(b.cells).slice(1)) expect(cell).toHaveClass("hidden", "sm:table-cell");

    expect(Array.from(a.cells).slice(1).map((c) => c.textContent)).toEqual(["5", "4.5 MB + 2 unknown", "6,246,825"]);
    expect(within(o.cells[0]).getByText("(unclaimed)")).toBeInTheDocument();
    expect(screen.getAllByRole("button")).toHaveLength(1);
  });

  it("says when counting took too long, and counts again when asked", async () => {
    mockApi.adminStorage.mockRejectedValueOnce(new ApiError("GET /admin/storage failed: 504", 504));
    render(<StorageReport />);
    await userEvent.click(showButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(STORAGE_TIMEOUT);

    mockApi.adminStorage.mockResolvedValueOnce(report);
    await userEvent.click(showButton());
    await screen.findByRole("table", { name: "Storage per user" });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("says when counting failed", async () => {
    mockApi.adminStorage.mockRejectedValueOnce(new ApiError("GET /admin/storage failed: 500", 500));
    render(<StorageReport />);
    await userEvent.click(showButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(STORAGE_FAILED);
  });

  it("keeps the last count on screen, dimmed, while counting again", async () => {
    await renderLoaded();
    const pending = deferred<AdminStorage>();
    mockApi.adminStorage.mockReturnValue(pending.promise);
    fireEvent.click(screen.getByRole("button", { name: "Count again" }));

    expect(screen.getByText(/^In all:/).parentElement).toHaveClass("opacity-60");
    await act(async () => pending.resolve({ ...report, items: 8 }));
    expect(screen.getByText(/^In all: 8 items/).parentElement).not.toHaveClass("opacity-60");
  });

  it("cancels the count when the page goes", async () => {
    mockApi.adminStorage.mockReturnValue(new Promise(() => {}));
    const { unmount } = render(<StorageReport />);
    await userEvent.click(showButton());
    const signal: AbortSignal = mockApi.adminStorage.mock.calls[0][0];
    expect(signal.aborted).toBe(false);
    unmount();
    expect(signal.aborted).toBe(true);
  });
});
