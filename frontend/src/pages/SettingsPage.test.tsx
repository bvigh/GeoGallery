import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { SettingsPage } from "@/pages/SettingsPage";
import type { ApiToken, CreatedApiToken } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    api: { listTokens: vi.fn(), createToken: vi.fn(), revokeToken: vi.fn() },
  };
});

const mockApi = api as unknown as {
  listTokens: ReturnType<typeof vi.fn>;
  createToken: ReturnType<typeof vi.fn>;
  revokeToken: ReturnType<typeof vi.fn>;
};

const future = new Date(Date.now() + 30 * 86400_000).toISOString();

const activeToken: ApiToken = {
  id: "t1",
  name: "cron",
  display_hint: "gg_pat_abc123",
  created_at: "2026-01-01T00:00:00Z",
  expires_at: future,
  last_used_at: null,
  revoked_at: null,
};

const revokedToken: ApiToken = { ...activeToken, id: "t2", name: "old", revoked_at: "2026-02-01T00:00:00Z" };

const created: CreatedApiToken = { ...activeToken, id: "t3", name: "laptop", token: "gg_pat_SECRETVALUE" };

async function openCreateDialog() {
  await userEvent.click(await screen.findByRole("button", { name: "Create token" }));
  return screen.findByRole("dialog");
}

describe("SettingsPage", () => {
  const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.listTokens.mockResolvedValue([activeToken, revokedToken]);
    mockApi.createToken.mockResolvedValue(created);
    mockApi.revokeToken.mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    if (originalClipboard) Object.defineProperty(navigator, "clipboard", originalClipboard);
    else delete (navigator as { clipboard?: unknown }).clipboard;
  });

  it("lists tokens with hint and status, revoke only for active ones", async () => {
    render(<SettingsPage />);
    const row = (await screen.findByText("cron")).closest("tr")!;
    expect(within(row).getByText("gg_pat_abc123…")).toBeInTheDocument();
    expect(within(row).getByText("active")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: "Revoke cron" })).toBeInTheDocument();
    const oldRow = screen.getByText("old").closest("tr")!;
    expect(within(oldRow).getByText("revoked")).toBeInTheDocument();
    expect(within(oldRow).queryByRole("button", { name: /Revoke/ })).not.toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    mockApi.listTokens.mockResolvedValue([]);
    render(<SettingsPage />);
    expect(await screen.findByText("No API tokens yet.")).toBeInTheDocument();
  });

  it("offers 30/90/365 day expiry with 90 as default and no 'never'", async () => {
    render(<SettingsPage />);
    const dialog = await openCreateDialog();
    const select = within(dialog).getByRole("combobox") as HTMLSelectElement;
    const values = Array.from(select.options).map((o) => o.value);
    expect(values).toEqual(["30", "90", "365"]);
    expect(select.value).toBe("90");
    expect(within(dialog).queryByText(/never/i)).not.toBeInTheDocument();
  });

  it("creates a token, shows it once, and clears it when the dialog closes", async () => {
    render(<SettingsPage />);
    let dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.selectOptions(within(dialog).getByRole("combobox"), "365");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));

    expect(mockApi.createToken).toHaveBeenCalledTimes(1);
    expect(mockApi.createToken).toHaveBeenCalledWith("laptop", 365);
    const tokenInput = await within(dialog).findByLabelText("New API token");
    expect(tokenInput).toHaveValue("gg_pat_SECRETVALUE");
    expect(tokenInput).toHaveAttribute("readonly");
    expect(tokenInput).toHaveAttribute("autocomplete", "off");
    expect(within(dialog).getByText(/You won't see this again/)).toBeInTheDocument();
    expect(within(dialog).getByText(/export GG_TOKEN=<the token above>/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Authorization: Bearer \$GG_TOKEN/)).not.toHaveTextContent("gg_pat_SECRETVALUE");
    await waitFor(() => expect(mockApi.listTokens).toHaveBeenCalledTimes(2));

    await userEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByDisplayValue("gg_pat_SECRETVALUE")).not.toBeInTheDocument();

    dialog = await openCreateDialog();
    expect(within(dialog).queryByLabelText("New API token")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("textbox")).toHaveValue("");
  });

  it("lets the one-time panel shrink so the long curl example cannot push Done out of the dialog", async () => {
    render(<SettingsPage />);
    const dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));
    const example = await within(dialog).findByText(/export GG_TOKEN=<the token above>/);
    expect(example).toHaveClass("overflow-x-auto");
    expect(within(dialog).getByTestId("one-time-token-panel")).toHaveClass("min-w-0");
  });

  it("copies with navigator.clipboard when available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    render(<SettingsPage />);
    const dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));
    await within(dialog).findByLabelText("New API token");
    await userEvent.click(within(dialog).getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith("gg_pat_SECRETVALUE");
    expect(await within(dialog).findByText("Copied")).toBeInTheDocument();
  });

  it("falls back to selecting the input when navigator.clipboard is undefined", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    render(<SettingsPage />);
    const dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));
    const input = (await within(dialog).findByLabelText("New API token")) as HTMLInputElement;
    await userEvent.click(within(dialog).getByRole("button", { name: "Copy" }));
    expect(await within(dialog).findByText("Press Ctrl/Cmd+C to copy")).toBeInTheDocument();
    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe("gg_pat_SECRETVALUE".length);
  });

  it("revokes only after confirmation, then refreshes", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    render(<SettingsPage />);
    const row = (await screen.findByText("cron")).closest("tr")!;

    await userEvent.click(within(row).getByRole("button", { name: "Revoke cron" }));
    expect(mockApi.revokeToken).not.toHaveBeenCalled();

    mockApi.listTokens.mockResolvedValue([{ ...activeToken, revoked_at: "2026-03-01T00:00:00Z" }]);
    await userEvent.click(within(row).getByRole("button", { name: "Revoke cron" }));
    expect(confirmSpy).toHaveBeenCalledTimes(2);
    expect(mockApi.revokeToken).toHaveBeenCalledWith("t1");
    await waitFor(() => expect(screen.queryByRole("button", { name: /Revoke/ })).not.toBeInTheDocument());
    expect(screen.getByText("revoked")).toBeInTheDocument();
  });

  it("keeps the revoke error visible after the list refreshes", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    mockApi.revokeToken.mockRejectedValue(new Error("boom"));
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<SettingsPage />);
    const row = (await screen.findByText("cron")).closest("tr")!;
    await userEvent.click(within(row).getByRole("button", { name: "Revoke cron" }));
    await waitFor(() => expect(mockApi.listTokens).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.getByText("Could not revoke token")).toBeInTheDocument();
  });

  it("clears a load error after a token is created", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.listTokens.mockRejectedValueOnce(new Error("boom"));
    render(<SettingsPage />);
    expect(await screen.findByText("Could not load tokens")).toBeInTheDocument();
    const dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));
    await within(dialog).findByLabelText("New API token");
    await waitFor(() => expect(screen.queryByText("Could not load tokens")).not.toBeInTheDocument());
  });

  it("shows a friendly message when the token cap is reached", async () => {
    mockApi.createToken.mockRejectedValue(new ApiError("POST /tokens failed: 409", 409));
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<SettingsPage />);
    const dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));
    expect(await within(dialog).findByText("Too many active tokens: revoke one first")).toBeInTheDocument();
  });

  it("drops a token that arrives after the dialog was closed", async () => {
    let resolve!: (t: CreatedApiToken) => void;
    mockApi.createToken.mockReturnValue(new Promise<CreatedApiToken>((r) => (resolve = r)));
    render(<SettingsPage />);
    let dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    resolve(created);
    await waitFor(() => expect(mockApi.listTokens).toHaveBeenCalledTimes(2));

    dialog = await openCreateDialog();
    expect(within(dialog).queryByLabelText("New API token")).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue("gg_pat_SECRETVALUE")).not.toBeInTheDocument();
  });

  it("does not close on Escape while the new token is shown", async () => {
    render(<SettingsPage />);
    const dialog = await openCreateDialog();
    await userEvent.type(within(dialog).getByRole("textbox"), "laptop");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create token" }));
    await within(dialog).findByLabelText("New API token");
    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByLabelText("New API token")).toHaveValue("gg_pat_SECRETVALUE");
  });

  it("folds the columns a phone has no room for into the name cell", async () => {
    render(<SettingsPage />);
    const row = (await screen.findByText("cron")).closest("tr")!;
    const [nameCell, hintCell, , , , statusCell, actionCell] = Array.from(row.cells);
    // Shown below sm only; the wide columns are hidden there.
    expect(within(nameCell).getByText(/^Token gg_pat_abc123… · active$/)).toBeInTheDocument();
    expect(within(nameCell).getByText(/^Created .* · Expires .* · Last used -$/)).toBeInTheDocument();
    expect(nameCell.querySelector("div")).toHaveClass("sm:hidden");
    for (const cell of [hintCell, statusCell]) expect(cell).toHaveClass("hidden", "sm:table-cell");
    expect(row.closest("table")!.tHead).toHaveClass("hidden", "sm:table-header-group");
    expect(within(actionCell).getByRole("button", { name: "Revoke cron" })).toHaveClass("touch:min-h-11");
  });
});
