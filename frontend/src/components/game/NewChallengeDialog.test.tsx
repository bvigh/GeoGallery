import type { ComponentProps } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { AuthContext } from "@/components/auth/AuthContext";
import { NewChallengeDialog } from "@/components/game/NewChallengeDialog";
import { emptyFilters } from "@/lib/filters";
import { BACK_DEPTH_KEY } from "@/lib/history";
import type { ChallengeSummary, CurrentUser, DirectoryUser } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ...actual, api: { listUsers: vi.fn(), createChallenge: vi.fn() } };
});

const mockApi = api as unknown as Record<"listUsers" | "createChallenge", ReturnType<typeof vi.fn>>;

const me: CurrentUser = { id: "u-me", email: "me@example.test", display_name: "Me Myself", role: "user" };

const directory: DirectoryUser[] = [
  { id: "u-me", display_name: "Me Myself", email: "me@example.test" },
  { id: "u-bob", display_name: "Bob", email: "bob@example.test" },
  { id: "u-carol", display_name: null, email: "carol@example.test" },
];

function manyUsers(n: number): DirectoryUser[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `u-${i}`,
    display_name: `Player ${String(i).padStart(2, "0")}`,
    email: null,
  }));
}

const created: ChallengeSummary = {
  id: "c-new",
  name: "Iceland",
  owner_id: "u-me",
  owner_name: "Me Myself",
  is_owner: true,
  created_at: "2026-11-16T10:00:00Z",
  member_count: 2,
  item_count: 5,
  played: 0,
  score: 0,
  finished: false,
};

type Props = ComponentProps<typeof NewChallengeDialog>;

function dialogElement(props: Props) {
  return (
    <AuthContext.Provider value={me}>
      <NewChallengeDialog {...props} />
    </AuthContext.Provider>
  );
}

function renderDialog(overrides: Partial<Props> = {}) {
  const props: Props = { open: true, onOpenChange: vi.fn(), filters: emptyFilters, onCreated: vi.fn(), ...overrides };
  const view = render(dialogElement(props));
  return { ...view, props };
}

// The user list replaces the loading line once listUsers settles.
async function loadedDialog() {
  const dialog = await screen.findByRole("dialog", { name: "New challenge" });
  await waitFor(() => expect(within(dialog).queryByText("Loading…")).not.toBeInTheDocument());
  return dialog;
}

// jsdom traverses history in setTimeout(0) steps; this lets every pending step land.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

const depth = () => ((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY] as number) ?? 0;

describe("NewChallengeDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.history.replaceState(null, "", "/game");
    mockApi.listUsers.mockResolvedValue(directory);
    mockApi.createChallenge.mockResolvedValue(created);
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(async () => {
    await settle();
    vi.restoreAllMocks();
  });

  it("offers every other user as a player, never the caller", async () => {
    renderDialog();
    const dialog = await loadedDialog();
    const players = within(within(dialog).getByRole("list", { name: "Other players" })).getAllByRole("listitem");
    expect(players.map((p) => p.textContent)).toEqual(["Bobbob@example.test", "carol@example.test"]);
    expect(within(dialog).getByText("You always play. Tick who else does.")).toBeInTheDocument();
    for (const box of within(dialog).getAllByRole("checkbox")) expect(box).not.toBeChecked();
  });

  it("says what the items are drawn from: everything, or the Game's filters", async () => {
    const { rerender, props } = renderDialog();
    const dialog = await loadedDialog();
    expect(dialog).toHaveTextContent("5 located items are drawn at random from what you can see and every player can already see.");
    expect(dialog).toHaveTextContent("No filters are set on the Game page.");
    expect(dialog).toHaveTextContent("A challenge shares nothing: an item a player cannot see yet is never drawn.");

    rerender(dialogElement({ ...props, filters: { ...emptyFilters, owner: "mine", albumIds: ["a1"] } }));
    expect(dialog).toHaveTextContent("from what the Game's filters select and every player can already see");
    expect(dialog).toHaveTextContent("Uses the Game page's filters (2 active).");
  });

  it("creates the challenge with the trimmed name, the ticked players and the Game's filters", async () => {
    const filters = { ...emptyFilters, owner: "mine" as const, albumIds: ["a1", "a2"], fromDate: "2024-05-01" };
    const { props } = renderDialog({ filters });
    const dialog = await loadedDialog();
    const create = within(dialog).getByRole("button", { name: "Create challenge" });
    expect(create).toBeDisabled();

    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "  Iceland ");
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));
    await userEvent.click(create);

    expect(mockApi.createChallenge).toHaveBeenCalledWith("Iceland", ["u-bob"], {
      from_date: "2024-05-01",
      album_id: ["a1", "a2"],
      owner: "mine",
    });
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith(created));
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
  });

  it("makes a challenge for the caller alone when nobody is ticked", async () => {
    const { props } = renderDialog();
    const dialog = await loadedDialog();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "Solo{Enter}");
    expect(mockApi.createChallenge).toHaveBeenCalledWith("Solo", [], {});
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled());
  });

  it("does not create a challenge without a name", async () => {
    renderDialog();
    const dialog = await loadedDialog();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "   {Enter}");
    expect(mockApi.createChallenge).not.toHaveBeenCalled();
  });

  it.each([
    {
      name: "too few items: the server's count",
      error: new ApiError("POST failed: 409", 409, "Only 3 located items match the filters and can be seen by every player. A challenge needs 5."),
      message: "Only 3 located items match the filters and can be seen by every player. A challenge needs 5.",
    },
    {
      name: "a refused body: the server's reason",
      error: new ApiError("POST failed: 400", 400, "a challenge has at most 20 players"),
      message: "a challenge has at most 20 players",
    },
    {
      name: "a player who is gone",
      error: new ApiError("POST failed: 404", 404),
      message: "One of the players is no longer available. Untick them and try again.",
    },
    { name: "a 409 without a reason", error: new ApiError("POST failed: 409", 409), message: "Could not create the challenge" },
    { name: "anything else", error: new ApiError("POST failed: 500", 500), message: "Could not create the challenge" },
  ])("says why it could not be made ($name) and stays open", async ({ error, message }) => {
    mockApi.createChallenge.mockRejectedValueOnce(error);
    const { props } = renderDialog();
    const dialog = await loadedDialog();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "Iceland");
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(message);
    expect(props.onCreated).not.toHaveBeenCalled();
    expect(props.onOpenChange).not.toHaveBeenCalled();
    // What was entered is kept, and the next try clears the message.
    expect(within(dialog).getByRole("textbox", { name: "Name" })).toHaveValue("Iceland");
    expect(within(dialog).getByRole("checkbox", { name: /Bob/ })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith(created));
  });

  it("is busy while creating, and creates once", async () => {
    let resolve!: (c: ChallengeSummary) => void;
    mockApi.createChallenge.mockReturnValue(new Promise((res) => (resolve = res)));
    const { props } = renderDialog();
    const dialog = await loadedDialog();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "Iceland{Enter}");
    const busy = within(dialog).getByRole("button", { name: "Creating…" });
    expect(busy).toBeDisabled();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "{Enter}");
    expect(mockApi.createChallenge).toHaveBeenCalledTimes(1);
    await act(async () => resolve(created));
    expect(props.onCreated).toHaveBeenCalledTimes(1);
  });

  it("still makes a challenge for the caller when the user list cannot be loaded", async () => {
    mockApi.listUsers.mockRejectedValue(new Error("offline"));
    const { props } = renderDialog();
    const dialog = await screen.findByRole("dialog", { name: "New challenge" });
    expect(
      await within(dialog).findByText("Could not load the user list. You can still make a challenge for yourself."),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole("checkbox")).toBeNull();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "Solo{Enter}");
    expect(mockApi.createChallenge).toHaveBeenCalledWith("Solo", [], {});
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled());
  });

  it("says so when there is nobody else to play with", async () => {
    mockApi.listUsers.mockResolvedValue([directory[0]]);
    renderDialog();
    const dialog = await loadedDialog();
    expect(within(dialog).getByText("There are no other users yet.")).toBeInTheDocument();
    expect(within(dialog).queryByRole("searchbox")).toBeNull();
  });

  it("has no search field for a short list", async () => {
    renderDialog();
    const dialog = await loadedDialog();
    expect(within(dialog).queryByRole("searchbox")).toBeNull();
  });

  it("searches a long list, and keeps ticked players listed so they can be unticked", async () => {
    mockApi.listUsers.mockResolvedValue([...manyUsers(9), { id: "u-zoe", display_name: "Zoë Ádám", email: "zoe@example.test" }]);
    renderDialog();
    const dialog = await loadedDialog();
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "Player 03" }));

    await userEvent.type(within(dialog).getByRole("searchbox", { name: "Search users" }), "zoe adam");
    const listed = () => within(dialog).getAllByRole("checkbox").map((box) => box.closest("label")!.textContent);
    expect(listed()).toEqual(["Player 03", "Zoë Ádámzoe@example.test"]);

    await userEvent.click(within(dialog).getByRole("checkbox", { name: "Player 03" }));
    expect(listed()).toEqual(["Zoë Ádámzoe@example.test"]);

    await userEvent.clear(within(dialog).getByRole("searchbox", { name: "Search users" }));
    await userEvent.type(within(dialog).getByRole("searchbox", { name: "Search users" }), "nobody");
    expect(within(dialog).getByText("No matching users.")).toBeInTheDocument();
  });

  it("stops at 19 other players", async () => {
    mockApi.listUsers.mockResolvedValue(manyUsers(21));
    renderDialog();
    const dialog = await loadedDialog();
    const boxes = within(dialog).getAllByRole("checkbox");
    for (const box of boxes.slice(0, 19)) await userEvent.click(box);

    expect(within(dialog).getByText("A challenge has at most 20 players.")).toBeInTheDocument();
    expect(boxes[19]).toBeDisabled();
    expect(boxes[20]).toBeDisabled();
    expect(boxes[0]).toBeEnabled();

    // Unticking one makes room again.
    await userEvent.click(boxes[0]);
    expect(boxes[19]).toBeEnabled();
    expect(within(dialog).queryByText("A challenge has at most 20 players.")).toBeNull();
  });

  it("starts empty on every opening, with the user list of that moment", async () => {
    const { rerender, props } = renderDialog();
    let dialog = await loadedDialog();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "Iceland");
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));

    rerender(dialogElement({ ...props, open: false }));
    expect(screen.queryByRole("dialog")).toBeNull();
    mockApi.listUsers.mockResolvedValue([...directory, { id: "u-dave", display_name: "Dave", email: null }]);
    rerender(dialogElement({ ...props, open: true }));

    dialog = await loadedDialog();
    expect(within(dialog).getByRole("textbox", { name: "Name" })).toHaveValue("");
    expect(within(dialog).getByRole("checkbox", { name: /Bob/ })).not.toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "Dave" })).toBeInTheDocument();
    expect(mockApi.listUsers).toHaveBeenCalledTimes(2);
  });

  it("loads nothing and holds no history entry while closed", async () => {
    renderDialog({ open: false });
    await settle();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(mockApi.listUsers).not.toHaveBeenCalled();
    expect(depth()).toBe(0);
  });

  it("closes on the back gesture", async () => {
    const { props } = renderDialog();
    await loadedDialog();
    await settle();
    expect(depth()).toBe(1);

    await act(async () => {
      const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
      window.history.back();
      await popped;
    });
    await settle();
    expect(props.onOpenChange).toHaveBeenCalledWith(false);
    expect(props.onCreated).not.toHaveBeenCalled();
  });

  it("gives every player's row a 44 px target on touch", async () => {
    renderDialog();
    const dialog = await loadedDialog();
    for (const box of within(dialog).getAllByRole("checkbox")) {
      expect(box.closest("label")!.className.split(" ")).toContain("touch:min-h-11");
    }
  });
});
