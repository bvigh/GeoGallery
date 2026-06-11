import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import { FilterPanel } from "./FilterPanel";
import type { FilterState, Trip } from "../../types";

const emptyFilters: FilterState = {
  fromDate: "",
  toDate: "",
  tripIds: [],
  mediaTypes: [],
};

const sampleTrips: Trip[] = [
  { id: "t1", name: "Iceland 2024", start_date: "2024-07-01", end_date: "2024-07-14", media_count: 10, cover_key: null },
  { id: "t2", name: "Tokyo Winter", start_date: "2024-12-20", end_date: null, media_count: 5, cover_key: null },
];

function renderPanel(
  overrides: Partial<FilterState> = {},
  activeCount = 0,
  onChange = vi.fn(),
  trips = sampleTrips
) {
  const filters = { ...emptyFilters, ...overrides };
  return { onChange, ...render(<FilterPanel trips={trips} filters={filters} onChange={onChange} activeCount={activeCount} />) };
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
    const { onChange } = renderPanel({ tripIds: ["t1"], mediaTypes: ["photo"] }, 2);
    await userEvent.click(screen.getByRole("button", { name: /filters/i }));
    await userEvent.click(screen.getByRole("button", { name: /reset/i }));
    expect(onChange).toHaveBeenCalledWith({ fromDate: "", toDate: "", tripIds: [], mediaTypes: [] });
  });

  it("badge shows activeCount when > 0", () => {
    renderPanel({}, 3);
    expect(screen.getByText("3")).toBeInTheDocument();
  });

  it("badge is hidden when activeCount is 0", () => {
    renderPanel({}, 0);
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });
});
