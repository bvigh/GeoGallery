import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { api } from "@/api/client";
import type { Album } from "@/types";
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
  confirmLabel = "Add",
  onPick,
}: Props) {
  const radioName = useId();
  const [albums, setAlbums] = useState<Album[]>([]);
  const [status, setStatus] = useState<"loading" | "ready" | "failed">("loading");
  // An album id or NEW_ALBUM; null until the list loads or the user picks.
  const [choice, setChoice] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
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
    setCreating(false);
    setError(null);
    api
      .listAlbums()
      .then((list) => {
        if (gen !== generation.current) return;
        setAlbums(list);
        setStatus("ready");
        // Adding the same trip again is idempotent, so offer the album it went into
        // last time instead of a duplicate with the same name.
        const match = list.find((a) => a.name === defaultName.trim());
        setChoice((prev) => prev ?? (match ? match.id : NEW_ALBUM));
      })
      .catch((e) => {
        console.error("Failed to load albums:", e);
        if (gen !== generation.current) return;
        setStatus("failed");
        setChoice((prev) => prev ?? NEW_ALBUM);
      });
  }, [open, defaultName]);

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
                <div className="max-h-64 space-y-1 overflow-y-auto">
                  {albums.map((album) => (
                    <label key={album.id} className="flex cursor-pointer items-center gap-2">
                      <input
                        type="radio"
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
              )
            )}
            <label className="flex cursor-pointer items-center gap-2">
              <input
                type="radio"
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
              className="w-full rounded-md border px-2 py-1"
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
