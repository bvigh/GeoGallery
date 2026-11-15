import type { ComponentProps } from "react";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { ChecksView } from "@/components/checks/ChecksView";
import {
  makeAgreement,
  makeAgreementFile,
  makeCheckList,
  makeChecks,
  makeFile,
  makeGroup,
} from "@/components/checks/checksTestUtils";
import type { EvidenceViewProps } from "@/components/evidence/EvidenceView";
import type { MediaGalleryOverlay, TimesChangedHint } from "@/components/MediaGalleryOverlay";
import {
  CHECKS_TIMEOUT,
  COMPARE_BUSY,
  COMPARE_NOTE,
  COMPARED_COLUMN_NOTES,
  COMPARING,
  DISTANCE_COLUMN,
  FILE_COLUMN_NOTES,
  findingText,
  NOTHING_FOUND,
  REGROUPED_TRIP_CLOSED,
  RESIDUALS_CAPTION,
  TRUNCATED_NOTE,
} from "@/lib/checks";
import { BACK_DEPTH_KEY } from "@/lib/history";
import { formatCount } from "@/lib/numbers";
import { formatInstant } from "@/lib/time";
import { SHIFT_OUTCOME_UNKNOWN } from "@/lib/timeEdit";
import type { AgreementResponse, ChecksFile, ChecksResponse, SetTakenAtResult, Trip } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    // get: the trips, after a time change in a trip's gallery.
    api: { checks: vi.fn(), checksAgreement: vi.fn(), get: vi.fn() },
  };
});

// The latest props of the evidence view and of a trip gallery. EvidenceView.test.tsx and
// MediaGalleryOverlay.test.tsx test the views themselves. With `history` set, each mock owns a
// history entry as the real view does (useCloseOnBack).
const evidence = vi.hoisted(() => ({ props: null as EvidenceViewProps | null, history: false }));
vi.mock("@/components/evidence/EvidenceView", async () => {
  const { useCloseOnBack } = await vi.importActual<typeof import("@/lib/history")>("@/lib/history");
  return {
    EvidenceView: (props: EvidenceViewProps) => {
      evidence.props = props;
      useCloseOnBack(props.onClose, evidence.history);
      return <div data-testid="evidence-view" data-media={props.mediaId} />;
    },
  };
});
const gallery = vi.hoisted(() => ({ props: null as GalleryProps | null, history: false }));
vi.mock("@/components/MediaGalleryOverlay", async () => {
  const { useCloseOnBack } = await vi.importActual<typeof import("@/lib/history")>("@/lib/history");
  return {
    MediaGalleryOverlay: (props: GalleryProps) => {
      gallery.props = props;
      useCloseOnBack(props.onClose, gallery.history);
      return <div data-testid="trip-gallery" data-item={props.initialItemId ?? ""} />;
    },
  };
});

const mockApi = api as unknown as Record<"checks" | "checksAgreement" | "get", ReturnType<typeof vi.fn>>;

type GalleryProps = ComponentProps<typeof MediaGalleryOverlay>;

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

function renderView() {
  const onClose = vi.fn();
  return { ...render(<ChecksView onClose={onClose} />), onClose };
}

const loaded = () => screen.findByRole("heading", { name: "Findings" });

const filesTable = () => screen.getByRole("table", { name: "Track files" }) as HTMLTableElement;

// The file names of the table's rows, top to bottom. Role queries over hundreds of rows take seconds.
function fileOrder(): string[] {
  return Array.from(filesTable().tBodies[0].rows, (row) => row.cells[0].textContent ?? "");
}

function rowOf(name: string): HTMLElement {
  return within(filesTable()).getByRole("cell", { name }).closest("tr")!;
}

// A row's cell for one column (cells keep the table's column order on a phone too).
function cellOf(name: string, column: number): HTMLElement {
  return within(rowOf(name)).getAllByRole("cell")[column];
}

const DISTANCE = 7;
const SPACING = 8;

function stubBrowserZone(timeZone: string) {
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
    timeZone,
  } as Intl.ResolvedDateTimeFormatOptions);
}

const staleGroup = makeGroup({
  key: "stale",
  label: "stale",
  count: 3,
  evidence: { max_delta_m: 250 },
  media_ids: ["m1", "m2", "m3"],
});
const farGroup = makeGroup({ key: "gpx+nmea", label: "gpx+nmea", count: 1, evidence: { max_km: 1.5 }, media_ids: ["m9"] });
const timeSaved: SetTakenAtResult = { taken_at: "2000-01-01T00:00:00Z", trip_id: "t1", followup_failed: false };
const withFindings = makeChecks({ checks: makeCheckList({ stale_estimates: [staleGroup], pair_far: [farGroup] }) });

beforeEach(() => {
  vi.clearAllMocks();
  evidence.props = null;
  evidence.history = false;
  gallery.props = null;
  gallery.history = false;
  mockApi.checks.mockResolvedValue(makeChecks());
  mockApi.checksAgreement.mockResolvedValue(makeAgreement());
});

// An open view would pop its history entry after the next test began.
afterEach(async () => {
  cleanup();
  await settle();
  vi.restoreAllMocks();
});

describe("ChecksView", () => {
  it("fills the screen under the evidence view, without the dialog marker that would mute its keys", async () => {
    renderView();
    await loaded();
    const root = screen.getByRole("dialog", { name: "Checks" });
    expect(root).toHaveClass("fixed", "inset-0", "z-[1000]", "flex", "flex-col");
    expect(root).not.toHaveAttribute("data-app-dialog");
  });

  it("says when the checks ran, in UTC and in the browser's zone, and the match window", async () => {
    stubBrowserZone("Asia/Tokyo");
    mockApi.checks.mockResolvedValue(makeChecks({ generated_at: "2000-01-02T03:04:05Z", window_s: 7200 }));
    renderView();
    await loaded();
    expect(screen.getByText("Checked, UTC").nextElementSibling).toHaveTextContent(formatInstant("2000-01-02T03:04:05Z", "UTC"));
    expect(screen.getByText("Checked, your browser").nextElementSibling).toHaveTextContent(
      formatInstant("2000-01-02T03:04:05Z", "Asia/Tokyo")
    );
    expect(screen.getByText("Match window").nextElementSibling).toHaveTextContent("2 h");
  });

  it("shows the findings first, then the formats against photos, then the track files", async () => {
    renderView();
    await loaded();
    expect(screen.getAllByRole("heading", { level: 3 }).map((heading) => heading.textContent)).toEqual([
      "Findings",
      "Formats against your photos",
      "Track files",
    ]);
  });

  it("gives every control a 44 px target on touch", async () => {
    mockApi.checks.mockResolvedValue({ ...withFindings, files: Array.from({ length: 201 }, (_, i) => makeFile({ source: `gpx:f${i}.gpx` })) });
    renderView();
    await loaded();
    const buttons = [
      screen.getByRole("button", { name: "Refresh" }),
      screen.getByRole("button", { name: "Close" }),
      screen.getByRole("button", { name: /Out-of-date estimates/ }),
      screen.getByRole("button", { name: "Open 3 estimates" }),
      screen.getByRole("button", { name: "Compare files" }),
      screen.getByRole("button", { name: "Points" }),
      screen.getByRole("button", { name: "Show more" }),
    ];
    for (const button of buttons) expect(button).toHaveClass("touch:min-h-11");
    expect(screen.getByRole("button", { name: "Close" })).toHaveClass("touch:min-w-11");
  });

  describe("loading", () => {
    it("says it is running the checks until they answer", async () => {
      const response = deferred<ChecksResponse>();
      mockApi.checks.mockReturnValue(response.promise);
      renderView();
      expect(screen.getByText("Running the checks… This takes a few seconds.")).toBeInTheDocument();

      await act(async () => response.resolve(makeChecks()));
      expect(screen.queryByText("Running the checks… This takes a few seconds.")).not.toBeInTheDocument();
      expect(screen.getByRole("heading", { name: "Findings" })).toBeInTheDocument();
    });

    it.each([
      ["the server's time limit", new ApiError("GET /checks failed: 504", 504), CHECKS_TIMEOUT],
      ["a server error", new ApiError("GET /checks failed: 500", 500), "Could not run the checks."],
      ["a dropped connection", new TypeError("Failed to fetch"), "Could not run the checks."],
    ])("says so on %s, and runs them again on request", async (_name, error, text) => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.checks.mockRejectedValueOnce(error);
      renderView();
      expect(await screen.findByText(text)).toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(await loaded()).toBeInTheDocument();
      expect(screen.queryByText(text)).not.toBeInTheDocument();
      expect(mockApi.checks).toHaveBeenCalledTimes(2);
      consoleError.mockRestore();
    });

    it("keeps the checks on screen, dimmed, while they run again", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      renderView();
      await loaded();
      const next = deferred<ChecksResponse>();
      mockApi.checks.mockReturnValue(next.promise);

      await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
      // Busy but never disabled, so it keeps the keyboard focus; a second press waits for the first.
      const refresh = screen.getByRole("button", { name: "Refresh" });
      expect(refresh).toHaveAttribute("aria-disabled", "true");
      expect(refresh).not.toHaveAttribute("disabled");
      expect(refresh).toHaveFocus();
      await userEvent.click(refresh);
      expect(mockApi.checks).toHaveBeenCalledTimes(2);
      const content = screen.getByRole("heading", { name: "Findings" }).closest("[aria-busy]");
      expect(content).toHaveAttribute("aria-busy", "true");
      expect(content).toHaveClass("opacity-60");
      expect(screen.getByText(findingText("stale_estimates", staleGroup).sentence)).toBeInTheDocument();

      await act(async () => next.resolve(makeChecks({ generated_at: "2000-02-03T00:00:00Z" })));
      expect(content).toHaveAttribute("aria-busy", "false");
      expect(refresh).toHaveAttribute("aria-disabled", "false");
      expect(screen.getByText("Checked, UTC").nextElementSibling).toHaveTextContent(formatInstant("2000-02-03T00:00:00Z", "UTC"));
      expect(screen.queryByText(findingText("stale_estimates", staleGroup).sentence)).not.toBeInTheDocument();
    });

    it("cancels the checks in flight when it closes", async () => {
      const response = deferred<ChecksResponse>();
      mockApi.checks.mockReturnValue(response.promise);
      const { unmount } = renderView();
      const checksSignal: AbortSignal = mockApi.checks.mock.calls[0][0];
      expect(checksSignal.aborted).toBe(false);
      unmount();
      expect(checksSignal.aborted).toBe(true);
      // A late answer changes nothing.
      await act(async () => response.resolve(makeChecks()));
    });

    it("cancels a comparison in flight when it closes", async () => {
      mockApi.checksAgreement.mockReturnValue(new Promise(() => {}));
      const { unmount } = renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Compare files" }));
      const agreementSignal: AbortSignal = mockApi.checksAgreement.mock.calls[0][1];
      expect(agreementSignal.aborted).toBe(false);

      unmount();
      expect(agreementSignal.aborted).toBe(true);
      expect(mockApi.checks).toHaveBeenCalledTimes(1);
      expect(mockApi.checksAgreement).toHaveBeenCalledTimes(1);
    });
  });

  describe("keys", () => {
    it("closes on Escape", async () => {
      const { onClose } = renderView();
      await loaded();
      await userEvent.keyboard("{Escape}");
      expect(onClose).toHaveBeenCalledTimes(1);
    });

    it("leaves Escape to the evidence view while it is open", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      const { onClose } = renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Open 3 estimates" }));
      expect(screen.getByTestId("evidence-view")).toBeInTheDocument();

      await userEvent.keyboard("{Escape}");
      expect(onClose).not.toHaveBeenCalled();
      expect(screen.getByTestId("evidence-view")).toBeInTheDocument();

      act(() => evidence.props!.onClose());
      await userEvent.keyboard("{Escape}");
      expect(onClose).toHaveBeenCalledTimes(1);
    });

    it("ignores a key an open dialog owns", async () => {
      const { onClose } = renderView();
      await loaded();
      const dialog = document.createElement("div");
      dialog.setAttribute("data-app-dialog", "");
      dialog.setAttribute("data-state", "open");
      document.body.appendChild(dialog);

      await userEvent.keyboard("{Escape}");
      expect(onClose).not.toHaveBeenCalled();
      dialog.remove();
    });

    it("leaves the search field's Escape alone", async () => {
      mockApi.checks.mockResolvedValue(makeChecks({ files: Array.from({ length: 8 }, (_, i) => makeFile({ source: `gpx:f${i}.gpx` })) }));
      const { onClose } = renderView();
      await loaded();
      await userEvent.type(screen.getByRole("searchbox", { name: "Search track files" }), "f1{Escape}");
      expect(onClose).not.toHaveBeenCalled();
    });
  });

  it("closes on the back gesture", async () => {
    const before = depth();
    const { onClose } = renderView();
    await loaded();
    await settle();
    expect(depth()).toBe(before + 1);

    await goBack();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  describe("focus", () => {
    afterEach(() => {
      document.body.replaceChildren();
    });

    const enabledControls = () =>
      within(screen.getByRole("dialog", { name: "Checks" }))
        .getAllByRole("button")
        .filter((button) => !button.hasAttribute("disabled"));

    it("moves in when the view opens, and back to what opened it when it closes", async () => {
      const opener = document.createElement("button");
      document.body.append(opener);
      opener.focus();

      const { unmount } = renderView();
      await loaded();
      expect(screen.getByRole("dialog", { name: "Checks" })).toHaveFocus();

      unmount();
      expect(opener).toHaveFocus();
    });

    it("wraps Tab inside the view in both directions", async () => {
      renderView();
      await loaded();
      const controls = enabledControls();

      controls[controls.length - 1].focus();
      await userEvent.tab();
      expect(controls[0]).toHaveFocus();

      await userEvent.tab({ shift: true });
      expect(controls[controls.length - 1]).toHaveFocus();
    });

    it("keeps the focus in the view when Try again goes away", async () => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.checks.mockRejectedValueOnce(new ApiError("GET /checks failed: 500", 500));
      renderView();
      await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
      expect(screen.getByRole("dialog", { name: "Checks" })).toHaveFocus();
      await loaded();
      consoleError.mockRestore();
    });

    it("moves the focus to the first new row when the last Show more goes away", async () => {
      // f0 is the oldest; the newest come first.
      const files = Array.from({ length: 250 }, (_, i) =>
        makeFile({ source: `gpx:f${i}.gpx`, last: new Date(Date.UTC(2000, 0, 1) + i * 60_000).toISOString() })
      );
      mockApi.checks.mockResolvedValue(makeChecks({ files }));
      renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Show more" }));
      // Row 201 is the newest of the rows the page added, and its name cell is no tab stop.
      const cell = filesTable().tBodies[0].rows[200].cells[0];
      expect(cell).toHaveTextContent("f49.gpx");
      expect(cell).toHaveFocus();
      expect(cell).toHaveAttribute("tabindex", "-1");
    });

    it("leaves the focus on Show more while more rows remain", async () => {
      const files = Array.from({ length: 450 }, (_, i) => makeFile({ source: `gpx:f${i}.gpx` }));
      mockApi.checks.mockResolvedValue(makeChecks({ files }));
      renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Show more" }));
      expect(screen.getByRole("button", { name: "Show more" })).toHaveFocus();
      expect(screen.getByText(`400 of 450 shown`)).toBeInTheDocument();
    });
  });

  describe("findings", () => {
    it("opens high severity checks, and keeps low severity ones collapsed until asked", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      renderView();
      await loaded();
      const stale = screen.getByRole("button", { name: /Out-of-date estimates/ });
      const far = screen.getByRole("button", { name: /Far-apart points from different formats/ });
      expect(stale).toHaveAttribute("aria-expanded", "true");
      expect(stale).toHaveAccessibleName("High severity: Out-of-date estimates, 3 estimates");
      expect(screen.getByText(findingText("stale_estimates", staleGroup).sentence)).toBeInTheDocument();
      expect(far).toHaveAttribute("aria-expanded", "false");
      expect(far).toHaveAccessibleName("Low severity: Far-apart points from different formats, 1 estimate");
      expect(screen.queryByText(findingText("pair_far", farGroup).sentence)).not.toBeInTheDocument();

      await userEvent.click(far);
      expect(far).toHaveAttribute("aria-expanded", "true");
      expect(screen.getByText(findingText("pair_far", farGroup).sentence)).toBeInTheDocument();

      await userEvent.click(stale);
      expect(screen.queryByText(findingText("stale_estimates", staleGroup).sentence)).not.toBeInTheDocument();
    });

    it("lists high severity first, whatever order the checks come in", async () => {
      mockApi.checks.mockResolvedValue({ ...withFindings, checks: [...withFindings.checks].reverse() });
      renderView();
      await loaded();
      expect(screen.getAllByRole("button", { name: /severity:/ }).map((button) => button.getAttribute("aria-label"))).toEqual([
        "High severity: Out-of-date estimates, 3 estimates",
        "Low severity: Far-apart points from different formats, 1 estimate",
      ]);
    });

    it("lists the checks that found nothing on one line", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      renderView();
      await loaded();
      const clear = screen.getByText("Nothing found for:").nextElementSibling as HTMLElement;
      expect(within(clear).getAllByRole("listitem").map((item) => item.textContent)).toEqual([
        "Impossible speeds between formats",
        "Track points in the future",
        "Items dated after their upload",
        "Points left by deleted items",
        "Estimates on KML, KMZ or GeoJSON points that other formats could place",
      ]);
      expect(screen.queryByText(NOTHING_FOUND)).not.toBeInTheDocument();
    });

    it("finds nothing for estimates on KML points only when other formats could place none of them", async () => {
      // A KML file placed 3 items, which only KML can place: the check counts none of them, and its
      // title says what it counts, so "Nothing found for" does not deny they exist.
      mockApi.checks.mockResolvedValue(
        makeChecks({
          files: [makeFile({ source: "kml:history.kml", format: "kml", kind: "time_may_be_estimated", items_placed: 3 })],
          checks: makeCheckList(),
        })
      );
      renderView();
      await loaded();
      const clear = screen.getByText("Nothing found for:").nextElementSibling as HTMLElement;
      expect(within(clear).getByText("Estimates on KML, KMZ or GeoJSON points that other formats could place")).toBeInTheDocument();
    });

    it("names what the estimates check counts in its header", async () => {
      const kml = makeGroup({ key: "kml", label: "kml", count: 1, evidence: { using: 4, over_100m: 1, over_1km: 0, max_move_m: 120 }, media_ids: ["m1"] });
      mockApi.checks.mockResolvedValue(makeChecks({ checks: makeCheckList({ time_may_be_estimated_used: [kml] }) }));
      renderView();
      await loaded();
      expect(screen.getByRole("button", { name: /severity: Estimates on KML/ })).toHaveAccessibleName(
        "Low severity: Estimates on KML, KMZ or GeoJSON points that other formats could place, 1 estimate"
      );
    });

    it("groups the digits of a check's total", async () => {
      const future = makeGroup({ key: "nmea:drive.nmea", label: "nmea:drive.nmea", count: 27558, evidence: { first: null, last: null } });
      mockApi.checks.mockResolvedValue(makeChecks({ checks: makeCheckList({ future_track_points: [future] }) }));
      renderView();
      await loaded();
      expect(screen.getByRole("button", { name: /Track points in the future/ })).toHaveAccessibleName(
        `High severity: Track points in the future, ${formatCount(27558)} points`
      );
    });

    it("describes each Open button by its finding, since several read alike", async () => {
      const kml = makeGroup({ key: "kml", label: "kml", count: 3, evidence: { using: 3 }, media_ids: ["k1", "k2", "k3"] });
      mockApi.checks.mockResolvedValue(
        makeChecks({ checks: makeCheckList({ stale_estimates: [staleGroup], time_may_be_estimated_used: [kml] }) })
      );
      renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: /severity: Estimates on KML/ }));
      const [stale, estimated] = screen.getAllByRole("button", { name: "Open 3 estimates" });
      expect(stale).toHaveAccessibleDescription(findingText("stale_estimates", staleGroup).sentence);
      expect(estimated).toHaveAccessibleDescription(findingText("time_may_be_estimated_used", kml).sentence);
    });

    it("wraps a long label with no space in it, rather than push the view sideways on a phone", async () => {
      const name = "nmea:20190714_083000_DASHCAM_FRONT_CAMERA_LOCKED_EVENT_FILE_WITH_NO_SPACE_AT_ALL.nmea";
      const future = makeGroup({ key: name, label: name, count: 2, evidence: { first: null, last: null } });
      mockApi.checks.mockResolvedValue(makeChecks({ checks: makeCheckList({ future_track_points: [future] }) }));
      renderView();
      await loaded();
      // overflow-wrap is inherited: the sentence, its notes and its times all wrap.
      expect(screen.getByText(findingText("future_track_points", future).sentence).closest("li")).toHaveClass(
        "[overflow-wrap:anywhere]"
      );
    });

    it("says when nothing at all was found", async () => {
      renderView();
      await loaded();
      expect(screen.getByText(NOTHING_FOUND)).toBeInTheDocument();
    });

    it("shows a finding's instants in UTC and the browser zone, each labelled", async () => {
      stubBrowserZone("America/New_York");
      const future = makeGroup({
        key: "nmea:drive.nmea",
        label: "nmea:drive.nmea",
        count: 2,
        evidence: { first: "2040-01-01T00:00:00Z", last: null },
      });
      mockApi.checks.mockResolvedValue(makeChecks({ checks: makeCheckList({ future_track_points: [future] }) }));
      renderView();
      await loaded();
      const findings = within(screen.getByRole("region", { name: "Findings" }));
      const first = findings.getByText("First").parentElement!;
      expect(within(first).getByText("UTC").nextElementSibling).toHaveTextContent(formatInstant("2040-01-01T00:00:00Z", "UTC"));
      expect(within(first).getByText("Your browser").nextElementSibling).toHaveTextContent(
        formatInstant("2040-01-01T00:00:00Z", "America/New_York")
      );
      expect(findings.getByText("Last").parentElement).toHaveTextContent("year out of range");
      // A point-only finding has no items to open.
      expect(screen.queryByRole("button", { name: /^Open/ })).not.toBeInTheDocument();
    });

    it("says when a group has more items than it lists", async () => {
      const many = makeGroup({ key: "stale", label: "stale", count: 450, evidence: { max_delta_m: 3 }, media_ids: ["m1", "m2"] });
      mockApi.checks.mockResolvedValue(makeChecks({ checks: makeCheckList({ stale_estimates: [many] }) }));
      renderView();
      await loaded();
      expect(screen.getByRole("button", { name: "Open 2 estimates" }).nextElementSibling).toHaveTextContent(
        "The first 2 of 450, oldest first."
      );
    });
  });

  describe("a finding's item in its trip", () => {
    const trip = { id: "t1", name: "Lakes 2023" };

    async function openSecondItem() {
      mockApi.checks.mockResolvedValue(withFindings);
      const view = renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Open 3 estimates" }));
      act(() => evidence.props!.onNext!());
      return view;
    }

    it("shows the item in its trip in place of the evidence view, then comes back to the same item", async () => {
      const { onClose } = await openSecondItem();
      expect(evidence.props).toMatchObject({ mediaId: "m2", onShowInTrip: expect.any(Function) });

      act(() => evidence.props!.onShowInTrip!(trip, "m2"));
      expect(screen.queryByTestId("evidence-view")).not.toBeInTheDocument();
      expect(screen.getByTestId("trip-gallery")).toHaveAttribute("data-item", "m2");
      expect(gallery.props).toMatchObject({
        title: "Lakes 2023",
        params: { trip_id: "t1" },
        route: { tab: "trips", collectionId: "t1" },
        inUrl: false,
        initialItemId: "m2",
      });

      // The gallery handles its own keys; the Checks view under it ignores them.
      await userEvent.keyboard("{Escape}");
      expect(onClose).not.toHaveBeenCalled();
      expect(screen.getByTestId("trip-gallery")).toBeInTheDocument();

      act(() => gallery.props!.onClose());
      expect(screen.queryByTestId("trip-gallery")).not.toBeInTheDocument();
      expect(evidence.props).toMatchObject({ mediaId: "m2", position: "2 / 3" });
      expect(mockApi.checks).toHaveBeenCalledTimes(1);
    });

    it.each([
      ["an item's location changed", () => gallery.props!.onItemsChanged!()],
      ["an item deleted", () => gallery.props!.onMediaDeleted!()],
    ])("runs the checks again once the evidence view closes, after %s in the trip", async (_name, change) => {
      await openSecondItem();
      act(() => evidence.props!.onShowInTrip!(trip, "m2"));
      act(change);
      act(() => gallery.props!.onClose());
      expect(mockApi.checks).toHaveBeenCalledTimes(1);

      act(() => evidence.props!.onClose());
      await waitFor(() => expect(mockApi.checks).toHaveBeenCalledTimes(2));
    });

    it("hands the history entry over both ways, so back closes the gallery, then the evidence view", async () => {
      evidence.history = true;
      gallery.history = true;
      await settle();
      window.history.replaceState(null, "", "/trips");
      const { onClose } = await openSecondItem();
      await settle();
      const entries = window.history.length;
      // The Checks view's entry and the evidence view's.
      expect(depth()).toBe(2);

      // The gallery takes the evidence view's entry over: no entry pushed or popped.
      act(() => evidence.props!.onShowInTrip!(trip, "m2"));
      await settle();
      expect(window.history.length).toBe(entries);
      expect(depth()).toBe(2);
      expect(window.location.pathname + window.location.search).toBe("/trips");

      // Closed with its own button, it hands the entry back the same way.
      act(() => gallery.props!.onClose());
      await settle();
      expect(window.history.length).toBe(entries);
      expect(depth()).toBe(2);

      // Back closes the innermost view: the gallery, and the evidence view comes back over the checks.
      act(() => evidence.props!.onShowInTrip!(trip, "m2"));
      await settle();
      await goBack();
      expect(screen.queryByTestId("trip-gallery")).not.toBeInTheDocument();
      expect(evidence.props).toMatchObject({ mediaId: "m2" });
      expect(depth()).toBe(2);
      expect(onClose).not.toHaveBeenCalled();

      await goBack();
      expect(screen.queryByTestId("evidence-view")).not.toBeInTheDocument();
      expect(depth()).toBe(1);
      expect(onClose).not.toHaveBeenCalled();

      await goBack();
      expect(onClose).toHaveBeenCalledTimes(1);
    });

    describe("after a time change in the trip", () => {
      function trip(id: string, name: string): Trip {
        return { id, name, start_date: null, end_date: null, media_count: 3, cover_key: null, user_edited: false };
      }

      async function changeTime(trips: Trip[], hint: TimesChangedHint) {
        await openSecondItem();
        act(() => evidence.props!.onShowInTrip!(trip("t1", "Lakes 2023"), "m2"));
        mockApi.get.mockResolvedValue(trips);
        await act(async () => gallery.props!.onTimesChanged!(hint));
        expect(mockApi.get).toHaveBeenCalledWith("/trips");
      }

      it("moves the gallery to the item's new trip when its own is gone", async () => {
        await changeTime([trip("t9", "Lakes 2023, regrouped")], { tripId: "t9", itemId: "m7", notice: "Shifted 2 items 1 h later." });
        expect(gallery.props).toMatchObject({
          title: "Lakes 2023, regrouped",
          params: { trip_id: "t9" },
          route: { tab: "trips", collectionId: "t9" },
          inUrl: false,
        });
        expect(screen.getByTestId("trip-gallery")).toHaveAttribute("data-item", "m7");
        expect(screen.queryByTestId("evidence-view")).not.toBeInTheDocument();
      });

      it("keeps the gallery on a trip still listed, under its name now", async () => {
        await changeTime([trip("t1", "Lakes, renamed"), trip("t9", "Other")], { tripId: "t9", itemId: "m2" });
        expect(gallery.props).toMatchObject({ title: "Lakes, renamed", params: { trip_id: "t1" } });
      });

      it("brings the evidence view back on the finding's item when neither trip is listed, saying why", async () => {
        await changeTime([trip("t5", "Elsewhere")], { tripId: null, itemId: "m2", notice: null });
        expect(screen.queryByTestId("trip-gallery")).not.toBeInTheDocument();
        expect(evidence.props).toMatchObject({ mediaId: "m2", position: "2 / 3", notice: REGROUPED_TRIP_CLOSED });
      });

      // The gallery said the shift may have been saved; closed, it can no longer say so, and a
      // second shift from a new opening would move the items twice.
      it("passes on what the gallery said of a shift that went unanswered", async () => {
        await changeTime([trip("t5", "Elsewhere")], { tripId: null, itemId: "m2", notice: SHIFT_OUTCOME_UNKNOWN });
        expect(screen.queryByTestId("trip-gallery")).not.toBeInTheDocument();
        expect(evidence.props!.notice).toBe(`${REGROUPED_TRIP_CLOSED} ${SHIFT_OUTCOME_UNKNOWN}`);
      });

      it.each<[string, () => void]>([
        ["stepping", () => evidence.props!.onNext!()],
        ["showing the item in its trip again, then closing it", () => {
          evidence.props!.onShowInTrip!(trip("t5", "Elsewhere"), "m2");
          gallery.props!.onClose();
        }],
      ])("drops the notice on %s", async (_name, action) => {
        await changeTime([trip("t5", "Elsewhere")], { tripId: null, itemId: "m2", notice: SHIFT_OUTCOME_UNKNOWN });
        expect(evidence.props!.notice).toContain(SHIFT_OUTCOME_UNKNOWN);
        act(action);
        expect(evidence.props!.notice).toBeNull();
      });

      it("drops the notice with the evidence view, so a finding opened next has none", async () => {
        await changeTime([trip("t5", "Elsewhere")], { tripId: null, itemId: "m2", notice: SHIFT_OUTCOME_UNKNOWN });
        act(() => evidence.props!.onClose());
        await userEvent.click(await screen.findByRole("button", { name: "Open 3 estimates" }));
        expect(evidence.props).toMatchObject({ mediaId: "m1", notice: null });
      });

      it("says nothing when the gallery stays or moves, and the user closes it", async () => {
        await changeTime([trip("t9", "Other")], { tripId: "t9", itemId: "m2", notice: SHIFT_OUTCOME_UNKNOWN });
        act(() => gallery.props!.onClose());
        expect(evidence.props).toMatchObject({ mediaId: "m2", notice: null });
      });

      // The gallery reports a time change as a change of its items too, before the hint.
      it("runs the checks again once the evidence view closes", async () => {
        await changeTime([trip("t1", "Lakes 2023")], { tripId: "t1", itemId: "m2" });
        act(() => gallery.props!.onItemsChanged!());
        act(() => gallery.props!.onClose());
        expect(mockApi.checks).toHaveBeenCalledTimes(1);

        act(() => evidence.props!.onClose());
        await waitFor(() => expect(mockApi.checks).toHaveBeenCalledTimes(2));
      });

      it("leaves the gallery as it is when the trips cannot be loaded", async () => {
        vi.spyOn(console, "error").mockImplementation(() => {});
        await openSecondItem();
        act(() => evidence.props!.onShowInTrip!(trip("t1", "Lakes 2023"), "m2"));
        mockApi.get.mockRejectedValue(new Error("offline"));
        await act(async () => gallery.props!.onTimesChanged!({ tripId: "t9", itemId: "m2" }));
        expect(gallery.props).toMatchObject({ title: "Lakes 2023", params: { trip_id: "t1" } });
      });

      // With neither trip listed, the gallery closed by hand has nothing to explain either.
      it.each([
        ["the item's new trip listed", [trip("t9", "Other")]],
        ["neither trip listed", [trip("t5", "Elsewhere")]],
      ])("leaves a gallery closed meanwhile closed, and says nothing, with %s", async (_name, trips) => {
        await openSecondItem();
        act(() => evidence.props!.onShowInTrip!(trip("t1", "Lakes 2023"), "m2"));
        let answer!: (trips: Trip[]) => void;
        mockApi.get.mockReturnValue(new Promise((resolve) => (answer = resolve)));
        const report = gallery.props!.onTimesChanged!;
        act(() => report({ tripId: "t9", itemId: "m2", notice: SHIFT_OUTCOME_UNKNOWN }));
        act(() => gallery.props!.onClose());

        await act(async () => answer(trips));
        expect(screen.queryByTestId("trip-gallery")).not.toBeInTheDocument();
        expect(evidence.props).toMatchObject({ mediaId: "m2", notice: null });
      });
    });

    it("closes the trip with the evidence view, and opens a new finding without it", async () => {
      await openSecondItem();
      act(() => evidence.props!.onShowInTrip!(trip, "m2"));
      act(() => gallery.props!.onClose());
      act(() => evidence.props!.onClose());
      await userEvent.click(await screen.findByRole("button", { name: "Open 3 estimates" }));
      expect(screen.queryByTestId("trip-gallery")).not.toBeInTheDocument();
      expect(evidence.props).toMatchObject({ mediaId: "m1" });
    });
  });

  describe("the evidence view", () => {
    it("opens on a group's first item and steps through its items, wrapping round", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Open 3 estimates" }));
      expect(evidence.props).toMatchObject({ mediaId: "m1", isOwn: true, position: "1 / 3" });

      act(() => evidence.props!.onNext!());
      expect(evidence.props).toMatchObject({ mediaId: "m2", position: "2 / 3" });
      act(() => evidence.props!.onNext!());
      act(() => evidence.props!.onNext!());
      expect(evidence.props).toMatchObject({ mediaId: "m1", position: "1 / 3" });
      act(() => evidence.props!.onPrev!());
      expect(evidence.props).toMatchObject({ mediaId: "m3", position: "3 / 3" });

      act(() => evidence.props!.onClose());
      expect(screen.queryByTestId("evidence-view")).not.toBeInTheDocument();
      expect(mockApi.checks).toHaveBeenCalledTimes(1);
    });

    it("offers no stepping for a single item", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: /Far-apart points/ }));
      await userEvent.click(screen.getByRole("button", { name: "Open 1 estimate" }));
      expect(evidence.props).toMatchObject({ mediaId: "m9", isOwn: true });
      expect(evidence.props!.onPrev).toBeUndefined();
      expect(evidence.props!.onNext).toBeUndefined();
      expect(evidence.props!.position).toBeUndefined();
    });

    it.each([
      ["a location saved", () => evidence.props!.onLocationSaved("m2", 1, 2)],
      ["a re-match", () => evidence.props!.onRematched()],
      ["a capture time changed", () => evidence.props!.onTimeChanged!("m2", timeSaved)],
    ])("runs the checks again once it closes after %s", async (_name, change) => {
      mockApi.checks.mockResolvedValue(withFindings);
      renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Open 3 estimates" }));
      act(() => evidence.props!.onNext!());

      act(change);
      expect(mockApi.checks).toHaveBeenCalledTimes(1);
      act(() => evidence.props!.onClose());
      await waitFor(() => expect(mockApi.checks).toHaveBeenCalledTimes(2));

      // Opened again without a change, it closes without one.
      await userEvent.click(await screen.findByRole("button", { name: "Open 3 estimates" }));
      act(() => evidence.props!.onClose());
      await settle();
      expect(mockApi.checks).toHaveBeenCalledTimes(2);
    });

    it("runs the checks again at once for a re-match that answers after the evidence view closed", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      const { unmount } = renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Open 3 estimates" }));
      // The evidence view calls back through the props of the render that started the re-match.
      const lateRematch = evidence.props!.onRematched;
      act(() => evidence.props!.onClose());
      expect(mockApi.checks).toHaveBeenCalledTimes(1);

      act(lateRematch);
      await waitFor(() => expect(mockApi.checks).toHaveBeenCalledTimes(2));

      // Opened again and closed without a change, it runs nothing more.
      await userEvent.click(await screen.findByRole("button", { name: "Open 3 estimates" }));
      act(() => evidence.props!.onClose());
      await settle();
      expect(mockApi.checks).toHaveBeenCalledTimes(2);

      // Once the view itself has closed, a re-match answering then changes nothing.
      unmount();
      act(lateRematch);
      await settle();
      expect(mockApi.checks).toHaveBeenCalledTimes(2);
    });

    it("waits for the evidence view to close for a re-match from an earlier one that answers meanwhile", async () => {
      mockApi.checks.mockResolvedValue(withFindings);
      renderView();
      await loaded();
      await userEvent.click(screen.getByRole("button", { name: "Open 3 estimates" }));
      const lateRematch = evidence.props!.onRematched;
      act(() => evidence.props!.onClose());
      await userEvent.click(screen.getByRole("button", { name: /Far-apart points/ }));
      await userEvent.click(screen.getByRole("button", { name: "Open 1 estimate" }));

      act(lateRematch);
      await settle();
      expect(mockApi.checks).toHaveBeenCalledTimes(1);
      act(() => evidence.props!.onClose());
      await waitFor(() => expect(mockApi.checks).toHaveBeenCalledTimes(2));
    });
  });

  describe("track files", () => {
    const threeFiles: ChecksFile[] = [
      makeFile({ source: "gpx:walk-a.gpx", points: 30, last: "2000-01-03T00:00:00Z" }),
      makeFile({ source: "kml:hike.kml", format: "kml", kind: "time_may_be_estimated", points: 500, last: "2000-01-02T00:00:00Z" }),
      makeFile({ source: "exif", format: "exif", kind: "derived", points: 4, last: null }),
    ];

    it("starts with the newest file first, and sorts by any column both ways", async () => {
      mockApi.checks.mockResolvedValue(makeChecks({ files: threeFiles }));
      renderView();
      await loaded();
      expect(fileOrder()).toEqual(["walk-a.gpx", "hike.kml", "Positions of your photos"]);
      expect(within(filesTable()).getByRole("columnheader", { name: "Last" })).toHaveAttribute("aria-sort", "descending");

      await userEvent.click(within(filesTable()).getByRole("button", { name: "Points" }));
      expect(fileOrder()).toEqual(["hike.kml", "walk-a.gpx", "Positions of your photos"]);
      expect(within(filesTable()).getByRole("columnheader", { name: "Points" })).toHaveAttribute("aria-sort", "descending");
      expect(within(filesTable()).getByRole("columnheader", { name: "Last" })).not.toHaveAttribute("aria-sort");

      await userEvent.click(within(filesTable()).getByRole("button", { name: "Points" }));
      expect(fileOrder()).toEqual(["Positions of your photos", "walk-a.gpx", "hike.kml"]);
      expect(within(filesTable()).getByRole("columnheader", { name: "Points" })).toHaveAttribute("aria-sort", "ascending");

      await userEvent.click(within(filesTable()).getByRole("button", { name: "Last" }));
      await userEvent.click(within(filesTable()).getByRole("button", { name: "Last" }));
      // A time JSON could not carry stays last both ways.
      expect(fileOrder()).toEqual(["hike.kml", "walk-a.gpx", "Positions of your photos"]);
    });

    it("shows each file's first and last time in UTC and the browser zone, each labelled", async () => {
      stubBrowserZone("Asia/Kolkata");
      mockApi.checks.mockResolvedValue(
        makeChecks({ files: [makeFile({ first: "2000-01-01T00:00:00Z", last: null })] })
      );
      renderView();
      await loaded();
      const first = cellOf("walk-a.gpx", 4);
      // The zone ends each time too, in a span of its own.
      expect(within(first).getByText("UTC", { selector: "dt" }).nextElementSibling).toHaveTextContent(
        formatInstant("2000-01-01T00:00:00Z", "UTC")
      );
      expect(within(first).getByText("Your browser", { selector: "dt" }).nextElementSibling).toHaveTextContent(
        formatInstant("2000-01-01T00:00:00Z", "Asia/Kolkata")
      );
      // A narrow column breaks a time only before its zone.
      expect(within(first).getByText("Asia/Kolkata")).toHaveClass("whitespace-nowrap");
      expect(within(first).getByText("Asia/Kolkata").previousElementSibling).toHaveClass("whitespace-nowrap");
      expect(cellOf("walk-a.gpx", 5)).toHaveTextContent("year out of range");
    });

    it("stacks each row on a phone, every cell named", async () => {
      renderView();
      await loaded();
      const row = rowOf("walk-a.gpx");
      expect(row).toHaveClass("block", "lg:table-row");
      const points = within(row).getAllByRole("cell")[3];
      expect(points).toHaveClass("flex", "lg:table-cell");
      expect(within(points).getByText("Points")).toHaveClass("lg:hidden");
    });

    it("searches every file once there are enough", async () => {
      const files = Array.from({ length: 8 }, (_, i) => makeFile({ source: `gpx:walk-${i}.gpx`, last: `2000-01-0${i + 1}T00:00:00Z` }));
      mockApi.checks.mockResolvedValue(makeChecks({ files: [...files, makeFile({ source: "nmea:drive.nmea", format: "nmea" })] }));
      renderView();
      await loaded();
      await userEvent.type(screen.getByRole("searchbox", { name: "Search track files" }), "nmea");
      expect(fileOrder()).toEqual(["drive.nmea"]);

      await userEvent.clear(screen.getByRole("searchbox", { name: "Search track files" }));
      await userEvent.type(screen.getByRole("searchbox", { name: "Search track files" }), "no such file");
      expect(screen.getByText("No track file matches.")).toBeInTheDocument();
    });

    it("offers no search for a few files", async () => {
      mockApi.checks.mockResolvedValue(makeChecks({ files: threeFiles }));
      renderView();
      await loaded();
      expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    });

    it("shows 200 rows at a time, sorted and searched over all of them", async () => {
      // f0 is the oldest; the newest come first.
      const files = Array.from({ length: 250 }, (_, i) =>
        makeFile({ source: `gpx:f${i}.gpx`, last: new Date(Date.UTC(2000, 0, 1) + i * 60_000).toISOString() })
      );
      mockApi.checks.mockResolvedValue(makeChecks({ files }));
      renderView();
      await loaded();
      expect(fileOrder()).toHaveLength(200);
      expect(fileOrder()[0]).toBe("f249.gpx");
      expect(screen.getByText("200 of 250 shown")).toBeInTheDocument();

      // f0 is past the first 200, and a search finds it.
      await userEvent.type(screen.getByRole("searchbox", { name: "Search track files" }), "f0.gpx");
      expect(fileOrder()).toEqual(["f0.gpx"]);
      await userEvent.clear(screen.getByRole("searchbox", { name: "Search track files" }));

      await userEvent.click(screen.getByRole("button", { name: "Show more" }));
      expect(fileOrder()).toHaveLength(250);
      expect(fileOrder()[249]).toBe("f0.gpx");
      expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
    });

    it("renders a file's label as text", async () => {
      const label = 'gpx:<img src="x" onerror="alert(1)">.gpx';
      const future = makeGroup({ key: "gpx:<b>bold</b>.gpx", label: "gpx:<b>bold</b>.gpx", count: 2, evidence: {} });
      mockApi.checks.mockResolvedValue(
        makeChecks({ files: [makeFile({ source: label })], checks: makeCheckList({ future_track_points: [future] }) })
      );
      renderView();
      await loaded();
      expect(screen.getByText('<img src="x" onerror="alert(1)">.gpx')).toBeInTheDocument();
      expect(screen.getByText(/^2 points of <b>bold<\/b>\.gpx are dated/)).toBeInTheDocument();
      const root = screen.getByRole("dialog", { name: "Checks" });
      expect(root.querySelector("img")).toBeNull();
      expect(root.querySelector("b")).toBeNull();
    });

    describe("compare files", () => {
      const walk = makeAgreementFile({ source: "gpx:walk-a.gpx", median_m: 12, matched: 18, samples: 20, modal_gap_share: 0.94 });
      const hike = makeAgreementFile({ source: "kml:hike.kml", median_m: null, matched: 3, samples: 20, modal_gap_share: 0.23 });
      // Both log files of threeFiles, in label order, in one answer.
      const agreement: AgreementResponse = makeAgreement({ files: [walk, hike] });
      // The same comparison in two answers: the server's time budget stopped the first after walk-a.gpx.
      const firstPart = makeAgreement({ files: [walk], total_files: 2, next_from: 1 });
      const lastPart = makeAgreement({ files: [hike], total_files: 2, next_from: null });
      const nothingMore = makeAgreement({ files: [], total_files: 2, next_from: 1 });

      const status = () => screen.getByRole("status");
      // The index each request started from.
      const froms = () => mockApi.checksAgreement.mock.calls.map((call) => call[0]);

      async function compare() {
        mockApi.checks.mockResolvedValue(makeChecks({ files: threeFiles }));
        renderView();
        await loaded();
        await userEvent.click(screen.getByRole("button", { name: "Compare files" }));
      }

      it("explains the columns above the table, the compared ones once there is a comparison", async () => {
        // The explanations come before the table in the page: after it they would sit below every row.
        const explained = (note: string) => {
          const text = screen.getByText(note);
          expect(text.compareDocumentPosition(filesTable()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        };
        mockApi.checksAgreement.mockResolvedValue(agreement);
        mockApi.checks.mockResolvedValue(makeChecks({ files: threeFiles }));
        renderView();
        await loaded();
        for (const [, note] of FILE_COLUMN_NOTES) explained(note);
        for (const [, note] of COMPARED_COLUMN_NOTES) expect(screen.queryByText(note)).not.toBeInTheDocument();

        await userEvent.click(screen.getByRole("button", { name: "Compare files" }));
        await within(filesTable()).findByRole("columnheader", { name: DISTANCE_COLUMN });
        for (const [, note] of [...FILE_COLUMN_NOTES, ...COMPARED_COLUMN_NOTES]) explained(note);
      });

      it("loads nothing until asked, then fills the distance and spacing columns", async () => {
        mockApi.checks.mockResolvedValue(makeChecks({ files: threeFiles }));
        const response = deferred<AgreementResponse>();
        mockApi.checksAgreement.mockReturnValue(response.promise);
        renderView();
        await loaded();
        expect(status()).toHaveTextContent(COMPARE_NOTE);
        expect(within(filesTable()).queryByRole("columnheader", { name: DISTANCE_COLUMN })).not.toBeInTheDocument();
        expect(mockApi.checksAgreement).not.toHaveBeenCalled();

        await userEvent.click(screen.getByRole("button", { name: "Compare files" }));
        expect(status()).toHaveTextContent(COMPARING);
        expect(mockApi.checksAgreement).toHaveBeenCalledWith(0, expect.any(AbortSignal));
        // Busy but never disabled, so it keeps the keyboard focus; a second press waits for the first.
        const button = screen.getByRole("button", { name: "Compare files" });
        expect(button).toHaveAttribute("aria-disabled", "true");
        expect(button).not.toHaveAttribute("disabled");
        expect(button).toHaveFocus();
        await userEvent.click(button);
        expect(mockApi.checksAgreement).toHaveBeenCalledTimes(1);

        await act(async () => response.resolve(agreement));
        expect(status()).toHaveTextContent("Compared 2 of 2 files.");
        expect(within(filesTable()).getByRole("columnheader", { name: DISTANCE_COLUMN })).toBeInTheDocument();
        expect(within(filesTable()).getByRole("columnheader", { name: "Spacing" })).toBeInTheDocument();
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("18 of 20 matched");
        expect(cellOf("walk-a.gpx", SPACING)).toHaveTextContent("94%");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("too few overlaps");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("3 of 20 matched");
        expect(cellOf("hike.kml", SPACING)).toHaveTextContent("23%");
        expect(cellOf("Positions of your photos", DISTANCE)).toHaveTextContent("not compared");
        expect(cellOf("Positions of your photos", SPACING)).toHaveTextContent("not compared");
        // On a phone each cell is named by its column.
        expect(within(cellOf("walk-a.gpx", DISTANCE)).getByText(DISTANCE_COLUMN)).toHaveClass("lg:hidden");
        expect(screen.getByRole("button", { name: "Compare again" })).toHaveAttribute("aria-disabled", "false");
        expect(screen.queryByRole("button", { name: "Compare the rest" })).not.toBeInTheDocument();
      });

      it("sorts by the distance to other formats, files without one last", async () => {
        mockApi.checksAgreement.mockResolvedValue(agreement);
        await compare();
        await userEvent.click(await within(filesTable()).findByRole("button", { name: DISTANCE_COLUMN }));
        expect(fileOrder()).toEqual(["walk-a.gpx", "Positions of your photos", "hike.kml"]);
      });

      it("goes on from where each answer stopped until the last file, filling the table as it goes", async () => {
        const first = deferred<AgreementResponse>();
        const last = deferred<AgreementResponse>();
        mockApi.checksAgreement.mockReturnValueOnce(first.promise).mockReturnValueOnce(last.promise);
        await compare();

        await act(async () => first.resolve(firstPart));
        expect(froms()).toEqual([0, 1]);
        expect(status()).toHaveTextContent("Compared 1 of 2 files…");
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("comparing…");
        expect(cellOf("hike.kml", SPACING)).toHaveTextContent("comparing…");
        expect(cellOf("Positions of your photos", DISTANCE)).toHaveTextContent("not compared");
        expect(screen.getByRole("button", { name: "Compare again" })).toHaveAttribute("aria-disabled", "true");

        await act(async () => last.resolve(lastPart));
        expect(froms()).toEqual([0, 1]);
        expect(status()).toHaveTextContent("Compared 2 of 2 files.");
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("too few overlaps");
        expect(screen.getByRole("button", { name: "Compare again" })).toHaveAttribute("aria-disabled", "false");
      });

      it("stops when an answer compares nothing, and goes on from there on request", async () => {
        mockApi.checksAgreement.mockResolvedValueOnce(firstPart).mockResolvedValueOnce(nothingMore);
        await compare();
        expect(await screen.findByText(TRUNCATED_NOTE)).toBeInTheDocument();
        // Asking again from the same file would never end.
        expect(froms()).toEqual([0, 1]);
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("not reached");
        expect(cellOf("hike.kml", SPACING)).toHaveTextContent("not reached");
        expect(cellOf("Positions of your photos", DISTANCE)).toHaveTextContent("not compared");

        const rest = screen.getByRole("button", { name: "Compare the rest" });
        expect(rest).toHaveClass("touch:min-h-11");
        const next = deferred<AgreementResponse>();
        mockApi.checksAgreement.mockReturnValueOnce(next.promise);
        await userEvent.click(rest);
        expect(froms()).toEqual([0, 1, 1]);
        // The button goes while the comparison runs; the busy Compare again keeps the focus.
        expect(screen.queryByRole("button", { name: "Compare the rest" })).not.toBeInTheDocument();
        expect(screen.getByRole("button", { name: "Compare again" })).toHaveFocus();
        expect(status()).toHaveTextContent("Compared 1 of 2 files…");
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");

        await act(async () => next.resolve(lastPart));
        expect(status()).toHaveTextContent("Compared 2 of 2 files.");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("too few overlaps");
      });

      it("stops on a failed request, keeping what it compared, and goes on from there on request", async () => {
        const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
        mockApi.checksAgreement.mockResolvedValueOnce(firstPart).mockRejectedValueOnce(new ApiError("boom", 500));
        await compare();
        expect(await screen.findByText(`Could not compare every file. Files marked "not reached" were left out.`)).toBeInTheDocument();
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("not reached");

        mockApi.checksAgreement.mockResolvedValueOnce(lastPart);
        await userEvent.click(screen.getByRole("button", { name: "Compare the rest" }));
        expect(await screen.findByText("Compared 2 of 2 files.")).toBeInTheDocument();
        expect(froms()).toEqual([0, 1, 1]);
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("too few overlaps");
        consoleError.mockRestore();
      });

      it("says when the server is busy with other comparisons, before any answer and part way", async () => {
        const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
        mockApi.checksAgreement.mockRejectedValueOnce(new ApiError("GET /checks/agreement failed: 503", 503));
        await compare();
        expect(await screen.findByText(COMPARE_BUSY)).toBeInTheDocument();
        expect(within(filesTable()).queryByRole("columnheader", { name: DISTANCE_COLUMN })).not.toBeInTheDocument();
        expect(screen.queryByRole("button", { name: "Compare the rest" })).not.toBeInTheDocument();

        mockApi.checksAgreement
          .mockResolvedValueOnce(firstPart)
          .mockRejectedValueOnce(new ApiError("GET /checks/agreement failed: 503", 503));
        await userEvent.click(screen.getByRole("button", { name: "Compare files" }));
        expect(await screen.findByText(`${COMPARE_BUSY} Files marked "not reached" were left out.`)).toBeInTheDocument();
        expect(froms()).toEqual([0, 0, 1]);
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");
        expect(screen.getByRole("button", { name: "Compare the rest" })).toBeInTheDocument();
        consoleError.mockRestore();
      });

      it("says so when the comparison fails, and keeps an earlier one", async () => {
        const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
        mockApi.checksAgreement.mockResolvedValueOnce(agreement).mockRejectedValueOnce(new ApiError("boom", 500));
        await compare();
        await userEvent.click(await screen.findByRole("button", { name: "Compare again" }));

        expect(await screen.findByText("Could not compare the files.")).toBeInTheDocument();
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("12 m");
        expect(screen.queryByRole("button", { name: "Compare the rest" })).not.toBeInTheDocument();
        consoleError.mockRestore();
      });

      it("starts over from the first file on Compare again, the last comparison on screen until the new one answers", async () => {
        mockApi.checksAgreement.mockResolvedValueOnce(agreement);
        await compare();
        await screen.findByText("Compared 2 of 2 files.");
        const first = deferred<AgreementResponse>();
        const last = deferred<AgreementResponse>();
        mockApi.checksAgreement.mockReturnValueOnce(first.promise).mockReturnValueOnce(last.promise);

        await userEvent.click(screen.getByRole("button", { name: "Compare again" }));
        expect(froms()).toEqual([0, 0]);
        expect(status()).toHaveTextContent(COMPARING);
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("too few overlaps");

        // From its first answer on, the table shows the new comparison alone.
        const moved = makeAgreementFile({ source: "gpx:walk-a.gpx", median_m: 40 });
        await act(async () => first.resolve(makeAgreement({ files: [moved], total_files: 2, next_from: 1 })));
        expect(cellOf("walk-a.gpx", DISTANCE)).toHaveTextContent("40 m");
        expect(cellOf("hike.kml", DISTANCE)).toHaveTextContent("comparing…");
        await act(async () => last.resolve(lastPart));
        expect(status()).toHaveTextContent("Compared 2 of 2 files.");
      });

      it("cancels the request in flight, and asks nothing more, when the view closes part way", async () => {
        const last = deferred<AgreementResponse>();
        mockApi.checksAgreement.mockResolvedValueOnce(firstPart).mockReturnValueOnce(last.promise);
        mockApi.checks.mockResolvedValue(makeChecks({ files: threeFiles }));
        const { unmount } = renderView();
        await loaded();
        await userEvent.click(screen.getByRole("button", { name: "Compare files" }));
        await waitFor(() => expect(froms()).toEqual([0, 1]));
        const signal: AbortSignal = mockApi.checksAgreement.mock.calls[1][1];
        expect(signal.aborted).toBe(false);

        unmount();
        expect(signal.aborted).toBe(true);
        await act(async () => last.resolve(makeAgreement({ files: [hike], total_files: 3, next_from: 2 })));
        expect(froms()).toEqual([0, 1]);
      });
    });
  });

  describe("formats against your photos", () => {
    it("lists each format's typical distance, with what it does and does not say", async () => {
      mockApi.checks.mockResolvedValue(
        makeChecks({
          residuals: [
            { format: "gpx", n: 696, p50_m: 11, p90_m: 48.6 },
            { format: "esoko", n: 552, p50_m: 340, p90_m: 2400 },
          ],
        })
      );
      renderView();
      await loaded();
      const table = screen.getByRole("table", { name: RESIDUALS_CAPTION });
      expect(RESIDUALS_CAPTION).toMatch(/It measures disagreement and does not say which side is right\./);
      const rows = within(table)
        .getAllByRole("row")
        .slice(1)
        .map((row) => within(row).getAllByRole("cell").map((cell) => cell.textContent));
      expect(rows).toEqual([
        ["gpx", "696", "11 m", "49 m"],
        ["esoko", "552", "340 m", "2.4 km"],
      ]);
    });

    it("says when no photo could be compared", async () => {
      renderView();
      await loaded();
      expect(
        screen.getByText("None of your photos with their own position has a point from your track files within 5 s.")
      ).toBeInTheDocument();
    });
  });
});
