import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, it, expect, vi } from "vitest";
import { FilterPanel } from "@/components/map/FilterPanel";
import { activeFilterCount, emptyFilters } from "@/lib/filters";
import type { Album, FilterState, Trip } from "@/types";

const sampleTrips: Trip[] = [
  { id: "t1", name: "Iceland 2024", start_date: "2024-07-01", end_date: "2024-07-14", media_count: 10, cover_key: null, user_edited: false },
  { id: "t2", name: "Tokyo Winter", start_date: "2024-12-20", end_date: null, media_count: 5, cover_key: null, user_edited: false },
];

function album(overrides: Partial<Album>): Album {
  return {
    id: "a0",
    name: "Album",
    owner_id: "u-me",
    owner_name: "Alice",
    is_owner: true,
    member_count: 2,
    media_count: 3,
    start_date: null,
    end_date: null,
    created_at: "2025-01-01T00:00:00Z",
    last_added_at: null,
    ...overrides,
  };
}

const sampleAlbums: Album[] = [
  album({ id: "a1", name: "Family" }),
  album({ id: "a2", name: "Road trip", owner_id: "u-bob", owner_name: "Bob", is_owner: false }),
];

function renderPanel(
  overrides: Partial<FilterState> = {},
  activeCount = 0,
  onChange = vi.fn(),
  trips = sampleTrips,
  albums = sampleAlbums
) {
  const filters = { ...emptyFilters, ...overrides };
  return {
    onChange,
    ...render(
      <FilterPanel trips={trips} albums={albums} filters={filters} onChange={onChange} activeCount={activeCount} />
    ),
  };
}

async function openPanel() {
  await userEvent.click(screen.getByRole("button", { name: /filters/i }));
}

describe("FilterPanel", () => {
  it("renders collapsed by default", () => {
    renderPanel();
    expect(screen.queryByLabelText("From date")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /filters/i })).toBeInTheDocument();
  });

  it("clicking toggle opens panel", async () => {
    renderPanel();
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    expect(screen.getByLabelText("From date")).toBeInTheDocument();
  });

  it("clicking toggle again closes panel", async () => {
    renderPanel();
    const btn = screen.getByRole("button", { name: /filters/i });
    await userEvent.click(btn);
    await userEvent.click(btn);
    expect(screen.queryByLabelText("From date")).not.toBeInTheDocument();
  });

  it("from date input calls onChange with updated fromDate", async () => {
    const { onChange } = renderPanel();
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.type(screen.getByLabelText("From date"), "2024-03-01");
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ fromDate: "2024-03-01" })
    );
  });

  it("to date input calls onChange with updated toDate", async () => {
    const { onChange } = renderPanel();
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.type(screen.getByLabelText("To date"), "2024-03-31");
    expect(onChange).toHaveBeenLastCalledWith(
      expect.objectContaining({ toDate: "2024-03-31" })
    );
  });

  it("checking a trip checkbox adds its id to tripIds", async () => {
    const { onChange } = renderPanel();
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.click(screen.getByLabelText("Iceland 2024"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ tripIds: ["t1"] })
    );
  });

  it("unchecking a trip checkbox removes its id", async () => {
    const { onChange } = renderPanel({ tripIds: ["t1"] });
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.click(screen.getByLabelText("Iceland 2024"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ tripIds: [] })
    );
  });

  it("Unassigned checkbox adds sentinel to tripIds", async () => {
    const { onChange } = renderPanel();
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.click(screen.getByLabelText("Unassigned"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ tripIds: ["unassigned"] })
    );
  });

  it("checking a media type adds it to mediaTypes", async () => {
    const { onChange } = renderPanel();
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.click(screen.getByLabelText("photo"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ mediaTypes: ["photo"] })
    );
  });

  it("unchecking a media type removes it", async () => {
    const { onChange } = renderPanel({ mediaTypes: ["photo"] });
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.click(screen.getByLabelText("photo"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ mediaTypes: [] })
    );
  });

  it("Reset button is hidden when activeCount is 0", async () => {
    renderPanel({}, 0);
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    expect(screen.queryByRole("button", { name: /reset/i })).not.toBeInTheDocument();
  });

  it("Reset button is visible when activeCount > 0", async () => {
    renderPanel({}, 2);
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    expect(screen.getByRole("button", { name: /reset/i })).toBeInTheDocument();
  });

  it("clicking Reset calls onChange with empty state", async () => {
    const { onChange } = renderPanel(
      { tripIds: ["t1"], albumIds: ["a1"], mediaTypes: ["photo"], owner: "shared" },
      3
    );
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.click(screen.getByRole("button", { name: /reset/i }));
    expect(onChange).toHaveBeenCalledWith(emptyFilters);
  });

  it("badge shows activeCount when > 0", () => {
    renderPanel({}, 3);
    expect(screen.getByText("3")).toBeInTheDocument();
  });

  it("badge is hidden when activeCount is 0", () => {
    renderPanel({}, 0);
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });

  describe("collections", () => {
    it("groups trips and albums under Collections", async () => {
      renderPanel();
      await openPanel();
      expect(screen.getByText("Collections")).toBeInTheDocument();
      const trips = within(screen.getByRole("group", { name: "Trips" }));
      expect(trips.getByLabelText("Unassigned")).toBeInTheDocument();
      expect(trips.getByLabelText("Iceland 2024")).toBeInTheDocument();
      const albums = within(screen.getByRole("group", { name: "Albums" }));
      expect(albums.getByLabelText("Family")).toBeInTheDocument();
      expect(albums.getByLabelText("Road trip by Bob")).toBeInTheDocument();
    });

    it.each([
      { label: "own album shows no owner", albums: [album({ name: "Mine" })], want: "Mine" },
      {
        label: "shared album shows the owner name",
        albums: [album({ name: "Theirs", is_owner: false, owner_name: "Carol" })],
        want: "Theirs by Carol",
      },
      {
        label: "shared album without an owner name",
        albums: [album({ name: "Theirs", is_owner: false, owner_name: null })],
        want: "Theirs by another user",
      },
    ])("$label", async ({ albums, want }) => {
      renderPanel({}, 0, vi.fn(), sampleTrips, albums);
      await openPanel();
      expect(within(screen.getByRole("group", { name: "Albums" })).getByLabelText(want)).toBeInTheDocument();
    });

    it("shows the albums group when there are no trips", async () => {
      renderPanel({}, 0, vi.fn(), []);
      await openPanel();
      expect(screen.queryByRole("group", { name: "Trips" })).not.toBeInTheDocument();
      expect(screen.queryByLabelText("Unassigned")).not.toBeInTheDocument();
      expect(within(screen.getByRole("group", { name: "Albums" })).getByLabelText("Family")).toBeInTheDocument();
    });

    it("hides the albums group when there are no albums", async () => {
      renderPanel({}, 0, vi.fn(), sampleTrips, []);
      await openPanel();
      expect(screen.getByRole("group", { name: "Trips" })).toBeInTheDocument();
      expect(screen.queryByRole("group", { name: "Albums" })).not.toBeInTheDocument();
    });

    it("hides Collections when there are neither trips nor albums", async () => {
      renderPanel({}, 0, vi.fn(), [], []);
      await openPanel();
      expect(screen.queryByText("Collections")).not.toBeInTheDocument();
    });

    it("checking an album adds its id to albumIds and keeps tripIds", async () => {
      const { onChange } = renderPanel({ tripIds: ["t1"] });
      await openPanel();
      await userEvent.click(screen.getByLabelText("Road trip by Bob"));
      expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ tripIds: ["t1"], albumIds: ["a2"] }));
    });

    it("unchecking an album removes its id", async () => {
      const { onChange } = renderPanel({ albumIds: ["a1", "a2"] });
      await openPanel();
      expect(screen.getByLabelText("Family")).toBeChecked();
      await userEvent.click(screen.getByLabelText("Family"));
      expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ albumIds: ["a2"] }));
    });
  });

  describe("owner filter", () => {
    it("checks All by default", async () => {
      renderPanel();
      await openPanel();
      const group = within(screen.getByRole("group", { name: "Show" }));
      expect(group.getByRole("radio", { name: "All" })).toBeChecked();
      expect(group.getByRole("radio", { name: "Mine" })).not.toBeChecked();
      expect(group.getByRole("radio", { name: "Shared with me" })).not.toBeChecked();
    });

    it("reflects the current owner value", async () => {
      renderPanel({ owner: "mine" });
      await openPanel();
      expect(screen.getByRole("radio", { name: "Mine" })).toBeChecked();
      expect(screen.getByRole("radio", { name: "All" })).not.toBeChecked();
    });

    it.each([
      { from: "all", click: "Shared with me", want: "shared" },
      { from: "all", click: "Mine", want: "mine" },
      { from: "shared", click: "All", want: "all" },
    ] as const)("$from → $click sets owner=$want", async ({ from, click, want }) => {
      const { onChange } = renderPanel({ owner: from, tripIds: ["t1"] });
      await openPanel();
      await userEvent.click(screen.getByRole("radio", { name: click }));
      expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ owner: want, tripIds: ["t1"] }));
    });
  });

  describe("active count", () => {
    // The parent computes activeCount with activeFilterCount; the panel shows it as a badge.
    it.each([
      { label: "ticked trips and albums count as one", filters: { tripIds: ["t1"], albumIds: ["a1", "a2"] }, want: "1" },
      { label: "owner other than all counts as one", filters: { owner: "shared" as const }, want: "1" },
      { label: "collections plus owner", filters: { albumIds: ["a1"], owner: "mine" as const }, want: "2" },
    ])("$label", async ({ filters, want }) => {
      const state = { ...emptyFilters, ...filters };
      renderPanel(state, activeFilterCount(state));
      expect(screen.getByRole("button", { name: /filters/i })).toHaveTextContent(want);
      await openPanel();
      expect(screen.getByRole("button", { name: /reset/i })).toBeInTheDocument();
    });
  });

  describe("collection search", () => {
    const manyTrips: Trip[] = Array.from({ length: 6 }, (_, i) => ({
      ...sampleTrips[0],
      id: `m${i}`,
      name: `Trip 202${i}-05-01`,
      start_date: `202${i}-05-01`,
    }));

    function renderMany(overrides: Partial<FilterState> = {}) {
      return renderPanel(overrides, 0, vi.fn(), [...manyTrips, sampleTrips[1]], sampleAlbums);
    }

    const labels = () =>
      screen
        .getAllByRole("checkbox")
        .map((c) => c.closest("label")?.textContent?.trim())
        .filter((l) => !["photo", "video", "photo_360", "video_360"].includes(l ?? ""));

    it("has no search field for a short list", async () => {
      renderPanel();
      await openPanel();
      expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    });

    it("filters trips and albums together, by name, date and album owner", async () => {
      renderMany();
      await openPanel();
      const search = screen.getByRole("searchbox", { name: "Search collections" });

      await userEvent.type(search, "tokyo");
      expect(labels()).toEqual(["Tokyo Winter"]);
      expect(screen.queryByRole("group", { name: "Albums" })).not.toBeInTheDocument();

      await userEvent.clear(search);
      await userEvent.type(search, "bob");
      expect(labels()).toEqual(["Road trip by Bob"]);
      expect(screen.queryByRole("group", { name: "Trips" })).not.toBeInTheDocument();

      await userEvent.clear(search);
      await userEvent.type(search, "2024-12");
      expect(labels()).toEqual(["Tokyo Winter"]);

      await userEvent.clear(search);
      await userEvent.type(search, "zzz");
      expect(screen.getByText("No matching trips or albums")).toBeInTheDocument();
    });

    it("keeps ticked collections listed while searching", async () => {
      renderMany({ tripIds: ["m1", "unassigned"], albumIds: ["a1"] });
      await openPanel();
      await userEvent.type(screen.getByRole("searchbox", { name: "Search collections" }), "tokyo");
      expect(labels()).toEqual(["Unassigned", "Tokyo Winter", "Trip 2021-05-01", "Family"]);
    });

    it("forgets the search when the panel closes", async () => {
      renderMany();
      await openPanel();
      await userEvent.type(screen.getByRole("searchbox", { name: "Search collections" }), "tokyo");
      await openPanel();
      await openPanel();
      expect(screen.getByRole("searchbox", { name: "Search collections" })).toHaveValue("");
    });
  });

  describe("select all and clear", () => {
    // Eight collections, so the search field is there.
    const trips: Trip[] = [
      { ...sampleTrips[0], id: "s1", name: "Iceland ring road", start_date: "2024-07-01", end_date: "2024-07-14" },
      { ...sampleTrips[0], id: "s2", name: "Iceland winter", start_date: "2025-02-01", end_date: "2025-02-05" },
      { ...sampleTrips[0], id: "s3", name: "Tokyo", start_date: "2024-12-20", end_date: null },
      { ...sampleTrips[0], id: "s4", name: "Rome", start_date: "2023-05-01", end_date: "2023-05-04" },
    ];
    const albums: Album[] = [
      album({ id: "b1", name: "Iceland highlights" }),
      album({ id: "b2", name: "Iceland", owner_id: "u-bob", owner_name: "Bob", is_owner: false }),
      album({ id: "b3", name: "Family" }),
      album({ id: "b4", name: "Dogs", owner_id: "u-bob", owner_name: "Bob", is_owner: false }),
    ];
    // Every other filter set, so a test can see them kept.
    const others = { fromDate: "2024-01-01", toDate: "2025-12-31", mediaTypes: ["photo" as const], owner: "mine" as const };

    function renderSet(overrides: Partial<FilterState> = {}, onChange = vi.fn()) {
      return renderPanel(overrides, 0, onChange, trips, albums);
    }

    // The panel as a page holds it: each change comes back as the new filters.
    function Stateful({ initial, onChange }: { initial: FilterState; onChange: (f: FilterState) => void }) {
      const [filters, setFilters] = useState(initial);
      return (
        <FilterPanel
          trips={trips}
          albums={albums}
          filters={filters}
          onChange={(next) => {
            onChange(next);
            setFilters(next);
          }}
          activeCount={activeFilterCount(filters)}
        />
      );
    }

    function renderStateful(overrides: Partial<FilterState> = {}) {
      const onChange = vi.fn();
      render(<Stateful initial={{ ...emptyFilters, ...overrides }} onChange={onChange} />);
      return { onChange };
    }

    const search = () => screen.getByRole("searchbox", { name: "Search collections" });
    const selectAll = () => screen.queryByRole("button", { name: /^Select / });
    const clear = () => screen.queryByRole("button", { name: /^Clear \d+ selected$/ });

    it.each([
      { label: "without a query", query: "" },
      { label: "with a query of spaces", query: "   " },
    ])("offers no Select all $label", async ({ query }) => {
      renderSet();
      await openPanel();
      if (query) await userEvent.type(search(), query);
      expect(selectAll()).not.toBeInTheDocument();
    });

    it("counts every match, ticked or not, and ticks the rest with the earlier ticks kept", async () => {
      const { onChange } = renderSet({ ...others, tripIds: ["unassigned", "s1"], albumIds: ["b3"] });
      await openPanel();
      await userEvent.type(search(), "iceland");
      const button = selectAll()!;
      expect(button).toHaveAccessibleName("Select all 4 matches");
      expect(button).toHaveAttribute("type", "button");

      await userEvent.click(button);
      expect(onChange).toHaveBeenCalledTimes(1);
      expect(onChange).toHaveBeenCalledWith({
        ...others,
        tripIds: ["unassigned", "s1", "s2"],
        albumIds: ["b3", "b1", "b2"],
      });
    });

    it("never ticks Unassigned, though the query matches it", async () => {
      const { onChange } = renderSet();
      await openPanel();
      // "d" is in Unassigned, Iceland, the road and the dogs.
      await userEvent.type(search(), "d");
      expect(screen.getByLabelText("Unassigned")).not.toBeChecked();
      await userEvent.click(selectAll()!);
      expect(onChange).toHaveBeenCalledWith({ ...emptyFilters, tripIds: ["s1", "s2"], albumIds: ["b1", "b2", "b4"] });
    });

    it("offers no Select all when only Unassigned matches", async () => {
      renderSet();
      await openPanel();
      await userEvent.type(search(), "assigned");
      expect(screen.getByLabelText("Unassigned")).toBeInTheDocument();
      expect(selectAll()).not.toBeInTheDocument();
    });

    it("offers no Select all once every match is ticked", async () => {
      renderSet({ tripIds: ["s3"] });
      await openPanel();
      await userEvent.type(search(), "tokyo");
      expect(selectAll()).not.toBeInTheDocument();
      await userEvent.clear(search());
      await userEvent.type(search(), "rome");
      expect(selectAll()).toHaveAccessibleName("Select the 1 match");
    });

    it("ticks one search's matches after another's, and hides itself once they are ticked", async () => {
      const { onChange } = renderStateful();
      await openPanel();
      await userEvent.type(search(), "iceland");
      await userEvent.click(selectAll()!);
      expect(selectAll()).not.toBeInTheDocument();
      expect(screen.getByLabelText("Iceland winter")).toBeChecked();
      expect(screen.getByLabelText("Iceland by Bob")).toBeChecked();

      await userEvent.clear(search());
      await userEvent.type(search(), "bob");
      // Bob's Iceland is ticked already; his dogs are not.
      expect(selectAll()).toHaveAccessibleName("Select all 2 matches");
      await userEvent.click(selectAll()!);
      expect(onChange).toHaveBeenLastCalledWith({ ...emptyFilters, tripIds: ["s1", "s2"], albumIds: ["b1", "b2", "b4"] });
      expect(clear()).toHaveAccessibleName("Clear 5 selected");
    });

    it("Clear unticks every trip and album and keeps every other filter", async () => {
      const { onChange } = renderSet({ ...others, tripIds: ["unassigned", "s1"], albumIds: ["b3"] });
      await openPanel();
      const button = clear()!;
      expect(button).toHaveAccessibleName("Clear 3 selected");
      expect(button).toHaveAttribute("type", "button");

      await userEvent.click(button);
      expect(onChange).toHaveBeenCalledTimes(1);
      expect(onChange).toHaveBeenCalledWith({ ...others, tripIds: [], albumIds: [] });
    });

    it("offers Clear only while a collection is ticked, search field or not", async () => {
      const { unmount } = renderSet({ ...others });
      await openPanel();
      expect(clear()).not.toBeInTheDocument();
      unmount();

      // Four collections: no search field, and Clear all the same.
      renderPanel({ albumIds: ["a2"] });
      await openPanel();
      expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
      expect(clear()).toHaveAccessibleName("Clear 1 selected");
    });

    it("keeps the search and the other filters after Clear", async () => {
      const { onChange } = renderStateful({ ...others, albumIds: ["b4"] });
      await openPanel();
      await userEvent.type(search(), "iceland");
      await userEvent.click(clear()!);
      expect(onChange).toHaveBeenLastCalledWith({ ...others, tripIds: [], albumIds: [] });
      expect(clear()).not.toBeInTheDocument();
      expect(search()).toHaveValue("iceland");
      expect(screen.queryByLabelText("Dogs by Bob")).not.toBeInTheDocument();
      expect(selectAll()).toHaveAccessibleName("Select all 4 matches");
    });

    it.each([
      { label: "Select all", query: "iceland", button: selectAll, filters: {} },
      { label: "Clear", query: "", button: clear, filters: { tripIds: ["s4"] } },
    ])("leaves the focus on the collections after $label, not on the page", async ({ query, button, filters }) => {
      renderStateful(filters);
      await openPanel();
      if (query) await userEvent.type(search(), query);
      await userEvent.click(button()!);
      expect(button()).not.toBeInTheDocument();
      const section = screen.getByRole("group", { name: "Collections" });
      expect(section).toHaveFocus();
      await userEvent.tab();
      expect(search()).toHaveFocus();
    });

    it("forgets a query whose field is gone", async () => {
      const onChange = vi.fn();
      const { rerender } = renderSet({}, onChange);
      await openPanel();
      await userEvent.type(search(), "d");
      expect(selectAll()).toBeInTheDocument();

      // The New challenge dialog narrows the albums to those every player is in.
      rerender(
        <FilterPanel trips={trips} albums={[albums[0], albums[2]]} filters={emptyFilters} onChange={onChange} activeCount={0} />
      );
      expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
      expect(selectAll()).not.toBeInTheDocument();
      // "d" is not in Family.
      expect(screen.getByLabelText("Family")).toBeInTheDocument();
    });

    it("gives both a finger-sized row", async () => {
      renderSet({ tripIds: ["s1"] });
      await openPanel();
      await userEvent.type(search(), "iceland");
      expect(selectAll()).toHaveClass("touch:min-h-11");
      expect(clear()).toHaveClass("touch:min-h-11");
    });
  });

  describe("trips by year", () => {
    // IDs must not collide with sampleTrips (t1, t2): duplicate keys confuse React.
    const yearTrips: Trip[] = [
      { ...sampleTrips[0], id: "y1", name: "Lisbon", start_date: "2026-04-02", end_date: "2026-04-09" },
      { ...sampleTrips[0], id: "y2", name: "Trip 2026-01-10", start_date: "2026-01-10", end_date: "2026-01-11" },
      { ...sampleTrips[0], id: "y3", name: "Iceland", start_date: "2025-07-01", end_date: "2025-07-14" },
      { ...sampleTrips[0], id: "y4", name: "Rome", start_date: "2025-03-01", end_date: "2025-03-05" },
      { ...sampleTrips[0], id: "y5", name: "Tokyo", start_date: "2023-12-20", end_date: null },
      { ...sampleTrips[0], id: "y6", name: "Shoebox", start_date: null, end_date: null },
    ];

    function renderYears(overrides: Partial<FilterState> = {}, onChange = vi.fn()) {
      return renderPanel(overrides, 0, onChange, yearTrips, sampleAlbums);
    }

    const tripsGroup = () => within(screen.getByRole("group", { name: "Trips" }));
    const headers = () => tripsGroup().queryAllByRole("button").map((b) => b.textContent);
    const tripLabels = () =>
      tripsGroup()
        .queryAllByRole("checkbox")
        .map((c) => c.closest("label")?.textContent?.trim());
    const header = (year: string) => tripsGroup().getByRole("button", { name: new RegExp(`^${year} `) });

    it("splits trips by year, newest first, with No date last", async () => {
      renderYears();
      await openPanel();
      expect(headers()).toEqual(["▾2026 (2)", "▸2025 (2)", "▸2023 (1)", "▸No date (1)"]);
    });

    it("keeps Unassigned above the years", async () => {
      renderYears();
      await openPanel();
      const group = screen.getByRole("group", { name: "Trips" });
      const unassigned = within(group).getByLabelText("Unassigned");
      expect(unassigned.compareDocumentPosition(header("2026")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("opens only the newest year, and a header toggles its trips", async () => {
      renderYears();
      await openPanel();
      expect(header("2026")).toHaveAttribute("aria-expanded", "true");
      expect(header("2025")).toHaveAttribute("aria-expanded", "false");
      expect(tripLabels()).toEqual(["Unassigned", "Lisbon", "Trip 2026-01-10"]);

      await userEvent.click(header("2025"));
      expect(header("2025")).toHaveAttribute("aria-expanded", "true");
      expect(tripLabels()).toEqual(["Unassigned", "Lisbon", "Trip 2026-01-10", "Iceland", "Rome"]);

      await userEvent.click(header("2026"));
      expect(header("2026")).toHaveAttribute("aria-expanded", "false");
      expect(tripLabels()).toEqual(["Unassigned", "Iceland", "Rome"]);

      await userEvent.click(header("No date"));
      expect(tripLabels()).toEqual(["Unassigned", "Iceland", "Rome", "Shoebox"]);
    });

    it("counts ticked trips in a collapsed year without opening it", async () => {
      renderYears({ tripIds: ["y3", "y4", "y1"] });
      await openPanel();
      expect(header("2025")).toHaveAccessibleName("2025 (2) · 2 selected");
      expect(header("2025")).toHaveAttribute("aria-expanded", "false");
      expect(header("2026")).toHaveAccessibleName("2026 (2) · 1 selected");
      expect(header("2023")).toHaveAccessibleName("2023 (1)");
    });

    it("opens years with matches while searching and restores them when cleared", async () => {
      renderYears();
      await openPanel();
      await userEvent.click(header("2023"));
      await userEvent.click(header("2026"));
      // The user's own state: 2023 open, everything else closed.
      expect(tripLabels()).toEqual(["Unassigned", "Tokyo"]);

      const search = screen.getByRole("searchbox", { name: "Search collections" });
      await userEvent.type(search, "2025");
      expect(headers()).toEqual(["▾2025 (2)"]);
      expect(tripLabels()).toEqual(["Iceland", "Rome"]);
      // Forced open, so a click could only change the hidden user state.
      expect(header("2025")).toBeDisabled();

      await userEvent.clear(search);
      await userEvent.type(search, "o");
      // Lisbon, Rome, Tokyo and Shoebox match; 2025 opens for Rome alone.
      expect(headers()).toEqual(["▾2026 (2)", "▾2025 (2)", "▾2023 (1)", "▾No date (1)"]);
      expect(tripLabels()).toEqual(["Lisbon", "Rome", "Tokyo", "Shoebox"]);

      await userEvent.clear(search);
      expect(headers()).toEqual(["▸2026 (2)", "▸2025 (2)", "▾2023 (1)", "▸No date (1)"]);
      expect(tripLabels()).toEqual(["Unassigned", "Tokyo"]);
    });

    it("opens the year of a ticked trip while searching", async () => {
      renderYears({ tripIds: ["y4"] });
      await openPanel();
      await userEvent.type(screen.getByRole("searchbox", { name: "Search collections" }), "lisbon");
      expect(headers()).toEqual(["▾2026 (2)", "▾2025 (2) · 1 selected"]);
      expect(tripLabels()).toEqual(["Lisbon", "Rome"]);
    });

    it("forgets the expansion when the panel closes", async () => {
      renderYears();
      await openPanel();
      await userEvent.click(header("2025"));
      await openPanel();
      await openPanel();
      expect(header("2025")).toHaveAttribute("aria-expanded", "false");
      expect(header("2026")).toHaveAttribute("aria-expanded", "true");
    });

    it("ticking a trip inside a year adds its id to tripIds", async () => {
      const { onChange } = renderYears({ tripIds: ["unassigned"] });
      await openPanel();
      await userEvent.click(header("2025"));
      await userEvent.click(screen.getByLabelText("Rome"));
      expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ tripIds: ["unassigned", "y4"] }));
    });

    it.each([
      { label: "one year", trips: sampleTrips, want: ["Unassigned", "Iceland 2024", "Tokyo Winter"] },
      {
        label: "only undated trips",
        trips: [{ ...sampleTrips[0], id: "u1", name: "Undated", start_date: null }],
        want: ["Unassigned", "Undated"],
      },
    ])("lists $label flat", async ({ trips, want }) => {
      renderPanel({}, 0, vi.fn(), trips, sampleAlbums);
      await openPanel();
      expect(headers()).toEqual([]);
      expect(tripLabels()).toEqual(want);
    });
  });

  describe("on a phone", () => {
    it("closes from the Done button below the options, keeping the filters", async () => {
      const { onChange } = renderPanel();
      await openPanel();
      await userEvent.click(screen.getByRole("button", { name: "Done" }));

      expect(screen.queryByLabelText("From date")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: /filters/i })).toHaveAttribute("aria-expanded", "false");
      expect(onChange).not.toHaveBeenCalled();
    });

    // jsdom has no layout: the classes are what keeps the panel inside a phone held sideways.
    it("stays inside its parent and scrolls its options, with Done always in view", async () => {
      renderPanel();
      await openPanel();
      const toggle = screen.getByRole("button", { name: /filters/i });
      const column = toggle.parentElement!;
      const body = screen.getByLabelText("From date").closest(".overflow-y-auto")!;
      const panel = body.parentElement!;
      const done = screen.getByRole("button", { name: "Done" });

      // Spans the parent but lets the map have the pointer beside the toggle and the panel.
      expect(column).toHaveClass("absolute", "top-4", "bottom-4", "flex-col", "pointer-events-none");
      expect(toggle).toHaveClass("pointer-events-auto", "shrink-0");
      expect(panel).toHaveClass("pointer-events-auto", "min-h-0", "flex-col", "w-[min(18rem,calc(100vw-2rem))]", "sm:w-64");
      expect(body).toHaveClass("min-h-0", "overscroll-contain");
      expect(body).toContainElement(screen.getByLabelText("photo"));
      expect(body).not.toContainElement(done);
      expect(panel).toContainElement(done);
      // Only where a finger is the pointer.
      expect(done.parentElement).toHaveClass("hidden", "touch:block");
    });

    it("scrolls the collections with the panel on touch, not in a list of their own", async () => {
      renderPanel();
      await openPanel();
      const list = screen.getByRole("group", { name: "Trips" }).parentElement!;

      expect(list).toHaveClass("max-h-56", "overflow-y-auto");
      expect(list).toHaveClass("touch:max-h-none", "touch:overflow-y-visible", "short:max-h-none", "short:overflow-y-visible");
    });

    it("gives every control a finger-sized row", async () => {
      renderPanel({}, 1, vi.fn(), [
        { ...sampleTrips[0], id: "y1", name: "Lisbon", start_date: "2026-04-02" },
        { ...sampleTrips[0], id: "y2", name: "Rome", start_date: "2025-03-01" },
      ]);
      await openPanel();

      for (const el of [
        screen.getByRole("button", { name: /^≡ Filters/ }),
        screen.getByLabelText("photo").closest("label"),
        screen.getByLabelText("Family").closest("label"),
        screen.getByRole("radio", { name: "Mine" }).closest("label"),
        screen.getByRole("button", { name: /^2025 / }),
        screen.getByLabelText("From date"),
        screen.getByRole("button", { name: /reset/i }),
        screen.getByRole("button", { name: "Done" }),
      ]) {
        expect(el).toHaveClass("touch:min-h-11");
      }
      expect(screen.getByLabelText("photo")).toHaveClass("touch:h-5", "touch:w-5");
    });
  });
});
