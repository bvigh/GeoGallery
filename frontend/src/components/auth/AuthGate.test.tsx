import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "@/api/client";
import App from "@/App";
import { AuthGate } from "@/components/auth/AuthGate";
import { useCurrentUser } from "@/components/auth/AuthContext";
import type { CurrentUser } from "@/types";

vi.mock("@/api/client", () => ({
  api: { me: vi.fn(), logout: vi.fn() },
}));

vi.mock("@/pages/MapPage", () => ({ MapPage: () => <div data-testid="map-page" /> }));
vi.mock("@/pages/TripsPage", () => ({ TripsPage: () => null }));
vi.mock("@/pages/GamePage", () => ({ GamePage: () => null }));
vi.mock("@/pages/UploadPage", () => ({ UploadPage: () => null }));
vi.mock("@/pages/SettingsPage", () => ({ SettingsPage: () => <div data-testid="settings-page" /> }));
vi.mock("@/pages/AdminPage", () => ({ AdminPage: () => <div data-testid="admin-page" /> }));

const mockApi = api as unknown as { me: ReturnType<typeof vi.fn>; logout: ReturnType<typeof vi.fn> };

const alice: CurrentUser = { id: "u1", email: "alice@example.test", display_name: "Alice", role: "admin" };
const bob: CurrentUser = { id: "u2", email: "bob@example.test", display_name: null, role: "user" };

function WhoAmI() {
  return <p>hello {useCurrentUser().id}</p>;
}

describe("AuthGate", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.logout.mockResolvedValue(undefined);
  });

  it("shows a spinner while loading, then the login screen for an anonymous visitor", async () => {
    let resolve: (u: CurrentUser | null) => void = () => {};
    mockApi.me.mockReturnValue(new Promise((r) => (resolve = r)));
    render(
      <AuthGate>
        <WhoAmI />
      </AuthGate>
    );
    expect(screen.getByRole("status")).toBeInTheDocument();
    resolve(null);
    expect(await screen.findByRole("link", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByText(/hello/)).not.toBeInTheDocument();
  });

  it("renders children with the user in context", async () => {
    mockApi.me.mockResolvedValue(bob);
    render(
      <AuthGate>
        <WhoAmI />
      </AuthGate>
    );
    expect(await screen.findByText("hello u2")).toBeInTheDocument();
  });

  it("shows an error when /auth/me fails", async () => {
    mockApi.me.mockRejectedValue(new Error("boom"));
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <AuthGate>
        <WhoAmI />
      </AuthGate>
    );
    expect(await screen.findByText(/Could not reach the server/)).toBeInTheDocument();
  });

  // 100vh is taller than what a phone browser shows, so h-screen made the page scroll.
  it("fills the visible height of the screen while loading and on an error, not 100vh", async () => {
    mockApi.me.mockRejectedValue(new Error("boom"));
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <AuthGate>
        <WhoAmI />
      </AuthGate>
    );
    const loading = screen.getByRole("status");
    expect(loading).toHaveClass("min-h-dvh");
    expect(loading).not.toHaveClass("h-screen");
    const error = await screen.findByText(/Could not reach the server/);
    expect(error).toHaveClass("min-h-dvh");
    expect(error).not.toHaveClass("h-screen");
  });
});

describe("App shell", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.logout.mockResolvedValue(undefined);
  });

  it("shows the Admin tab for admins", async () => {
    mockApi.me.mockResolvedValue(alice);
    render(<App />);
    expect(await screen.findByText("Alice")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Settings" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Admin" }));
    expect(screen.getByTestId("admin-page")).toBeInTheDocument();
  });

  it("hides the Admin tab for regular users and falls back to email", async () => {
    mockApi.me.mockResolvedValue(bob);
    render(<App />);
    expect(await screen.findByText("bob@example.test")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Settings" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Admin" })).not.toBeInTheDocument();
  });

  it("logs out from the header", async () => {
    mockApi.me.mockResolvedValue(bob);
    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "Logout" }));
    expect(mockApi.logout).toHaveBeenCalledTimes(1);
  });
});
