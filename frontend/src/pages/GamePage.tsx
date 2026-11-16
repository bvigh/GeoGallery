import { useCallback, useEffect, useState } from "react";
import { Plus } from "lucide-react";
import type { Album, ChallengeSummary, FilterState, GameRound, GuessResult, Trip } from "@/types";
import { api, ApiError } from "@/api/client";
import { activeFilterCount, emptyFilters, filterParams } from "@/lib/filters";
import { challengeStatus, playersLabel, playLabel } from "@/lib/challenge";
import { useHistoryChange } from "@/lib/history";
import { currentRoute } from "@/lib/routes";
import { FilterPanel } from "@/components/map/FilterPanel";
import { ChallengeView } from "@/components/game/ChallengeView";
import { NewChallengeDialog } from "@/components/game/NewChallengeDialog";
import { RoundPlay, type Guess } from "@/components/game/RoundPlay";
import { RoundResult } from "@/components/game/RoundResult";

type GameState = "idle" | "playing" | "result";

// The challenge the URL names, to open over the page.
function challengeFromUrl(): string | null {
  const route = currentRoute();
  return route.tab === "game" ? route.collectionId : null;
}

// Small text buttons on a mouse; finger-sized, with readable text, on touch.
const TEXT_BUTTON_CLASS = "text-xs touch:min-h-11 touch:min-w-11 touch:px-2 touch:text-sm";

export function GamePage() {
  const [state, setState] = useState<GameState>("idle");
  const [round, setRound] = useState<GameRound | null>(null);
  // Counts the rounds started: each gets its own RoundPlay, so none starts with the last one's guess.
  const [roundNo, setRoundNo] = useState(0);
  const [guess, setGuess] = useState<Guess | null>(null);
  const [result, setResult] = useState<GuessResult | null>(null);
  const [trips, setTrips] = useState<Trip[]>([]);
  const [albums, setAlbums] = useState<Album[]>([]);
  const [filters, setFilters] = useState<FilterState>(emptyFilters);
  // Why there is no round to play, shown above Start Round.
  const [notice, setNotice] = useState<string | null>(null);
  const [guessError, setGuessError] = useState<string | null>(null);

  const [challenges, setChallenges] = useState<ChallengeSummary[] | null>(null);
  // What went wrong with the challenges, or why one closed by itself.
  const [challengeNotice, setChallengeNotice] = useState<string | null>(null);
  // The open challenge. From the URL it opens at once, before the list is in: the view itself
  // finds out whether the caller may see it.
  const [openChallenge, setOpenChallenge] = useState<string | null>(challengeFromUrl);
  const [creating, setCreating] = useState(false);

  const loadChallenges = useCallback(() => {
    api
      .listChallenges()
      .then(setChallenges)
      .catch((e) => {
        console.error(e);
        setChallengeNotice("Could not load the challenges");
      });
  }, []);

  useEffect(() => {
    api.get<Trip[]>("/trips").then(setTrips);
    api.listAlbums().then(setAlbums).catch(() => {});
    loadChallenges();
  }, [loadChallenges]);

  // Back or forward onto a challenge's entry while none is open (forward after back): open it
  // again. It takes that entry over, as this runs before history is brought in line.
  useHistoryChange(() => {
    const next = challengeFromUrl();
    if (next !== null) setOpenChallenge((open) => open ?? next);
  });

  const activeCount = activeFilterCount(filters);

  const changeFilters = (next: FilterState) => {
    setFilters(next);
    // The notice was about the previous filters.
    setNotice(null);
  };

  const startRound = async () => {
    let r: GameRound;
    try {
      r = await api.get<GameRound>("/game/round", filterParams(filters));
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        // With filters set, a 404 only says that nothing matches them.
        setNotice(activeCount > 0 ? "No geolocated media match these filters" : "No geolocated media yet");
        setState("idle");
        return;
      }
      throw e;
    }
    setNotice(null);
    setGuessError(null);
    setRound(r);
    setRoundNo((n) => n + 1);
    setGuess(null);
    setResult(null);
    setState("playing");
  };

  const submitGuess = async (picked: Guess, date: string) => {
    if (!round) return;
    setGuessError(null);
    let res: GuessResult;
    try {
      res = await api.post<GuessResult>("/game/guess", {
        media_id: round.id,
        guess_lat: picked.lat,
        guess_lng: picked.lng,
        guess_date: date,
        max_dist_km: round.max_dist_km,
      });
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        // Deleted by its owner, or no longer shared with the player (album or membership gone).
        // The presigned URL keeps showing it for a while, so say why the round ends.
        setNotice("This photo is no longer available");
        setRound(null);
        setState("idle");
        return;
      }
      console.error("Failed to submit guess:", e);
      setGuessError("Could not submit guess");
      return;
    }
    setGuess(picked);
    setResult(res);
    setState("result");
  };

  const deleteChallenge = async (c: ChallengeSummary) => {
    if (!confirm(`Delete challenge "${c.name}"? Every player's guesses and scores in it are deleted. The media stay.`))
      return;
    setChallengeNotice(null);
    try {
      await api.deleteChallenge(c.id);
    } catch (e) {
      console.error(e);
      setChallengeNotice(
        e instanceof ApiError && e.status === 404 ? "That challenge was already deleted." : "Could not delete the challenge",
      );
    }
    loadChallenges();
  };

  const leaveChallenge = async (c: ChallengeSummary) => {
    if (!confirm(`Leave "${c.name}"? Your guesses and your score in it are deleted.`)) return;
    setChallengeNotice(null);
    try {
      await api.leaveChallenge(c.id);
    } catch (e) {
      console.error(e);
      setChallengeNotice(
        e instanceof ApiError && e.status === 404 ? "You are no longer in that challenge." : "Could not leave the challenge",
      );
    }
    loadChallenges();
  };

  return (
    <div className="relative flex h-full flex-col">
      {state === "idle" && (
        <>
          <FilterPanel
            trips={trips}
            albums={albums}
            filters={filters}
            onChange={changeFilters}
            activeCount={activeCount}
          />
          {/* The page scrolls here: the challenges can outgrow a phone's screen. */}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {/* The vertical padding keeps the text clear of the Filters button on short screens. */}
            <div className="mx-auto max-w-xl px-6 py-16 text-center">
              <h1 className="mb-4 text-2xl font-bold">GeoGallery Game</h1>
              <p className="mb-6 text-muted-foreground">
                You'll be shown a photo or video from your gallery. Guess where and
                when it was taken.
              </p>
              {notice && (
                <p className="mb-6 text-sm font-medium">{notice}</p>
              )}
              <button
                className="rounded-lg bg-primary px-6 py-2 text-primary-foreground font-medium touch:min-h-11"
                onClick={startRound}
              >
                Start Round
              </button>
            </div>

            <section aria-labelledby="challenges-heading" className="mx-auto max-w-xl px-6 pb-10">
              <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                <h2 id="challenges-heading" className="text-lg font-semibold">
                  Challenges
                </h2>
                <button
                  onClick={() => setCreating(true)}
                  className="flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm font-medium hover:bg-muted touch:min-h-11"
                >
                  <Plus className="h-4 w-4" aria-hidden="true" />
                  New challenge…
                </button>
              </div>
              <p className="mb-3 text-sm text-muted-foreground">
                Five fixed rounds that you and the players you choose each guess once, with a table of everyone's
                scores. You choose the players and what the items are drawn from when you make one.
              </p>
              {challengeNotice && (
                <p role="alert" className="mb-3 text-sm text-red-500">
                  {challengeNotice}
                </p>
              )}
              {challenges && challenges.length === 0 && (
                <p className="text-sm text-muted-foreground">
                  No challenges yet. Make one, or ask someone to add you to theirs.
                </p>
              )}
              <ul aria-label="Challenges" className="space-y-2">
                {challenges?.map((c) => (
                  <li key={c.id} className="rounded-lg border bg-card p-3 shadow-sm">
                    <div className="flex flex-wrap items-start justify-between gap-x-2">
                      <h3 className="min-w-0 font-medium touch:py-2.5">
                        <button
                          type="button"
                          onClick={() => setOpenChallenge(c.id)}
                          className="text-left [overflow-wrap:anywhere] hover:underline"
                        >
                          {c.name}
                        </button>
                      </h3>
                      <div className="ml-auto flex shrink-0 items-center gap-2 touch:gap-0">
                        {c.is_owner ? (
                          <button
                            className={`text-destructive hover:text-destructive/80 ${TEXT_BUTTON_CLASS}`}
                            aria-label={`Delete ${c.name}`}
                            onClick={() => deleteChallenge(c)}
                          >
                            Delete
                          </button>
                        ) : (
                          <button
                            className={`text-muted-foreground hover:text-foreground ${TEXT_BUTTON_CLASS}`}
                            aria-label={`Leave ${c.name}`}
                            onClick={() => leaveChallenge(c)}
                          >
                            Leave
                          </button>
                        )}
                        <button
                          aria-label={`${playLabel(c)}: ${c.name}`}
                          onClick={() => setOpenChallenge(c.id)}
                          className="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground touch:min-h-11"
                        >
                          {playLabel(c)}
                        </button>
                      </div>
                    </div>
                    <p className="text-sm text-muted-foreground">
                      {c.is_owner ? "Yours" : `by ${c.owner_name ?? "another user"}`} · {playersLabel(c.member_count)}
                    </p>
                    <p className="text-sm text-muted-foreground">{challengeStatus(c)}</p>
                  </li>
                ))}
              </ul>
            </section>
          </div>

          <NewChallengeDialog
            open={creating}
            onOpenChange={setCreating}
            filters={filters}
            trips={trips}
            albums={albums}
            onCreated={(c) => {
              setChallengeNotice(null);
              loadChallenges();
              setOpenChallenge(c.id);
            }}
          />
        </>
      )}

      {state === "playing" && round && (
        <RoundPlay key={roundNo} round={round} onSubmit={submitGuess} error={guessError} />
      )}

      {state === "result" && result && guess && (
        <RoundResult guess={guess} result={result} nextLabel="Next Round" onNext={startRound} />
      )}

      {openChallenge && (
        <ChallengeView
          key={openChallenge}
          challengeId={openChallenge}
          onClose={(why) => {
            setOpenChallenge(null);
            setChallengeNotice(why ?? null);
            // The player's standing in it has moved on.
            loadChallenges();
          }}
        />
      )}
    </div>
  );
}
