import { useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { api, ApiError } from "@/api/client";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { FilterFields } from "@/components/map/FilterPanel";
import { SearchInput } from "@/components/SearchInput";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { CHALLENGE_ROUNDS, MAX_OTHER_PLAYERS, randomChallengeName } from "@/lib/challenge";
import { activeFilterCount, filterParams } from "@/lib/filters";
import { useCloseOnBack } from "@/lib/history";
import { matchesSearch, SEARCH_MIN_ITEMS } from "@/lib/search";
import type { Album, ChallengeSummary, DirectoryUser, FilterState, Trip } from "@/types";

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const TOUCH_INPUT_CLASS = "touch:py-2 touch:text-base";

// Browsers submit a form on Enter in any of its inputs, a checkbox, a radio button and a date field
// too. Among the players and the filters that would make the challenge half-way through choosing:
// only the name field and the button make it.
function keepEnterFromSubmitting(e: KeyboardEvent<HTMLElement>) {
  if (e.key === "Enter" && e.target instanceof HTMLInputElement) e.preventDefault();
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // The Game page's filters, which the dialog's own start from, and the trips and albums they
  // choose among.
  filters: FilterState;
  trips: Trip[];
  albums: Album[];
  onCreated: (challenge: ChallengeSummary) => void;
}

// Makes a challenge: the other players, then the filters its items are drawn from, then a name,
// which may be left out. The filters are the dialog's own: they start from the Game page's and
// change nothing there. The five items are drawn by the server from what the filters select and
// every chosen player can already see, so the dialog cannot know beforehand whether there are
// enough: a refusal says how many there were.
export function NewChallengeDialog({ open, onOpenChange, filters, trips, albums, onCreated }: Props) {
  const me = useCurrentUser();
  const [name, setName] = useState("");
  // What the challenge is called when the name is left empty: chosen when the dialog opens, so it
  // can be read before creating.
  const [fallbackName, setFallbackName] = useState("");
  const [draft, setDraft] = useState<FilterState>(filters);
  const [users, setUsers] = useState<DirectoryUser[] | null>(null);
  const [usersFailed, setUsersFailed] = useState(false);
  const [chosen, setChosen] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const nameHintId = useId();
  const pageFilters = useRef(filters);
  pageFilters.current = filters;

  const close = () => onOpenChange(false);
  useCloseOnBack(close, open);

  // Each opening starts empty, on the Game page's filters of that moment, with a new name to fall
  // back on and today's user list.
  useEffect(() => {
    if (!open) return;
    setName("");
    setFallbackName(randomChallengeName());
    setDraft(pageFilters.current);
    setChosen([]);
    setQuery("");
    setError(null);
    setUsers(null);
    setUsersFailed(false);
    let current = true;
    api
      .listUsers()
      .then((list) => current && setUsers(list.filter((u) => u.id !== me.id)))
      .catch((e) => {
        console.error(e);
        if (current) setUsersFailed(true);
      });
    return () => {
      current = false;
    };
  }, [open, me.id]);

  const toggle = (id: string) =>
    setChosen((ids) => (ids.includes(id) ? ids.filter((v) => v !== id) : [...ids, id]));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      const created = await api.createChallenge(name.trim() || fallbackName, chosen, filterParams(draft));
      onCreated(created);
      close();
    } catch (err) {
      console.error(err);
      if (err instanceof ApiError && (err.status === 409 || err.status === 400) && err.reason) {
        setError(err.reason);
      } else if (err instanceof ApiError && err.status === 404) {
        setError("One of the players is no longer available. Untick them and try again.");
      } else {
        setError("Could not create the challenge");
      }
    } finally {
      setSubmitting(false);
    }
  };

  const full = chosen.length >= MAX_OTHER_PLAYERS;
  // Ticked users stay listed while searching, so they can always be unticked.
  const visible = (users ?? []).filter(
    (u) => chosen.includes(u.id) || matchesSearch(query, u.display_name, u.email),
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>New challenge</DialogTitle>
          <DialogDescription>
            {CHALLENGE_ROUNDS} located items, the same for every player. Each player guesses each of them once.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-5 text-sm">
          <fieldset className="space-y-2" onKeyDown={keepEnterFromSubmitting}>
            <legend className="font-medium">Players</legend>
            <p className="text-muted-foreground">You always play. Tick who else does.</p>
            {usersFailed ? (
              <p className="text-red-500">Could not load the user list. You can still make a challenge for yourself.</p>
            ) : users === null ? (
              <p className="text-muted-foreground">Loading…</p>
            ) : users.length === 0 ? (
              <p className="text-muted-foreground">There are no other users yet.</p>
            ) : (
              <>
                {users.length >= SEARCH_MIN_ITEMS && (
                  <SearchInput
                    value={query}
                    onChange={setQuery}
                    label="Search users"
                    placeholder="Search by name or email…"
                  />
                )}
                {visible.length === 0 ? (
                  <p className="text-muted-foreground">No matching users.</p>
                ) : (
                  // Phones held sideways scroll the whole dialog instead of a list nested inside it.
                  <ul aria-label="Other players" className="max-h-56 space-y-1 overflow-y-auto short:max-h-none">
                    {visible.map((u) => {
                      const label = u.display_name ?? u.email ?? u.id;
                      const ticked = chosen.includes(u.id);
                      return (
                        <li key={u.id}>
                          <label className="flex cursor-pointer items-center gap-2 touch:min-h-11">
                            <input
                              type="checkbox"
                              checked={ticked}
                              disabled={!ticked && full}
                              onChange={() => toggle(u.id)}
                              className="h-4 w-4 shrink-0"
                            />
                            <span className="min-w-0 [overflow-wrap:anywhere]">
                              {label}
                              {u.display_name && u.email && (
                                <span className="ml-2 text-xs text-muted-foreground">{u.email}</span>
                              )}
                            </span>
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                )}
                {full && <p className="text-muted-foreground">A challenge has at most {MAX_OTHER_PLAYERS + 1} players.</p>}
              </>
            )}
          </fieldset>

          <fieldset className="space-y-2" onKeyDown={keepEnterFromSubmitting}>
            <legend className="font-medium">Items</legend>
            <p className="text-muted-foreground">
              Drawn from what these filters select, and only from items every player can already see: a challenge
              shares nothing. The rounds come from different places and times where the items allow, and from items
              the players have not had in a challenge first.
            </p>
            <div className="space-y-3">
              <FilterFields
                trips={trips}
                albums={albums}
                filters={draft}
                onChange={setDraft}
                activeCount={activeFilterCount(draft)}
              />
            </div>
          </fieldset>

          <div>
            <label className="block">
              <span className="mb-1 block font-medium">
                Name <span className="font-normal text-muted-foreground">(optional)</span>
              </span>
              <input
                maxLength={200}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={fallbackName}
                aria-describedby={nameHintId}
                className={`w-full rounded-md border px-2 py-1 ${TOUCH_INPUT_CLASS}`}
              />
            </label>
            <p id={nameHintId} className="mt-1 text-muted-foreground">
              Left empty, the challenge is called “{fallbackName}”.
            </p>
          </div>

          {error && (
            <p role="alert" className="text-red-500">
              {error}
            </p>
          )}
          <DialogFooter>
            <button
              type="submit"
              disabled={submitting}
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50 touch:min-h-11"
            >
              {submitting ? "Creating…" : "Create challenge"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
