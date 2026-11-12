import type { ComponentProps } from "react";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { EvidenceView, type EvidenceViewProps } from "@/components/evidence/EvidenceView";
import { makeBracket, makeEstimate, makeReread, makeResponse, type ResponseOverrides } from "@/components/evidence/evidenceTestUtils";
import type { EvidenceMap } from "@/components/evidence/EvidenceMap";
import type { PickedPoint } from "@/components/evidence/PointCard";
import { ITEM_GONE, NOT_OWNER, REMATCH_FAILED } from "@/lib/evidence";
import { BACK_DEPTH_KEY } from "@/lib/useCloseOnBack";
import type { ProvenanceResponse } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    api: {
      mediaProvenance: vi.fn(),
      mediaProvenanceFile: vi.fn(),
      runMatcher: vi.fn(),
      updateLocation: vi.fn(),
    },
  };
});

const pickedPoint: PickedPoint = {
  format: "gpx",
  source: "gpx:picked-track.gpx",
  t: "2000-01-01T00:00:40Z",
  lat: 10,
  lng: 20,
  accuracyM: null,
  altitudeM: null,
  gapS: 20,
  kind: "log",
};

// Stands in for Leaflet: the two things the view asks of a map are a point being picked and a
// click away from any point.
vi.mock("@/components/evidence/EvidenceMap", () => ({
  EvidenceMap: ({ data, inert, onPick }: ComponentProps<typeof EvidenceMap>) => (
    <div data-testid="evidence-map" data-item={data.item.id} data-inert={String(inert)}>
      <button onClick={() => onPick(pickedPoint)}>pick a point</button>
      <button onClick={() => onPick(null)}>click away</button>
    </div>
  ),
}));

const mockApi = api as unknown as Record<
  "mediaProvenance" | "mediaProvenanceFile" | "runMatcher" | "updateLocation",
  ReturnType<typeof vi.fn>
>;

// How many fullscreen views hold a history entry (useCloseOnBack).
const depth = () => ((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY] as number) ?? 0;

// jsdom traverses history in setTimeout(0) steps; this lets every pending step land.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

async function goBack() {
  await act(async () => {
    const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
    window.history.back();
    await popped;
  });
  await settle();
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

// What each id answers; every other id gets a plain response.
function serve(responses: Record<string, ResponseOverrides> = {}) {
  mockApi.mediaProvenance.mockImplementation((id: string) => Promise.resolve(makeResponse(id, responses[id] ?? {})));
}

function renderView(props: Partial<EvidenceViewProps> = {}) {
  const all: EvidenceViewProps = {
    mediaId: "a",
    isOwn: true,
    onClose: vi.fn(),
    onLocationSaved: vi.fn(),
    onRematched: vi.fn(),
    ...props,
  };
  const view = render(<EvidenceView {...all} />);
  return { ...view, props: all, update: (next: Partial<EvidenceViewProps>) => view.rerender(<EvidenceView {...all} {...next} />) };
}

const loaded = (id: string) => screen.findByText(`${id}.jpg`);

// A pair whose earlier point was written by the photo "behind".
const linkedNow = makeEstimate(
  {
    before: makeBracket({
      source: "exif",
      format: "exif",
      kind: "exif",
      origin: { state: "linked", media: [{ id: "behind", filename: "behind.jpg" }] },
    }),
    after: null,
  },
  { shape: "one_sided", pair_gap_s: null, pair_km: null, t: null, distance_factor: null }
);

const burstNow = makeEstimate(
  {
    before: makeBracket({
      source: "exif",
      format: "exif",
      kind: "exif",
      origin: {
        state: "burst",
        media: [
          { id: "m1", filename: "m1.jpg" },
          { id: "m2", filename: "m2.jpg" },
        ],
      },
    }),
    after: null,
  },
  { shape: "one_sided", pair_gap_s: null, pair_km: null, t: null, distance_factor: null }
);

beforeEach(() => {
  vi.clearAllMocks();
  serve();
  mockApi.mediaProvenanceFile.mockResolvedValue(makeReread());
  mockApi.runMatcher.mockResolvedValue(undefined);
  mockApi.updateLocation.mockResolvedValue(undefined);
});

// An open view would pop its history entry after the next test began.
afterEach(async () => {
  cleanup();
  await settle();
});

describe("EvidenceView", () => {
  it("fills the screen above the fullscreen views, without the dialog marker that would mute its keys", async () => {
    renderView();
    await loaded("a");
    const root = screen.getByRole("dialog", { name: "Why here?" });
    expect(root).toHaveClass("fixed", "inset-0", "z-[1100]", "flex", "flex-col");
    expect(root).not.toHaveAttribute("data-app-dialog");
  });

  it("shows the state, the sentence, the time and the points, in that order", async () => {
    renderView();
    await loaded("a");
    const regions = screen.getAllByRole("region").map((el) => el.getAttribute("aria-label"));
    expect(regions).toEqual(["Placement", "Time", "Points", "Actions"]);
    expect(screen.getByText("Up to date")).toBeInTheDocument();
    expect(screen.getByText("Between two points 1 min and 2 min away.")).toBeInTheDocument();
  });

  it("lays the map and facts out for each screen", async () => {
    renderView();
    await loaded("a");
    const map = screen.getByTestId("evidence-map").parentElement!;
    const facts = screen.getByRole("region", { name: "Placement" }).parentElement!.parentElement!;
    const body = map.parentElement!;
    expect(body).toHaveClass("flex-col", "md:flex-row", "short:flex-row");
    expect(map).toHaveClass("relative", "h-[40vh]", "supports-[height:100dvh]:h-[40dvh]", "md:flex-1", "md:min-w-0", "short:flex-1");
    expect(facts).toHaveClass("min-h-0", "flex-1", "overflow-y-auto", "md:w-[420px]", "md:flex-none");
  });

  it("gives every header control a 44 px target on touch", async () => {
    renderView({ onPrev: vi.fn(), onNext: vi.fn(), position: "2 / 4" });
    await loaded("a");
    for (const name of ["Previous", "Next", "Close"]) {
      expect(screen.getByRole("button", { name })).toHaveClass("touch:min-h-11", "touch:min-w-11");
    }
    expect(screen.getByText("2 / 4")).toBeInTheDocument();
  });

  it("hides the stepping controls when the parent gives none", async () => {
    renderView();
    await loaded("a");
    expect(screen.queryByRole("button", { name: "Previous" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
  });

  it("sends no request for a non-owner and says why", async () => {
    renderView({ isOwn: false });
    expect(screen.getByText(NOT_OWNER)).toBeInTheDocument();
    await settle();
    expect(mockApi.mediaProvenance).not.toHaveBeenCalled();
    expect(mockApi.mediaProvenanceFile).not.toHaveBeenCalled();
    expect(screen.queryByTestId("evidence-map")).not.toBeInTheDocument();
  });

  describe("keys", () => {
    it("closes on Escape and steps on the arrows", async () => {
      const { props } = renderView({ onPrev: vi.fn(), onNext: vi.fn() });
      await loaded("a");

      await userEvent.keyboard("{ArrowLeft}");
      expect(props.onPrev).toHaveBeenCalledTimes(1);
      await userEvent.keyboard("{ArrowRight}");
      expect(props.onNext).toHaveBeenCalledTimes(1);
      expect(props.onClose).not.toHaveBeenCalled();

      await userEvent.keyboard("{Escape}");
      expect(props.onClose).toHaveBeenCalledTimes(1);
    });

    it("does not step with an arrow pressed while the editor is open", async () => {
      const { props } = renderView({ onPrev: vi.fn(), onNext: vi.fn() });
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "Place by hand" }));
      expect(screen.getByRole("button", { name: "Save location" })).toBeInTheDocument();

      await userEvent.keyboard("{ArrowRight}{ArrowLeft}");
      expect(props.onPrev).not.toHaveBeenCalled();
      expect(props.onNext).not.toHaveBeenCalled();
      expect(screen.getByText(/^Edit GPS location/)).toHaveTextContent("a.jpg");
    });

    it("closes only the editor on Escape with it open", async () => {
      const { props } = renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "Place by hand" }));
      await settle();

      await userEvent.keyboard("{Escape}");
      expect(screen.queryByRole("button", { name: "Save location" })).not.toBeInTheDocument();
      expect(props.onClose).not.toHaveBeenCalled();

      await userEvent.keyboard("{Escape}");
      expect(props.onClose).toHaveBeenCalledTimes(1);
    });

    it("closes only the point card on Escape with it open", async () => {
      const { props } = renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "pick a point" }));
      expect(screen.getByRole("region", { name: "Track point" })).toBeInTheDocument();

      await userEvent.keyboard("{Escape}");
      expect(screen.queryByRole("region", { name: "Track point" })).not.toBeInTheDocument();
      expect(props.onClose).not.toHaveBeenCalled();

      await userEvent.keyboard("{Escape}");
      expect(props.onClose).toHaveBeenCalledTimes(1);
    });

    it("ignores a key an open dialog owns", async () => {
      const { props } = renderView({ onNext: vi.fn() });
      await loaded("a");
      const dialog = document.createElement("div");
      dialog.setAttribute("data-app-dialog", "");
      dialog.setAttribute("data-state", "open");
      document.body.appendChild(dialog);

      await userEvent.keyboard("{Escape}{ArrowRight}");
      expect(props.onClose).not.toHaveBeenCalled();
      expect(props.onNext).not.toHaveBeenCalled();
      dialog.remove();
    });

    it("leaves a text field's keys alone", async () => {
      const { props } = renderView({ onNext: vi.fn() });
      await loaded("a");
      const input = document.createElement("input");
      document.body.appendChild(input);
      input.focus();

      await userEvent.keyboard("{ArrowRight}{Escape}");
      expect(props.onNext).not.toHaveBeenCalled();
      expect(props.onClose).not.toHaveBeenCalled();
      input.remove();
    });
  });

  it("closes on the back gesture", async () => {
    const before = depth();
    const { props } = renderView();
    await loaded("a");
    await settle();
    expect(depth()).toBe(before + 1);

    await goBack();
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  describe("the point card", () => {
    it("is a sibling of the map, anchored to its bottom edge", async () => {
      renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "pick a point" }));
      const card = screen.getByRole("region", { name: "Track point" });
      expect(card).toHaveClass("absolute", "bottom-0", "max-h-full", "overflow-y-auto");
      expect(card.parentElement).toBe(screen.getByTestId("evidence-map").parentElement);
      expect(card).toHaveTextContent("picked-track.gpx");
      expect(card).toHaveTextContent("log");
    });

    it("closes with its 44 px button", async () => {
      renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "pick a point" }));
      const close = screen.getByRole("button", { name: "Close point" });
      expect(close).toHaveClass("h-11", "w-11");
      await userEvent.click(close);
      expect(screen.queryByRole("region", { name: "Track point" })).not.toBeInTheDocument();
    });

    it("closes on a click away from any point", async () => {
      renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "pick a point" }));
      await userEvent.click(screen.getByRole("button", { name: "click away" }));
      expect(screen.queryByRole("region", { name: "Track point" })).not.toBeInTheDocument();
    });

    it("closes on a step to another item", async () => {
      const { update } = renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "pick a point" }));

      update({ mediaId: "b" });
      expect(screen.queryByRole("region", { name: "Track point" })).not.toBeInTheDocument();
    });
  });

  describe("loading", () => {
    it("keeps the previous content, dimmed and inert, until the next response arrives", async () => {
      const { update } = renderView();
      await loaded("a");
      expect(screen.getByTestId("evidence-map")).toHaveAttribute("data-inert", "false");

      const next = deferred<ProvenanceResponse>();
      mockApi.mediaProvenance.mockReturnValue(next.promise);
      update({ mediaId: "b" });

      expect(screen.getByText("a.jpg")).toBeInTheDocument();
      expect(screen.getByTestId("evidence-map")).toHaveAttribute("data-item", "a");
      expect(screen.getByTestId("evidence-map")).toHaveAttribute("data-inert", "true");
      expect(screen.getByTestId("evidence-map").parentElement).toHaveClass("pointer-events-none");

      await act(async () => next.resolve(makeResponse("b")));
      expect(screen.getByText("b.jpg")).toBeInTheDocument();
      expect(screen.getByTestId("evidence-map")).toHaveAttribute("data-inert", "false");
    });

    it("ignores a late response for an item it has left", async () => {
      const first = deferred<ProvenanceResponse>();
      const second = deferred<ProvenanceResponse>();
      mockApi.mediaProvenance.mockImplementation((id: string) => (id === "a" ? first.promise : second.promise));
      const { update } = renderView();
      update({ mediaId: "b" });

      await act(async () => second.resolve(makeResponse("b")));
      await act(async () => first.resolve(makeResponse("a")));

      expect(screen.getByText("b.jpg")).toBeInTheDocument();
      expect(screen.getByTestId("evidence-map")).toHaveAttribute("data-item", "b");
    });

    it("keeps Place by hand shut between a step and its response", async () => {
      const { update } = renderView();
      await loaded("a");
      const placeByHand = screen.getByRole("button", { name: "Place by hand" });
      expect(placeByHand).toBeEnabled();

      const next = deferred<ProvenanceResponse>();
      mockApi.mediaProvenance.mockReturnValue(next.promise);
      update({ mediaId: "b" });

      const during = screen.getByRole("button", { name: "Place by hand" });
      expect(during).toBeDisabled();
      await userEvent.click(during);
      expect(screen.queryByRole("button", { name: "Save location" })).not.toBeInTheDocument();

      await act(async () => next.resolve(makeResponse("b")));
      expect(screen.queryByRole("button", { name: "Save location" })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Place by hand" })).toBeEnabled();
    });

    it("leaves an open editor on its item when a response arrives later", async () => {
      const { update } = renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "Place by hand" }));
      await settle();

      const next = deferred<ProvenanceResponse>();
      mockApi.mediaProvenance.mockReturnValue(next.promise);
      update({ mediaId: "b" });
      await act(async () => next.resolve(makeResponse("b")));

      expect(screen.getByText(/^Edit GPS location/)).toHaveTextContent("a.jpg");
    });

    it.each([
      ["a 404", new ApiError("gone", 404), ITEM_GONE, false],
      ["a server error", new ApiError("boom", 500), "Could not load this item.", true],
      ["a dropped connection", new TypeError("Failed to fetch"), "Could not load this item.", true],
    ])("handles %s", async (_name, error, text, offersRetry) => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.mediaProvenance.mockRejectedValueOnce(error);
      renderView();
      expect(await screen.findByText(text)).toBeInTheDocument();
      expect(!!screen.queryByRole("button", { name: "Try again" })).toBe(offersRetry);
      expect(screen.queryByText(REMATCH_FAILED)).not.toBeInTheDocument();
      if (offersRetry) {
        await userEvent.click(screen.getByRole("button", { name: "Try again" }));
        expect(await loaded("a")).toBeInTheDocument();
        expect(mockApi.mediaProvenance).toHaveBeenCalledTimes(2);
      }
      consoleError.mockRestore();
    });
  });

  describe("a step that fails", () => {
    it("does not leave the previous item's name and map beside the failure", async () => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      serve();
      const { update } = renderView();
      await loaded("a");
      mockApi.mediaProvenance.mockRejectedValueOnce(new ApiError("gone", 404));

      update({ mediaId: "b" });

      expect(await screen.findByText(ITEM_GONE)).toBeInTheDocument();
      expect(screen.queryByText("a.jpg")).not.toBeInTheDocument();
      expect(screen.queryByTestId("evidence-map")).not.toBeInTheDocument();
      consoleError.mockRestore();
    });

    it("keeps the item on screen when its own reload fails", async () => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      serve({ a: { placement: { state: "stale" } } });
      mockApi.runMatcher.mockResolvedValue(undefined);
      renderView();
      await loaded("a");
      mockApi.mediaProvenance.mockRejectedValueOnce(new TypeError("Failed to fetch"));

      await userEvent.click(screen.getByRole("button", { name: "Re-match my library" }));

      expect(await screen.findByText("Could not load this item.")).toBeInTheDocument();
      expect(screen.getByText("a.jpg")).toBeInTheDocument();
      expect(screen.getByTestId("evidence-map")).toBeInTheDocument();
      consoleError.mockRestore();
    });
  });

  describe("focus", () => {
    const enabledControls = () =>
      within(screen.getByRole("dialog", { name: "Why here?" }))
        .getAllByRole("button")
        .filter((button) => !button.hasAttribute("disabled"));

    function outsideButton() {
      const button = document.createElement("button");
      document.body.append(button);
      return button;
    }

    afterEach(() => {
      document.body.replaceChildren();
    });

    it("moves in when the view opens, and back to what opened it when it closes", async () => {
      serve();
      const opener = outsideButton();
      opener.focus();

      const { unmount } = renderView();
      await loaded("a");
      expect(screen.getByRole("dialog", { name: "Why here?" })).toHaveFocus();

      unmount();
      expect(opener).toHaveFocus();
    });

    it("wraps Tab inside the view in both directions", async () => {
      serve();
      renderView();
      await loaded("a");
      const controls = enabledControls();

      controls[controls.length - 1].focus();
      await userEvent.tab();
      expect(controls[0]).toHaveFocus();

      await userEvent.tab({ shift: true });
      expect(controls[controls.length - 1]).toHaveFocus();
    });

    it("brings the focus back in when it has left the view", async () => {
      serve();
      const outside = outsideButton();
      renderView();
      await loaded("a");

      outside.focus();
      await userEvent.tab();

      expect(enabledControls()[0]).toHaveFocus();
    });
  });

  describe("actions", () => {
    it.each([
      ["current", false],
      ["stale", true],
      ["no_support", false],
      ["pending", true],
      ["no_bracket", false],
      ["no_time", false],
      ["own_position", false],
      ["manual", false],
    ] as const)("offers Re-match in the %s state: %s", async (state, offered) => {
      serve({ a: { placement: { state } } });
      renderView();
      await loaded("a");
      expect(!!screen.queryByRole("button", { name: "Re-match my library" })).toBe(offered);
    });

    it("never offers Re-match for a gps_source of none", async () => {
      serve({ a: { item: { gps_source: "none" }, placement: { state: "pending" } } });
      renderView();
      await loaded("a");
      expect(screen.queryByRole("button", { name: "Re-match my library" })).not.toBeInTheDocument();
    });

    it('offers "Move by hand" for a manual position and "Place by hand" otherwise', async () => {
      serve({ a: { placement: { state: "manual", now: null } } });
      renderView();
      await loaded("a");
      expect(screen.getByRole("button", { name: "Move by hand" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Place by hand" })).not.toBeInTheDocument();
    });

    it("re-matches, shows it is busy, then refetches and tells the parent", async () => {
      serve({ a: { placement: { state: "stale" } } });
      const run = deferred<void>();
      mockApi.runMatcher.mockReturnValue(run.promise);
      const { props } = renderView();
      await loaded("a");
      expect(mockApi.mediaProvenance).toHaveBeenCalledTimes(1);

      await userEvent.click(screen.getByRole("button", { name: "Re-match my library" }));
      expect(screen.getByRole("button", { name: "Re-matching…" })).toBeDisabled();
      expect(props.onRematched).not.toHaveBeenCalled();

      await act(async () => run.resolve());
      expect(mockApi.mediaProvenance).toHaveBeenCalledTimes(2);
      expect(props.onRematched).toHaveBeenCalledTimes(1);
      expect(screen.queryByText(REMATCH_FAILED)).not.toBeInTheDocument();
    });

    it("keeps Re-match disabled until the refetch has answered", async () => {
      serve({ a: { placement: { state: "stale" } } });
      const { props } = renderView();
      await loaded("a");
      const refetch = deferred<ProvenanceResponse>();
      mockApi.mediaProvenance.mockReturnValue(refetch.promise);

      await userEvent.click(screen.getByRole("button", { name: "Re-match my library" }));
      await waitFor(() => expect(props.onRematched).toHaveBeenCalledTimes(1));
      expect(screen.getByRole("button", { name: "Re-match my library" })).toBeDisabled();

      await act(async () => refetch.resolve(makeResponse("a", { placement: { state: "stale" } })));
      expect(screen.getByRole("button", { name: "Re-match my library" })).toBeEnabled();
    });

    it("shows a failed Re-match and still refetches and tells the parent", async () => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      serve({ a: { placement: { state: "stale" } } });
      mockApi.runMatcher.mockRejectedValue(new TypeError("Failed to fetch"));
      const { props } = renderView();
      await loaded("a");

      await userEvent.click(screen.getByRole("button", { name: "Re-match my library" }));
      expect(await screen.findByText(REMATCH_FAILED)).toBeInTheDocument();
      expect(mockApi.mediaProvenance).toHaveBeenCalledTimes(2);
      expect(props.onRematched).toHaveBeenCalledTimes(1);
      consoleError.mockRestore();
    });

    it("keeps the failure sentence when the connection is down and the refetch fails too", async () => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      serve({ a: { placement: { state: "stale" } } });
      mockApi.runMatcher.mockRejectedValue(new TypeError("Failed to fetch"));
      const { props } = renderView();
      await loaded("a");
      mockApi.mediaProvenance.mockRejectedValue(new TypeError("Failed to fetch"));

      await userEvent.click(screen.getByRole("button", { name: "Re-match my library" }));

      expect(await screen.findByText("Could not load this item.")).toBeInTheDocument();
      expect(screen.getByText(REMATCH_FAILED)).toBeInTheDocument();
      expect(mockApi.mediaProvenance).toHaveBeenCalledTimes(2);
      expect(props.onRematched).toHaveBeenCalledTimes(1);

      // Back online: the facts return and the sentence moves under the buttons, once.
      serve({ a: { placement: { state: "stale" } } });
      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      await screen.findByRole("button", { name: "Place by hand" });
      expect(screen.getAllByText(REMATCH_FAILED)).toHaveLength(1);
      consoleError.mockRestore();
    });

    it("saves through the editor, reports the item on screen and reloads", async () => {
      const { props } = renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "Place by hand" }));
      await userEvent.type(screen.getByRole("textbox", { name: "Coordinates" }), "1, 2");
      await userEvent.click(screen.getByRole("button", { name: "Go" }));
      await userEvent.click(screen.getByRole("button", { name: "Save location" }));

      await waitFor(() => expect(props.onLocationSaved).toHaveBeenCalledWith("a", 1, 2));
      expect(mockApi.updateLocation).toHaveBeenCalledWith("a", 1, 2);
      expect(screen.queryByRole("button", { name: "Save location" })).not.toBeInTheDocument();
      await waitFor(() => expect(mockApi.mediaProvenance).toHaveBeenCalledTimes(2));
    });

    it("opens the editor on the stored position", async () => {
      serve({ a: { item: { lat: 11.5, lng: 22.5 } } });
      renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "Place by hand" }));
      expect(screen.getByText("11.500000, 22.500000")).toBeInTheDocument();
    });
  });

  describe("jumping to the item behind a point", () => {
    async function jumped(onPrev = vi.fn(), onNext = vi.fn()) {
      serve({ a: { placement: { now: linkedNow } } });
      const view = renderView({ onPrev, onNext, position: "2 / 4" });
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "Your photo behind.jpg" }));
      await loaded("behind");
      await settle();
      return view;
    }

    it("opens the linked item in the same view, and Back returns to mediaId", async () => {
      await jumped();
      expect(mockApi.mediaProvenance).toHaveBeenLastCalledWith("behind");
      expect(screen.getByTestId("evidence-map")).toHaveAttribute("data-item", "behind");

      await userEvent.click(screen.getByRole("button", { name: "Back to a.jpg" }));
      expect(await loaded("a")).toBeInTheDocument();
      expect(mockApi.mediaProvenance).toHaveBeenLastCalledWith("a");
      expect(screen.queryByRole("button", { name: "Back to a.jpg" })).not.toBeInTheDocument();
    });

    it("hides previous, position and next while jumped, and steps with no arrow key", async () => {
      const onPrev = vi.fn();
      const onNext = vi.fn();
      await jumped(onPrev, onNext);
      expect(screen.queryByRole("button", { name: "Previous" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Next" })).not.toBeInTheDocument();
      expect(screen.queryByText("2 / 4")).not.toBeInTheDocument();

      await userEvent.keyboard("{ArrowLeft}{ArrowRight}");
      expect(onPrev).not.toHaveBeenCalled();
      expect(onNext).not.toHaveBeenCalled();
    });

    it("lists a burst's items, each opening that item", async () => {
      serve({ a: { placement: { now: burstNow } } });
      renderView();
      await loaded("a");

      await userEvent.click(screen.getByRole("button", { name: "2 items at this second" }));
      const row = screen.getByRole("button", { name: "m2.jpg" });
      await userEvent.click(row);
      await loaded("m2");
      expect(mockApi.mediaProvenance).toHaveBeenLastCalledWith("m2");
    });

    it("returns to mediaId on Escape before a second Escape closes the view", async () => {
      const view = await jumped();
      await userEvent.keyboard("{Escape}");
      expect(await loaded("a")).toBeInTheDocument();
      expect(view.props.onClose).not.toHaveBeenCalled();

      await userEvent.keyboard("{Escape}");
      expect(view.props.onClose).toHaveBeenCalledTimes(1);
    });

    it("returns to mediaId on the back gesture before a second one closes the view", async () => {
      const before = depth();
      const view = await jumped();
      expect(depth()).toBe(before + 2);

      await goBack();
      expect(await loaded("a")).toBeInTheDocument();
      expect(view.props.onClose).not.toHaveBeenCalled();
      expect(depth()).toBe(before + 1);

      await goBack();
      expect(view.props.onClose).toHaveBeenCalledTimes(1);
    });

    it("clears the jump when mediaId changes", async () => {
      const view = await jumped();
      expect(screen.getByRole("button", { name: "Back to a.jpg" })).toBeInTheDocument();

      view.update({ mediaId: "b" });
      expect(screen.queryByRole("button", { name: /^Back to/ })).not.toBeInTheDocument();
      expect(await loaded("b")).toBeInTheDocument();
      expect(mockApi.mediaProvenance).toHaveBeenLastCalledWith("b");
    });

    it("always goes back to mediaId after a further jump", async () => {
      const behind = makeEstimate(
        {
          before: makeBracket({
            source: "exif",
            format: "exif",
            kind: "exif",
            origin: { state: "linked", media: [{ id: "deeper", filename: "deeper.jpg" }] },
          }),
          after: null,
        },
        { shape: "one_sided", pair_gap_s: null, pair_km: null, t: null, distance_factor: null }
      );
      serve({ a: { placement: { now: linkedNow } }, behind: { placement: { now: behind } } });
      renderView();
      await loaded("a");
      await userEvent.click(screen.getByRole("button", { name: "Your photo behind.jpg" }));
      await loaded("behind");
      await userEvent.click(screen.getByRole("button", { name: "Your photo deeper.jpg" }));
      await loaded("deeper");

      expect(screen.getByRole("button", { name: "Back to a.jpg" })).toBeInTheDocument();
    });

    it("reports a save with the id of the item on screen", async () => {
      const view = await jumped();
      await userEvent.click(screen.getByRole("button", { name: "Place by hand" }));
      await userEvent.type(screen.getByRole("textbox", { name: "Coordinates" }), "1, 2");
      await userEvent.click(screen.getByRole("button", { name: "Go" }));
      await userEvent.click(screen.getByRole("button", { name: "Save location" }));

      await waitFor(() => expect(view.props.onLocationSaved).toHaveBeenCalledWith("behind", 1, 2));
    });

    it("cannot reach a jump chip while the previous item is dimmed", async () => {
      serve({ a: { placement: { now: linkedNow } } });
      const { update } = renderView();
      await loaded("a");
      mockApi.mediaProvenance.mockReturnValue(new Promise(() => {}));
      update({ mediaId: "b" });
      expect(within(screen.getByRole("region", { name: "Points" })).getByRole("button", { name: "Your photo behind.jpg" })).toBeDisabled();
    });
  });
});
