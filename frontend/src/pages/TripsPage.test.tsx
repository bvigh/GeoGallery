import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "../api/client";
import { TripsPage } from "./TripsPage";
import type { Trip } from "../types";

vi.mock("../api/client", () => ({
  api: {
    get: vi.fn(),
    patch: vi.fn(),
  },
}));

const mockApi = api as unknown as { get: ReturnType<typeof vi.fn>; patch: ReturnType<typeof vi.fn> };

const sampleTrips: Trip[] = [
  {
    id: "t1",
    name: "Iceland 2024",
    start_date: "2024-07-01",
    end_date: "2024-07-14",
    media_count: 42,
    cover_key: null,
  },
  {
    id: "t2",
    name: "Tokyo Winter",
    start_date: "2024-12-20",
    end_date: null,
    media_count: 5,
    cover_key: null,
  },
];

describe("TripsPage", () => {
  beforeEach(() => {
    mockApi.get.mockResolvedValue(sampleTrips);
    mockApi.patch.mockResolvedValue({});
  });

  it("displays trip names after API data loads", async () => {
    render(<TripsPage />);
    expect(await screen.findByText("Iceland 2024")).toBeInTheDocument();
    expect(screen.getByText("Tokyo Winter")).toBeInTheDocument();
  });

  it("clicking Edit activates edit mode for that trip", async () => {
    render(<TripsPage />);
    await screen.findByText("Iceland 2024");

    const editButtons = screen.getAllByRole("button", { name: "Edit" });
    await userEvent.click(editButtons[0]);

    const input = screen.getByDisplayValue("Iceland 2024");
    expect(input).toBeInTheDocument();
  });
});
