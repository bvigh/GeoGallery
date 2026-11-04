import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api } from "@/api/client";
import App from "@/App";

vi.mock("@/api/client", () => ({
  api: { me: vi.fn(), logout: vi.fn() },
}));
// Pages that throw while rendering, as a Leaflet or three.js error in a fullscreen view would.
const crash = vi.hoisted(() => ({ map: false, game: false }));
vi.mock("@/pages/MapPage", () => ({
  MapPage: ({ active }: { active: boolean }) => {
    if (crash.map) throw new Error("map crashed");
    return <div data-testid="map-page" data-active={String(active)} />;
  },
}));
vi.mock("@/pages/TripsPage", () => ({ TripsPage: () => null }));
vi.mock("@/pages/AlbumsPage", () => ({ AlbumsPage: () => <div data-testid="albums-page" /> }));
vi.mock("@/pages/GamePage", () => ({
  GamePage: () => {
    if (crash.game) throw new Error("game crashed");
    return <div data-testid="game-page" />;
  },
}));
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
    expect(await screen.findByRole("alert")).toHaveTextContent("Logout failed, try again");
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

  // Below lg the labels are visually hidden, so each tab keeps its label as its name and tooltip.
  it("keeps every tab reachable by name, with a tooltip and a decorative icon", async () => {
    render(<App />);
    const nav = await screen.findByRole("navigation");
    for (const name of ["Map", "Trips", "Albums", "Game", "Upload", "Settings"]) {
      const tab = within(nav).getByRole("button", { name });
      expect(tab).toHaveAttribute("title", name);
      expect(within(tab).getByText(name)).toHaveClass("sr-only", "lg:not-sr-only");
      expect(tab.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    }
    expect(within(nav).queryByRole("button", { name: "Admin" })).not.toBeInTheDocument();
  });

  it("shows the Admin tab to admins", async () => {
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: "Alice", role: "admin" });
    render(<App />);
    const nav = await screen.findByRole("navigation");
    expect(within(nav).getByRole("button", { name: "Admin" })).toHaveAttribute("title", "Admin");
  });

  it("marks only the active tab as the current page", async () => {
    render(<App />);
    const nav = await screen.findByRole("navigation");
    const current = () =>
      within(nav)
        .getAllByRole("button")
        .filter((b) => b.getAttribute("aria-current") === "page")
        .map((b) => b.textContent);
    expect(current()).toEqual(["Map"]);

    await userEvent.click(within(nav).getByRole("button", { name: "Game" }));
    expect(current()).toEqual(["Game"]);
    expect(within(nav).getByRole("button", { name: "Map" })).not.toHaveAttribute("aria-current");
  });
});

describe("App header", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: "Alice", role: "user" });
  });

  it("keeps the Logout name and tooltip when the button is icon-only", async () => {
    render(<App />);
    const logout = await screen.findByRole("button", { name: "Logout" });
    expect(logout).toHaveAttribute("title", "Logout");
    expect(within(logout).getByText("Logout")).toHaveClass("sr-only", "lg:not-sr-only");
  });

  it("shows the user's name only from lg up, falling back to the email", async () => {
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: null, role: "user" });
    render(<App />);
    expect(await screen.findByText("a@b.c")).toHaveClass("hidden", "lg:inline");
  });

  // A long name or email once squeezed the scrollbar-less tab strip on desktop, clipping the last
  // tabs. From lg the nav keeps its width and the name truncates, with the full name as tooltip.
  it("truncates a long name instead of squeezing the tabs from lg up", async () => {
    const long = "alexandra.konstantinopoulou@long-company-domain.example";
    mockApi.me.mockResolvedValue({ id: "u1", email: long, display_name: null, role: "admin" });
    render(<App />);
    const nav = await screen.findByRole("navigation");
    expect(nav).toHaveClass("lg:shrink-0");
    const name = screen.getByText(long);
    expect(name).toHaveClass("min-w-0", "truncate");
    expect(name).toHaveAttribute("title", long);
    expect(name.parentElement).toHaveClass("ml-auto", "min-w-0");
  });

  // Seven 44px tabs plus Logout need ~384px; at 40px an admin's tabs fit a 360px phone.
  it("narrows the touch tabs below sm only, and never Logout", async () => {
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: "Alice", role: "admin" });
    render(<App />);
    const nav = await screen.findByRole("navigation");
    for (const tab of within(nav).getAllByRole("button")) {
      expect(tab).toHaveClass("touch:min-h-11", "touch:min-w-10", "sm:touch:min-w-11");
      expect(tab).not.toHaveClass("touch:min-w-11");
    }
    const logout = screen.getByRole("button", { name: "Logout" });
    expect(logout).toHaveClass("touch:min-h-11", "touch:min-w-11");
    expect(logout).not.toHaveClass("touch:min-w-10");
  });

  // h-screen is taller than a phone's visible area, so the page would scroll the header away.
  // h-dvh alone would leave no height (and a blank map) where dvh is unsupported; #root sets
  // 100% then 100dvh, so the shell takes h-full from it.
  it("sizes the shell from #root and lets the tab strip scroll on its own", async () => {
    render(<App />);
    const nav = await screen.findByRole("navigation");
    const shell = nav.closest("header")!.parentElement!;
    expect(shell).toHaveClass("h-full", "overflow-clip-safe");
    expect(shell).not.toHaveClass("h-screen", "h-dvh", "overflow-clip");
    expect(nav).toHaveClass("min-w-0", "overflow-x-auto");
  });

  // overflow-hidden still scrolls on focus() / scrollIntoView(), which slid the map page up.
  // A clipped flex item needs min-h-0, or it grows to its content and the pages stop scrolling.
  it("clips <main> so script cannot scroll it, while it still shrinks to the shell", async () => {
    render(<App />);
    const main = (await screen.findByRole("main")) as HTMLElement;
    expect(main).toHaveClass("min-h-0", "flex-1", "overflow-clip-safe");
    expect(main).not.toHaveClass("overflow-hidden", "overflow-clip");
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

describe("App error boundaries", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.me.mockResolvedValue({ id: "u1", email: "a@b.c", display_name: "Alice", role: "user" });
    // React and the boundary both log the error, and jsdom reports React's development rethrow
    // as an uncaught window error unless it is cancelled.
    vi.spyOn(console, "error").mockImplementation(() => {});
    window.addEventListener("error", cancel);
  });

  afterEach(() => {
    crash.map = false;
    crash.game = false;
    window.removeEventListener("error", cancel);
    vi.restoreAllMocks();
  });

  function cancel(e: ErrorEvent) {
    e.preventDefault();
  }

  it("shows a fallback for a page that throws, keeping the header and the other tabs", async () => {
    crash.game = true;
    render(<App />);
    const nav = await screen.findByRole("navigation");
    await userEvent.click(within(nav).getByRole("button", { name: "Game" }));

    expect(screen.getByRole("alert")).toHaveTextContent("Something went wrong");
    expect(screen.getByRole("main")).toContainElement(screen.getByRole("alert"));
    expect(screen.getByRole("button", { name: "Logout" })).toBeInTheDocument();

    await userEvent.click(within(nav).getByRole("button", { name: "Albums" }));
    expect(screen.getByTestId("albums-page")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    // A fresh boundary when the tab is opened again.
    crash.game = false;
    await userEvent.click(within(nav).getByRole("button", { name: "Game" }));
    expect(screen.getByTestId("game-page")).toBeInTheDocument();
  });

  it("mounts the page again on Try again", async () => {
    crash.game = true;
    render(<App />);
    const nav = await screen.findByRole("navigation");
    await userEvent.click(within(nav).getByRole("button", { name: "Game" }));
    expect(screen.getByRole("alert")).toBeInTheDocument();

    crash.game = false;
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.getByTestId("game-page")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the other tabs working when the map page throws", async () => {
    crash.map = true;
    render(<App />);
    const nav = await screen.findByRole("navigation");
    expect(screen.getByRole("alert")).toHaveTextContent("Something went wrong");

    await userEvent.click(within(nav).getByRole("button", { name: "Albums" }));
    expect(screen.getByTestId("albums-page")).toBeInTheDocument();
    // The map's fallback stays in the hidden map container.
    expect(screen.getByRole("alert", { hidden: true }).parentElement).toHaveClass("hidden");
  });
});
