import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { SearchInput } from "@/components/SearchInput";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { CHALLENGE_ROUNDS, MAX_OTHER_PLAYERS } from "@/lib/challenge";
import { activeFilterCount, filterParams } from "@/lib/filters";
import { useCloseOnBack } from "@/lib/history";
import { matchesSearch, SEARCH_MIN_ITEMS } from "@/lib/search";
import type { ChallengeSummary, DirectoryUser, FilterState } from "@/types";

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const TOUCH_INPUT_CLASS = "touch:py-2 touch:text-base";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // The Game page's filters: the items are drawn from what they select.
  filters: FilterState;
  onCreated: (challenge: ChallengeSummary) => void;
}

// Makes a challenge: a name and the other players. The five items are drawn by the server from
// what the Game's filters select and every chosen player can already see, so the dialog cannot
// know beforehand whether there are enough: a refusal says how many there were.
export function NewChallengeDialog({ open, onOpenChange, filters, onCreated }: Props) {
  const me = useCurrentUser();
  const [name, setName] = useState("");
  const [users, setUsers] = useState<DirectoryUser[] | null>(null);
  const [usersFailed, setUsersFailed] = useState(false);
  const [chosen, setChosen] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const close = () => onOpenChange(false);
  useCloseOnBack(close, open);

  // Each opening starts empty and with today's user list.
  useEffect(() => {
    if (!open) return;
    setName("");
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
    const trimmed = name.trim();
    if (!trimmed || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      const created = await api.createChallenge(trimmed, chosen, filterParams(filters));
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

  const filterCount = activeFilterCount(filters);
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
            {CHALLENGE_ROUNDS} located items are drawn at random from what{" "}
            {filterCount === 0 ? "you can see" : "the Game's filters select"} and every player can already see. Each
            player guesses each of them once.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4 text-sm">
          <label className="block">
            <span className="mb-1 block font-medium">Name</span>
            <input
              required
              autoFocus
              maxLength={200}
              value={name}
              onChange={(e) => setName(e.target.value)}
              className={`w-full rounded-md border px-2 py-1 ${TOUCH_INPUT_CLASS}`}
            />
          </label>

          <p className="text-muted-foreground">
            {filterCount === 0
              ? "No filters are set on the Game page."
              : `Uses the Game page's filters (${filterCount} active).`}{" "}
            A challenge shares nothing: an item a player cannot see yet is never drawn.
          </p>

          <fieldset className="space-y-2">
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

          {error && (
            <p role="alert" className="text-red-500">
              {error}
            </p>
          )}
          <DialogFooter>
            <button
              type="submit"
              disabled={submitting || name.trim() === ""}
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
            >
              {submitting ? "Creating…" : "Create challenge"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
