import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { GamePage } from "@/pages/GamePage";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), getKeyframes: vi.fn() } };
});

vi.mock("@/components/map/FilterPanel", () => ({ FilterPanel: () => null }));
vi.mock("@/components/viewer/Panorama360", () => ({ default: () => null }));
vi.mock("@/components/viewer/Video360", () => ({ default: () => null }));

const mockApi = api as unknown as { get: ReturnType<typeof vi.fn> };

describe("GamePage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders the empty state when /game/round returns 404", async () => {
    mockApi.get.mockImplementation((path: string) => {
      if (path === "/trips") return Promise.resolve([]);
      return Promise.reject(new ApiError("GET /game/round failed: 404", 404));
    });
    render(<GamePage />);
    expect(screen.queryByText("No geolocated media yet")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start Round" }));
    expect(await screen.findByText("No geolocated media yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start Round" })).toBeInTheDocument();
  });
});
