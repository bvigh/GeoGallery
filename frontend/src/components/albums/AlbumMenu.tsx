import { useEffect, useRef, useState, type FormEvent } from "react";
import { AlbumIcon } from "lucide-react";
import { api } from "@/api/client";
import type { Album, MediaItem } from "@/types";
import { matchesSearch, SEARCH_MIN_ITEMS } from "@/lib/search";
import { byRecentUse } from "@/lib/albums";
import { SearchInput } from "@/components/SearchInput";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type AlbumRef = Pick<Album, "id" | "name">;

interface Props {
  item: MediaItem;
  // Only the media owner can put an item into albums (no re-sharing), so
  // everyone else gets a read-only list of the albums it reached them through.
  isOwn: boolean;
  // "panel" is a full-width text button (MapPage popup), "overlay" an icon
  // button for the dark gallery header.
  variant?: "panel" | "overlay";
  // Called once the server added the item to (added) or removed it from an album,
  // so hosts showing an album's media can follow.
  onChange?: (albumId: string, added: boolean) => void;
  // Called once an album was created from the menu, before the item is added to it.
  onAlbumCreated?: (album: Album) => void;
}

export function AlbumMenu({ item, isOwn, variant = "panel", onChange, onAlbumCreated }: Props) {
  const [open, setOpen] = useState(false);
  // Every album the caller belongs to; only loaded for their own items.
  const [albums, setAlbums] = useState<Album[]>([]);
  // The caller's albums that contain this item.
  const [containing, setContaining] = useState<AlbumRef[]>([]);
  const [status, setStatus] = useState<"loading" | "ready" | "failed">("loading");
  const [pending, setPending] = useState<string[]>([]);
  const [newName, setNewName] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Bumped on close and item change so late responses for an old load are dropped.
  const generation = useRef(0);

  useEffect(() => {
    generation.current++;
    setOpen(false);
  }, [item.id]);

  async function load() {
    const gen = ++generation.current;
    setStatus("loading");
    setError(null);
    setNewName(null);
    setQuery("");
    try {
      const [all, inAlbums] = await Promise.all([
        isOwn ? api.listAlbums() : Promise.resolve([]),
        api.mediaAlbums(item.id),
      ]);
      if (gen !== generation.current) return;
      // The albums the item is in come first, then the recently used ones. Sorted
      // once per load, so ticking does not move rows under the pointer.
      const inIds = new Set(inAlbums.map((a) => a.id));
      setAlbums(byRecentUse(all).sort((a, b) => Number(inIds.has(b.id)) - Number(inIds.has(a.id))));
      setContaining(inAlbums);
      setStatus("ready");
    } catch (e) {
      console.error("Failed to load albums:", e);
      if (gen === generation.current) setStatus("failed");
    }
  }

  function handleOpenChange(next: boolean) {
    if (next) load();
    else generation.current++;
    setOpen(next);
  }

  async function toggle(album: AlbumRef, add: boolean) {
    const without = (list: AlbumRef[]) => list.filter((a) => a.id !== album.id);
    setPending((p) => [...p, album.id]);
    setError(null);
    setContaining((prev) => (add ? [...without(prev), album] : without(prev)));
    try {
      if (add) await api.addToAlbum(album.id, { mediaIds: [item.id] });
      else await api.removeFromAlbum(album.id, { mediaIds: [item.id] });
    } catch (e) {
      console.error("Failed to update album:", e);
      setContaining((prev) => (add ? without(prev) : [...without(prev), album]));
      setError(add ? `Could not add to "${album.name}"` : `Could not remove from "${album.name}"`);
      return;
    } finally {
      setPending((p) => p.filter((id) => id !== album.id));
    }
    onChange?.(album.id, add);
  }

  async function handleCreate(e: FormEvent) {
    e.preventDefault();
    const name = newName?.trim() ?? "";
    if (!name || creating) return;
    setCreating(true);
    setError(null);
    try {
      const album = await api.createAlbum(name);
      // List the album before adding to it, so a failed add is one tick away from a retry.
      setAlbums((prev) => [...prev, album]);
      setNewName(null);
      onAlbumCreated?.(album);
      await toggle(album, true);
    } catch (err) {
      console.error("Failed to create album:", err);
      setError("Could not create album");
    } finally {
      setCreating(false);
    }
  }

  const isIn = (album: AlbumRef) => containing.some((a) => a.id === album.id);
  // Ticked albums stay listed while searching, so they can always be unticked.
  const visibleAlbums = albums.filter((a) => isIn(a) || matchesSearch(query, a.name, a.owner_name));

  const trigger =
    variant === "overlay" ? (
      <button
        className="text-white/60 hover:text-white"
        onClick={() => handleOpenChange(true)}
        aria-label="Albums"
        title="Albums"
      >
        <AlbumIcon className="h-5 w-5" />
      </button>
    ) : (
      <button
        className="mt-1 flex w-full items-center justify-center gap-1 rounded border border-border px-2 py-1 text-xs hover:bg-muted"
        onClick={() => handleOpenChange(true)}
      >
        <AlbumIcon className="h-3 w-3" />
        Albums
      </button>
    );

  return (
    <>
      {trigger}
      <Dialog open={open} onOpenChange={handleOpenChange}>
        {/* Keys stay in the dialog: the viewers behind it navigate and close on window keydown. */}
        <DialogContent className="max-w-sm" onKeyDown={(e) => e.stopPropagation()}>
          <DialogHeader>
            <DialogTitle>Albums</DialogTitle>
            <DialogDescription className="break-all">
              {isOwn
                ? `Choose the albums "${item.filename}" is in.`
                : `"${item.filename}" is shared with you through these albums.`}
            </DialogDescription>
          </DialogHeader>

          {status === "loading" ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : status === "failed" ? (
            <p className="text-sm text-red-500">Could not load albums</p>
          ) : isOwn ? (
            <div className="space-y-3 text-sm">
              {albums.length >= SEARCH_MIN_ITEMS && (
                <SearchInput value={query} onChange={setQuery} label="Search albums" />
              )}
              {albums.length === 0 ? (
                <p className="text-muted-foreground">No albums yet.</p>
              ) : visibleAlbums.length === 0 ? (
                <p className="text-muted-foreground">No matching albums.</p>
              ) : (
                <div role="group" aria-label="Your albums" className="max-h-64 space-y-1 overflow-y-auto">
                  {visibleAlbums.map((album) => (
                    <label key={album.id} className="flex items-center gap-2 cursor-pointer">
                      <input
                        type="checkbox"
                        className="rounded"
                        checked={isIn(album)}
                        disabled={pending.includes(album.id)}
                        onChange={(e) => toggle(album, e.target.checked)}
                      />
                      {album.name}
                      {!album.is_owner && (
                        <span className="text-xs text-muted-foreground">
                          by {album.owner_name ?? "another user"}
                        </span>
                      )}
                    </label>
                  ))}
                </div>
              )}

              {newName === null ? (
                <button
                  type="button"
                  className="text-primary hover:underline"
                  onClick={() => setNewName(query.trim().slice(0, 200))}
                >
                  New album…
                </button>
              ) : (
                <form onSubmit={handleCreate} className="flex gap-2">
                  <input
                    required
                    autoFocus
                    maxLength={200}
                    aria-label="New album name"
                    placeholder="Album name"
                    value={newName}
                    onChange={(e) => setNewName(e.target.value)}
                    className="min-w-0 flex-1 rounded-md border px-2 py-1"
                  />
                  <button
                    type="submit"
                    disabled={creating || newName.trim() === ""}
                    className="rounded-md bg-primary px-3 py-1 font-medium text-primary-foreground disabled:opacity-50"
                  >
                    Create
                  </button>
                </form>
              )}
            </div>
          ) : containing.length === 0 ? (
            <p className="text-sm text-muted-foreground">Not in any of your albums.</p>
          ) : (
            <ul aria-label="Shared through" className="max-h-64 space-y-1 overflow-y-auto text-sm">
              {containing.map((album) => (
                <li key={album.id}>{album.name}</li>
              ))}
            </ul>
          )}

          {error && <p className="text-sm text-red-500">{error}</p>}
        </DialogContent>
      </Dialog>
    </>
  );
}
