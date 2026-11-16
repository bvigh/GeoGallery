import { useCallback, useEffect, useRef, useState } from "react";
import { X } from "lucide-react";
import { api, ApiError } from "@/api/client";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { CopyField } from "@/components/CopyField";
import { ChallengeResults, Scoreboard } from "@/components/game/ChallengeResults";
import { RoundItemView } from "@/components/game/RoundItemView";
import { RoundPlay, type Guess } from "@/components/game/RoundPlay";
import { RoundResult } from "@/components/game/RoundResult";
import {
  ALREADY_GUESSED,
  CHALLENGE_GONE,
  FRESH_URLS_MS,
  playersLabel,
  ROUND_GONE,
  roundItemState,
  type RoundItemState,
} from "@/lib/challenge";
import { keyBelongsToDialog } from "@/lib/dialogKeys";
import { isTextField, keepTabInside } from "@/lib/focusTrap";
import { useCloseOnBack } from "@/lib/history";
import { challengeRoute, routeUrl } from "@/lib/routes";
import type { Challenge, ChallengeRoundResult, GuessResult } from "@/types";

// The guess just scored, shown until the player moves on.
interface Scored {
  position: number;
  number: number;
  guess: Guess;
  result: GuessResult;
}

type Phase = "overview" | "playing" | "scored";

// A round's item full size over the results (RoundItemView): which round, as the results showed it
// when the view opened, and what the view shows.
interface ItemRound {
  position: number;
  number: number;
  mediaType: string | null;
}
interface ItemView {
  round: ItemRound;
  state: RoundItemState;
}

interface Props {
  challengeId: string;
  // Called to close the view; `notice` says why when the view closed by itself (the challenge is
  // gone, or the caller does not play in it).
  onClose: (notice?: string) => void;
}

// One challenge, full screen over the Game page: the standings, the player's rounds one by one
// (each guessed once), and, when none is left, every round's answer with everyone's guesses.
// Which round is next is the server's to say, so the view asks again after every guess and has
// nothing to restore after a reload but the challenge's ID in the URL.
export function ChallengeView({ challengeId, onClose }: Props) {
  const me = useCurrentUser();
  const [challenge, setChallenge] = useState<Challenge | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [loading, setLoading] = useState(true);
  const [phase, setPhase] = useState<Phase>("overview");
  const [scored, setScored] = useState<Scored | null>(null);
  const [guessError, setGuessError] = useState<string | null>(null);
  // Why a round ended without a score, said on the overview.
  const [notice, setNotice] = useState<string | null>(null);
  const submitting = useRef(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  // The latest load, and its request: an older load that answers late is dropped.
  const loadId = useRef(0);
  const controller = useRef<AbortController | null>(null);
  // When the challenge on screen came (Date.now()): its media URLs are signed for 15 minutes.
  const loadedAt = useRef(0);
  const [itemView, setItemView] = useState<ItemView | null>(null);
  // Counts the item views opened and closed: a fetch for one that has closed answers into nothing.
  const itemOpenings = useRef(0);

  // What a load came to: the challenge, a failure (said on screen), or null when a later load, the
  // view's closing or the challenge's 404 took over.
  const load = useCallback(async (): Promise<Challenge | "failed" | null> => {
    const id = ++loadId.current;
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    setLoading(true);
    try {
      const next = await api.getChallenge(challengeId, abort.signal);
      if (id !== loadId.current || abort.signal.aborted) return null;
      setChallenge(next);
      loadedAt.current = Date.now();
      setLoadFailed(false);
      return next;
    } catch (e) {
      if (id !== loadId.current || abort.signal.aborted) return null;
      if (e instanceof ApiError && e.status === 404) {
        onCloseRef.current(CHALLENGE_GONE);
        return null;
      }
      console.error("Failed to load the challenge:", e);
      setLoadFailed(true);
      return "failed";
    } finally {
      if (id === loadId.current) setLoading(false);
    }
  }, [challengeId]);

  useEffect(() => {
    void load();
    // A closed view takes no answer: load() drops one whose request was aborted.
    return () => controller.current?.abort();
  }, [load]);

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    rootRef.current?.focus();
    return () => {
      if (opener?.isConnected) opener.focus();
    };
  }, []);

  useCloseOnBack(() => onClose(), true, routeUrl(challengeRoute(challengeId)));

  const itemOpen = itemView !== null;

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (keyBelongsToDialog(e)) return;
      // A round's item full size covers the view and handles every key itself, Tab included.
      if (itemOpen) return;
      if (e.key === "Tab") {
        keepTabInside(e, rootRef.current);
        return;
      }
      if (isTextField(e.target)) return;
      if (e.key === "Escape") onCloseRef.current();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [itemOpen]);

  // The challenge fetched again for a round's item: fresh URLs, and the round as the server has it
  // now. The item view says Loading meanwhile, then shows the item, or why it cannot.
  const fetchItem = async (round: ItemRound) => {
    const opening = ++itemOpenings.current;
    setItemView({ round, state: { status: "loading" } });
    const next = await load();
    if (opening !== itemOpenings.current || next === null) return;
    setItemView({
      round,
      state: next === "failed" ? { status: "failed" } : roundItemState(next.results, round.position),
    });
  };

  // Opens a round's item full size. Its URL came with the challenge, signed for 15 minutes: while
  // the challenge is younger than FRESH_URLS_MS the view shows it at once, after that it fetches
  // the challenge again first.
  const openItem = (result: ChallengeRoundResult) => {
    const round = { position: result.position, number: result.number, mediaType: result.media_type };
    if (Date.now() - loadedAt.current < FRESH_URLS_MS) {
      itemOpenings.current++;
      setItemView({ round, state: { status: "shown", result } });
      return;
    }
    void fetchItem(round);
  };

  const closeItem = () => {
    itemOpenings.current++;
    setItemView(null);
  };

  // The screen changes under the focused button (Play, Submit, Next round): the focus moves to the
  // view rather than fall to <body>, where Tab would start behind it.
  const show = (next: Phase) => {
    rootRef.current?.focus();
    setPhase(next);
  };

  const round = challenge?.round ?? null;

  const submit = async (guess: Guess, date: string) => {
    // One guess per round: a second click must not send a second one, which would be refused.
    if (!round || submitting.current) return;
    submitting.current = true;
    setGuessError(null);
    try {
      const result = await api.guessChallenge(challengeId, round.position, guess, date);
      setScored({ position: round.position, number: round.number, guess, result });
      show("scored");
      void load();
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 409)) {
        // Guessed from another tab, or the item is gone or no longer shared: the server says what
        // is next. A 404 for the challenge itself closes the view from load().
        setNotice(e.status === 409 ? ALREADY_GUESSED : ROUND_GONE);
        show("overview");
        void load();
        return;
      }
      console.error("Failed to submit guess:", e);
      setGuessError("Could not submit guess");
    } finally {
      submitting.current = false;
    }
  };

  // After a scored guess: on to the round the reloaded challenge names, else to the standings.
  // Until that reload has answered, the challenge still names the round just guessed.
  const upToDate = challenge !== null && !loading && !loadFailed && round?.position !== scored?.position;
  const nextRound = upToDate ? round : null;
  const moveOn = () => {
    setScored(null);
    setNotice(null);
    show(nextRound ? "playing" : "overview");
  };

  const play = () => {
    setNotice(null);
    setGuessError(null);
    show("playing");
  };

  const title = challenge?.name ?? "Challenge";
  const playing = phase === "playing" && round !== null;
  const showScored = phase === "scored" && scored !== null;
  let stage = "";
  if (playing) stage = `Round ${round.number} of ${challenge?.item_count ?? round.number}`;
  else if (showScored) stage = `Round ${scored.number} of ${challenge?.item_count ?? scored.number}`;

  return (
    <div
      ref={rootRef}
      role="dialog"
      aria-modal="true"
      aria-label={title}
      tabIndex={-1}
      className="fixed inset-0 z-[1000] flex flex-col bg-background text-foreground outline-none"
    >
      <header className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-semibold">{title}</h2>
          {stage && <p className="text-xs text-muted-foreground">{stage}</p>}
        </div>
        <button
          type="button"
          onClick={() => onClose()}
          aria-label="Close"
          className="flex items-center justify-center text-muted-foreground hover:text-foreground touch:min-h-11 touch:min-w-11"
        >
          <X className="h-5 w-5" aria-hidden="true" />
        </button>
      </header>

      <div className="min-h-0 flex-1">
        {playing ? (
          // Keyed by the round: each one starts with no guess, an empty date and the small map.
          <RoundPlay key={round.position} round={round} onSubmit={submit} error={guessError} />
        ) : showScored ? (
          <RoundResult
            guess={scored.guess}
            result={scored.result}
            heading={stage}
            nextLabel={nextRound ? "Next round" : upToDate ? "See results" : "Continue"}
            onNext={moveOn}
          />
        ) : (
          <div className="h-full overflow-y-auto">
            <div className="mx-auto max-w-3xl space-y-6 p-4 sm:p-6">
              {notice && (
                <p role="status" className="rounded-md border p-3 text-sm">
                  {notice}
                </p>
              )}
              {challenge === null ? (
                loadFailed ? (
                  <div className="space-y-2">
                    <p className="text-sm">Could not load the challenge.</p>
                    <button
                      type="button"
                      onClick={() => {
                        rootRef.current?.focus();
                        void load();
                      }}
                      className="rounded border border-border px-3 py-1.5 text-sm hover:bg-muted touch:min-h-11"
                    >
                      Try again
                    </button>
                  </div>
                ) : (
                  <p className="text-sm text-muted-foreground">Loading…</p>
                )
              ) : (
                <Overview
                  challenge={challenge}
                  meId={me.id}
                  stale={loadFailed}
                  busy={loading}
                  onPlay={play}
                  onRetry={() => void load()}
                  onOpenItem={openItem}
                />
              )}
            </div>
          </div>
        )}
      </div>

      {itemView && (
        <RoundItemView
          number={itemView.round.number}
          mediaType={itemView.round.mediaType}
          state={itemView.state}
          onRetry={() => void fetchItem(itemView.round)}
          onClose={closeItem}
          returnFocus={() => rootRef.current?.focus()}
        />
      )}
    </div>
  );
}

function Overview({
  challenge,
  meId,
  stale,
  busy,
  onPlay,
  onRetry,
  onOpenItem,
}: {
  challenge: Challenge;
  meId: string;
  // The last reload failed: what is shown may be behind.
  stale: boolean;
  // A reload is on its way: the round shown may be one the player has just guessed.
  busy: boolean;
  onPlay: () => void;
  onRetry: () => void;
  onOpenItem: (result: ChallengeRoundResult) => void;
}) {
  const { round, results } = challenge;
  const link = window.location.origin + routeUrl(challengeRoute(challenge.id));
  return (
    <>
      <div className="space-y-2">
        <p className="text-sm text-muted-foreground">
          {challenge.is_owner ? "Your challenge" : `A challenge by ${challenge.owner_name ?? "another user"}`} ·{" "}
          {playersLabel(challenge.member_count)}
        </p>
        {stale && (
          <p role="alert" className="text-sm text-red-500">
            Could not refresh the challenge.{" "}
            <button type="button" onClick={onRetry} className="underline touch:min-h-11">
              Try again
            </button>
          </p>
        )}
        {round ? (
          <>
            <p className="text-sm">
              {challenge.item_count === 1 ? "One round" : `${challenge.item_count} rounds`}, the same for every player.
              You get one guess per round: pick the place on the map, and the date if you like. Everyone's guesses
              are shown once you have played every round.
            </p>
            {/* Busy, never disabled: a disabled button would drop the keyboard focus to <body>. */}
            <button
              type="button"
              onClick={busy ? undefined : onPlay}
              aria-disabled={busy}
              className="rounded-lg bg-primary px-6 py-2 font-medium text-primary-foreground aria-disabled:opacity-50 touch:min-h-11"
            >
              {challenge.played === 0 ? "Play" : "Continue"} with round {round.number} of {challenge.item_count}
            </button>
          </>
        ) : (
          <p className="text-sm">
            {challenge.played === 0
              ? "There is no round for you to play here."
              : "You have played every round. Here is how everyone did."}
          </p>
        )}
      </div>

      <section aria-labelledby="challenge-scores" className="space-y-2">
        <h3 id="challenge-scores" className="text-sm font-semibold">
          High scores
        </h3>
        <Scoreboard members={challenge.members} meId={meId} rounds={challenge.item_count} />
      </section>

      {results && (
        <section aria-label="Rounds" className="space-y-2">
          <ChallengeResults results={results} members={challenge.members} meId={meId} onOpenItem={onOpenItem} />
        </section>
      )}

      <section aria-labelledby="challenge-link" className="space-y-2">
        <h3 id="challenge-link" className="text-sm font-semibold">
          Link
        </h3>
        <p className="text-sm text-muted-foreground">
          This link opens the challenge for its players, after they sign in. It gives nobody else access.
        </p>
        <CopyField value={link} label="Link to this challenge" />
      </section>
    </>
  );
}
