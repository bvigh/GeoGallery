import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "@/api/client";
import App from "@/App";

vi.mock("@/api/client", () => ({
  api: { me: vi.fn(), logout: vi.fn() },
}));
vi.mock("@/pages/MapPage", () => ({ MapPage: () => null }));
vi.mock("@/pages/TripsPage", () => ({ TripsPage: () => null }));
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
