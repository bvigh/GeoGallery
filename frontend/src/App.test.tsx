import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "@/api/client";
import App from "@/App";

vi.mock("@/api/client", () => ({
  api: { me: vi.fn(), logout: vi.fn() },
}));
vi.mock("@/pages/MapPage", () => ({
  MapPage: ({ active }: { active: boolean }) => <div data-testid="map-page" data-active={String(active)} />,
}));
vi.mock("@/pages/TripsPage", () => ({ TripsPage: () => null }));
vi.mock("@/pages/AlbumsPage", () => ({ AlbumsPage: () => <div data-testid="albums-page" /> }));
vi.mock("@/pages/GamePage", () => ({ GamePage: () => null }));
vi.mock("@/pages/UploadPage", () => ({ UploadPage: () => null }));
vi.mock("@/pages/SettingsPage", () => ({ SettingsPage: () => null }));
vi.mock("@/pages/AdminPage", () => ({ AdminPage: () => null }));

const mockApi = api as unknown as {
  me: ReturnType<typeof vi.fn>;
  logout: ReturnType<typeof vi.fn>;
};

describe("App logout", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: "Alice", role: "user" });
  });

  it("shows an error when logout fails", async () => {
    mockApi.logout.mockRejectedValue(new Error("boom"));
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "Logout" }));
    expect(await screen.findByText("Logout failed, try again")).toBeInTheDocument();
  });

  it("shows no error when logout succeeds", async () => {
    mockApi.logout.mockResolvedValue(undefined);
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "Logout" }));
    await waitFor(() => expect(mockApi.logout).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("Logout failed, try again")).not.toBeInTheDocument();
  });
});

describe("App tabs", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: "Alice", role: "user" });
  });

  it("shows the Albums tab after Trips and opens the Albums page", async () => {
    render(<App />);
    const nav = await screen.findByRole("navigation");
    const labels = within(nav).getAllByRole("button").map((b) => b.textContent);
    expect(labels).toEqual(["Map", "Trips", "Albums", "Game", "Upload", "Settings"]);
    expect(screen.queryByTestId("albums-page")).not.toBeInTheDocument();

    await userEvent.click(within(nav).getByRole("button", { name: "Albums" }));
    expect(screen.getByTestId("albums-page")).toBeInTheDocument();
  });
});

describe("App map tab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: "Alice", role: "user" });
  });

  // Unmounting the map would throw away its markers and reload them all on the next visit.
  it("keeps the map page mounted, hidden, while another tab is open", async () => {
    render(<App />);
    const mapPage = await screen.findByTestId("map-page");
    expect(mapPage).toHaveAttribute("data-active", "true");
    expect(mapPage.parentElement).not.toHaveClass("hidden");

    const nav = screen.getByRole("navigation");
    await userEvent.click(within(nav).getByRole("button", { name: "Albums" }));
    expect(screen.getByTestId("map-page")).toBe(mapPage);
    expect(mapPage).toHaveAttribute("data-active", "false");
    expect(mapPage.parentElement).toHaveClass("hidden");

    await userEvent.click(within(nav).getByRole("button", { name: "Map" }));
    expect(screen.getByTestId("map-page")).toBe(mapPage);
    expect(mapPage).toHaveAttribute("data-active", "true");
    expect(mapPage.parentElement).not.toHaveClass("hidden");
  });
});
