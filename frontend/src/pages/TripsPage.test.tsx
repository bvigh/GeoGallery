import type { ComponentProps } from "react";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "../api/client";
import { BACK_DEPTH_KEY } from "@/lib/history";
import { TripsPage } from "./TripsPage";
import type * as Gallery from "@/components/MediaGalleryOverlay";
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

// The browser's back (-1) or forward (1) button: resolves once the popstate has been handled.
async function traverse(delta: number) {
  await act(async () => {
    const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
    window.history.go(delta);
    await popped;
  });
}

// Lets a traversal the history module started (leaving an entry nobody owns) land.
async function settleHistory() {
  await act(async () => {
    for (let round = 0; round < 12; round++) await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

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
    gallery.props = null;
    window.history.replaceState(null, "", "/trips");
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

    const el = screen.getByTestId("gallery");
    expect(el).toHaveAttribute("data-title", "Tokyo Winter");
    expect(JSON.parse(el.getAttribute("data-params")!)).toEqual({ trip_id: "t2" });
    // In the URL, on its first item.
    expect(gallery.props!.route).toEqual({ tab: "trips", collectionId: "t2" });
    expect(gallery.props!.initialItemId).toBeNull();

    act(() => gallery.props!.onClose());
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });

  describe("gallery from the URL", () => {
    const TRIP = "0b6f3c1e-5d1a-4c47-9a8e-2f0d7b9c4e11";
    const ITEM = "7c2e9d40-1b3a-4f6e-8d5c-a9e1f0b2c3d4";
    const named: Trip = { ...sampleTrips[0], id: TRIP, name: "Named trip", user_edited: true };

    it("opens at once on the URL's item, and names the trip once the list is in", async () => {
      let resolve!: (trips: Trip[]) => void;
      mockApi.get.mockReturnValue(new Promise((r) => (resolve = r)));
      window.history.replaceState(null, "", `/trips/${TRIP}?item=${ITEM}`);
      render(<TripsPage />);

      const el = screen.getByTestId("gallery");
      expect(el).toHaveAttribute("data-title", "");
      expect(JSON.parse(el.getAttribute("data-params")!)).toEqual({ trip_id: TRIP });
      expect(gallery.props!.route).toEqual({ tab: "trips", collectionId: TRIP });
      expect(gallery.props!.initialItemId).toBe(ITEM);

      await act(async () => resolve([...sampleTrips, named]));
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-title", "Named trip");
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("closes and says why when the trip is not in the caller's list", async () => {
      window.history.replaceState(null, "", `/trips/${TRIP}?item=${ITEM}`);
      render(<TripsPage />);
      expect(screen.getByTestId("gallery")).toBeInTheDocument();

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "That trip is not available. Trips you have not edited get new links after each upload."
      );
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
      expect(screen.getByText("Iceland 2024")).toBeInTheDocument();
    });

    it("stays open, without a name, when the list cannot be loaded", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.get.mockRejectedValue(new Error("offline"));
      window.history.replaceState(null, "", `/trips/${TRIP}`);
      render(<TripsPage />);
      await act(async () => {});
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-title", "");
      expect(gallery.props!.initialItemId).toBeNull();
    });

    // The gallery's entry after back closed it. The mocked gallery owns no entry, so the history
    // module leaves it again afterwards; the real one takes it over (history.test.tsx).
    async function forwardOntoGalleryEntry() {
      window.history.pushState({ [BACK_DEPTH_KEY]: 1 }, "", `/trips/${TRIP}?item=${ITEM}`);
      await traverse(-1);
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
      await traverse(1);
    }

    it("opens again on its item when forward lands on its entry", async () => {
      mockApi.get.mockResolvedValue([...sampleTrips, named]);
      render(<TripsPage />);
      await screen.findByText("Named trip");

      await forwardOntoGalleryEntry();
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-title", "Named trip");
      expect(gallery.props!.route).toEqual({ tab: "trips", collectionId: TRIP });
      expect(gallery.props!.initialItemId).toBe(ITEM);
      await settleHistory();
    });

    it("says why when forward lands on the entry of a trip that is gone", async () => {
      render(<TripsPage />);
      await screen.findByText("Iceland 2024");

      await forwardOntoGalleryEntry();
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
      expect(screen.getByRole("alert")).toHaveTextContent("That trip is not available.");
      await settleHistory();
    });
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

  it("cancels an edit without saving, by button or Escape", async () => {
    render(<TripsPage />);
    await screen.findByText("Iceland 2024");

    await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]);
    await userEvent.clear(screen.getByRole("textbox", { name: "Trip name" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Trip name" }), "Renamed");
    // A phone has no Escape key, so the button is the way out.
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("textbox", { name: "Trip name" })).not.toBeInTheDocument();
    expect(screen.getByText("Iceland 2024")).toBeInTheDocument();
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]);
    await userEvent.type(screen.getByRole("textbox", { name: "Trip name" }), "{Escape}");
    expect(screen.queryByRole("textbox", { name: "Trip name" })).not.toBeInTheDocument();
    expect(mockApi.patch).not.toHaveBeenCalled();
  });

  it("labels the edit fields and saves them", async () => {
    render(<TripsPage />);
    await screen.findByText("Iceland 2024");
    await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]);

    expect(screen.getByLabelText("Start date")).toHaveValue("2024-07-01");
    expect(screen.getByLabelText("End date")).toHaveValue("2024-07-14");
    // iOS zooms into a focused field with text under 16px and stays zoomed.
    expect(screen.getByRole("textbox", { name: "Trip name" })).toHaveClass("touch:text-base");

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mockApi.patch).toHaveBeenCalledWith("/trips/t1", {
      name: "Iceland 2024",
      start_date: "2024-07-01",
      end_date: "2024-07-14",
    });
  });

  it("makes the card actions finger-sized on touch", async () => {
    render(<TripsPage />);
    await screen.findByText("Iceland 2024");
    for (const name of ["Add to album…", "Edit"]) {
      expect(screen.getAllByRole("button", { name })[0]).toHaveClass("touch:min-h-11", "touch:min-w-11");
    }
  });
});
