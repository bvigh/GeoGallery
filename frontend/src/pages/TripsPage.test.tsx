import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "../api/client";
import { TripsPage } from "./TripsPage";
import type { Album, Trip } from "../types";

vi.mock("../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/client")>();
  return {
    ApiError: actual.ApiError,
    api: {
      get: vi.fn(),
      patch: vi.fn(),
      listAlbums: vi.fn(),
      createAlbum: vi.fn(),
      addToAlbum: vi.fn(),
    },
  };
});

vi.mock("@/components/MediaGalleryOverlay", () => ({
  MediaGalleryOverlay: ({ title, params }: { title: string; params: Record<string, string> }) => (
    <div data-testid="gallery" data-title={title} data-params={JSON.stringify(params)} />
  ),
}));

const mockApi = api as unknown as Record<
  "get" | "patch" | "listAlbums" | "createAlbum" | "addToAlbum",
  ReturnType<typeof vi.fn>
>;

const sampleTrips: Trip[] = [
  {
    id: "t1",
    name: "Iceland 2024",
    start_date: "2024-07-01",
    end_date: "2024-07-14",
    media_count: 42,
    cover_key: null,
    user_edited: false,
  },
  {
    id: "t2",
    name: "Tokyo Winter",
    start_date: "2024-12-20",
    end_date: null,
    media_count: 5,
    cover_key: null,
    user_edited: false,
  },
];

function makeAlbum(overrides: Partial<Album>): Album {
  return {
    id: "a1",
    name: "Family",
    owner_id: "u-me",
    owner_name: "Me",
    is_owner: true,
    member_count: 2,
    media_count: 3,
    start_date: null,
    end_date: null,
    created_at: "2026-01-01T00:00:00Z",
    last_added_at: null,
    ...overrides,
  };
}

const family = makeAlbum({ id: "a1", name: "Family" });

// Cards render in API order: Iceland 2024 first.
async function openPickerForIceland() {
  await screen.findByText("Iceland 2024");
  await userEvent.click(screen.getAllByRole("button", { name: "Add to album…" })[0]);
  const dialog = await screen.findByRole("dialog", { name: 'Add "Iceland 2024" to an album' });
  await within(dialog).findByRole("radio", { name: "New album" });
  return dialog;
}

describe("TripsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.get.mockResolvedValue(sampleTrips);
    mockApi.patch.mockResolvedValue({});
    mockApi.listAlbums.mockResolvedValue([family]);
    mockApi.createAlbum.mockResolvedValue(makeAlbum({ id: "a-new", name: "Iceland 2024" }));
    mockApi.addToAlbum.mockResolvedValue({ added: 42 });
  });

  it("displays trip names after API data loads", async () => {
    render(<TripsPage />);
    expect(await screen.findByText("Iceland 2024")).toBeInTheDocument();
    expect(screen.getByText("Tokyo Winter")).toBeInTheDocument();
  });

  it("clicking Edit activates edit mode for that trip", async () => {
    render(<TripsPage />);
    await screen.findByText("Iceland 2024");

    const editButtons = screen.getAllByRole("button", { name: "Edit" });
    await userEvent.click(editButtons[0]);

    const input = screen.getByDisplayValue("Iceland 2024");
    expect(input).toBeInTheDocument();
  });

  it("opens the gallery for a trip's media", async () => {
    render(<TripsPage />);
    await userEvent.click(await screen.findByText("Tokyo Winter"));

    const gallery = screen.getByTestId("gallery");
    expect(gallery).toHaveAttribute("data-title", "Tokyo Winter");
    expect(JSON.parse(gallery.getAttribute("data-params")!)).toEqual({ trip_id: "t2" });
  });

  it("opens the album picker for a trip without opening its gallery", async () => {
    render(<TripsPage />);
    const dialog = await openPickerForIceland();

    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
    expect(mockApi.listAlbums).toHaveBeenCalledTimes(1);
    expect(within(dialog).getByRole("textbox", { name: "New album name" })).toHaveValue("Iceland 2024");
    // Clicks inside the dialog must not reach the card either.
    await userEvent.click(await within(dialog).findByRole("radio", { name: "Family" }));
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });

  it("adds the trip to the chosen album and reports the count", async () => {
    render(<TripsPage />);
    const dialog = await openPickerForIceland();

    await userEvent.click(await within(dialog).findByRole("radio", { name: "Family" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(mockApi.addToAlbum).toHaveBeenCalledWith("a1", { tripId: "t1" });
    expect(mockApi.createAlbum).not.toHaveBeenCalled();
    expect(await screen.findByRole("status")).toHaveTextContent('Added 42 items to "Family"');
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });

  it("creates an album named after the trip and adds the trip to it", async () => {
    mockApi.listAlbums.mockResolvedValue([]);
    mockApi.addToAlbum.mockResolvedValue({ added: 1 });
    render(<TripsPage />);
    const dialog = await openPickerForIceland();

    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Iceland 2024");
    expect(mockApi.addToAlbum).toHaveBeenCalledWith("a-new", { tripId: "t1" });
    expect(await screen.findByRole("status")).toHaveTextContent('Added 1 item to "Iceland 2024"');
  });

  it("says so when the album already had everything", async () => {
    mockApi.addToAlbum.mockResolvedValue({ added: 0 });
    render(<TripsPage />);
    const dialog = await openPickerForIceland();

    await userEvent.click(await within(dialog).findByRole("radio", { name: "Family" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(await screen.findByRole("status")).toHaveTextContent(
      'Added 0 items to "Family": everything in the trip is already in it'
    );
  });

  it.each([
    {
      name: "a missing album or trip",
      err: new ApiError("POST /albums/a1/media failed: 404", 404),
      want: 'Could not add "Iceland 2024" to "Family": the album or the trip is no longer available',
    },
    {
      name: "any other failure",
      err: new ApiError("POST /albums/a1/media failed: 500", 500),
      want: 'Could not add "Iceland 2024" to "Family"',
    },
  ])("shows an error for $name", async ({ err, want }) => {
    mockApi.addToAlbum.mockRejectedValue(err);
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<TripsPage />);
    const dialog = await openPickerForIceland();

    await userEvent.click(await within(dialog).findByRole("radio", { name: "Family" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect((await screen.findByRole("alert")).textContent).toBe(want);
  });

  it("shows the add as pending until it settles, then lets it be dismissed", async () => {
    let resolve!: (v: { added: number }) => void;
    mockApi.addToAlbum.mockReturnValue(new Promise((r) => (resolve = r)));
    render(<TripsPage />);
    const dialog = await openPickerForIceland();

    await userEvent.click(await within(dialog).findByRole("radio", { name: "Family" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent('Adding "Iceland 2024" to "Family"…');
    expect(within(status).queryByRole("button", { name: "Dismiss" })).not.toBeInTheDocument();

    resolve({ added: 42 });
    expect(await screen.findByText('Added 42 items to "Family"')).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("closing the picker adds nothing", async () => {
    render(<TripsPage />);
    const dialog = await openPickerForIceland();

    await userEvent.click(within(dialog).getByRole("button", { name: "Close" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mockApi.addToAlbum).not.toHaveBeenCalled();
    expect(mockApi.createAlbum).not.toHaveBeenCalled();
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });
});
