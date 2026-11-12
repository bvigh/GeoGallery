import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/api/client";
import { makeEvidence, makeReread, makeResponse, type ResponseOverrides } from "@/components/evidence/evidenceTestUtils";
import { TimeCard } from "@/components/evidence/TimeCard";
import { REREAD_NOT_RECORDED, VIDEO_TIME_MAY_BE_END } from "@/lib/evidence";
import { browserTimeZone, formatInstant } from "@/lib/time";
import type { FileReread } from "@/types";

vi.mock("@/api/client", () => ({
  api: { mediaProvenanceFile: vi.fn() },
}));

const reread = api.mediaProvenanceFile as unknown as ReturnType<typeof vi.fn>;

const notRecorded: ResponseOverrides = { time: { recorded: "not_recorded", evidence: null } };

function card(id: string, overrides: ResponseOverrides = {}, inert = false) {
  const data = makeResponse(id, overrides);
  return <TimeCard item={data.item} time={data.time} inert={inert} />;
}

function cameraRow() {
  return screen.getByText("Camera").parentElement!.querySelector("dd")!.textContent;
}

// The chip row, right under the clocks.
function chips() {
  return [...screen.getByRole("region", { name: "Time" }).querySelectorAll("span.rounded-full")].map((el) => el.textContent);
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

beforeEach(() => {
  // Only the timers: the default also replaces Intl, which the zone test spies on.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  reread.mockReset();
  reread.mockResolvedValue(makeReread());
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("TimeCard", () => {
  describe("the three clocks", () => {
    it("labels UTC and the browser with their zones", () => {
      const zone = vi
        .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
        .mockReturnValue({ timeZone: "Asia/Tokyo" } as Intl.ResolvedDateTimeFormatOptions);
      render(card("a"));
      expect(screen.getAllByText(formatInstant("2000-01-01T00:01:00Z", "UTC")).length).toBeGreaterThan(0);
      expect(screen.getAllByText(formatInstant("2000-01-01T00:01:00Z", "Asia/Tokyo")).length).toBeGreaterThan(0);
      zone.mockRestore();
    });

    it("shows the wall clock with its offset and how the offset was obtained", () => {
      render(card("a", { time: { evidence: makeEvidence({ zone: "gps", offset_used_min: 120 }) } }));
      expect(cameraRow()).toBe("2000:01:01 01:01:00 +02:00 · measured");
    });

    it('reads "not recorded" when there is neither evidence nor a re-read', () => {
      render(card("a", notRecorded));
      expect(cameraRow()).toBe("not recorded");
      expect(chips()[0]).toBe("not recorded");
    });

    it.each([
      ["mtime", "No wall clock. This is the file's modified date. · file date"],
      ["none", "No wall clock."],
    ] as const)("words a src of %s", (src, want) => {
      render(card("a", { time: { evidence: makeEvidence({ src, zone: null, wall: null, offset_used_min: null }) } }));
      expect(cameraRow()).toBe(want);
    });

    it("says no wall clock was written for a video container", () => {
      render(
        card("a", {
          item: { media_type: "video" },
          time: { evidence: makeEvidence({ src: "container", zone: null, wall: null, offset_used_min: null }) },
        })
      );
      expect(cameraRow()).toMatch(/No wall clock was written/);
    });

    it("has a capture time of none when the item has none", () => {
      render(card("a", { item: { taken_at: null } }));
      expect(screen.getAllByText("UTC")[0].parentElement!.querySelector("dd")).toHaveTextContent("not recorded");
    });
  });

  describe("the chips", () => {
    it("shows the time origin and the zone rule", () => {
      render(card("a"));
      expect(chips()).toEqual(["EXIF capture time", "Etc/GMT-1; nothing in the file confirms this"]);
    });

    it("notes that a modified time is often a copy time", () => {
      render(card("a", { time: { evidence: makeEvidence({ src: "mtime", zone: null, wall: null, offset_used_min: null }) } }));
      expect(screen.getByText("A modified time is often a copy or download time.")).toBeInTheDocument();
    });

    it("warns when the time was read as UTC by fallback", () => {
      render(card("a", { time: { evidence: makeEvidence({ camera_zone: "server zone (unset)", camera_zone_utc_fallback: true }) } }));
      expect(screen.getByText(/This time was read as UTC/)).toBeInTheDocument();
    });

    it("follows the re-read when nothing was recorded", async () => {
      render(card("a", notRecorded));
      await advance(400);
      expect(chips()[0]).toBe("EXIF capture time");
    });
  });

  describe("a video", () => {
    const video = { media_type: "video" } as const;
    const fromContainer = makeEvidence({ src: "container", zone: null, wall: null, offset_used_min: null });
    const containerRule = (): FileReread["rule_today"] => ({
      source: "container",
      zone_rule: null,
      offset_min: null,
      zone_name: null,
      taken_at: "2000-01-01T00:01:00Z",
    });

    it("shows its duration and that a container time can be the end of the clip", () => {
      render(card("a", { item: { ...video, duration_sec: 90 }, time: { evidence: fromContainer } }));
      expect(screen.getByText(new RegExp(`Duration 1 min 30 s. ${VIDEO_TIME_MAY_BE_END}`))).toBeInTheDocument();
    });

    it.each(["mtime", "none"] as const)("keeps the duration but not the clip's end for a src of %s", (src) => {
      const evidence = makeEvidence({ src, zone: null, wall: null, offset_used_min: null });
      render(card("a", { item: { ...video, duration_sec: 90 }, time: { evidence } }));
      expect(screen.getByText("Duration 1 min 30 s.")).toBeInTheDocument();
      expect(screen.queryByText(VIDEO_TIME_MAY_BE_END)).not.toBeInTheDocument();
    });

    it("has no line at all without a duration or a container time", () => {
      const evidence = makeEvidence({ src: "none", zone: null, wall: null, offset_used_min: null });
      render(card("a", { item: video, time: { evidence } }));
      expect(screen.queryByText(/Duration/)).not.toBeInTheDocument();
      expect(screen.queryByText(VIDEO_TIME_MAY_BE_END)).not.toBeInTheDocument();
    });

    it("goes by the recorded origin, even if a re-read now finds a container time", async () => {
      const evidence = makeEvidence({ src: "mtime", zone: null, wall: null, offset_used_min: null });
      reread.mockResolvedValue(
        makeReread({ rule_today: containerRule(), checks: [{ id: "video_time_may_be_end", level: "info", delta_s: null }] })
      );
      render(card("a", { item: video, time: { evidence } }));
      fireEvent.click(screen.getByRole("button", { name: "Check against the file" }));
      await advance(0);
      expect(reread).toHaveBeenCalledTimes(1);
      expect(screen.queryByText(VIDEO_TIME_MAY_BE_END)).not.toBeInTheDocument();
    });

    it("with nothing recorded, waits for the re-read to say the time came from the container", async () => {
      reread.mockResolvedValue(
        makeReread({ rule_today: containerRule(), checks: [{ id: "video_time_may_be_end", level: "info", delta_s: null }] })
      );
      render(card("a", { item: video, time: { recorded: "not_recorded", evidence: null } }));
      expect(screen.queryByText(VIDEO_TIME_MAY_BE_END)).not.toBeInTheDocument();

      await advance(400);

      expect(screen.getAllByText(VIDEO_TIME_MAY_BE_END)).toHaveLength(1);
    });

    it("with nothing recorded, says nothing about the clip's end when the re-read found no time", async () => {
      reread.mockResolvedValue(makeReread({ rule_today: null, class: "no_file_time", delta_s: null }));
      render(card("a", { item: video, time: { recorded: "not_recorded", evidence: null } }));
      await advance(400);
      expect(reread).toHaveBeenCalledTimes(1);
      expect(screen.queryByText(VIDEO_TIME_MAY_BE_END)).not.toBeInTheDocument();
    });

    it("has no such line for a photo", () => {
      render(card("a"));
      expect(screen.queryByText(VIDEO_TIME_MAY_BE_END)).not.toBeInTheDocument();
    });
  });

  describe("the file re-read", () => {
    it("loads after 400 ms when nothing was recorded", async () => {
      render(card("a", notRecorded));
      expect(screen.getByText(REREAD_NOT_RECORDED)).toBeInTheDocument();

      await advance(399);
      expect(reread).not.toHaveBeenCalled();
      await advance(1);
      expect(reread).toHaveBeenCalledTimes(1);
      expect(reread.mock.calls[0][0]).toBe("a");
      expect(reread.mock.calls[0][1]).toBeInstanceOf(AbortSignal);
      expect(screen.getByText("Today's rules agree with the stored time.")).toBeInTheDocument();
      expect(cameraRow()).toBe("2000:01:01 01:01:00 +01:00 · assumed");
    });

    it("does not start when the item changes within the 400 ms", async () => {
      const { rerender } = render(card("a", notRecorded));
      await advance(300);
      rerender(card("b", notRecorded));
      await advance(300);
      expect(reread).not.toHaveBeenCalled();
      await advance(100);
      expect(reread).toHaveBeenCalledTimes(1);
      expect(reread.mock.calls[0][0]).toBe("b");
    });

    it("aborts the request when the item changes, and drops a late answer", async () => {
      let answer!: (value: FileReread) => void;
      reread.mockImplementation(() => new Promise((resolve) => { answer = resolve; }));
      const { rerender } = render(card("a", notRecorded));
      await advance(400);
      const signal = reread.mock.calls[0][1] as AbortSignal;
      expect(signal.aborted).toBe(false);

      rerender(card("b", notRecorded));
      expect(signal.aborted).toBe(true);
      await act(async () => answer(makeReread()));
      expect(cameraRow()).toBe("not recorded");
    });

    it("aborts when the view steps away, before the next response has arrived", async () => {
      let answer!: (value: FileReread) => void;
      reread.mockImplementation(() => new Promise((resolve) => { answer = resolve; }));
      const { rerender } = render(card("a", notRecorded));
      await advance(400);
      const signal = reread.mock.calls[0][1] as AbortSignal;

      rerender(card("a", notRecorded, true));
      expect(signal.aborted).toBe(true);
      await act(async () => answer(makeReread()));
      expect(cameraRow()).toBe("not recorded");
    });

    it("waits for the button when evidence exists", async () => {
      render(card("a"));
      await advance(5000);
      expect(reread).not.toHaveBeenCalled();
      expect(screen.queryByText(REREAD_NOT_RECORDED)).not.toBeInTheDocument();

      fireEvent.click(screen.getByRole("button", { name: "Check against the file" }));
      expect(reread).toHaveBeenCalledTimes(1);
      await advance(0);
      expect(screen.getByText("Today's rules agree with the stored time.")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Check against the file" })).not.toBeInTheDocument();
    });

    it("asks again for the next item's button", async () => {
      const { rerender } = render(card("a"));
      fireEvent.click(screen.getByRole("button", { name: "Check against the file" }));
      await advance(0);

      rerender(card("b"));
      expect(screen.getByRole("button", { name: "Check against the file" })).toBeEnabled();
      expect(reread).toHaveBeenCalledTimes(1);
    });

    it("does not read again after a reload of the same item", async () => {
      const { rerender } = render(card("a", notRecorded));
      await advance(400);
      rerender(card("a", { ...notRecorded, placement: { state: "stale" } }));
      await advance(1000);
      expect(reread).toHaveBeenCalledTimes(1);
    });

    it("says when the read failed and offers another try", async () => {
      const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
      reread.mockRejectedValueOnce(new Error("GET failed: 502"));
      render(card("a", notRecorded));
      await advance(400);
      expect(screen.getByText("Could not read the file.")).toBeInTheDocument();

      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      await advance(0);
      expect(reread).toHaveBeenCalledTimes(2);
      expect(screen.queryByText("Could not read the file.")).not.toBeInTheDocument();
      consoleError.mockRestore();
    });

    it("shows the checks, without repeating the zone chip's note", async () => {
      reread.mockResolvedValue(
        makeReread({
          file: { ...makeReread().file, offset_tag: "+01:00", offset_ignored: true },
          checks: [
            { id: "offset_ignored", level: "info", delta_s: null },
            { id: "taken_after_upload", level: "warn", delta_s: 3600 },
            { id: "a_check_from_the_future", level: "info", delta_s: null },
          ],
        })
      );
      render(card("a", notRecorded));
      await advance(400);
      expect(screen.getAllByText("The file declares +01:00, which is ignored for this make.")).toHaveLength(1);
      expect(screen.getByText("The stored time is 1 h after the upload. A correct clock cannot produce that.")).toBeInTheDocument();
    });

    it("keeps the file's tags in a disclosure, as text", async () => {
      reread.mockResolvedValue(makeReread({ tags: [{ name: "Model", value: "<b>Cam</b>" }] }));
      render(card("a", notRecorded));
      await advance(400);
      const summary = screen.getByText("Tags in the file (1)");
      expect(summary.closest("details")).not.toHaveAttribute("open");
      expect(screen.getByText("<b>Cam</b>")).toBeInTheDocument();
      expect(document.querySelector("details b")).toBeNull();
    });
  });

  describe("the other instants", () => {
    it("shows them in UTC and the browser zone, with no camera row", () => {
      // Not UTC, or the UTC and browser rows read the same and the process's zone decides this test.
      const zone = vi
        .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
        .mockReturnValue({ timeZone: "Asia/Tokyo" } as Intl.ResolvedDateTimeFormatOptions);
      render(card("a", { time: { evidence: makeEvidence({ gps_utc: "2000-01-01T00:02:00Z" }) } }));
      expect(screen.getByText("Uploaded")).toBeInTheDocument();
      expect(screen.getByText("GPS clock")).toBeInTheDocument();
      expect(screen.getByText(formatInstant("2000-01-01T00:02:00Z", browserTimeZone()))).toBeInTheDocument();
      expect(screen.getByText(formatInstant("2000-01-01T00:02:00Z", "UTC"))).toBeInTheDocument();
      expect(screen.getAllByText("Camera")).toHaveLength(1);
      zone.mockRestore();
    });
  });
});
