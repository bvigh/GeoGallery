import { useEffect, useState, type ComponentProps } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { ShiftTimesDialog } from "@/components/ShiftTimesDialog";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { BACK_DEPTH_KEY, useCloseOnBack } from "@/lib/history";
import { formatCount } from "@/lib/numbers";
import { formatInstant } from "@/lib/time";
import { formatDayRange, SHIFT_OUTCOME_UNKNOWN } from "@/lib/timeEdit";
import type { MediaItem, ShiftTimesResult } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ApiError: actual.ApiError, api: { shiftTimes: vi.fn() } };
});

// The browser's zone is fixed, so no expectation depends on the runner's (UTC on CI).
vi.mock("@/lib/time", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/time")>();
  return { ...actual, browserTimeZone: () => "Europe/Warsaw" };
});

const mockApi = api as unknown as { shiftTimes: ReturnType<typeof vi.fn> };

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

function item(id: string, fields: Partial<MediaItem>): MediaItem {
  return {
    id,
    filename: `${id}.jpg`,
    media_type: "photo",
    lat: null,
    lng: null,
    gps_confidence: null,
    gps_source: null,
    taken_at: null,
    storage_key: `media/me/${id}.jpg`,
    trip_id: null,
    owner_id: "me",
    owner_name: "Me",
    camera_model: null,
    ...fields,
  };
}

// A trip with a camera whose clock was off and a phone whose clock was right, in gallery order.
const camera = [
  item("c1", { camera_model: "DC-TZ200", taken_at: "2019-03-14T09:00:00Z" }),
  item("c3", { camera_model: "DC-TZ200", taken_at: "2019-03-13T12:00:00Z" }),
  item("c2", { camera_model: "DC-TZ200", taken_at: "2019-03-12T08:00:00Z" }),
];
const phone = [
  item("p1", { camera_model: "SM-S921B", taken_at: "2019-03-13T10:00:00Z" }),
  item("p2", { camera_model: "SM-S921B", taken_at: "2019-03-13T11:00:00Z" }),
];
const unknown = [item("u1", { camera_model: null, taken_at: "2019-03-13T15:00:00Z" })];
const mixed = [...camera, ...phone, ...unknown];

const shifted: ShiftTimesResult = { shifted: 3, undated: 0, trips: [{ id: "t2", items: 3 }], followup_failed: false };

type Props = ComponentProps<typeof ShiftTimesDialog>;

function renderDialog(overrides: Partial<Props> = {}) {
  const props: Props = {
    open: true,
    onOpenChange: vi.fn(),
    title: 'Shift times in "Iceland 2019"',
    items: mixed,
    currentUserId: "me",
    onShifted: vi.fn(),
    ...overrides,
  };
  const view = render(<ShiftTimesDialog {...props} />);
  return { ...view, props };
}

const box = (name: string | RegExp) => screen.getByRole("checkbox", { name });
const applyButton = () => screen.getByRole("button", { name: /^Shift/ });

function typeAmount(fields: { days?: string; hours?: string; minutes?: string; seconds?: string }) {
  const labels = { days: "Days", hours: "Hours", minutes: "Minutes", seconds: "Seconds" } as const;
  for (const [field, value] of Object.entries(fields)) {
    fireEvent.change(screen.getByLabelText(labels[field as keyof typeof labels]), { target: { value } });
  }
}

describe("ShiftTimesDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.shiftTimes.mockResolvedValue(shifted);
  });

  // A dialog left open would pop its history entry after the next test began.
  afterEach(async () => {
    cleanup();
    await settle();
  });

  it("lists the caller's dated items by camera, most items first, with their days in the browser's zone", () => {
    renderDialog();
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: 'Shift times in "Iceland 2019"' })).toBeInTheDocument();

    const boxes = within(dialog).getAllByRole("checkbox");
    expect(boxes.map((b) => b.closest("label")?.textContent)).toEqual([
      `DC-TZ200 3 items · ${formatDayRange("2019-03-12T08:00:00Z", "2019-03-14T09:00:00Z", "Europe/Warsaw")}`,
      `SM-S921B 2 items · ${formatDayRange("2019-03-13T10:00:00Z", "2019-03-13T11:00:00Z", "Europe/Warsaw")}`,
      `No camera model in the file: photos 1 item · ${formatDayRange("2019-03-13T15:00:00Z", "2019-03-13T15:00:00Z", "Europe/Warsaw")}`,
    ]);
  });

  // A trip mixes a phone whose clock was right with the camera that was off.
  it("ticks no camera when there are several", () => {
    renderDialog();
    for (const b of screen.getAllByRole("checkbox")) expect(b).not.toBeChecked();
    expect(applyButton()).toBeDisabled();
    expect(applyButton()).toHaveTextContent("Shift items");
    expect(screen.getByText("Tick at least one camera.")).toBeInTheDocument();
    expect(applyButton()).toHaveAccessibleDescription("Tick at least one camera.");
  });

  it("ticks the only camera there is", () => {
    renderDialog({ items: camera });
    expect(box(/^DC-TZ200/)).toBeChecked();
    expect(applyButton()).toHaveTextContent("Shift 3 items");
  });

  it("waits for an amount, then previews the shift of the earliest ticked item", async () => {
    renderDialog();
    await userEvent.click(box(/^DC-TZ200/));
    expect(applyButton()).toBeDisabled();
    expect(screen.getByText("Enter how far to shift the times.")).toBeInTheDocument();

    typeAmount({ hours: "1" });
    expect(applyButton()).toBeEnabled();
    expect(applyButton()).toHaveTextContent("Shift 3 items");
    expect(screen.getByText("Shifts 3 items 1 h later.")).toBeInTheDocument();
    const preview = screen.getByText(/^The earliest,/);
    expect(preview).toHaveTextContent(
      `The earliest, c2.jpg: ${formatInstant("2019-03-12T08:00:00Z", "Europe/Warsaw")} → ${formatInstant(
        "2019-03-12T09:00:00Z",
        "Europe/Warsaw"
      )}`
    );

    // Ticking the phone too makes its earlier item the one previewed.
    await userEvent.click(box(/^SM-S921B/));
    expect(screen.getByText("Shifts 5 items 1 h later.")).toBeInTheDocument();
    expect(screen.getByText(/^The earliest,/)).toHaveTextContent("c2.jpg");
  });

  it.each([
    { name: "later", direction: "Later", fields: { days: "1", hours: "2", minutes: "3", seconds: "4" }, delta: 93784, words: "1 d 2 h 3 min 4 s later" },
    { name: "earlier", direction: "Earlier", fields: { hours: "1" }, delta: -3600, words: "1 h earlier" },
    { name: "earlier by a count past its unit", direction: "Earlier", fields: { minutes: "90" }, delta: -5400, words: "1 h 30 min earlier" },
  ])("sends the ticked items and the amount, $name", async ({ direction, fields, delta, words }) => {
    const { props } = renderDialog();
    await userEvent.click(box(/^DC-TZ200/));
    await userEvent.click(screen.getByRole("radio", { name: direction }));
    typeAmount(fields);
    expect(screen.getByText(`Shifts 3 items ${words}.`)).toBeInTheDocument();

    await userEvent.click(applyButton());
    expect(mockApi.shiftTimes).toHaveBeenCalledTimes(1);
    const [ids, sent] = mockApi.shiftTimes.mock.calls[0];
    expect([...ids].sort()).toEqual(["c1", "c2", "c3"]);
    // The host hears the same items sent.
    expect(sent).toBe(delta);
    await waitFor(() => expect(props.onShifted).toHaveBeenCalledWith(shifted, delta, ids));
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
  });

  it("starts later, and an amount of zero earlier is still no shift", async () => {
    renderDialog({ items: camera });
    expect(screen.getByRole("radio", { name: "Later" })).toBeChecked();
    await userEvent.click(screen.getByRole("radio", { name: "Earlier" }));
    typeAmount({ days: "0", hours: "0" });
    expect(applyButton()).toBeDisabled();
    expect(screen.getByText("Enter how far to shift the times.")).toBeInTheDocument();
  });

  it.each([
    ["a negative count", { hours: "-1" }],
    ["a fraction", { hours: "1.5" }],
    ["a word", { minutes: "ten" }],
  ])("refuses %s", (_name, fields) => {
    renderDialog({ items: camera });
    typeAmount(fields);
    expect(applyButton()).toBeDisabled();
    expect(applyButton()).toHaveAccessibleDescription("Enter whole numbers of 0 or more.");
  });

  it("refuses a shift past 100 years", () => {
    renderDialog({ items: camera });
    typeAmount({ days: "36526" });
    expect(applyButton()).toBeDisabled();
    expect(screen.getByText("A shift can be at most 100 years.")).toBeInTheDocument();
  });

  it("refuses a shift that would move a time past what the backend stores", () => {
    renderDialog({ items: [item("late", { camera_model: "X", taken_at: "9998-12-31T12:00:00Z" })] });
    typeAmount({ days: "1" });
    expect(applyButton()).toBeDisabled();
    expect(screen.getByText("This would move some times outside the years 1 to 9998.")).toBeInTheDocument();
  });

  it("refuses more than 5000 items at once, saying why", async () => {
    const many = Array.from({ length: 5001 }, (_, i) =>
      item(`m${i}`, { camera_model: "DC-TZ200", taken_at: "2019-03-13T12:00:00Z" })
    );
    renderDialog({ items: [...many, ...phone] });
    typeAmount({ hours: "1" });
    await userEvent.click(box(/^DC-TZ200/));
    expect(applyButton()).toBeDisabled();
    expect(applyButton()).toHaveTextContent(`Shift ${formatCount(5001)} items`);
    expect(
      screen.getByText(`At most ${formatCount(5000)} items can be shifted at once, and ${formatCount(5001)} items are ticked.`)
    ).toBeInTheDocument();

    await userEvent.click(box(/^DC-TZ200/));
    await userEvent.click(box(/^SM-S921B/));
    expect(applyButton()).toBeEnabled();
  });

  it("leaves out undated items and other people's items, saying so", async () => {
    const { props } = renderDialog({
      items: [
        ...camera,
        item("undated", { camera_model: "DC-TZ200" }),
        item("bob1", { camera_model: "DC-TZ200", taken_at: "2019-03-13T12:00:00Z", owner_id: "bob", owner_name: "Bob" }),
        item("bob2", { camera_model: "iPhone 12", taken_at: "2019-03-13T12:00:00Z", owner_id: "bob", owner_name: "Bob" }),
      ],
    });
    expect(screen.getAllByRole("checkbox")).toHaveLength(1);
    expect(screen.getByText("1 item without a capture time is left out: there is no time to shift.")).toBeInTheDocument();
    expect(
      screen.getByText("2 items shared by others are left out: only their owners can change their times.")
    ).toBeInTheDocument();

    typeAmount({ hours: "1" });
    await userEvent.click(applyButton());
    expect([...mockApi.shiftTimes.mock.calls[0][0]].sort()).toEqual(["c1", "c2", "c3"]);
    await waitFor(() => expect(props.onShifted).toHaveBeenCalled());
  });

  it("says when no item of the caller's has a time to shift", () => {
    renderDialog({
      items: [
        item("undated", {}),
        item("bob1", { taken_at: "2019-03-13T12:00:00Z", owner_id: "bob" }),
      ],
    });
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(applyButton()).toBeDisabled();
    expect(screen.getByText("None of your items here has a capture time to shift.")).toBeInTheDocument();
  });

  it("shows the items loading, and a load failure", () => {
    const { rerender, props } = renderDialog({ items: null });
    expect(screen.getByText("Loading items…")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Shift/ })).not.toBeInTheDocument();

    rerender(<ShiftTimesDialog {...props} items={null} loadError="Could not load the trip's items." />);
    expect(screen.getByText("Could not load the trip's items.")).toBeInTheDocument();
    expect(screen.queryByText("Loading items…")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Shift/ })).not.toBeInTheDocument();

    rerender(<ShiftTimesDialog {...props} items={camera} loadError={null} />);
    expect(box(/^DC-TZ200/)).toBeChecked();
  });

  it("shifts once, shows that it is busy, then reports and closes", async () => {
    let resolve!: (r: ShiftTimesResult) => void;
    mockApi.shiftTimes.mockReturnValue(new Promise<ShiftTimesResult>((r) => (resolve = r)));
    const { props } = renderDialog({ items: camera });
    typeAmount({ hours: "1" });

    await userEvent.click(applyButton());
    expect(applyButton()).toHaveTextContent("Shifting…");
    expect(applyButton()).toBeDisabled();
    expect(box(/^DC-TZ200/)).toBeDisabled();
    fireEvent.submit(applyButton().closest("form")!);
    expect(mockApi.shiftTimes).toHaveBeenCalledTimes(1);
    expect(props.onShifted).not.toHaveBeenCalled();

    await act(async () => resolve(shifted));
    expect(props.onShifted).toHaveBeenCalledWith(shifted, 3600, ["c1", "c3", "c2"]);
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
  });

  // The backend answers these before or at its commit: nothing was shifted.
  it.each([
    ["the server's reason for a 400", new ApiError("POST failed: 400", 400, "delta_seconds must not be zero"), "delta_seconds must not be zero"],
    ["a sentence for a 400 without a reason", new ApiError("POST failed: 400", 400), "The server refused this shift."],
    ["that items are gone for a 404", new ApiError("POST failed: 404", 404), "Some of these items are no longer available. Reload and try again."],
    ["a retry sentence for a server error", new ApiError("POST failed: 500", 500), "Could not shift the times. Try again."],
  ])("shows %s, and stays open to try again", async (_name, failure, message) => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.shiftTimes.mockRejectedValueOnce(failure);
    const onOutcomeUnknown = vi.fn();
    const { props } = renderDialog({ items: camera, onOutcomeUnknown });
    typeAmount({ hours: "1" });

    await userEvent.click(applyButton());
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(props.onShifted).not.toHaveBeenCalled();
    expect(props.onOpenChange).not.toHaveBeenCalled();
    expect(onOutcomeUnknown).not.toHaveBeenCalled();
    expect(applyButton()).toHaveTextContent("Shift 3 items");
    expect(applyButton()).toBeEnabled();

    await userEvent.click(applyButton());
    await waitFor(() => expect(props.onShifted).toHaveBeenCalledWith(shifted, 3600, expect.any(Array)));
  });

  // A gateway gave up, or the connection dropped, perhaps after the commit: a second shift of the
  // same items would move them twice.
  it.each([
    ["a gateway timeout", new ApiError("POST failed: 504", 504)],
    ["a bad gateway", new ApiError("POST failed: 502", 502)],
    ["an unavailable server", new ApiError("POST failed: 503", 503)],
    ["a network failure", new TypeError("Failed to fetch")],
  ])("never offers to shift again after %s, and has the host reload", async (_name, failure) => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.shiftTimes.mockRejectedValueOnce(failure);
    const onOutcomeUnknown = vi.fn();
    const { props } = renderDialog({ items: camera, onOutcomeUnknown });
    typeAmount({ hours: "1" });

    await userEvent.click(applyButton());
    expect(await screen.findByText(SHIFT_OUTCOME_UNKNOWN)).toBeInTheDocument();
    expect(screen.queryByText(/try again/i)).not.toBeInTheDocument();
    expect(onOutcomeUnknown).toHaveBeenCalledTimes(1);
    expect(props.onShifted).not.toHaveBeenCalled();
    expect(props.onOpenChange).not.toHaveBeenCalled();
    expect(applyButton()).toBeDisabled();
    expect(applyButton()).toHaveAccessibleDescription(SHIFT_OUTCOME_UNKNOWN);

    // Not by another amount either, nor by submitting the form.
    typeAmount({ hours: "2" });
    expect(applyButton()).toBeDisabled();
    fireEvent.submit(applyButton().closest("form")!);
    expect(mockApi.shiftTimes).toHaveBeenCalledTimes(1);
  });

  it("starts afresh on the next opening after an answer that did not come", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.shiftTimes.mockRejectedValueOnce(new ApiError("POST failed: 504", 504));
    const { rerender, props } = renderDialog({ items: camera });
    typeAmount({ hours: "1" });
    await userEvent.click(applyButton());
    expect(await screen.findByText(SHIFT_OUTCOME_UNKNOWN)).toBeInTheDocument();

    rerender(<ShiftTimesDialog {...props} open={false} />);
    rerender(<ShiftTimesDialog {...props} open />);
    expect(screen.queryByText(SHIFT_OUTCOME_UNKNOWN)).not.toBeInTheDocument();
    typeAmount({ hours: "1" });
    expect(applyButton()).toBeEnabled();
  });

  it("has the host reload after an answer that did not come though the dialog closed meanwhile", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    let reject!: (e: unknown) => void;
    mockApi.shiftTimes.mockReturnValue(new Promise<ShiftTimesResult>((_r, j) => (reject = j)));
    const onOutcomeUnknown = vi.fn();
    const { rerender, props } = renderDialog({ items: camera, onOutcomeUnknown });
    typeAmount({ hours: "1" });
    await userEvent.click(applyButton());
    rerender(<ShiftTimesDialog {...props} onOutcomeUnknown={onOutcomeUnknown} open={false} />);

    await act(async () => reject(new TypeError("Failed to fetch")));
    expect(onOutcomeUnknown).toHaveBeenCalledTimes(1);
  });

  it("reports a shift that finishes after the dialog closed, without closing again", async () => {
    let resolve!: (r: ShiftTimesResult) => void;
    mockApi.shiftTimes.mockReturnValue(new Promise<ShiftTimesResult>((r) => (resolve = r)));
    const { rerender, props } = renderDialog({ items: camera });
    typeAmount({ hours: "1" });
    await userEvent.click(applyButton());
    rerender(<ShiftTimesDialog {...props} open={false} />);

    await act(async () => resolve(shifted));
    expect(props.onShifted).toHaveBeenCalledWith(shifted, 3600, ["c1", "c3", "c2"]);
    expect(props.onOpenChange).not.toHaveBeenCalled();
  });

  it("starts again with the default ticks and no amount on every opening", async () => {
    const { rerender, props } = renderDialog();
    await userEvent.click(box(/^DC-TZ200/));
    typeAmount({ hours: "1" });
    rerender(<ShiftTimesDialog {...props} open={false} />);
    rerender(<ShiftTimesDialog {...props} open />);
    expect(box(/^DC-TZ200/)).not.toBeChecked();
    expect(screen.getByLabelText("Hours")).toHaveValue("");
  });

  it("is touch-sized", () => {
    renderDialog();
    expect(box(/^DC-TZ200/).closest("label")).toHaveClass("touch:min-h-11");
    expect(screen.getByRole("radio", { name: "Later" }).closest("label")).toHaveClass("touch:min-h-11");
    for (const label of ["Days", "Hours", "Minutes", "Seconds"]) {
      expect(screen.getByLabelText(label)).toHaveClass("touch:min-h-11", "touch:text-base");
      expect(screen.getByLabelText(label)).toHaveAttribute("inputmode", "numeric");
    }
    // DialogFooter gives its buttons 44 px on touch.
    expect(applyButton().parentElement).toHaveClass("touch:[&>*]:min-h-11");
  });

  describe("over a fullscreen view", () => {
    // A gallery's essentials: a history entry of its own, and keys on window that leave a dialog's
    // keys alone. The dialog opens from a button, after the gallery holds its entry.
    function Gallery({ onGalleryClose, onDialogClose }: { onGalleryClose: () => void; onDialogClose: () => void }) {
      const [shifting, setShifting] = useState(false);
      useCloseOnBack(onGalleryClose);
      useEffect(() => {
        function onKey(e: KeyboardEvent) {
          if (keyBelongsToDialog(e)) return;
          if (e.key === "Escape") onGalleryClose();
        }
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
      }, [onGalleryClose]);
      return (
        <>
          <button onClick={() => setShifting(true)}>Shift times…</button>
          <ShiftTimesDialog
            open={shifting}
            onOpenChange={(open) => {
              if (!open) onDialogClose();
              setShifting(open);
            }}
            title="Shift times"
            items={mixed}
            currentUserId="me"
            onShifted={vi.fn()}
          />
        </>
      );
    }

    async function openOverGallery(onGalleryClose: () => void, onDialogClose: () => void) {
      render(<Gallery onGalleryClose={onGalleryClose} onDialogClose={onDialogClose} />);
      await settle();
      expect(depth()).toBe(1);
      await userEvent.click(screen.getByRole("button", { name: "Shift times…" }));
      await settle();
      expect(depth()).toBe(2);
    }

    it("is a dialog the view's key handler leaves alone", () => {
      renderDialog();
      expect(screen.getByRole("dialog")).toHaveAttribute("data-app-dialog");
      expect(keyBelongsToDialog(new KeyboardEvent("keydown", { key: "ArrowRight" }))).toBe(true);
    });

    it("closes only the dialog on Escape", async () => {
      const onGalleryClose = vi.fn();
      const onDialogClose = vi.fn();
      await openOverGallery(onGalleryClose, onDialogClose);

      await userEvent.click(screen.getByLabelText("Hours"));
      await userEvent.keyboard("{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(onDialogClose).toHaveBeenCalledTimes(1);
      expect(onGalleryClose).not.toHaveBeenCalled();
      await settle();
      expect(depth()).toBe(1);
    });

    it("closes only the dialog on the phone's back gesture", async () => {
      const onGalleryClose = vi.fn();
      const onDialogClose = vi.fn();
      await openOverGallery(onGalleryClose, onDialogClose);

      await act(async () => {
        const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
        window.history.back();
        await popped;
      });
      await settle();
      expect(onDialogClose).toHaveBeenCalledTimes(1);
      expect(onGalleryClose).not.toHaveBeenCalled();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(depth()).toBe(1);
    });
  });
});
