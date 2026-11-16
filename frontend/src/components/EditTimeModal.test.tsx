import { useEffect, useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { EditTimeModal } from "@/components/EditTimeModal";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { BACK_DEPTH_KEY, useCloseOnBack } from "@/lib/history";
import { formatInstant } from "@/lib/time";
import type { SetTakenAtResult } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ApiError: actual.ApiError, api: { setTakenAt: vi.fn() } };
});

// The browser's zone is fixed, so no expectation depends on the runner's (UTC on CI).
vi.mock("@/lib/time", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/time")>();
  return { ...actual, browserTimeZone: () => "Europe/Warsaw" };
});

const mockApi = api as unknown as { setTakenAt: ReturnType<typeof vi.fn> };

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

const dated = { id: "m1", filename: "P1033627.JPG", taken_at: "2019-03-13T18:28:15Z" };
const undated = { id: "m2", filename: "clip.mp4", taken_at: null };
const saved: SetTakenAtResult = { taken_at: "2019-03-13T17:28:15Z", trip_id: "t1", followup_failed: false };

function renderModal(item: { id: string; filename: string; taken_at: string | null } | null = dated) {
  const onClose = vi.fn();
  const onSaved = vi.fn();
  const view = render(<EditTimeModal open item={item} onClose={onClose} onSaved={onSaved} />);
  return { ...view, onClose, onSaved };
}

const dateField = () => screen.getByLabelText("Date");
const timeField = () => screen.getByLabelText("Time");
const zoneField = () => screen.getByLabelText("Time zone");
const saveButton = () => screen.getByRole("button", { name: /^Sav/ });

// One second on from the stored 19:28:15 in Warsaw: a change to save.
function changeTime() {
  fireEvent.change(timeField(), { target: { value: "19:28:16" } });
}

describe("EditTimeModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.setTakenAt.mockResolvedValue(saved);
  });

  // A dialog left open would pop its history entry after the next test began.
  afterEach(async () => {
    cleanup();
    await settle();
  });

  it("starts from the stored time as a wall clock in the browser's zone, seconds included", () => {
    renderModal();
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Edit capture time" })).toBeInTheDocument();
    expect(within(dialog).getByText("P1033627.JPG")).toBeInTheDocument();
    expect(dateField()).toHaveValue("2019-03-13");
    // 18:28:15 UTC is 19:28:15 in Warsaw in March.
    expect(timeField()).toHaveValue("19:28:15");
    expect(timeField()).toHaveAttribute("step", "1");
    expect(zoneField()).toHaveValue("Europe/Warsaw");
    // Nothing to save until a field changes.
    expect(saveButton()).toBeDisabled();
  });

  it("shows the stored time in UTC and in the browser's zone", () => {
    renderModal();
    expect(
      screen.getByText(
        `Stored: ${formatInstant(dated.taken_at, "UTC")} · ${formatInstant(dated.taken_at, "Europe/Warsaw")}`
      )
    ).toBeInTheDocument();
  });

  it("explains what else moves, and that server retime keeps the time", () => {
    renderModal();
    const description = screen.getByText(/own track point moves with it/);
    expect(description).toHaveTextContent("trips are detected again");
    expect(description).toHaveTextContent("stays when an administrator runs server retime");
  });

  it("starts an undated item empty, and saves once a date and a time are entered", async () => {
    const { onSaved } = renderModal(undated);
    expect(screen.getByText("No capture time stored.")).toBeInTheDocument();
    expect(dateField()).toHaveValue("");
    expect(timeField()).toHaveValue("");
    expect(zoneField()).toHaveValue("Europe/Warsaw");
    expect(saveButton()).toBeDisabled();

    fireEvent.change(dateField(), { target: { value: "2020-08-01" } });
    expect(saveButton()).toBeDisabled();
    fireEvent.change(timeField(), { target: { value: "09:15:30" } });
    expect(saveButton()).toBeEnabled();

    await userEvent.click(saveButton());
    expect(mockApi.setTakenAt).toHaveBeenCalledWith("m2", "2020-08-01T09:15:30", { zone: "Europe/Warsaw" });
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(saved));
  });

  it("sends the wall clock with the zone chosen", async () => {
    renderModal();
    await userEvent.selectOptions(zoneField(), "America/New_York");
    await userEvent.click(saveButton());
    expect(mockApi.setTakenAt).toHaveBeenCalledWith("m1", "2019-03-13T19:28:15", { zone: "America/New_York" });
  });

  it("offers every fixed offset, and sends the one chosen", async () => {
    renderModal();
    const offsets = within(zoneField()).getByRole("group", { name: "Fixed UTC offsets" });
    const options = within(offsets).getAllByRole("option");
    expect(options).toHaveLength(105);
    expect(options[0]).toHaveTextContent("UTC-12:00");
    expect(options[options.length - 1]).toHaveTextContent("UTC+14:00");

    await userEvent.selectOptions(zoneField(), "UTC+02:00");
    fireEvent.change(timeField(), { target: { value: "20:28:15" } });
    await userEvent.click(saveButton());
    expect(mockApi.setTakenAt).toHaveBeenCalledWith("m1", "2019-03-13T20:28:15", { utcOffsetMin: 120 });
  });

  it("keeps the seconds typed", async () => {
    renderModal();
    fireEvent.change(timeField(), { target: { value: "07:05:09" } });
    await userEvent.click(saveButton());
    expect(mockApi.setTakenAt).toHaveBeenLastCalledWith("m1", "2019-03-13T07:05:09", { zone: "Europe/Warsaw" });
  });

  it("reads a time a browser wrote without seconds as :00", async () => {
    renderModal();
    fireEvent.change(timeField(), { target: { value: "07:05" } });
    await userEvent.click(saveButton());
    expect(mockApi.setTakenAt).toHaveBeenLastCalledWith("m1", "2019-03-13T07:05:00", { zone: "Europe/Warsaw" });
  });

  it("keeps Save off while the fields still name the stored time", async () => {
    renderModal();
    expect(saveButton()).toBeDisabled();

    fireEvent.change(dateField(), { target: { value: "2019-03-14" } });
    expect(saveButton()).toBeEnabled();
    fireEvent.change(dateField(), { target: { value: "2019-03-13" } });
    expect(saveButton()).toBeDisabled();

    await userEvent.selectOptions(zoneField(), "UTC");
    expect(saveButton()).toBeEnabled();
    await userEvent.selectOptions(zoneField(), "Europe/Warsaw");
    expect(saveButton()).toBeDisabled();

    fireEvent.change(timeField(), { target: { value: "19:28:16" } });
    expect(saveButton()).toBeEnabled();
  });

  it("takes a time a browser wrote without its zero seconds as the stored one", () => {
    renderModal({ ...dated, taken_at: "2019-03-13T18:28:00Z" });
    fireEvent.change(timeField(), { target: { value: "19:28" } });
    expect(saveButton()).toBeDisabled();
  });

  // Ingest stores a file's modified time with milliseconds; the fields hold whole seconds, and
  // saving them unchanged would record a change of under a second.
  it("sends nothing for a time stored with a fraction of a second, saved unchanged", async () => {
    renderModal({ ...dated, taken_at: "2019-03-13T18:28:15.640Z" });
    expect(timeField()).toHaveValue("19:28:15");
    expect(saveButton()).toBeDisabled();
    await userEvent.click(saveButton());
    fireEvent.submit(saveButton().closest("form")!);
    expect(mockApi.setTakenAt).not.toHaveBeenCalled();
  });

  describe("a time in an hour the zone showed twice", () => {
    // Warsaw's clocks went back from 03:00 CEST to 02:00 CET on 27 October 2024 (01:00 UTC), so
    // 02:30 was shown at 00:30 and again at 01:30 UTC.
    it.each([
      ["the second 02:30, in winter time", "2024-10-27T01:30:00Z", "offset:60", "UTC+01:00", 60],
      ["the first 02:30, in summer time", "2024-10-27T00:30:00Z", "offset:120", "UTC+02:00", 120],
    ])("starts %s at the stored instant's own offset, and says why", (_name, takenAt, value, label, offsetMin) => {
      renderModal({ ...dated, taken_at: takenAt });
      expect(dateField()).toHaveValue("2024-10-27");
      expect(timeField()).toHaveValue("02:30:00");
      expect(zoneField()).toHaveValue(value);
      expect(within(zoneField()).getByRole("option", { selected: true })).toHaveTextContent(label);
      const hint = `Europe/Warsaw showed this time twice, as its clocks went back, so the zone starts as the stored time's own offset, ${label}.`;
      expect(screen.getByText(hint)).toBeInTheDocument();
      expect(zoneField()).toHaveAccessibleDescription(expect.stringContaining(hint));
      // The fields name the stored instant, so there is nothing to save.
      const instant = Date.parse("2024-10-27T02:30:00Z") - offsetMin * 60000;
      expect(new Date(instant).toISOString()).toBe(new Date(takenAt).toISOString());
      expect(saveButton()).toBeDisabled();
    });

    it("sends the offset with a time changed in that hour", async () => {
      renderModal({ ...dated, taken_at: "2024-10-27T01:30:00Z" });
      fireEvent.change(timeField(), { target: { value: "02:45:00" } });
      await userEvent.click(saveButton());
      expect(mockApi.setTakenAt).toHaveBeenCalledWith("m1", "2024-10-27T02:45:00", { utcOffsetMin: 60 });
    });

    it("keeps the zone for a time it showed once", () => {
      renderModal({ ...dated, taken_at: "2024-10-27T02:30:00Z" });
      expect(timeField()).toHaveValue("03:30:00");
      expect(zoneField()).toHaveValue("Europe/Warsaw");
      expect(screen.queryByText(/showed this time twice/)).not.toBeInTheDocument();
    });
  });

  it("refuses a year the backend cannot store", () => {
    renderModal();
    fireEvent.change(dateField(), { target: { value: "9999-01-01" } });
    expect(saveButton()).toBeDisabled();
    expect(screen.getByText("Enter a real date and time in the years 1 to 9998.")).toBeInTheDocument();
  });

  it("saves once, shows that it is busy, then reports and closes", async () => {
    let resolve!: (r: SetTakenAtResult) => void;
    mockApi.setTakenAt.mockReturnValue(new Promise<SetTakenAtResult>((r) => (resolve = r)));
    const { onSaved, onClose } = renderModal();

    changeTime();
    await userEvent.click(saveButton());
    expect(saveButton()).toHaveTextContent("Saving…");
    expect(saveButton()).toBeDisabled();
    fireEvent.submit(saveButton().closest("form")!);
    expect(mockApi.setTakenAt).toHaveBeenCalledTimes(1);
    expect(onSaved).not.toHaveBeenCalled();

    await act(async () => resolve(saved));
    expect(onSaved).toHaveBeenCalledWith(saved);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["the server's reason for a 400", new ApiError("PATCH failed: 400", 400, "2025-03-30T02:30:00 does not exist in Europe/Warsaw: the clocks skipped it"), "2025-03-30T02:30:00 does not exist in Europe/Warsaw: the clocks skipped it"],
    ["a sentence for a 400 without a reason", new ApiError("PATCH failed: 400", 400), "The server refused this time."],
    ["that the item is gone for a 404", new ApiError("PATCH failed: 404", 404), "This item is no longer available."],
    ["a retry sentence for a server error", new ApiError("PATCH failed: 500", 500), "Could not save the time. Try again."],
    ["a retry sentence for a network failure", new TypeError("Failed to fetch"), "Could not save the time. Try again."],
  ])("shows %s, and stays open to try again", async (_name, failure, message) => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.setTakenAt.mockRejectedValueOnce(failure);
    const { onSaved, onClose } = renderModal();

    changeTime();
    await userEvent.click(saveButton());
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(saveButton()).toHaveTextContent("Save time");
    expect(saveButton()).toBeEnabled();

    await userEvent.click(saveButton());
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(saved));
    expect(screen.queryByText(message)).not.toBeInTheDocument();
  });

  it("reports a save that finishes after the dialog closed, without closing again", async () => {
    let resolve!: (r: SetTakenAtResult) => void;
    mockApi.setTakenAt.mockReturnValue(new Promise<SetTakenAtResult>((r) => (resolve = r)));
    const onSaved = vi.fn();
    const onClose = vi.fn();
    const { rerender } = render(<EditTimeModal open item={dated} onClose={onClose} onSaved={onSaved} />);
    changeTime();
    await userEvent.click(saveButton());
    rerender(<EditTimeModal open={false} item={dated} onClose={onClose} onSaved={onSaved} />);

    await act(async () => resolve(saved));
    expect(onSaved).toHaveBeenCalledWith(saved);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("renders nothing and holds no history entry without an item", async () => {
    renderModal(null);
    await settle();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(depth()).toBe(0);
  });

  it("starts again from the stored time on every opening", async () => {
    const props = { item: dated, onClose: vi.fn(), onSaved: vi.fn() };
    const { rerender } = render(<EditTimeModal open {...props} />);
    fireEvent.change(timeField(), { target: { value: "01:02:03" } });
    rerender(<EditTimeModal open={false} {...props} />);
    rerender(<EditTimeModal open {...props} />);
    expect(timeField()).toHaveValue("19:28:15");
  });

  it("is touch-sized", () => {
    renderModal();
    for (const field of [dateField(), timeField(), zoneField()]) {
      expect(field).toHaveClass("touch:min-h-11", "touch:text-base");
    }
    // DialogFooter gives its buttons 44 px on touch.
    expect(saveButton().parentElement).toHaveClass("touch:[&>*]:min-h-11");
  });

  describe("over a fullscreen view", () => {
    // A gallery's essentials: a history entry of its own, and keys on window that leave a dialog's
    // keys alone. The dialog opens from a button, after the gallery holds its entry.
    function Gallery({ onGalleryClose, onModalClose }: { onGalleryClose: () => void; onModalClose: () => void }) {
      const [editing, setEditing] = useState(false);
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
          <button onClick={() => setEditing(true)}>Edit capture time</button>
          <EditTimeModal
            open={editing}
            item={dated}
            onClose={() => {
              onModalClose();
              setEditing(false);
            }}
            onSaved={vi.fn()}
          />
        </>
      );
    }

    async function openOverGallery(onGalleryClose: () => void, onModalClose: () => void) {
      render(<Gallery onGalleryClose={onGalleryClose} onModalClose={onModalClose} />);
      await settle();
      expect(depth()).toBe(1);
      await userEvent.click(screen.getByRole("button", { name: "Edit capture time" }));
      await settle();
      expect(depth()).toBe(2);
    }

    it("is a dialog the view's key handler leaves alone", () => {
      renderModal();
      expect(screen.getByRole("dialog")).toHaveAttribute("data-app-dialog");
      expect(keyBelongsToDialog(new KeyboardEvent("keydown", { key: "ArrowRight" }))).toBe(true);
    });

    it("closes only the dialog on Escape", async () => {
      const onGalleryClose = vi.fn();
      const onModalClose = vi.fn();
      await openOverGallery(onGalleryClose, onModalClose);

      await userEvent.click(timeField());
      await userEvent.keyboard("{Escape}");
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(onModalClose).toHaveBeenCalledTimes(1);
      expect(onGalleryClose).not.toHaveBeenCalled();
      await settle();
      expect(depth()).toBe(1);
    });

    it("closes only the dialog on the phone's back gesture", async () => {
      const onGalleryClose = vi.fn();
      const onModalClose = vi.fn();
      await openOverGallery(onGalleryClose, onModalClose);

      await act(async () => {
        const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
        window.history.back();
        await popped;
      });
      await settle();
      expect(onModalClose).toHaveBeenCalledTimes(1);
      expect(onGalleryClose).not.toHaveBeenCalled();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(depth()).toBe(1);
    });
  });
});
