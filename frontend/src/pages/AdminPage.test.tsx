import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api } from "@/api/client";
import { AdminPage } from "@/pages/AdminPage";
import { AuthContext } from "@/components/auth/AuthContext";
import type { AdminUser, CurrentUser } from "@/types";

vi.mock("@/api/client", () => ({
  api: { adminListUsers: vi.fn(), adminSetDisabled: vi.fn(), adminCreateInvite: vi.fn() },
}));

const mockApi = api as unknown as {
  adminListUsers: ReturnType<typeof vi.fn>;
  adminSetDisabled: ReturnType<typeof vi.fn>;
  adminCreateInvite: ReturnType<typeof vi.fn>;
};

const me: CurrentUser = { id: "u1", email: "alice@example.test", display_name: "Alice", role: "admin" };

const users: AdminUser[] = [
  { ...me, disabled: false, last_login_at: "2026-10-01T10:00:00Z", created_at: "2026-01-01T00:00:00Z" },
  {
    id: "u2",
    email: "bob@example.test",
    display_name: "Bob",
    role: "user",
    disabled: false,
    last_login_at: null,
    created_at: "2026-01-02T00:00:00Z",
  },
  {
    id: "u3",
    email: null,
    display_name: "Carol",
    role: "user",
    disabled: true,
    last_login_at: null,
    created_at: "2026-01-03T00:00:00Z",
  },
];

function renderPage() {
  return render(
    <AuthContext.Provider value={me}>
      <AdminPage />
    </AuthContext.Provider>
  );
}

describe("AdminPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.adminListUsers.mockResolvedValue(users);
    mockApi.adminSetDisabled.mockResolvedValue(undefined);
    mockApi.adminCreateInvite.mockResolvedValue("http://localhost:5173/api/auth/login?invite=tok");
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("lists users with email, role and status, without a self-disable button", async () => {
    renderPage();
    const bobRow = (await screen.findByText("bob@example.test")).closest("tr")!;
    expect(within(bobRow).getByText("user")).toBeInTheDocument();
    expect(within(bobRow).getByText("active")).toBeInTheDocument();
    expect(within(bobRow).getByRole("button", { name: "Disable bob@example.test" })).toBeInTheDocument();

    const carolRow = screen.getByText("Carol").closest("tr")!;
    expect(within(carolRow).getByText("disabled")).toBeInTheDocument();
    expect(within(carolRow).getByRole("button", { name: "Enable Carol" })).toBeInTheDocument();

    const aliceRow = screen.getByText("alice@example.test").closest("tr")!;
    expect(within(aliceRow).queryByRole("button")).not.toBeInTheDocument();
  });

  it("toggles disabled after confirming, and refreshes the list", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    renderPage();
    const bobRow = (await screen.findByText("bob@example.test")).closest("tr")!;
    await userEvent.click(within(bobRow).getByRole("button", { name: "Disable bob@example.test" }));
    expect(mockApi.adminSetDisabled).not.toHaveBeenCalled();

    mockApi.adminListUsers.mockResolvedValue(users.map((u) => (u.id === "u2" ? { ...u, disabled: true } : u)));
    await userEvent.click(within(bobRow).getByRole("button", { name: "Disable bob@example.test" }));
    expect(mockApi.adminSetDisabled).toHaveBeenCalledWith("u2", true);
    await waitFor(() => {
      const row = screen.getByText("bob@example.test").closest("tr")!;
      expect(within(row).getByRole("button", { name: "Enable bob@example.test" })).toBeInTheDocument();
    });

    const carolRow = screen.getByText("Carol").closest("tr")!;
    await userEvent.click(within(carolRow).getByRole("button", { name: "Enable Carol" }));
    expect(mockApi.adminSetDisabled).toHaveBeenCalledWith("u3", false);
    expect(confirmSpy).toHaveBeenCalledTimes(2);
  });

  it("keeps the error visible when disabling fails", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.adminSetDisabled.mockRejectedValue(new Error("boom"));
    renderPage();
    const bobRow = (await screen.findByText("bob@example.test")).closest("tr")!;
    await userEvent.click(within(bobRow).getByRole("button", { name: "Disable bob@example.test" }));
    await waitFor(() => expect(mockApi.adminListUsers).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.getByText("Could not disable user")).toBeInTheDocument();
  });

  it("creates an invite for the chosen role and shows a copyable link", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    renderPage();
    await userEvent.selectOptions(await screen.findByLabelText("Invite role"), "admin");
    await userEvent.click(screen.getByRole("button", { name: "Create invite" }));
    expect(mockApi.adminCreateInvite).toHaveBeenCalledWith("admin");
    const link = await screen.findByLabelText("Invite link");
    expect(link).toHaveValue("http://localhost:5173/api/auth/login?invite=tok");
    await userEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(await screen.findByText("Press Ctrl/Cmd+C to copy")).toBeInTheDocument();
  });

  it("folds the columns a phone has no room for into the name cell", async () => {
    renderPage();
    const row = (await screen.findByText("bob@example.test")).closest("tr")!;
    const [nameCell, emailCell, roleCell, loginCell, statusCell, actionCell] = Array.from(row.cells);
    // Shown below sm only; the wide columns are hidden there.
    expect(within(nameCell).getByText("bob@example.test · user · active")).toBeInTheDocument();
    expect(within(nameCell).getByText("Last login -")).toBeInTheDocument();
    expect(nameCell.querySelector("div")).toHaveClass("sm:hidden");
    for (const cell of [emailCell, roleCell, loginCell, statusCell]) expect(cell).toHaveClass("hidden", "sm:table-cell");
    expect(emailCell).toHaveClass("[overflow-wrap:anywhere]");
    expect(row.closest("table")!.tHead).toHaveClass("hidden", "sm:table-header-group");
    expect(within(actionCell).getByRole("button", { name: "Disable bob@example.test" })).toHaveClass("touch:min-h-11");

    const carolRow = screen.getByText("Carol").closest("tr")!;
    expect(within(carolRow.cells[0]).getByText("No email · user · disabled")).toBeInTheDocument();
  });
});
