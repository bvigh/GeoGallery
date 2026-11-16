import type { ComponentProps } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "../api/client";
import { BACK_DEPTH_KEY } from "@/lib/history";
import { readLastViewed, writeLastViewed } from "@/lib/lastViewed";
import { TripsPage } from "./TripsPage";
import { AuthContext } from "@/components/auth/AuthContext";
import type * as Gallery from "@/components/MediaGalleryOverlay";
import { FOLLOWUP_FAILED_SHIFT, SHIFT_OUTCOME_UNKNOWN } from "@/lib/timeEdit";
import type { Album, CurrentUser, MediaItem, Trip } from "../types";

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
      // The real Checks view.
      checks: vi.fn(),
      checksAgreement: vi.fn(),
      // The real shift dialog.
      listMedia: vi.fn(),
      shiftTimes: vi.fn(),
    },
  };
});

type GalleryProps = ComponentProps<typeof Gallery.MediaGalleryOverlay>;

// The real overlay needs Leaflet and media URLs; the page only decides its props. With ownsEntry
// set, it holds a history entry showing its route, as the real one does (on the item it was opened
// on), and openNested opens a view without a URL over it (the evidence view).
const gallery = vi.hoisted(() => ({
  props: null as GalleryProps | null,
  ownsEntry: false,
  openNested: null as (() => void) | null,
}));
vi.mock("@/components/MediaGalleryOverlay", async () => {
  const { useState } = await import("react");
  const { useCloseOnBack } = await import("@/lib/history");
  const { galleryRoute, routeUrl } = await import("@/lib/routes");
  return {
    MediaGalleryOverlay: (props: GalleryProps) => {
      gallery.props = props;
      const [nested, setNested] = useState(false);
      gallery.openNested = () => setNested(true);
      const { route } = props;
      const url = route ? routeUrl(galleryRoute(route.tab, route.collectionId, props.initialItemId ?? null)) : null;
      useCloseOnBack(props.onClose, gallery.ownsEntry, url);
      useCloseOnBack(() => setNested(false), nested);
      return (
        <div
          data-testid="gallery"
          data-title={props.title}
          data-params={JSON.stringify(props.params)}
          data-nested={String(nested)}
        />
      );
    },
  };
});

const mockApi = api as unknown as Record<
  "get" | "patch" | "listAlbums" | "createAlbum" | "addToAlbum" | "checks" | "listMedia" | "shiftTimes",
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

const me: CurrentUser = { id: "u-me", email: "me@example.test", display_name: "Me", role: "user" };

// jsdom has no scrollIntoView.
const scrollIntoView = vi.fn();

function renderPage() {
  return render(
    <AuthContext.Provider value={me}>
      <TripsPage />
    </AuthContext.Provider>
  );
}

function card(name: string): HTMLElement {
  return screen.getByRole("article", { name });
}

function storageUnavailable() {
  vi.spyOn(window, "sessionStorage", "get").mockImplementation(() => {
    throw new DOMException("The operation is insecure.", "SecurityError");
  });
}

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

function deferredTrips() {
  let resolve!: (trips: Trip[]) => void;
  const promise = new Promise<Trip[]>((r) => (resolve = r));
  return { promise, resolve };
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
    gallery.ownsEntry = false;
    gallery.openNested = null;
    window.history.replaceState(null, "", "/trips");
    window.sessionStorage.clear();
    Element.prototype.scrollIntoView = scrollIntoView;
    mockApi.get.mockResolvedValue(sampleTrips);
    mockApi.patch.mockResolvedValue({});
    mockApi.listAlbums.mockResolvedValue([family]);
    mockApi.createAlbum.mockResolvedValue(makeAlbum({ id: "a-new", name: "Iceland 2024" }));
    mockApi.addToAlbum.mockResolvedValue({ added: 42 });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    delete (Element.prototype as Partial<Element>).scrollIntoView;
  });

  it("displays trip names after API data loads", async () => {
    renderPage();
    expect(await screen.findByText("Iceland 2024")).toBeInTheDocument();
    expect(screen.getByText("Tokyo Winter")).toBeInTheDocument();
  });

  it("clicking Edit activates edit mode for that trip", async () => {
    renderPage();
    await screen.findByText("Iceland 2024");

    const editButtons = screen.getAllByRole("button", { name: "Edit" });
    await userEvent.click(editButtons[0]);

    const input = screen.getByDisplayValue("Iceland 2024");
    expect(input).toBeInTheDocument();
  });

  it("opens the gallery for a trip's media", async () => {
    renderPage();
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

  describe("last viewed trip", () => {
    it("marks the trip whose gallery closed last and scrolls it into view", async () => {
      renderPage();
      await userEvent.click(await screen.findByText("Tokyo Winter"));
      expect(screen.queryByRole("article", { current: true })).not.toBeInTheDocument();

      act(() => gallery.props!.onClose());
      expect(screen.getByRole("article", { current: true })).toBe(card("Tokyo Winter"));
      expect(card("Tokyo Winter")).toHaveClass("ring-2", "ring-primary");
      expect(card("Iceland 2024")).not.toHaveClass("ring-2");
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
      expect(scrollIntoView.mock.contexts[0]).toBe(card("Tokyo Winter"));

      await userEvent.click(screen.getByText("Iceland 2024"));
      act(() => gallery.props!.onClose());
      expect(screen.getByRole("article", { current: true })).toBe(card("Iceland 2024"));
      expect(scrollIntoView.mock.contexts[1]).toBe(card("Iceland 2024"));
    });

    // A tab switch unmounts the page, and a reload mounts it again too.
    it("keeps the mark when the page mounts again", async () => {
      const { unmount } = renderPage();
      await userEvent.click(await screen.findByText("Tokyo Winter"));
      act(() => gallery.props!.onClose());
      unmount();
      scrollIntoView.mockClear();

      renderPage();
      await screen.findByText("Tokyo Winter");
      expect(screen.getByRole("article", { current: true })).toBe(card("Tokyo Winter"));
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView.mock.contexts[0]).toBe(card("Tokyo Winter"));
    });

    it("marks a trip whose ID is gone from the list on no card", async () => {
      writeLastViewed(me.id, "trips", "t-regrouped");
      renderPage();
      await screen.findByText("Tokyo Winter");
      expect(screen.queryByRole("article", { current: true })).not.toBeInTheDocument();
      expect(scrollIntoView).not.toHaveBeenCalled();
    });

    it("marks the trip only until the page unmounts when storage is unavailable", async () => {
      storageUnavailable();
      const { unmount } = renderPage();
      await userEvent.click(await screen.findByText("Tokyo Winter"));
      act(() => gallery.props!.onClose());
      expect(screen.getByRole("article", { current: true })).toBe(card("Tokyo Winter"));
      unmount();

      renderPage();
      await screen.findByText("Tokyo Winter");
      expect(screen.queryByRole("article", { current: true })).not.toBeInTheDocument();
    });
  });

  describe("gallery from the URL", () => {
    const TRIP = "0b6f3c1e-5d1a-4c47-9a8e-2f0d7b9c4e11";
    const ITEM = "7c2e9d40-1b3a-4f6e-8d5c-a9e1f0b2c3d4";
    const named: Trip = { ...sampleTrips[0], id: TRIP, name: "Named trip", user_edited: true };

    it("opens at once on the URL's item, and names the trip once the list is in", async () => {
      let resolve!: (trips: Trip[]) => void;
      mockApi.get.mockReturnValue(new Promise((r) => (resolve = r)));
      window.history.replaceState(null, "", `/trips/${TRIP}?item=${ITEM}`);
      renderPage();

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
      renderPage();
      expect(screen.getByTestId("gallery")).toBeInTheDocument();

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "That trip is not available. Trips you have not edited get new links after each upload or time change."
      );
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
      expect(screen.getByText("Iceland 2024")).toBeInTheDocument();
      expect(screen.queryByRole("article", { current: true })).not.toBeInTheDocument();
      expect(readLastViewed(me.id, "trips")).toBeNull();
    });

    it("scrolls to the last viewed trip only once it closes, and then marks its own", async () => {
      writeLastViewed(me.id, "trips", "t2");
      mockApi.get.mockResolvedValue([...sampleTrips, named]);
      window.history.replaceState(null, "", `/trips/${TRIP}?item=${ITEM}`);
      renderPage();
      await screen.findByText("Named trip", { selector: "h2" });
      expect(screen.getByRole("article", { current: true })).toBe(card("Tokyo Winter"));
      expect(scrollIntoView).not.toHaveBeenCalled();

      act(() => gallery.props!.onClose());
      expect(screen.getByRole("article", { current: true })).toBe(card("Named trip"));
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView.mock.contexts[0]).toBe(card("Named trip"));
      expect(readLastViewed(me.id, "trips")).toBe(TRIP);
    });

    it("stays open, without a name, when the list cannot be loaded", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.get.mockRejectedValue(new Error("offline"));
      window.history.replaceState(null, "", `/trips/${TRIP}`);
      renderPage();
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
      renderPage();
      await screen.findByText("Named trip");

      await forwardOntoGalleryEntry();
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-title", "Named trip");
      expect(gallery.props!.route).toEqual({ tab: "trips", collectionId: TRIP });
      expect(gallery.props!.initialItemId).toBe(ITEM);
      await settleHistory();
    });

    it("says why when forward lands on the entry of a trip that is gone", async () => {
      renderPage();
      await screen.findByText("Iceland 2024");

      await forwardOntoGalleryEntry();
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
      expect(screen.getByRole("alert")).toHaveTextContent("That trip is not available.");
      await settleHistory();
    });
  });

  it("opens the album picker for a trip without opening its gallery", async () => {
    renderPage();
    const dialog = await openPickerForIceland();

    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
    expect(mockApi.listAlbums).toHaveBeenCalledTimes(1);
    expect(within(dialog).getByRole("textbox", { name: "New album name" })).toHaveValue("Iceland 2024");
    // Clicks inside the dialog must not reach the card either.
    await userEvent.click(await within(dialog).findByRole("radio", { name: "Family" }));
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });

  it("adds the trip to the chosen album and reports the count", async () => {
    renderPage();
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
    renderPage();
    const dialog = await openPickerForIceland();

    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Iceland 2024");
    expect(mockApi.addToAlbum).toHaveBeenCalledWith("a-new", { tripId: "t1" });
    expect(await screen.findByRole("status")).toHaveTextContent('Added 1 item to "Iceland 2024"');
  });

  it("says so when the album already had everything", async () => {
    mockApi.addToAlbum.mockResolvedValue({ added: 0 });
    renderPage();
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
    renderPage();
    const dialog = await openPickerForIceland();

    await userEvent.click(await within(dialog).findByRole("radio", { name: "Family" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Add" }));

    expect((await screen.findByRole("alert")).textContent).toBe(want);
  });

  it("shows the add as pending until it settles, then lets it be dismissed", async () => {
    let resolve!: (v: { added: number }) => void;
    mockApi.addToAlbum.mockReturnValue(new Promise((r) => (resolve = r)));
    renderPage();
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
    renderPage();
    const dialog = await openPickerForIceland();

    await userEvent.click(within(dialog).getByRole("button", { name: "Close" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mockApi.addToAlbum).not.toHaveBeenCalled();
    expect(mockApi.createAlbum).not.toHaveBeenCalled();
    expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
  });

  it("cancels an edit without saving, by button or Escape", async () => {
    renderPage();
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
    renderPage();
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

  describe("checks", () => {
    const checksView = () => screen.queryByRole("dialog", { name: "Checks" });

    // A time changed there ran trip detection, which gave every trip nobody edited a new ID.
    it("loads the trips again when the view closes", async () => {
      mockApi.checks.mockReturnValue(new Promise(() => {}));
      renderPage();
      await screen.findByText("Iceland 2024");
      await userEvent.click(screen.getByRole("button", { name: "Checks" }));
      expect(mockApi.get).toHaveBeenCalledTimes(1);

      mockApi.get.mockResolvedValue([{ ...sampleTrips[0], id: "t1-new" }, sampleTrips[1]]);
      await userEvent.keyboard("{Escape}");
      expect(checksView()).not.toBeInTheDocument();
      await waitFor(() => expect(mockApi.get).toHaveBeenCalledTimes(2));
      expect(mockApi.get).toHaveBeenLastCalledWith("/trips");
      // The card now opens the trip under its new ID.
      await userEvent.click(screen.getByText("Iceland 2024"));
      expect(gallery.props!.params).toEqual({ trip_id: "t1-new" });
      await settleHistory();
    });

    it("runs nothing until the header's button opens the view", async () => {
      mockApi.checks.mockReturnValue(new Promise(() => {}));
      renderPage();
      await screen.findByText("Iceland 2024");
      expect(mockApi.checks).not.toHaveBeenCalled();
      expect(checksView()).not.toBeInTheDocument();

      const button = screen.getByRole("button", { name: "Checks" });
      expect(button).toHaveClass("touch:min-h-11");
      await userEvent.click(button);
      expect(checksView()).toBeInTheDocument();
      expect(mockApi.checks).toHaveBeenCalledTimes(1);
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();

      await userEvent.click(within(checksView()!).getByRole("button", { name: "Close" }));
      expect(checksView()).not.toBeInTheDocument();
      // The closed view's history entry is left again.
      await settleHistory();
    });

    it("opens again on a new run each time", async () => {
      mockApi.checks.mockReturnValue(new Promise(() => {}));
      renderPage();
      await screen.findByText("Iceland 2024");
      await userEvent.click(screen.getByRole("button", { name: "Checks" }));
      await userEvent.keyboard("{Escape}");
      expect(checksView()).not.toBeInTheDocument();
      await settleHistory();

      await userEvent.click(screen.getByRole("button", { name: "Checks" }));
      expect(mockApi.checks).toHaveBeenCalledTimes(2);
      expect(mockApi.checks.mock.calls[0][0].aborted).toBe(true);
      await userEvent.keyboard("{Escape}");
      await settleHistory();
    });
  });

  describe("shift times", () => {
    const dialog = () => screen.queryByRole("dialog", { name: 'Shift times in "Iceland 2024"' });

    function photo(id: string, taken_at: string | null): MediaItem {
      return {
        id,
        filename: `${id}.jpg`,
        media_type: "photo",
        lat: null,
        lng: null,
        gps_confidence: null,
        gps_source: null,
        taken_at,
        storage_key: `media/u-me/${id}.jpg`,
        trip_id: "t1",
        owner_id: "u-me",
        owner_name: "Me",
        camera_model: "DC-TZ200",
      };
    }
    const tripItems = [photo("p1", "2024-07-01T10:00:00Z"), photo("p2", "2024-07-02T10:00:00Z")];

    async function openShift() {
      renderPage();
      await screen.findByText("Iceland 2024");
      await userEvent.click(screen.getAllByRole("button", { name: "Shift times…" })[0]);
      expect(dialog()).toBeInTheDocument();
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
    }

    async function shiftOneHour() {
      await userEvent.type(within(dialog()!).getByLabelText("Hours"), "1");
      await userEvent.click(within(dialog()!).getByRole("button", { name: "Shift 2 items" }));
    }

    it("loads the trip's items into the dialog, shifts them and reloads the trips", async () => {
      mockApi.listMedia.mockResolvedValue(tripItems);
      mockApi.shiftTimes.mockResolvedValue({ shifted: 2, undated: 0, trips: [{ id: "t9", items: 2 }], followup_failed: false });
      await openShift();
      expect(mockApi.listMedia).toHaveBeenCalledWith({ trip_id: "t1" }, { signal: expect.any(AbortSignal) });
      expect(await within(dialog()!).findByRole("checkbox", { name: /DC-TZ200/ })).toBeChecked();
      expect(mockApi.get).toHaveBeenCalledTimes(1);

      const regrouped = [{ ...sampleTrips[0], id: "t9" }, sampleTrips[1]];
      mockApi.get.mockResolvedValue(regrouped);
      await shiftOneHour();

      expect(mockApi.shiftTimes).toHaveBeenCalledWith(["p1", "p2"], 3600);
      expect(await screen.findByRole("status")).toHaveTextContent("Shifted 2 items 1 h later.");
      expect(dialog()).not.toBeInTheDocument();
      await waitFor(() => expect(mockApi.get).toHaveBeenCalledTimes(2));
      expect(card("Iceland 2024")).toBeInTheDocument();
    });

    it("says when the follow-up failed", async () => {
      mockApi.listMedia.mockResolvedValue(tripItems);
      mockApi.shiftTimes.mockResolvedValue({ shifted: 2, undated: 0, trips: [{ id: "t1", items: 2 }], followup_failed: true });
      await openShift();
      await within(dialog()!).findByRole("checkbox", { name: /DC-TZ200/ });
      await shiftOneHour();
      expect(await screen.findByRole("alert")).toHaveTextContent(`Shifted 2 items 1 h later. ${FOLLOWUP_FAILED_SHIFT}`);
    });

    // A gateway's 504 after the commit: the shift may have been saved, and trips regrouped.
    it("reloads the trips and says why when the shift's answer did not come", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listMedia.mockResolvedValue(tripItems);
      mockApi.shiftTimes.mockRejectedValue(new ApiError("POST /media/shift-time failed: 504", 504));
      await openShift();
      await within(dialog()!).findByRole("checkbox", { name: /DC-TZ200/ });
      mockApi.get.mockResolvedValue([{ ...sampleTrips[0], id: "t9" }, sampleTrips[1]]);
      await shiftOneHour();

      // Behind the dialog, which hides the page from the accessibility tree.
      expect(await screen.findByRole("alert", { hidden: true })).toHaveTextContent(
        "The server did not answer a shift in time, and it may have been saved. The trips are loaded again: check the items' times before shifting again."
      );
      await waitFor(() => expect(mockApi.get).toHaveBeenCalledTimes(2));
      // The dialog stays open, saying the same, with nothing more to apply.
      expect(within(dialog()!).getByText(SHIFT_OUTCOME_UNKNOWN)).toBeInTheDocument();
      expect(within(dialog()!).getByRole("button", { name: "Shift 2 items" })).toBeDisabled();
      expect(mockApi.shiftTimes).toHaveBeenCalledTimes(1);
      await userEvent.keyboard("{Escape}");
      await settleHistory();
    });

    it("says it is loading, and aborts the list when the dialog closes before it arrives", async () => {
      mockApi.listMedia.mockReturnValue(new Promise(() => {}));
      await openShift();
      expect(within(dialog()!).getByText("Loading items…")).toBeInTheDocument();
      const { signal } = mockApi.listMedia.mock.calls[0][1] as { signal: AbortSignal };
      expect(signal.aborted).toBe(false);

      await userEvent.keyboard("{Escape}");
      expect(dialog()).not.toBeInTheDocument();
      expect(signal.aborted).toBe(true);
      expect(mockApi.shiftTimes).not.toHaveBeenCalled();
      await settleHistory();
    });

    it("says when the items could not be loaded", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listMedia.mockRejectedValue(new ApiError("GET /media failed: 500", 500));
      await openShift();
      expect(await within(dialog()!).findByText("Could not load the trip's items. Close this and try again.")).toBeInTheDocument();
      await userEvent.keyboard("{Escape}");
      await settleHistory();
    });

    it("never shows one trip's items in another trip's dialog", async () => {
      const tokyo = () => screen.queryByRole("dialog", { name: 'Shift times in "Tokyo Winter"' });
      mockApi.listMedia.mockResolvedValue(tripItems);
      await openShift();
      await within(dialog()!).findByRole("checkbox", { name: /DC-TZ200/ });
      await userEvent.keyboard("{Escape}");
      await settleHistory();

      mockApi.listMedia.mockReturnValue(new Promise(() => {}));
      await userEvent.click(screen.getAllByRole("button", { name: "Shift times…" })[1]);
      expect(within(tokyo()!).getByText("Loading items…")).toBeInTheDocument();
      expect(within(tokyo()!).queryByRole("checkbox")).not.toBeInTheDocument();
      expect(mockApi.listMedia).toHaveBeenLastCalledWith({ trip_id: "t2" }, { signal: expect.any(AbortSignal) });
      await userEvent.keyboard("{Escape}");
      await settleHistory();
    });
  });

  describe("a time change in the gallery", () => {
    const named: Trip = { ...sampleTrips[1], id: "t-named", name: "Named trip", user_edited: true };
    const regrouped: Trip = { ...sampleTrips[0], id: "t1-new", name: "Iceland 2024 (new)" };

    async function openIceland() {
      renderPage();
      await userEvent.click(await screen.findByText("Iceland 2024"));
      expect(gallery.props!.params).toEqual({ trip_id: "t1" });
    }

    it("keeps the gallery on a trip that is still listed, under its new name", async () => {
      await openIceland();
      mockApi.get.mockResolvedValue([{ ...sampleTrips[0], name: "Iceland, renamed" }, sampleTrips[1]]);
      await act(async () => gallery.props!.onTimesChanged!({ tripId: "t-other", itemId: "m1" }));

      expect(mockApi.get).toHaveBeenCalledTimes(2);
      expect(gallery.props!.params).toEqual({ trip_id: "t1" });
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-title", "Iceland, renamed");
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("moves the gallery to the item's new trip when its own is gone, on the same item", async () => {
      await openIceland();
      mockApi.get.mockResolvedValue([regrouped, named]);
      await act(async () => gallery.props!.onTimesChanged!({ tripId: "t1-new", itemId: "m1" }));

      expect(gallery.props).toMatchObject({
        title: "Iceland 2024 (new)",
        params: { trip_id: "t1-new" },
        route: { tab: "trips", collectionId: "t1-new" },
        initialItemId: "m1",
      });
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      // The trip it now shows is the one marked when it closes.
      act(() => gallery.props!.onClose());
      expect(readLastViewed(me.id, "trips")).toBe("t1-new");
    });

    it.each([
      ["the item is in no trip", null],
      ["the item's trip is not listed either", "t-unknown"],
    ])("closes the gallery and says why when its trip is gone and %s", async (_name, tripId) => {
      await openIceland();
      mockApi.get.mockResolvedValue([named]);
      await act(async () => gallery.props!.onTimesChanged!({ tripId, itemId: "m1" }));

      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
      expect(screen.getByRole("alert")).toHaveTextContent(
        "The time change regrouped that trip, so its gallery closed. Its items are in the trips listed here."
      );
      expect(screen.getByText("Named trip")).toBeInTheDocument();
    });

    it("leaves a gallery closed or moved on meanwhile alone", async () => {
      await openIceland();
      const reload = deferredTrips();
      mockApi.get.mockReturnValue(reload.promise);
      const report = gallery.props!.onTimesChanged!;
      act(() => report({ tripId: "t1-new", itemId: "m1" }));
      act(() => gallery.props!.onClose());
      await userEvent.click(screen.getByText("Tokyo Winter"));

      await act(async () => reload.resolve([regrouped, sampleTrips[1]]));
      expect(gallery.props!.params).toEqual({ trip_id: "t2" });
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    // The evidence view over the gallery holds the entry above the gallery's. Re-pointing the
    // gallery rewrites only the current entry's URL, so back from the evidence view lands on an
    // entry still naming the trip that is gone, until the history module writes the gallery's URL
    // there again.
    it("keeps a re-pointed gallery when back from a view over it lands on the old trip's entry", async () => {
      // Routes take UUIDs only.
      const OLD = "0b6f3c1e-5d1a-4c47-9a8e-2f0d7b9c4e11";
      const NEW = "5d0c2a77-3e4b-4f1a-9c8d-6e2f1b0a9d33";
      const ITEM = "7c2e9d40-1b3a-4f6e-8d5c-a9e1f0b2c3d4";
      const before: Trip = { ...sampleTrips[0], id: OLD, name: "Auto trip" };
      const after: Trip = { ...sampleTrips[0], id: NEW, name: "Auto trip, regrouped" };
      const here = () => window.location.pathname + window.location.search;
      gallery.ownsEntry = true;
      mockApi.get.mockResolvedValue([before, sampleTrips[1]]);
      window.history.replaceState(null, "", `/trips/${OLD}?item=${ITEM}`);
      renderPage();
      await screen.findByText("Auto trip", { selector: "h2" });
      await settleHistory();
      expect(here()).toBe(`/trips/${OLD}?item=${ITEM}`);
      act(() => gallery.openNested!());
      await settleHistory();
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-nested", "true");

      mockApi.get.mockResolvedValue([after, sampleTrips[1]]);
      await act(async () => gallery.props!.onTimesChanged!({ tripId: NEW, itemId: ITEM }));
      expect(gallery.props!.params).toEqual({ trip_id: NEW });
      await settleHistory();

      await traverse(-1);
      await settleHistory();
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-nested", "false");
      expect(gallery.props!.params).toEqual({ trip_id: NEW });
      expect(screen.getByTestId("gallery")).toHaveAttribute("data-title", "Auto trip, regrouped");
      // The history module writes the gallery's URL on its entry again (at most every 250 ms).
      await waitFor(() => expect(here()).toBe(`/trips/${NEW}?item=${ITEM}`));
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();

      // Back again closes the gallery itself, quietly.
      await traverse(-1);
      await settleHistory();
      expect(screen.queryByTestId("gallery")).not.toBeInTheDocument();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("keeps the gallery as it is when the trips cannot be reloaded", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      await openIceland();
      mockApi.get.mockRejectedValue(new Error("offline"));
      await act(async () => gallery.props!.onTimesChanged!({ tripId: "t1-new", itemId: "m1" }));
      expect(gallery.props!.params).toEqual({ trip_id: "t1" });
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });
  });

  it("makes the card actions finger-sized on touch", async () => {
    renderPage();
    await screen.findByText("Iceland 2024");
    for (const name of ["Add to album…", "Shift times…", "Edit"]) {
      expect(screen.getAllByRole("button", { name })[0]).toHaveClass("touch:min-h-11", "touch:min-w-11");
    }
  });
});
