import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { makeBracket, makeEstimate, makeResponse } from "@/components/evidence/evidenceTestUtils";
import { PlacementCard } from "@/components/evidence/PlacementCard";
import { REMATCH_HEADING, STANDING_LINE } from "@/lib/evidence";
import { formatInstant } from "@/lib/time";
import type { Bracket, OtherFormat, ProvenanceResponse } from "@/types";

type Placement = ProvenanceResponse["placement"];

function renderCard(placement: Partial<Placement> = {}, item: Partial<ProvenanceResponse["item"]> = {}, onJump = vi.fn()) {
  const data = makeResponse("a", { placement, item });
  render(<PlacementCard item={data.item} placement={data.placement} onJump={onJump} />);
  return { onJump };
}

const row = makeBracket({ gap_s: 0 });
const exact = makeEstimate(
  { before: row, after: row, confidence: 1 },
  { shape: "exact", pair_gap_s: 0, pair_km: 0, t: null, confidence: 1, implied_kmh: null, distance_factor: 1 }
);

const photoPoint = (filename: string): Bracket =>
  makeBracket({
    source: "exif",
    format: "exif",
    kind: "exif",
    origin: { state: "linked", media: [{ id: "behind", filename }] },
  });

function other(overrides: Partial<OtherFormat> = {}): OtherFormat {
  return {
    format: "nmea",
    source: "nmea:a.nmea",
    t: "2000-01-01T00:00:30Z",
    lat: 10.3,
    lng: 20.3,
    gap_s: 30,
    accuracy_m: 12,
    distance_m: 1500,
    used: false,
    ...overrides,
  };
}

describe("PlacementCard", () => {
  describe("the bracket points", () => {
    it("lists both points of a two-sided bracket", () => {
      renderCard();
      expect(screen.getAllByText("File")).toHaveLength(2);
    });

    it("lists an exact bracket's one point once", () => {
      renderCard({ now: exact });
      expect(screen.getAllByText("File")).toHaveLength(1);
    });

    it("shows a point's facts, with UTC and the browser zone labelled", () => {
      // Not UTC, or the UTC and browser rows read the same and the process's zone decides this test.
      const zone = vi
        .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
        .mockReturnValue({ timeZone: "Asia/Tokyo" } as Intl.ResolvedDateTimeFormatOptions);
      renderCard({ now: makeEstimate({ after: null }, { shape: "one_sided", pair_gap_s: null, pair_km: null, t: null }) });
      expect(screen.getByText("walk-a.gpx")).toBeInTheDocument();
      expect(screen.getByText(formatInstant("2000-01-01T00:00:00Z", "UTC"))).toBeInTheDocument();
      expect(screen.getByText("Your browser")).toBeInTheDocument();
      expect(screen.getByText(formatInstant("2000-01-01T00:00:00Z", "Asia/Tokyo"))).toBeInTheDocument();
      expect(screen.queryByText("Camera")).not.toBeInTheDocument();
      expect(screen.getByText("1 min")).toBeInTheDocument();
      expect(screen.getByText("5 m")).toBeInTheDocument();
      expect(screen.getByText("100 m")).toBeInTheDocument();
      zone.mockRestore();
    });

    it('reads "not recorded" for a missing accuracy or altitude', () => {
      const bare = makeBracket({ accuracy_m: null, altitude_m: null });
      renderCard({ now: makeEstimate({ before: bare, after: null }, { shape: "one_sided" }) });
      expect(screen.getAllByText("not recorded")).toHaveLength(2);
    });

    it("adds the format's time note", () => {
      renderCard({ now: makeEstimate({ before: makeBracket({ format: "kml", source: "kml:a.kml" }), after: null }, { shape: "one_sided" }) });
      expect(screen.getByText(/times may be estimated by the exporter/)).toBeInTheDocument();
    });

    it("renders a file name as text", () => {
      const label = "gpx:<img src=x onerror=alert(1)>.gpx";
      renderCard({ now: makeEstimate({ before: makeBracket({ source: label }), after: null }, { shape: "one_sided" }) });
      expect(screen.getByText("<img src=x onerror=alert(1)>.gpx")).toBeInTheDocument();
      expect(document.querySelector("img")).toBeNull();
    });
  });

  describe("headings and lines", () => {
    it('heads the points "What a re-match would use" when stale', () => {
      renderCard({ state: "stale", now: makeEstimate({ delta_m: 500 }) });
      expect(screen.getByRole("heading", { name: REMATCH_HEADING })).toBeInTheDocument();
    });

    it("uses a neutral heading otherwise", () => {
      renderCard();
      expect(screen.queryByRole("heading", { name: REMATCH_HEADING })).not.toBeInTheDocument();
      expect(screen.getByRole("heading", { name: "Track points" })).toBeInTheDocument();
    });

    it("shows the arithmetic, and the confidence last", () => {
      renderCard();
      const labels = screen.getAllByRole("term").map((el) => el.textContent);
      expect(labels.slice(-6)).toEqual([
        "Span",
        "Distance",
        "Position along the pair",
        "Time factor",
        "Distance factor",
        "Confidence",
      ]);
      expect(screen.getByText("3 min")).toBeInTheDocument();
      expect(screen.getByText("2.0 km")).toBeInTheDocument();
      expect(screen.getByText("33%")).toBeInTheDocument();
      expect(screen.getByText("0.900")).toBeInTheDocument();
      expect(screen.getByText("0.800")).toBeInTheDocument();
      expect(screen.getByText("50.0%")).toBeInTheDocument();
    });

    it("has no distance factor for a one-sided bracket", () => {
      renderCard({
        now: makeEstimate({ after: null }, { shape: "one_sided", pair_gap_s: null, pair_km: null, t: null, distance_factor: null }),
      });
      expect(screen.queryByText("Distance factor")).not.toBeInTheDocument();
      expect(screen.queryByText("Span")).not.toBeInTheDocument();
    });

    it("always has the standing line under the points", () => {
      renderCard();
      expect(screen.getByText(STANDING_LINE)).toBeInTheDocument();
    });

    it("explains 100% only for an exact bracket", () => {
      renderCard({ now: exact });
      expect(screen.getByText(/100% means a track point at the same second/)).toBeInTheDocument();
    });

    it("shows the warning sentence, and confidence 0 only once", () => {
      renderCard({
        now: makeEstimate({ confidence: 0 }, { confidence: 0 }),
        warnings: [
          { code: "confidence_zero", level: "info" },
          { code: "pair_far", level: "warn" },
        ],
      });
      expect(screen.getAllByText(/^Confidence 0:/)).toHaveLength(1);
      expect(screen.getByText(/different formats and are 2.0 km apart/)).toBeInTheDocument();
    });
  });

  describe("the bracket kind chip", () => {
    it("is plain text for a log and for a deleted item", () => {
      const orphan = makeBracket({ kind: "exif", format: "exif", source: "exif", origin: { state: "orphan", media: [] } });
      renderCard({ now: makeEstimate({ before: orphan, after: makeBracket() }) });
      expect(screen.getByText("A deleted item").tagName).toBe("SPAN");
      expect(screen.getByText("Log").tagName).toBe("SPAN");
    });

    it("opens the photo behind a linked point", async () => {
      const { onJump } = renderCard({ now: makeEstimate({ before: photoPoint("sunset.jpg"), after: null }, { shape: "one_sided" }) });
      await userEvent.click(screen.getByRole("button", { name: "Your photo sunset.jpg" }));
      expect(onJump).toHaveBeenCalledWith("behind");
    });

    it("expands a burst into its items, each a button", async () => {
      const media = [
        { id: "m1", filename: "one.jpg" },
        { id: "m2", filename: "two.jpg" },
      ];
      const burst = makeBracket({ kind: "exif", format: "exif", source: "exif", origin: { state: "burst", media } });
      const { onJump } = renderCard({ now: makeEstimate({ before: burst, after: null }, { shape: "one_sided" }) });

      const chip = screen.getByRole("button", { name: "2 items at this second" });
      expect(chip).toHaveAttribute("aria-expanded", "false");
      expect(screen.queryByRole("button", { name: "two.jpg" })).not.toBeInTheDocument();

      await userEvent.click(chip);
      expect(chip).toHaveAttribute("aria-expanded", "true");
      await userEvent.click(screen.getByRole("button", { name: "two.jpg" }));
      expect(onJump).toHaveBeenCalledWith("m2");
    });

    it("disables the jump while the card shows a previous item", () => {
      const data = makeResponse("a", {
        placement: { now: makeEstimate({ before: photoPoint("sunset.jpg"), after: null }, { shape: "one_sided" }) },
      });
      render(<PlacementCard item={data.item} placement={data.placement} inert onJump={vi.fn()} />);
      expect(screen.getByRole("button", { name: "Your photo sunset.jpg" })).toBeDisabled();
    });

    it("gives a jump a 44 px target on touch", () => {
      renderCard({ now: makeEstimate({ before: photoPoint("sunset.jpg"), after: null }, { shape: "one_sided" }) });
      expect(screen.getByRole("button", { name: "Your photo sunset.jpg" })).toHaveClass("touch:min-h-11", "touch:min-w-11");
    });
  });

  describe("the others table", () => {
    it("has one row per format with gap, accuracy and distance", () => {
      renderCard({ others: [other(), other({ format: "gpx", source: "gpx:b.gpx", accuracy_m: null, distance_m: null })] });
      const rows = screen.getAllByRole("row");
      expect(rows).toHaveLength(3);
      const nmea = within(rows[1]);
      expect(nmea.getByText("nmea")).toBeInTheDocument();
      expect(nmea.getByText("30 s")).toBeInTheDocument();
      expect(nmea.getByText("12 m")).toBeInTheDocument();
      expect(nmea.getByText("1.5 km")).toBeInTheDocument();
      expect(within(rows[2]).getAllByText("not recorded")).toHaveLength(2);
    });

    it("marks the rows the estimate used", () => {
      renderCard({ others: [other({ used: true }), other({ format: "gpx", source: "gpx:b.gpx" })] });
      const rows = screen.getAllByRole("row");
      expect(within(rows[1]).getByText("used")).toBeInTheDocument();
      expect(within(rows[2]).queryByText("used")).not.toBeInTheDocument();
    });
  });

  describe("the independence line", () => {
    const usesPhoto = makeEstimate({ before: makeBracket({ kind: "exif", format: "exif", source: "exif", origin: { state: "orphan", media: [] } }) });
    const independent = { lat: 10, lng: 20, confidence: 0.5, moved_m: 2500, before: null, after: null };

    it.each<[string, Partial<Placement>, string | null]>([
      ["a derived point with a log alternative", { now: usesPhoto, independent }, "Without photos and pins this would move 2.5 km."],
      ["a derived point alone", { now: usesPhoto, independent: null }, "No log supports this. It rests only on other photos or pins."],
      ["an own position with logs nearby", { state: "own_position", now: null, independent }, "The logs at that moment are 2.5 km from this position."],
      ["a manual position with logs nearby", { state: "manual", now: null, independent }, "The logs at that moment are 2.5 km from this position."],
      ["an own position with no log", { state: "own_position", now: null, independent: null }, "No log point is within 1 h of this item's time."],
      ["a manual position with no log", { state: "manual", now: null, independent: null }, "No log point is within 1 h of this item's time."],
    ])("%s", (_name, placement, sentence) => {
      renderCard(placement);
      expect(screen.getByText(sentence!)).toBeInTheDocument();
    });

    it("has none for a pair of logs", () => {
      renderCard();
      expect(screen.queryByText(/Without photos and pins/)).not.toBeInTheDocument();
      expect(screen.queryByText(/No log supports this/)).not.toBeInTheDocument();
    });
  });

  it("renders nothing when there is nothing to say", () => {
    const data = makeResponse("a", { placement: { state: "no_time", now: null } });
    data.item.taken_at = null;
    const { container } = render(<PlacementCard item={data.item} placement={data.placement} onJump={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });
});
