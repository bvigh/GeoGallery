import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/api/client";
import {
  makeEvidence,
  makeEvidenceClockRule,
  makeReread,
  makeResponse,
  type ResponseOverrides,
} from "@/components/evidence/evidenceTestUtils";
import { TimeCard } from "@/components/evidence/TimeCard";
import {
  CLOCK_RULE_KEEPS_TIME,
  EDIT_KEEPS_TIME,
  REREAD_NOT_RECORDED,
  timeEditSentence,
  VIDEO_TIME_MAY_BE_END,
} from "@/lib/evidence";
import { browserTimeZone, formatInstant } from "@/lib/time";
import type { FileReread, TimeEdit } from "@/types";

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
      clock_rule: null,
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

  describe("a clock rule", () => {
    const byRule = makeEvidence({
      zone: "clock_rule",
      offset_used_min: 120,
      make: "Panasonic",
      model: "DC-TZ200",
      clock_rule: makeEvidenceClockRule(),
    });

    it("words the offset as the owner's rule and names the camera", () => {
      render(card("a", { time: { evidence: byRule } }));
      expect(cameraRow()).toBe("2000:01:01 01:01:00 +02:00 · your rule");
      expect(chips()).toEqual(["EXIF capture time", "Your rule for Panasonic DC-TZ200: clock showed UTC+02:00"]);
      expect(screen.getByText(CLOCK_RULE_KEEPS_TIME)).toBeInTheDocument();
    });

    it("names today's rule when the re-read differs because of it", async () => {
      reread.mockResolvedValue(
        makeReread({
          rule_today: {
            source: "exif_original",
            zone_rule: "clock_rule",
            offset_min: 120,
            zone_name: null,
            clock_rule: makeEvidenceClockRule(),
            taken_at: "2000-01-01T23:01:00Z",
          },
          delta_s: -3600,
          class: "whole_offset",
        })
      );
      render(card("a"));
      fireEvent.click(screen.getByRole("button", { name: "Check against the file" }));
      await advance(0);
      expect(screen.getByText("Differs by exactly 1 h. An older rule or a changed setting would explain it.")).toBeInTheDocument();
      expect(
        screen.getByText(
          "Today's reading uses your rule for Panasonic DC-TZ200 (clock showed UTC+02:00). A stored time moves only when you edit or shift it, or an administrator runs server retime."
        )
      ).toBeInTheDocument();
      // The chips still describe how the stored time was read.
      expect(chips()[1]).toBe("Etc/GMT-1; nothing in the file confirms this");
    });
  });

  describe("a time moved by server retime", () => {
    const retimed = makeEvidence({ by: "retime", retimed_from: "2000-01-01T01:01:00Z" });

    it("says it was re-timed and lists the earlier time in UTC and the browser zone", () => {
      const zone = vi
        .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
        .mockReturnValue({ timeZone: "Asia/Tokyo" } as Intl.ResolvedDateTimeFormatOptions);
      render(card("a", { time: { recorded: "retime", evidence: retimed } }));
      expect(screen.getByText("Re-timed on the server: 1 h earlier than the time stored before.")).toBeInTheDocument();
      const before = screen.getByText("Before re-time").parentElement!;
      expect(before).toHaveTextContent(formatInstant("2000-01-01T01:01:00Z", "UTC"));
      expect(before).toHaveTextContent(formatInstant("2000-01-01T01:01:00Z", "Asia/Tokyo"));
      zone.mockRestore();
    });

    it("counts as recorded: the file is read only on request", async () => {
      render(card("a", { time: { recorded: "retime", evidence: retimed } }));
      await advance(5000);
      expect(reread).not.toHaveBeenCalled();
      expect(screen.queryByText(REREAD_NOT_RECORDED)).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Check against the file" })).toBeInTheDocument();
    });

    it("says nothing of a re-time for a time recorded at ingest", () => {
      render(card("a"));
      expect(screen.queryByText(/Re-timed/)).not.toBeInTheDocument();
      expect(screen.queryByText("Before re-time")).not.toBeInTheDocument();
    });
  });

  describe("a time the owner changed", () => {
    // The stored 00:01 came from a change that moved the time an hour back.
    const edit: TimeEdit = {
      v: 1,
      kind: "set",
      at: "2026-11-15T19:00:00Z",
      from: "2000-01-01T01:01:00Z",
      original: "2000-01-01T02:01:00Z",
      delta_s: -3600,
      wall: "2000-01-01T01:01:00",
      zone: "Europe/Warsaw",
      offset_min: 60,
    };
    const edited: ResponseOverrides = { time: { edit } };

    function stubZone() {
      return vi
        .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
        .mockReturnValue({ timeZone: "Asia/Tokyo" } as Intl.ResolvedDateTimeFormatOptions);
    }

    it("says who changed it, when and to what, and that server retime keeps it", () => {
      const zone = stubZone();
      render(card("a", edited));
      expect(screen.getByText(timeEditSentence(edit, "Asia/Tokyo"))).toBeInTheDocument();
      expect(screen.getByText(timeEditSentence(edit, "Asia/Tokyo"))).toHaveTextContent("Asia/Tokyo");
      expect(screen.getByText("Entered as 2000-01-01 01:01:00 in Europe/Warsaw, UTC+01:00 on that date.")).toBeInTheDocument();
      expect(screen.getByText(EDIT_KEEPS_TIME)).toBeInTheDocument();
      zone.mockRestore();
    });

    it("lists the time before the change and the one before the first change, in UTC and the browser zone", () => {
      const zone = stubZone();
      render(card("a", edited));
      const before = screen.getByText("Before your change").parentElement!;
      expect(before).toHaveTextContent(formatInstant("2000-01-01T01:01:00Z", "UTC"));
      expect(before).toHaveTextContent(formatInstant("2000-01-01T01:01:00Z", "Asia/Tokyo"));
      const first = screen.getByText("Before your first change").parentElement!;
      expect(first).toHaveTextContent(formatInstant("2000-01-01T02:01:00Z", "UTC"));
      expect(first).toHaveTextContent(formatInstant("2000-01-01T02:01:00Z", "Asia/Tokyo"));
      zone.mockRestore();
    });

    it("leaves out the time before the first change when it is the one before this change", () => {
      render(card("a", { time: { edit: { ...edit, original: edit.from } } }));
      expect(screen.getByText("Before your change")).toBeInTheDocument();
      expect(screen.queryByText("Before your first change")).not.toBeInTheDocument();
    });

    // The evidence still says how the file's own time was read; the stored time is the owner's.
    function fileClockRow() {
      return screen.getByText("Camera clock in the file").parentElement!.querySelector("dd")!.textContent;
    }

    it("marks the stored time as changed by hand, and what follows as the file's reading", () => {
      render(card("a", edited));
      expect(chips()).toEqual(["Set by hand", "EXIF capture time", "Etc/GMT-1; nothing in the file confirms this"]);
      expect(screen.getByText("Read from the file:")).toBeInTheDocument();
      expect(screen.queryByText("Camera")).not.toBeInTheDocument();
      expect(fileClockRow()).toBe("2000:01:01 01:01:00 +01:00 · assumed");
    });

    it("marks a shifted time as shifted", () => {
      render(card("a", { time: { edit: { ...edit, kind: "shift", wall: null, zone: null, offset_min: null } } }));
      expect(chips()[0]).toBe("Shifted by hand");
    });

    it("words an undated item given a time by hand", () => {
      const none = makeEvidence({ src: "none", zone: null, wall: null, offset_used_min: null });
      const first: TimeEdit = { ...edit, from: null, original: null, delta_s: null };
      render(card("a", { time: { evidence: none, edit: first } }));
      expect(screen.getAllByText("UTC")[0].parentElement).toHaveTextContent(formatInstant("2000-01-01T00:01:00Z", "UTC"));
      expect(chips()).toEqual(["Set by hand", "No capture time"]);
      expect(fileClockRow()).toBe("No wall clock.");
      expect(screen.getByText(/The item had no capture time before\.$/)).toBeInTheDocument();
      expect(screen.queryByText("Before your change")).not.toBeInTheDocument();
    });

    it("never calls a time set by hand the file's modified date", async () => {
      const mtime = makeEvidence({ src: "mtime", zone: null, wall: null, offset_used_min: null });
      reread.mockResolvedValue(makeReread({ class: "no_file_time", delta_s: null, rule_today: null }));
      render(card("a", { time: { evidence: mtime, edit } }));
      expect(fileClockRow()).toBe("No wall clock in the file.");
      expect(chips()).toEqual(["Set by hand", "File modified time, may be wrong"]);
      fireEvent.click(screen.getByRole("button", { name: "Check against the file" }));
      await advance(0);
      expect(
        screen.getByText(
          "No capture time could be read from this file. This reader understands EXIF in JPEG and TIFF only. The stored time is the one you set by hand."
        )
      ).toBeInTheDocument();
      expect(screen.queryByText(/This is the file's modified date/)).not.toBeInTheDocument();
      expect(screen.queryByText(/The stored time is the file's modified date/)).not.toBeInTheDocument();
    });

    it("says the file's time, not the stored one, was read as UTC", () => {
      const utc = makeEvidence({ camera_zone: "server zone (unset)", camera_zone_utc_fallback: true });
      render(card("a", { time: { evidence: utc, edit } }));
      expect(
        screen.getByText("The file's time was read as UTC, because CAMERA_TIMEZONE is unset and the server's zone is UTC.")
      ).toBeInTheDocument();
      expect(screen.queryByText(/^This time was read as UTC/)).not.toBeInTheDocument();
    });

    it("words a shift, which entered no wall clock", () => {
      const shift: TimeEdit = { ...edit, kind: "shift", wall: null, zone: null, offset_min: null };
      render(card("a", { time: { edit: shift } }));
      expect(screen.getByText(/^You shifted this time 1 h earlier on /)).toBeInTheDocument();
      expect(screen.queryByText(/^Entered as/)).not.toBeInTheDocument();
    });

    it("says in the file re-read that the stored time need not match the file", async () => {
      reread.mockResolvedValue(makeReread({ class: "whole_offset", delta_s: 3600 }));
      render(card("a", edited));
      fireEvent.click(screen.getByRole("button", { name: "Check against the file" }));
      await advance(0);
      expect(screen.getByText("Differs by exactly 1 h. Your change explains the difference.")).toBeInTheDocument();
      expect(screen.queryByText(/older rule/)).not.toBeInTheDocument();
    });

    it("measures a re-time from the time it left, not the time changed since", () => {
      const retimed = makeEvidence({ by: "retime", retimed_from: "2000-01-01T03:01:00Z" });
      render(card("a", { time: { recorded: "retime", evidence: retimed, edit } }));
      // server retime moved 03:01 to 02:01; the owner then moved 02:01 to 01:01 and on to 00:01.
      expect(screen.getByText("Re-timed on the server: 1 h earlier than the time stored before.")).toBeInTheDocument();
    });

    it.each([
      ["null", { time: { edit: null } }],
      ["missing, from an older backend", {}],
    ] as const)("says nothing of a change when the edit is %s", (_name, overrides) => {
      render(card("a", overrides));
      expect(screen.queryByText(/You (set|shifted) this time/)).not.toBeInTheDocument();
      expect(screen.queryByText(EDIT_KEEPS_TIME)).not.toBeInTheDocument();
      expect(screen.queryByText("Before your change")).not.toBeInTheDocument();
    });

    it("reads the file again once the stored time changed, after 400 ms", async () => {
      const { rerender } = render(card("a", notRecorded));
      await advance(400);
      expect(reread).toHaveBeenCalledTimes(1);

      rerender(card("a", { ...notRecorded, item: { taken_at: "2000-01-01T00:00:00Z" } }));
      expect(screen.queryByText("Today's rules agree with the stored time.")).not.toBeInTheDocument();
      await advance(399);
      expect(reread).toHaveBeenCalledTimes(1);
      await advance(1);
      expect(reread).toHaveBeenCalledTimes(2);
      expect(screen.getByText("Today's rules agree with the stored time.")).toBeInTheDocument();
    });

    it("offers the button again once a time read on request changed", async () => {
      const { rerender } = render(card("a"));
      fireEvent.click(screen.getByRole("button", { name: "Check against the file" }));
      await advance(0);
      expect(screen.queryByRole("button", { name: "Check against the file" })).not.toBeInTheDocument();

      rerender(card("a", { item: { taken_at: "2000-01-01T00:00:00Z" }, ...edited }));
      expect(screen.getByRole("button", { name: "Check against the file" })).toBeEnabled();
      expect(screen.queryByText(/Today's rules agree/)).not.toBeInTheDocument();
      expect(reread).toHaveBeenCalledTimes(1);
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
