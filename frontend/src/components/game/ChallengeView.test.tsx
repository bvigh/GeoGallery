import type { ComponentProps } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { AuthContext } from "@/components/auth/AuthContext";
import type { ChallengeResults } from "@/components/game/ChallengeResults";
import { ChallengeView } from "@/components/game/ChallengeView";
import type { RoundPlay } from "@/components/game/RoundPlay";
import type { RoundResult } from "@/components/game/RoundResult";
import { ALREADY_GUESSED, CHALLENGE_GONE, ROUND_GONE } from "@/lib/challenge";
import { BACK_DEPTH_KEY } from "@/lib/history";
import type { Challenge, ChallengeRound, CurrentUser, GuessResult } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return { ...actual, api: { getChallenge: vi.fn(), guessChallenge: vi.fn() } };
});

type PlayProps = ComponentProps<typeof RoundPlay>;
type ResultProps = ComponentProps<typeof RoundResult>;
type ResultsProps = ComponentProps<typeof ChallengeResults>;

// The round screens are the Game page's own (GamePage.test.tsx); here they are stand-ins that
// record what the view gives them. `mounts` counts RoundPlay instances: one per round.
const stubs = vi.hoisted(() => ({
  play: null as PlayProps | null,
  mounts: 0,
  result: null as ResultProps | null,
  results: null as ResultsProps | null,
}));

vi.mock("@/components/game/RoundPlay", async () => {
  const { useEffect } = await import("react");
  return {
    RoundPlay: (props: PlayProps) => {
      stubs.play = props;
      useEffect(() => {
        stubs.mounts++;
      }, []);
      return (
        <div data-testid="round-play" data-media={props.round.id}>
          {props.error && <p role="alert">{props.error}</p>}
          <button onClick={() => props.onSubmit({ lat: 47.2, lng: 19.1 }, "2024-05-11")}>Submit</button>
        </div>
      );
    },
  };
});

vi.mock("@/components/game/RoundResult", () => ({
  RoundResult: (props: ResultProps) => {
    stubs.result = props;
    return (
      <div data-testid="round-result" data-heading={props.heading}>
        <button onClick={props.onNext}>{props.nextLabel}</button>
      </div>
    );
  },
}));

vi.mock("@/components/game/ChallengeResults", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/components/game/ChallengeResults")>();
  return {
    ...actual,
    ChallengeResults: (props: ResultsProps) => {
      stubs.results = props;
      return <div data-testid="results" />;
    },
  };
});

const mockApi = api as unknown as Record<"getChallenge" | "guessChallenge", ReturnType<typeof vi.fn>>;

const ID = "0b6f3c1e-5d1a-4c47-9a8e-2f0d7b9c4e11";
const me: CurrentUser = { id: "u-me", email: "me@example.test", display_name: "Me Myself", role: "user" };

function roundAt(position: number, number = position): ChallengeRound {
  return {
    position,
    number,
    id: `m${position}`,
    media_type: "photo",
    url: `https://s3.example/m${position}`,
    max_dist_km: 500,
    bounds: [47, 16, 48.5, 19.5],
  };
}

function challenge(extra: Partial<Challenge> = {}): Challenge {
  return {
    id: ID,
    name: "Family five",
    owner_id: "u-alice",
    owner_name: "Alice",
    is_owner: false,
    created_at: "2026-11-01T10:00:00Z",
    member_count: 2,
    item_count: 5,
    played: 0,
    score: 0,
    finished: false,
    members: [
      { user_id: "u-alice", name: "Alice", is_owner: true, played: 5, score: 21000 },
      { user_id: "u-me", name: "Me Myself", is_owner: false, played: 0, score: 0 },
    ],
    round: roundAt(1),
    results: null,
    ...extra,
  };
}

const finished = challenge({
  played: 5,
  score: 18000,
  finished: true,
  round: null,
  results: [
    {
      position: 1,
      number: 1,
      available: true,
      media_id: "m1",
      media_type: "photo",
      url: "https://s3.example/m1",
      actual_lat: 47.5,
      actual_lng: 19,
      actual_date: null,
      guesses: [],
    },
  ],
});

const scored: GuessResult = {
  actual_lat: 47.5,
  actual_lng: 19,
  actual_date: null,
  distance_km: 3.2,
  date_diff_days: null,
  location_score: 4800,
  time_score: null,
  total_score: 4800,
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

// How many fullscreen views hold a history entry (useCloseOnBack).
const depth = () => ((window.history.state as Record<string, unknown> | null)?.[BACK_DEPTH_KEY] as number) ?? 0;

// jsdom traverses history in setTimeout(0) steps; this lets every pending step land.
async function settle() {
  await act(async () => {
    for (let round = 0; round < 12; round++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
  });
}

function renderView(onClose = vi.fn()) {
  const view = render(
    <AuthContext.Provider value={me}>
      <ChallengeView challengeId={ID} onClose={onClose} />
    </AuthContext.Provider>,
  );
  return { ...view, onClose };
}

async function openOverview(onClose = vi.fn()) {
  const view = renderView(onClose);
  await screen.findByRole("table", { name: "High scores" });
  return view;
}

// Opens the view and starts the round it offers.
async function startRound() {
  const view = await openOverview();
  await userEvent.click(screen.getByRole("button", { name: /^(Play|Continue) with round/ }));
  expect(screen.getByTestId("round-play")).toBeInTheDocument();
  return view;
}

describe("ChallengeView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubs.play = null;
    stubs.mounts = 0;
    stubs.result = null;
    stubs.results = null;
    window.history.replaceState(null, "", "/game");
    mockApi.getChallenge.mockResolvedValue(challenge());
    mockApi.guessChallenge.mockResolvedValue(scored);
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(async () => {
    await settle();
    vi.restoreAllMocks();
  });

  describe("overview", () => {
    it("says whose challenge it is, offers the first round and lists the standings", async () => {
      await openOverview();
      expect(mockApi.getChallenge).toHaveBeenCalledWith(ID, expect.any(AbortSignal));
      expect(screen.getByRole("dialog", { name: "Family five" })).toBeInTheDocument();
      expect(screen.getByText("A challenge by Alice · 2 players")).toBeInTheDocument();
      expect(screen.getByText(/You get one guess per round/)).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Play with round 1 of 5" })).toBeInTheDocument();
      const rows = within(screen.getByRole("table", { name: "High scores" })).getAllByRole("row");
      expect(rows[1]).toHaveTextContent("1ALAlice5 / 521,000 pts");
      expect(rows[2]).toHaveTextContent("–MMMe Myself (you)0 / 5–");
      expect(screen.queryByTestId("results")).toBeNull();
    });

    it("says Loading until the challenge is in", async () => {
      const pending = deferred<Challenge>();
      mockApi.getChallenge.mockReturnValue(pending.promise);
      renderView();
      expect(screen.getByText("Loading…")).toBeInTheDocument();
      expect(screen.getByRole("dialog", { name: "Challenge" })).toBeInTheDocument();
      await act(async () => pending.resolve(challenge()));
      expect(screen.queryByText("Loading…")).toBeNull();
    });

    it("offers to continue where the player left off", async () => {
      mockApi.getChallenge.mockResolvedValue(challenge({ played: 2, score: 7000, round: roundAt(3) }));
      await openOverview();
      expect(screen.getByRole("button", { name: "Continue with round 3 of 5" })).toBeInTheDocument();
    });

    it("names the owner's own challenge as theirs", async () => {
      mockApi.getChallenge.mockResolvedValue(challenge({ is_owner: true, owner_id: me.id, member_count: 1 }));
      await openOverview();
      expect(screen.getByText("Your challenge · 1 player")).toBeInTheDocument();
    });

    it("shows a finished player every round's results, and no round to play", async () => {
      mockApi.getChallenge.mockResolvedValue(finished);
      await openOverview();
      expect(screen.getByText("You have played every round. Here is how everyone did.")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /with round/ })).toBeNull();
      expect(screen.getByTestId("results")).toBeInTheDocument();
      expect(stubs.results).toMatchObject({ results: finished.results, members: finished.members, meId: me.id });
    });

    it("says so when no round is the player's to play", async () => {
      mockApi.getChallenge.mockResolvedValue({ ...finished, played: 0, score: 0, results: [] });
      await openOverview();
      expect(screen.getByText("There is no round for you to play here.")).toBeInTheDocument();
    });

    it("gives the link that opens the challenge for its players", async () => {
      await openOverview();
      expect(screen.getByRole("textbox", { name: "Link to this challenge" })).toHaveValue(
        `${window.location.origin}/game/${ID}`,
      );
      expect(screen.getByText(/It gives nobody else access/)).toBeInTheDocument();
    });
  });

  describe("loading failures", () => {
    it("closes with the reason when the challenge is gone or not the caller's", async () => {
      mockApi.getChallenge.mockRejectedValue(new ApiError("GET /challenges failed: 404", 404));
      const { onClose } = renderView();
      await waitFor(() => expect(onClose).toHaveBeenCalledWith(CHALLENGE_GONE));
    });

    it("offers Try again on any other failure, and keeps the focus in the view", async () => {
      mockApi.getChallenge.mockRejectedValueOnce(new ApiError("GET /challenges failed: 500", 500));
      const { onClose } = renderView();
      expect(await screen.findByText("Could not load the challenge.")).toBeInTheDocument();
      expect(onClose).not.toHaveBeenCalled();

      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(await screen.findByRole("button", { name: "Play with round 1 of 5" })).toBeInTheDocument();
      expect(mockApi.getChallenge).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("dialog")).toContainElement(document.activeElement as HTMLElement);
    });

    it("aborts the request in flight when it closes", async () => {
      mockApi.getChallenge.mockReturnValue(new Promise(() => {}));
      const { unmount, onClose } = renderView();
      const signal = mockApi.getChallenge.mock.calls[0][1] as AbortSignal;
      expect(signal.aborted).toBe(false);
      unmount();
      expect(signal.aborted).toBe(true);
      expect(onClose).not.toHaveBeenCalled();
    });
  });

  describe("playing", () => {
    it("plays the round the challenge names and sends its position with the guess", async () => {
      await startRound();
      expect(stubs.play!.round).toEqual(roundAt(1));
      expect(screen.getByText("Round 1 of 5")).toBeInTheDocument();
      expect(screen.queryByRole("table")).toBeNull();

      mockApi.getChallenge.mockResolvedValue(challenge({ played: 1, score: 4800, round: roundAt(2) }));
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      expect(mockApi.guessChallenge).toHaveBeenCalledWith(ID, 1, { lat: 47.2, lng: 19.1 }, "2024-05-11");

      const result = await screen.findByTestId("round-result");
      expect(result).toHaveAttribute("data-heading", "Round 1 of 5");
      expect(stubs.result).toMatchObject({ guess: { lat: 47.2, lng: 19.1 }, result: scored });
      expect(screen.queryByTestId("round-play")).toBeNull();
    });

    it("moves on to the next round, which starts afresh", async () => {
      await startRound();
      mockApi.getChallenge.mockResolvedValue(challenge({ played: 1, score: 4800, round: roundAt(2) }));
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      await userEvent.click(await screen.findByRole("button", { name: "Next round" }));

      expect(screen.getByTestId("round-play")).toHaveAttribute("data-media", "m2");
      expect(screen.getByText("Round 2 of 5")).toBeInTheDocument();
      expect(stubs.mounts).toBe(2);
      expect(mockApi.getChallenge).toHaveBeenCalledTimes(2);
    });

    it("counts rounds as the challenge does once one of them is gone", async () => {
      mockApi.getChallenge.mockResolvedValue(challenge({ item_count: 4, played: 1, round: roundAt(3, 2) }));
      await startRound();
      expect(screen.getByText("Round 2 of 4")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      expect(mockApi.guessChallenge).toHaveBeenCalledWith(ID, 3, expect.anything(), expect.anything());
    });

    it("goes from the last round to the results", async () => {
      mockApi.getChallenge.mockResolvedValue(challenge({ played: 4, round: roundAt(5) }));
      await startRound();
      mockApi.getChallenge.mockResolvedValue(finished);
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));

      await userEvent.click(await screen.findByRole("button", { name: "See results" }));
      expect(screen.getByTestId("results")).toBeInTheDocument();
      expect(screen.getByRole("table", { name: "High scores" })).toBeInTheDocument();
      expect(screen.queryByTestId("round-result")).toBeNull();
    });

    it("sends one guess however often Submit is pressed", async () => {
      await startRound();
      const pending = deferred<GuessResult>();
      mockApi.guessChallenge.mockReturnValue(pending.promise);
      const submit = screen.getByRole("button", { name: "Submit" });
      fireEvent.click(submit);
      fireEvent.click(submit);
      fireEvent.click(submit);
      expect(mockApi.guessChallenge).toHaveBeenCalledTimes(1);
      await act(async () => pending.resolve(scored));
    });

    it("keeps the round open with an error when the guess could not be sent", async () => {
      await startRound();
      mockApi.guessChallenge.mockRejectedValueOnce(new ApiError("POST failed: 500", 500));
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("Could not submit guess");
      expect(screen.getByTestId("round-play")).toBeInTheDocument();
      expect(mockApi.getChallenge).toHaveBeenCalledTimes(1);

      // The same guess can be sent again, and the error goes.
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      expect(await screen.findByTestId("round-result")).toBeInTheDocument();
      expect(mockApi.guessChallenge).toHaveBeenCalledTimes(2);
    });

    it.each([
      { status: 409, notice: ALREADY_GUESSED },
      { status: 404, notice: ROUND_GONE },
    ])("returns to the standings with the reason on a $status, and asks what is next", async ({ status, notice }) => {
      await startRound();
      mockApi.guessChallenge.mockRejectedValue(new ApiError(`POST failed: ${status}`, status));
      mockApi.getChallenge.mockResolvedValue(challenge({ played: 1, round: roundAt(2) }));
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));

      expect(await screen.findByRole("status")).toHaveTextContent(notice);
      expect(await screen.findByRole("button", { name: "Continue with round 2 of 5" })).toBeInTheDocument();
      expect(screen.queryByTestId("round-play")).toBeNull();
      expect(mockApi.getChallenge).toHaveBeenCalledTimes(2);

      // The notice was about that round: it goes with the next one.
      await userEvent.click(screen.getByRole("button", { name: "Continue with round 2 of 5" }));
      expect(screen.queryByRole("status")).toBeNull();
    });

    it("closes with the reason when the challenge itself went while a guess was sent", async () => {
      const { onClose } = await startRound();
      mockApi.guessChallenge.mockRejectedValue(new ApiError("POST failed: 404", 404));
      mockApi.getChallenge.mockRejectedValue(new ApiError("GET failed: 404", 404));
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      await waitFor(() => expect(onClose).toHaveBeenCalledWith(CHALLENGE_GONE));
    });
  });

  describe("after a guess, while the challenge is asked what is next", () => {
    it("says Continue until the answer is in, then names the next step", async () => {
      await startRound();
      const pending = deferred<Challenge>();
      mockApi.getChallenge.mockReturnValue(pending.promise);
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      expect(await screen.findByRole("button", { name: "Continue" })).toBeInTheDocument();

      await act(async () => pending.resolve(challenge({ played: 1, round: roundAt(2) })));
      expect(screen.getByRole("button", { name: "Next round" })).toBeInTheDocument();
    });

    it("does not offer the round just guessed again", async () => {
      await startRound();
      const pending = deferred<Challenge>();
      mockApi.getChallenge.mockReturnValue(pending.promise);
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      await userEvent.click(await screen.findByRole("button", { name: "Continue" }));

      // The standings, with the challenge as it was before the guess: its Play button is off.
      const play = screen.getByRole("button", { name: "Play with round 1 of 5" });
      expect(play).toHaveAttribute("aria-disabled", "true");
      await userEvent.click(play);
      expect(screen.queryByTestId("round-play")).toBeNull();

      await act(async () => pending.resolve(challenge({ played: 1, round: roundAt(2) })));
      const next = screen.getByRole("button", { name: "Continue with round 2 of 5" });
      expect(next).toHaveAttribute("aria-disabled", "false");
      await userEvent.click(next);
      expect(screen.getByTestId("round-play")).toHaveAttribute("data-media", "m2");
    });

    it("says that the standings could not be refreshed, and tries again", async () => {
      await startRound();
      mockApi.getChallenge.mockRejectedValueOnce(new ApiError("GET failed: 502", 502));
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      await userEvent.click(await screen.findByRole("button", { name: "Continue" }));

      expect(await screen.findByRole("alert")).toHaveTextContent("Could not refresh the challenge.");
      mockApi.getChallenge.mockResolvedValue(challenge({ played: 1, round: roundAt(2) }));
      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(await screen.findByRole("button", { name: "Continue with round 2 of 5" })).toBeInTheDocument();
      expect(screen.queryByRole("alert")).toBeNull();
    });
  });

  describe("keys, focus and history", () => {
    it("takes the focus when it opens and gives it back when it closes", async () => {
      const opener = document.createElement("button");
      document.body.append(opener);
      opener.focus();
      const { unmount } = await openOverview();
      expect(document.activeElement).toBe(screen.getByRole("dialog"));
      unmount();
      expect(document.activeElement).toBe(opener);
      opener.remove();
    });

    it("keeps Tab inside the view", async () => {
      await openOverview();
      const dialog = screen.getByRole("dialog");
      const close = screen.getByRole("button", { name: "Close" });
      const copy = screen.getByRole("button", { name: "Copy" });

      // From the page behind the view (the focus fell to <body>), Tab comes into the view.
      dialog.blur();
      expect(document.activeElement).toBe(document.body);
      fireEvent.keyDown(window, { key: "Tab" });
      expect(document.activeElement).toBe(close);
      copy.focus();
      fireEvent.keyDown(copy, { key: "Tab" });
      expect(document.activeElement).toBe(close);
      fireEvent.keyDown(close, { key: "Tab", shiftKey: true });
      expect(document.activeElement).toBe(copy);
      expect(dialog).toContainElement(document.activeElement as HTMLElement);
    });

    it("closes on Escape and on the Close button, but not on Escape in a text field", async () => {
      const { onClose } = await openOverview();
      const link = screen.getByRole("textbox", { name: "Link to this challenge" });
      fireEvent.keyDown(link, { key: "Escape" });
      expect(onClose).not.toHaveBeenCalled();

      fireEvent.keyDown(window, { key: "Escape" });
      expect(onClose).toHaveBeenCalledTimes(1);
      expect(onClose).toHaveBeenCalledWith();

      await userEvent.click(screen.getByRole("button", { name: "Close" }));
      expect(onClose).toHaveBeenCalledTimes(2);
    });

    it("leaves a key that belongs to a dialog over it alone", async () => {
      const { onClose } = await openOverview();
      const dialog = document.createElement("div");
      dialog.setAttribute("data-app-dialog", "");
      dialog.setAttribute("data-state", "open");
      document.body.append(dialog);
      fireEvent.keyDown(window, { key: "Escape" });
      expect(onClose).not.toHaveBeenCalled();
      dialog.remove();

      const handled = new KeyboardEvent("keydown", { key: "Escape", cancelable: true, bubbles: true });
      handled.preventDefault();
      window.dispatchEvent(handled);
      expect(onClose).not.toHaveBeenCalled();
    });

    it("moves the focus to the view when the screen changes under the pressed button", async () => {
      await startRound();
      expect(document.activeElement).toBe(screen.getByRole("dialog"));
      mockApi.getChallenge.mockResolvedValue(challenge({ played: 1, round: roundAt(2) }));
      await userEvent.click(screen.getByRole("button", { name: "Submit" }));
      await screen.findByTestId("round-result");
      expect(document.activeElement).toBe(screen.getByRole("dialog"));
    });

    it("shows its URL while open, and the back gesture closes it", async () => {
      const { onClose } = await openOverview();
      await settle();
      expect(depth()).toBe(1);
      expect(window.location.pathname).toBe(`/game/${ID}`);

      await act(async () => {
        const popped = new Promise((resolve) => window.addEventListener("popstate", resolve, { once: true }));
        window.history.back();
        await popped;
      });
      await settle();
      expect(onClose).toHaveBeenCalledTimes(1);
      expect(window.location.pathname).toBe("/game");
    });

    it("gives the Close button a 44 px target on touch", async () => {
      await openOverview();
      const classes = screen.getByRole("button", { name: "Close" }).className.split(" ");
      expect(classes).toEqual(expect.arrayContaining(["touch:min-h-11", "touch:min-w-11"]));
    });
  });
});
