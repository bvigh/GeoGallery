import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { api } from "@/api/client";
import type { Album } from "@/types";
import { matchesSearch, SEARCH_MIN_ITEMS } from "@/lib/search";
import { byRecentUse } from "@/lib/albums";
import { SearchInput } from "@/components/SearchInput";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

// albums.name has CHECK (char_length(name) BETWEEN 1 AND 200).
const MAX_NAME_LENGTH = 200;

const NEW_ALBUM = "new";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  // What adding does, e.g. that a trip is a one-time snapshot.
  description: string;
  // Prefills the name of a new album, e.g. the trip being added.
  defaultName?: string;
  // Preselects this album when it is still listed, e.g. the one chosen last time.
  initialAlbumId?: string;
  confirmLabel?: string;
  // Called with the chosen album, existing or just created; the dialog then closes.
  // Adding to it is the caller's job, so one picker serves every kind of selection.
  onPick: (album: Album) => void;
}

export function AlbumPickerDialog({
  open,
  onOpenChange,
  title,
  description,
  defaultName = "",
  initialAlbumId,
  confirmLabel = "Add",
  onPick,
}: Props) {
  const radioName = useId();
  const [albums, setAlbums] = useState<Album[]>([]);
  const [status, setStatus] = useState<"loading" | "ready" | "failed">("loading");
  // An album id or NEW_ALBUM; null until the list loads or the user picks.
  const [choice, setChoice] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Bumped on every open and close so late responses for an old opening are dropped.
  const generation = useRef(0);

  useEffect(() => {
    const gen = ++generation.current;
    if (!open) return;
    setAlbums([]);
    setStatus("loading");
    setChoice(null);
    setNewName(defaultName.slice(0, MAX_NAME_LENGTH));
    setQuery("");
    setCreating(false);
    setError(null);
    api
      .listAlbums()
      .then((list) => {
        if (gen !== generation.current) return;
        setAlbums(byRecentUse(list));
        setStatus("ready");
        // Adding the same trip again is idempotent, so offer the album it went into
        // last time instead of a duplicate with the same name. The most recent album is
        // never preselected: on the Upload page that would silently share the uploads.
        const match =
          list.find((a) => a.id === initialAlbumId) ?? list.find((a) => a.name === defaultName.trim());
        setChoice((prev) => prev ?? (match ? match.id : NEW_ALBUM));
      })
      .catch((e) => {
        console.error("Failed to load albums:", e);
        if (gen !== generation.current) return;
        setStatus("failed");
        setChoice((prev) => prev ?? NEW_ALBUM);
      });
  }, [open, defaultName, initialAlbumId]);

  const pick = (album: Album) => {
    onPick(album);
    onOpenChange(false);
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (creating) return;
    if (choice !== NEW_ALBUM) {
      const album = albums.find((a) => a.id === choice);
      if (album) pick(album);
      return;
    }
    const name = newName.trim();
    if (!name) return;
    const gen = generation.current;
    setCreating(true);
    setError(null);
    try {
      const album = await api.createAlbum(name);
      // The user submitted, and the album now exists: finish the add even if the
      // dialog was closed meanwhile, or the album would be left empty for nothing.
      onPick(album);
      if (gen === generation.current) onOpenChange(false);
    } catch (err) {
      console.error("Failed to create album:", err);
      if (gen === generation.current) setError("Could not create album");
    } finally {
      if (gen === generation.current) setCreating(false);
    }
  };

  // The chosen album stays listed while searching, so the choice is never hidden.
  const visibleAlbums = albums.filter((a) => a.id === choice || matchesSearch(query, a.name, a.owner_name));

  const canSubmit =
    !creating && (choice === NEW_ALBUM ? newName.trim() !== "" : albums.some((a) => a.id === choice));

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle className="break-words">{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-3 text-sm">
          <fieldset className="space-y-2">
            <legend className="sr-only">Album</legend>
            {status === "loading" ? (
              <p className="text-muted-foreground">Loading albums…</p>
            ) : status === "failed" ? (
              <p className="text-red-500">Could not load albums</p>
            ) : (
              albums.length > 0 && (
                <>
                  {albums.length >= SEARCH_MIN_ITEMS && (
                    <SearchInput value={query} onChange={setQuery} label="Search albums" />
                  )}
                  {visibleAlbums.length === 0 && <p className="text-muted-foreground">No matching albums</p>}
                  {/* Phones held sideways scroll the whole dialog instead of a list nested inside it. */}
                  <div className="max-h-64 space-y-1 overflow-y-auto short:max-h-none">
                    {visibleAlbums.map((album) => (
                      <label key={album.id} className="flex cursor-pointer items-center gap-2 touch:min-h-11">
                        <input
                          type="radio"
                          className="shrink-0 touch:h-5 touch:w-5"
                          name={radioName}
                          value={album.id}
                          checked={choice === album.id}
                          onChange={() => setChoice(album.id)}
                        />
                        <span className="min-w-0 break-words">{album.name}</span>
                        {!album.is_owner && (
                          <span className="shrink-0 text-xs text-muted-foreground">
                            by {album.owner_name ?? "another user"}
                          </span>
                        )}
                      </label>
                    ))}
                  </div>
                </>
              )
            )}
            <label className="flex cursor-pointer items-center gap-2 touch:min-h-11">
              <input
                type="radio"
                className="shrink-0 touch:h-5 touch:w-5"
                name={radioName}
                value={NEW_ALBUM}
                checked={choice === NEW_ALBUM}
                onChange={() => setChoice(NEW_ALBUM)}
              />
              New album
            </label>
            <input
              maxLength={MAX_NAME_LENGTH}
              aria-label="New album name"
              placeholder="Album name"
              value={newName}
              onFocus={() => setChoice(NEW_ALBUM)}
              onChange={(e) => {
                setChoice(NEW_ALBUM);
                setNewName(e.target.value);
              }}
              className="w-full rounded-md border px-2 py-1 touch:py-2 touch:text-base"
            />
          </fieldset>
          {error && <p className="text-red-500">{error}</p>}
          <DialogFooter>
            <button
              type="submit"
              disabled={!canSubmit}
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
            >
              {creating ? "Creating…" : confirmLabel}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
