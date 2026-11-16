import type { ComponentProps } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { AuthContext } from "@/components/auth/AuthContext";
import { NewChallengeDialog } from "@/components/game/NewChallengeDialog";
import { randomChallengeName } from "@/lib/challenge";
import { emptyFilters } from "@/lib/filters";
import { BACK_DEPTH_KEY } from "@/lib/history";
import type { Album, ChallengeSummary, CurrentUser, DirectoryUser, Trip } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ...actual, api: { listUsers: vi.fn(), listAlbums: vi.fn(), createChallenge: vi.fn() } };
});

// The name a challenge falls back on is random: the tests choose it.
vi.mock("@/lib/challenge", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/challenge")>();
  return { ...actual, randomChallengeName: vi.fn() };
});

const mockApi = api as unknown as Record<"listUsers" | "listAlbums" | "createChallenge", ReturnType<typeof vi.fn>>;
const mockName = vi.mocked(randomChallengeName);

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

const albums: Album[] = [
  {
    id: "a1",
    name: "Family",
    owner_id: "u-me",
    owner_name: "Me Myself",
    is_owner: true,
    member_count: 3,
    media_count: 40,
    start_date: null,
    end_date: null,
    created_at: "2026-01-01T00:00:00Z",
    last_added_at: null,
  },
  {
    id: "a2",
    name: "Iceland 2024",
    owner_id: "u-dana",
    owner_name: "Dana",
    is_owner: false,
    member_count: 2,
    media_count: 12,
    start_date: null,
    end_date: null,
    created_at: "2026-02-01T00:00:00Z",
    last_added_at: null,
  },
];

const trips: Trip[] = [
  { id: "t1", name: "Tatra", start_date: "2024-07-01", end_date: "2024-07-09", media_count: 9, cover_key: null, user_edited: true },
];

type Props = ComponentProps<typeof NewChallengeDialog>;

function dialogElement(props: Props) {
  return (
    <AuthContext.Provider value={me}>
      <NewChallengeDialog {...props} />
    </AuthContext.Provider>
  );
}

function renderDialog(overrides: Partial<Props> = {}) {
  const props: Props = {
    open: true,
    onOpenChange: vi.fn(),
    filters: emptyFilters,
    trips: [],
    albums: [],
    onCreated: vi.fn(),
    ...overrides,
  };
  const view = render(dialogElement(props));
  return { ...view, props };
}

// The user list replaces the loading line once listUsers settles.
async function loadedDialog() {
  const dialog = await screen.findByRole("dialog", { name: "New challenge" });
  await waitFor(() => expect(within(dialog).queryByText("Loading…")).not.toBeInTheDocument());
  return dialog;
}

const nameField = (dialog: HTMLElement) => within(dialog).getByRole("textbox", { name: "Name (optional)" });
// The players' checkboxes: the filters under them have checkboxes of their own.
const playerBoxes = (dialog: HTMLElement) =>
  within(within(dialog).getByRole("list", { name: "Other players" })).getAllByRole("checkbox");

// jsdom traverses history in setTimeout(0) steps; this lets every pending step land.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

const depth = () => ((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY] as number) ?? 0;

// Which of the albums each other player is in. GET /api/albums?member_id=… answers with the
// caller's albums every named player is in.
function playersIn(albumsOf: Record<string, string[]>) {
  mockApi.listAlbums.mockImplementation(({ memberIds }: { memberIds: string[] }) =>
    Promise.resolve(albums.filter((a) => memberIds.every((id) => albumsOf[id]?.includes(a.id)))),
  );
}

// The albums the dialog offers, as their checkboxes are labelled: someone else's says whose it is.
function offeredAlbums(dialog: HTMLElement): string[] {
  const items = within(dialog).getByRole("group", { name: "Items" });
  const group = within(items).queryByRole("group", { name: "Albums" });
  return group ? within(group).getAllByRole("checkbox").map((box) => box.closest("label")!.textContent!) : [];
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("NewChallengeDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.history.replaceState(null, "", "/game");
    mockApi.listUsers.mockResolvedValue(directory);
    // Unless a test says otherwise, every player is in every album.
    mockApi.listAlbums.mockResolvedValue(albums);
    mockApi.createChallenge.mockResolvedValue(created);
    mockName.mockReturnValue("Cheerful Cheetah");
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
    for (const box of playerBoxes(dialog)) expect(box).not.toBeChecked();
  });

  it("says what a challenge is and how its items are drawn", async () => {
    renderDialog();
    const dialog = await loadedDialog();
    expect(dialog).toHaveTextContent("5 located items, the same for every player. Each player guesses each of them once.");
    const items = within(dialog).getByRole("group", { name: "Items" });
    expect(items).toHaveTextContent(
      "Drawn from what these filters select, and only from items every player can already see: a challenge shares nothing.",
    );
    expect(items).toHaveTextContent(
      "The rounds come from different places and times where the items allow, and from items the players have not had in a challenge first.",
    );
    expect(items).toHaveTextContent("Only the albums every player is in are listed.");
  });

  it("asks for the players first, then the filters, then the name", async () => {
    renderDialog({ albums, trips });
    const dialog = await loadedDialog();
    const players = within(dialog).getByRole("group", { name: "Players" });
    const items = within(dialog).getByRole("group", { name: "Items" });
    const name = nameField(dialog);
    expect(players.compareDocumentPosition(items) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(items.compareDocumentPosition(name) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // The regular filters, all of them.
    expect(within(items).getByRole("radio", { name: "Shared with me" })).toBeInTheDocument();
    expect(within(items).getByLabelText("From date")).toBeInTheDocument();
    expect(within(items).getByLabelText("To date")).toBeInTheDocument();
    expect(within(items).getByRole("checkbox", { name: /Family/ })).toBeInTheDocument();
    expect(within(items).getByRole("checkbox", { name: /Iceland 2024/ })).toBeInTheDocument();
    expect(within(items).getByRole("checkbox", { name: "Tatra" })).toBeInTheDocument();
    expect(within(items).getByRole("checkbox", { name: "video_360" })).toBeInTheDocument();
  });

  it("draws from the albums and the dates chosen in the dialog", async () => {
    const { props } = renderDialog({ albums, trips });
    const dialog = await loadedDialog();
    const items = within(dialog).getByRole("group", { name: "Items" });
    expect(within(items).queryByRole("button", { name: "Reset filters" })).toBeNull();

    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));
    await userEvent.click(within(items).getByRole("checkbox", { name: /Family/ }));
    await userEvent.click(within(items).getByRole("checkbox", { name: /Iceland 2024/ }));
    fireEvent.change(within(items).getByLabelText("From date"), { target: { value: "2024-05-01" } });
    fireEvent.change(within(items).getByLabelText("To date"), { target: { value: "2024-09-30" } });
    await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));

    expect(mockApi.createChallenge).toHaveBeenCalledWith("Cheerful Cheetah", ["u-bob"], {
      from_date: "2024-05-01",
      to_date: "2024-09-30",
      album_id: ["a1", "a2"],
    });
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith(created));
  });

  it("starts from the Game page's filters, which the dialog's own then replace", async () => {
    const filters = { ...emptyFilters, owner: "mine" as const, albumIds: ["a1"], fromDate: "2024-05-01" };
    renderDialog({ filters, albums, trips });
    const dialog = await loadedDialog();
    const items = within(dialog).getByRole("group", { name: "Items" });
    expect(within(items).getByRole("radio", { name: "Mine" })).toBeChecked();
    expect(within(items).getByRole("checkbox", { name: /Family/ })).toBeChecked();
    expect(within(items).getByLabelText("From date")).toHaveValue("2024-05-01");

    // Reset is a plain button: it clears the filters and creates nothing.
    await userEvent.click(within(items).getByRole("button", { name: "Reset filters" }));
    expect(mockApi.createChallenge).not.toHaveBeenCalled();
    expect(within(items).getByRole("radio", { name: "All" })).toBeChecked();
    expect(within(items).getByRole("checkbox", { name: /Family/ })).not.toBeChecked();

    await userEvent.click(within(items).getByRole("checkbox", { name: "Tatra" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));
    expect(mockApi.createChallenge).toHaveBeenCalledWith("Cheerful Cheetah", [], { trip_id: ["t1"] });
  });

  it("creates only from the name field and the button: Enter among the players and the filters does nothing", async () => {
    renderDialog({ albums });
    const dialog = await loadedDialog();
    const items = within(dialog).getByRole("group", { name: "Items" });
    // A browser submits the form on Enter in an input unless the keydown is cancelled; jsdom
    // submits nothing, so the cancelling is what there is to see.
    const cancelled = (el: HTMLElement) => !fireEvent.keyDown(el, { key: "Enter" });
    expect(cancelled(within(dialog).getByRole("checkbox", { name: /Bob/ }))).toBe(true);
    expect(cancelled(within(items).getByRole("checkbox", { name: /Family/ }))).toBe(true);
    expect(cancelled(within(items).getByRole("radio", { name: "Mine" }))).toBe(true);
    expect(cancelled(within(items).getByLabelText("From date"))).toBe(true);
    expect(cancelled(nameField(dialog))).toBe(false);
    expect(cancelled(within(dialog).getByRole("button", { name: "Create challenge" }))).toBe(false);
  });

  it("creates the challenge with the trimmed name, the ticked players and the Game's filters", async () => {
    const filters = { ...emptyFilters, owner: "mine" as const, albumIds: ["a1", "a2"], fromDate: "2024-05-01" };
    const { props } = renderDialog({ filters });
    const dialog = await loadedDialog();
    const create = within(dialog).getByRole("button", { name: "Create challenge" });

    await userEvent.type(nameField(dialog), "  Iceland ");
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
    await userEvent.type(nameField(dialog), "Solo{Enter}");
    expect(mockApi.createChallenge).toHaveBeenCalledWith("Solo", [], {});
    await waitFor(() => expect(props.onCreated).toHaveBeenCalled());
  });

  it("calls a challenge without a name by the name it shows", async () => {
    const { props } = renderDialog();
    const dialog = await loadedDialog();
    const create = within(dialog).getByRole("button", { name: "Create challenge" });
    expect(create).toBeEnabled();
    expect(nameField(dialog)).toHaveAttribute("placeholder", "Cheerful Cheetah");
    expect(nameField(dialog)).toHaveAccessibleDescription("Left empty, the challenge is called “Cheerful Cheetah”.");

    await userEvent.click(create);
    expect(mockApi.createChallenge).toHaveBeenCalledWith("Cheerful Cheetah", [], {});
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith(created));
  });

  it("takes a name of spaces as no name", async () => {
    renderDialog();
    const dialog = await loadedDialog();
    await userEvent.type(nameField(dialog), "   {Enter}");
    expect(mockApi.createChallenge).toHaveBeenCalledWith("Cheerful Cheetah", [], {});
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
    await userEvent.type(nameField(dialog), "Iceland");
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(message);
    expect(props.onCreated).not.toHaveBeenCalled();
    expect(props.onOpenChange).not.toHaveBeenCalled();
    // What was entered is kept, and the next try clears the message.
    expect(nameField(dialog)).toHaveValue("Iceland");
    expect(within(dialog).getByRole("checkbox", { name: /Bob/ })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith(created));
  });

  it("is busy while creating, and creates once", async () => {
    let resolve!: (c: ChallengeSummary) => void;
    mockApi.createChallenge.mockReturnValue(new Promise((res) => (resolve = res)));
    const { props } = renderDialog();
    const dialog = await loadedDialog();
    await userEvent.type(nameField(dialog), "Iceland{Enter}");
    const busy = within(dialog).getByRole("button", { name: "Creating…" });
    expect(busy).toBeDisabled();
    await userEvent.type(nameField(dialog), "{Enter}");
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
    expect(within(dialog).queryByRole("list", { name: "Other players" })).toBeNull();
    await userEvent.type(nameField(dialog), "Solo{Enter}");
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
    const listed = () => playerBoxes(dialog).map((box) => box.closest("label")!.textContent);
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
    const boxes = playerBoxes(dialog);
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

  it("starts again on every opening: the page's filters, a new name to fall back on, the user list of that moment", async () => {
    const { rerender, props } = renderDialog({ albums });
    let dialog = await loadedDialog();
    await userEvent.type(nameField(dialog), "Iceland");
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Family/ }));

    rerender(dialogElement({ ...props, open: false }));
    expect(screen.queryByRole("dialog")).toBeNull();
    mockApi.listUsers.mockResolvedValue([...directory, { id: "u-dave", display_name: "Dave", email: null }]);
    mockName.mockReturnValue("Witty Wombat");
    // The page's filters changed meanwhile: the next opening starts from them.
    rerender(dialogElement({ ...props, open: true, filters: { ...emptyFilters, albumIds: ["a2"] } }));

    dialog = await loadedDialog();
    expect(nameField(dialog)).toHaveValue("");
    expect(nameField(dialog)).toHaveAttribute("placeholder", "Witty Wombat");
    expect(within(dialog).getByRole("checkbox", { name: /Bob/ })).not.toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: /Family/ })).not.toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: /Iceland 2024/ })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "Dave" })).toBeInTheDocument();
    expect(mockApi.listUsers).toHaveBeenCalledTimes(2);
  });

  it("keeps its own filters and its name while the page behind it renders again", async () => {
    const { rerender, props } = renderDialog({ albums });
    const dialog = await loadedDialog();
    await userEvent.click(within(dialog).getByRole("checkbox", { name: /Family/ }));
    mockName.mockReturnValue("Witty Wombat");

    rerender(dialogElement({ ...props, filters: { ...emptyFilters } }));
    expect(within(dialog).getByRole("checkbox", { name: /Family/ })).toBeChecked();
    expect(nameField(dialog)).toHaveAttribute("placeholder", "Cheerful Cheetah");
    expect(mockApi.listUsers).toHaveBeenCalledTimes(1);
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

  it("gives every checkbox row and the button a 44 px target on touch", async () => {
    renderDialog({ albums, trips });
    const dialog = await loadedDialog();
    for (const box of within(dialog).getAllByRole("checkbox")) {
      expect(box.closest("label")!.className.split(" ")).toContain("touch:min-h-11");
    }
    expect(within(dialog).getByRole("button", { name: "Create challenge" }).className.split(" ")).toContain("touch:min-h-11");
  });

  describe("albums", () => {
    const both = ["Family", "Iceland 2024 by Dana"];

    it("offers only the albums every ticked player is in, and all of them again without other players", async () => {
      playersIn({ "u-bob": ["a1", "a2"], "u-carol": ["a1"] });
      renderDialog({ albums, trips });
      const dialog = await loadedDialog();
      const tick = (name: RegExp) => userEvent.click(within(dialog).getByRole("checkbox", { name }));
      expect(offeredAlbums(dialog)).toEqual(both);

      await tick(/carol@example\.test/);
      await waitFor(() => expect(offeredAlbums(dialog)).toEqual(["Family"]));
      await tick(/Bob/);
      expect(mockApi.listAlbums).toHaveBeenLastCalledWith({ memberIds: ["u-carol", "u-bob"], signal: expect.any(AbortSignal) });
      await waitFor(() => expect(mockApi.listAlbums).toHaveBeenCalledTimes(2));
      expect(offeredAlbums(dialog)).toEqual(["Family"]);

      // Carol unticked: Bob's albums. Bob unticked too: every album, without asking.
      await tick(/carol@example\.test/);
      expect(mockApi.listAlbums).toHaveBeenLastCalledWith({ memberIds: ["u-bob"], signal: expect.any(AbortSignal) });
      await waitFor(() => expect(offeredAlbums(dialog)).toEqual(both));
      await tick(/Bob/);
      expect(offeredAlbums(dialog)).toEqual(both);
      expect(mockApi.listAlbums).toHaveBeenCalledTimes(3);
      // The trips are the caller's own and stay.
      expect(within(dialog).getByRole("checkbox", { name: "Tatra" })).toBeInTheDocument();
      expect(within(dialog).queryByRole("status")).toBeNull();
    });

    it("unticks an album a ticked player is not in, says so, and does not tick it again", async () => {
      playersIn({ "u-bob": ["a1"] });
      renderDialog({ albums });
      const dialog = await loadedDialog();
      const items = within(dialog).getByRole("group", { name: "Items" });
      const bob = within(dialog).getByRole("checkbox", { name: /Bob/ });
      await userEvent.click(within(items).getByRole("checkbox", { name: /Iceland 2024/ }));
      await userEvent.click(within(items).getByRole("checkbox", { name: /Family/ }));

      await userEvent.click(bob);
      expect(await within(dialog).findByRole("status")).toHaveTextContent(
        /^Unticked “Iceland 2024”: not every player is in it\.$/,
      );
      expect(offeredAlbums(dialog)).toEqual(["Family"]);
      expect(within(items).getByRole("checkbox", { name: /Family/ })).toBeChecked();

      // Back on the list without Bob, unticked, and the note goes with it.
      await userEvent.click(bob);
      expect(offeredAlbums(dialog)).toEqual(both);
      expect(within(items).getByRole("checkbox", { name: /Iceland 2024/ })).not.toBeChecked();
      expect(within(dialog).queryByRole("status")).toBeNull();

      // Bob again: nothing ticked leaves, so nothing is said.
      await userEvent.click(bob);
      await waitFor(() => expect(offeredAlbums(dialog)).toEqual(["Family"]));
      expect(within(dialog).queryByRole("status")).toBeNull();

      await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));
      expect(mockApi.createChallenge).toHaveBeenCalledWith("Cheerful Cheetah", ["u-bob"], { album_id: ["a1"] });
    });

    it("unticks albums the Game page's filters ticked too, and says when no album is left", async () => {
      playersIn({ "u-bob": [] });
      const filters = { ...emptyFilters, albumIds: ["a1", "a2"], fromDate: "2024-05-01" };
      renderDialog({ albums, filters });
      const dialog = await loadedDialog();
      await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));

      const notes = await within(dialog).findAllByRole("status");
      expect(notes.map((n) => n.textContent)).toEqual([
        "Unticked “Family” and “Iceland 2024”: not every player is in them.",
        "None of your albums has every player in it.",
      ]);
      expect(offeredAlbums(dialog)).toEqual([]);
      await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));
      // The other filters stay as they were.
      expect(mockApi.createChallenge).toHaveBeenCalledWith("Cheerful Cheetah", ["u-bob"], { from_date: "2024-05-01" });
    });

    it("keeps every album, and the ticks, when the lookup fails", async () => {
      mockApi.listAlbums.mockRejectedValue(new ApiError("GET /albums failed: 500", 500));
      renderDialog({ albums, filters: { ...emptyFilters, albumIds: ["a2"] } });
      const dialog = await loadedDialog();
      await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));

      expect(await within(dialog).findByRole("status")).toHaveTextContent(
        "Could not check which albums every player is in, so all of yours are listed.",
      );
      expect(offeredAlbums(dialog)).toEqual(both);
      expect(within(dialog).getByRole("checkbox", { name: /Iceland 2024/ })).toBeChecked();
      await userEvent.click(within(dialog).getByRole("button", { name: "Create challenge" }));
      expect(mockApi.createChallenge).toHaveBeenCalledWith("Cheerful Cheetah", ["u-bob"], { album_id: ["a2"] });

      // Without other players there is nothing to look up, so nothing failed.
      await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));
      expect(within(dialog).queryByRole("status")).toBeNull();
    });

    it("counts only the answer for the players ticked now, and keeps the albums until it comes", async () => {
      const forBob = deferred<Album[]>();
      const forBoth = deferred<Album[]>();
      mockApi.listAlbums.mockReturnValueOnce(forBob.promise).mockReturnValueOnce(forBoth.promise);
      renderDialog({ albums });
      const dialog = await loadedDialog();
      await userEvent.click(within(dialog).getByRole("checkbox", { name: /Bob/ }));
      await userEvent.click(within(dialog).getByRole("checkbox", { name: /carol@example\.test/ }));

      const [[first], [second]] = mockApi.listAlbums.mock.calls as [{ memberIds: string[]; signal: AbortSignal }][];
      expect(first.memberIds).toEqual(["u-bob"]);
      expect(first.signal.aborted).toBe(true);
      expect(second.memberIds).toEqual(["u-bob", "u-carol"]);
      expect(second.signal.aborted).toBe(false);
      expect(offeredAlbums(dialog)).toEqual(both);

      await act(async () => forBoth.resolve([albums[1]]));
      expect(offeredAlbums(dialog)).toEqual(["Iceland 2024 by Dana"]);
      // The answer for Bob alone comes late and changes nothing; neither would its failure.
      await act(async () => forBob.resolve([albums[0]]));
      expect(offeredAlbums(dialog)).toEqual(["Iceland 2024 by Dana"]);
      expect(within(dialog).queryByRole("status")).toBeNull();
    });

    it("cancels the lookup when it closes, and offers every album again on the next opening", async () => {
      const pending = deferred<Album[]>();
      mockApi.listAlbums.mockReturnValueOnce(pending.promise);
      const { rerender, props } = renderDialog({ albums });
      await loadedDialog();
      await userEvent.click(screen.getByRole("checkbox", { name: /Bob/ }));
      const [[{ signal }]] = mockApi.listAlbums.mock.calls as [{ signal: AbortSignal }][];

      rerender(dialogElement({ ...props, open: false }));
      expect(signal.aborted).toBe(true);
      await act(async () => pending.reject(new DOMException("The operation was aborted.", "AbortError")));
      expect(console.error).not.toHaveBeenCalled();

      rerender(dialogElement({ ...props, open: true }));
      const dialog = await loadedDialog();
      expect(offeredAlbums(dialog)).toEqual(both);
      expect(within(dialog).queryByRole("status")).toBeNull();
      expect(mockApi.listAlbums).toHaveBeenCalledTimes(1);
    });
  });
});
