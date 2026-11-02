import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Plus } from "lucide-react";
import { api } from "@/api/client";
import { describeError } from "@/lib/errors";
import { matchesSearch, SEARCH_MIN_ITEMS } from "@/lib/search";
import type { Album, AlbumMember, DirectoryUser, MediaItem } from "@/types";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { MediaGalleryOverlay } from "@/components/MediaGalleryOverlay";
import { SearchInput } from "@/components/SearchInput";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

// GET /api/media returns at most this many rows per request.
const MEDIA_PAGE_SIZE = 500;

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function formatDay(value: string): string {
  // A bare date would parse as UTC midnight and could show as the day before.
  const date = /^\d{4}-\d{2}-\d{2}$/.test(value) ? new Date(`${value}T00:00`) : new Date(value);
  return date.toLocaleDateString();
}

// start_date and end_date are the earliest and latest taken_at in the album.
function formatDateRange(start: string | null, end: string | null): string {
  const days = [start, end].filter((d): d is string => d !== null).map(formatDay);
  if (days.length === 0) return "No dates";
  return days[0] === days[days.length - 1] ? days[0] : `${days[0]} – ${days[1]}`;
}

function userLabel(u: { display_name: string | null; email: string | null }, fallback: string): string {
  return u.display_name ?? u.email ?? fallback;
}

// The caller's own items in the album: exactly what leaving removes.
async function countOwnItems(albumId: string): Promise<number> {
  let total = 0;
  for (let offset = 0; ; offset += MEDIA_PAGE_SIZE) {
    const page = await api.get<MediaItem[] | null>("/media", {
      album_id: albumId,
      owner: "mine",
      offset: String(offset),
    });
    total += (page ?? []).length;
    if ((page ?? []).length < MEDIA_PAGE_SIZE) return total;
  }
}

function leaveMessage(name: string, ownItems: number | null): string {
  if (ownItems === 0) return `Leave "${name}"? You will lose access to its items.`;
  const items = ownItems === null ? "Your items" : `Your ${plural(ownItems, "item")}`;
  return `Leave "${name}"? ${items} will be removed from the album, and you will lose access to it.`;
}

function NewAlbumDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setName("");
      setError(null);
    }
    onOpenChange(next);
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      await api.createAlbum(trimmed);
      onCreated();
      handleOpenChange(false);
    } catch (err) {
      console.error(err);
      setError("Could not create album");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>New album</DialogTitle>
          <DialogDescription>
            Add members and media once it exists. Members see every item in the album.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-3 text-sm">
          <label className="block">
            <span className="mb-1 block font-medium">Name</span>
            <input
              required
              autoFocus
              maxLength={200}
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full rounded-md border px-2 py-1"
            />
          </label>
          {error && <p className="text-red-500">{error}</p>}
          <DialogFooter>
            <button
              type="submit"
              disabled={submitting || name.trim() === ""}
              className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
            >
              {submitting ? "Creating…" : "Create album"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function MembersPanel({
  album,
  onChanged,
  onLeft,
}: {
  album: Album;
  // Members or their contributions changed: the album's counts are stale.
  onChanged: () => void;
  // Called with the album the caller left, even if the panel closed meanwhile.
  onLeft: (albumId: string) => void;
}) {
  const me = useCurrentUser();
  const [members, setMembers] = useState<AlbumMember[] | null>(null);
  // The user directory; only the album owner adds members, so only they load it.
  const [directory, setDirectory] = useState<DirectoryUser[] | null>(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // False once the dialog closed. The panel is keyed by album, so this also covers
  // switching to another album's members.
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const loadMembers = useCallback(
    () =>
      api
        .listAlbumMembers(album.id)
        // The owner first, then everyone else in the backend's order.
        .then((list) => setMembers([...list].sort((a, b) => Number(b.is_owner) - Number(a.is_owner))))
        .catch((e) => {
          console.error(e);
          setError(describeError("Could not load members", e));
        }),
    [album.id]
  );

  useEffect(() => {
    loadMembers();
  }, [loadMembers]);

  useEffect(() => {
    if (!album.is_owner) return;
    api
      .listUsers()
      .then(setDirectory)
      .catch((e) => {
        console.error(e);
        setError("Could not load the user list");
      });
  }, [album.is_owner]);

  const candidates =
    directory && members ? directory.filter((u) => !members.some((m) => m.user_id === u.id)) : [];
  const visibleCandidates = candidates.filter((u) => matchesSearch(query, u.display_name, u.email));

  const addMember = async (u: DirectoryUser) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await api.addAlbumMember(album.id, u.id);
      await loadMembers();
      onChanged();
    } catch (err) {
      console.error(err);
      setError(describeError("Could not add member", err, "the user is not available, or the album no longer exists"));
    } finally {
      setBusy(false);
    }
  };

  const removeMember = async (m: AlbumMember) => {
    const name = userLabel(m, "this member");
    if (!confirm(`Remove ${name} from "${album.name}"? Their items will be removed from the album, and they will lose access to it.`))
      return;
    setBusy(true);
    setError(null);
    try {
      await api.removeAlbumMember(album.id, m.user_id);
      await loadMembers();
      onChanged();
    } catch (err) {
      console.error(err);
      setError(describeError("Could not remove member", err));
    } finally {
      setBusy(false);
    }
  };

  const leave = async () => {
    setBusy(true);
    setError(null);
    // The count only makes the confirm precise; leaving works without it.
    const ownItems = await countOwnItems(album.id).catch((e) => {
      console.error(e);
      return null;
    });
    // Counting a large album takes a while; a dialog closed meanwhile must not ask.
    if (!mounted.current) return;
    if (!confirm(leaveMessage(album.name, ownItems))) {
      setBusy(false);
      return;
    }
    try {
      await api.removeAlbumMember(album.id, me.id);
      onLeft(album.id);
    } catch (err) {
      console.error(err);
      setError(describeError("Could not leave album", err));
      setBusy(false);
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle className="break-words">Members of "{album.name}"</DialogTitle>
        <DialogDescription>
          {album.is_owner
            ? "Members see every item in the album and can add their own media."
            : `Owned by ${album.owner_name ?? "another user"}. Members see every item in the album.`}
        </DialogDescription>
      </DialogHeader>

      {members === null ? (
        !error && <p className="text-sm text-muted-foreground">Loading…</p>
      ) : (
        <ul aria-label="Members" className="max-h-64 space-y-1 overflow-y-auto text-sm">
          {members.map((m) => {
            const name = userLabel(m, "Unknown user");
            return (
              <li key={m.user_id} className="flex items-center justify-between gap-2">
                <span className="min-w-0 break-words">
                  {name}
                  {m.user_id === me.id && <span className="text-muted-foreground"> (you)</span>}
                  {m.is_owner && <span className="ml-2 text-xs text-muted-foreground">Owner</span>}
                </span>
                {album.is_owner && !m.is_owner && (
                  <button
                    onClick={() => removeMember(m)}
                    disabled={busy}
                    aria-label={`Remove ${name}`}
                    className="rounded-md border px-2 py-1 text-xs font-medium text-red-600 hover:bg-red-50 disabled:opacity-50"
                  >
                    Remove
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {album.is_owner && members && directory && (
        candidates.length === 0 ? (
          <p className="text-sm text-muted-foreground">Everyone in the user list is already a member.</p>
        ) : (
          <div className="space-y-2 text-sm">
            <p className="font-medium">Add members</p>
            {candidates.length >= SEARCH_MIN_ITEMS && (
              <SearchInput
                value={query}
                onChange={setQuery}
                label="Search users"
                placeholder="Search by name or email…"
              />
            )}
            {visibleCandidates.length === 0 ? (
              <p className="text-muted-foreground">No matching users.</p>
            ) : (
              <ul aria-label="Users to add" className="max-h-48 space-y-1 overflow-y-auto">
                {visibleCandidates.map((u) => {
                  const name = userLabel(u, u.id);
                  return (
                    <li key={u.id} className="flex items-center justify-between gap-2">
                      <span className="min-w-0 break-words">
                        {name}
                        {u.display_name && u.email && (
                          <span className="ml-2 text-xs text-muted-foreground">{u.email}</span>
                        )}
                      </span>
                      <button
                        onClick={() => addMember(u)}
                        disabled={busy}
                        aria-label={`Add ${name}`}
                        className="shrink-0 rounded-md border px-2 py-1 text-xs font-medium hover:bg-muted disabled:opacity-50"
                      >
                        Add
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        )
      )}

      {error && <p className="text-sm text-red-500">{error}</p>}

      {!album.is_owner && (
        <DialogFooter>
          <button
            onClick={leave}
            disabled={busy}
            className="rounded-lg border px-4 py-2 text-sm font-medium text-red-600 hover:bg-red-50 disabled:opacity-50"
          >
            Leave album
          </button>
        </DialogFooter>
      )}
    </>
  );
}

export function AlbumsPage() {
  const me = useCurrentUser();
  const [albums, setAlbums] = useState<Album[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  // Ids, not Album objects, so the gallery and dialog follow refreshed counts and names.
  const [galleryId, setGalleryId] = useState<string | null>(null);
  const [membersId, setMembersId] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");

  const load = useCallback(() => {
    api
      .listAlbums()
      .then(setAlbums)
      .catch((e) => {
        console.error(e);
        setError("Could not load albums");
      });
  }, []);

  useEffect(load, [load]);

  const galleryAlbum = albums?.find((a) => a.id === galleryId) ?? null;
  const membersAlbum = albums?.find((a) => a.id === membersId) ?? null;

  const startRename = (album: Album) => {
    setRenaming(album.id);
    setRenameValue(album.name);
  };

  const saveRename = async (album: Album) => {
    const name = renameValue.trim();
    setRenaming(null);
    if (!name || name === album.name) return;
    setError(null);
    setAlbums((prev) => prev?.map((a) => (a.id === album.id ? { ...a, name } : a)) ?? prev);
    try {
      await api.renameAlbum(album.id, name);
    } catch (e) {
      console.error(e);
      setError(describeError("Could not rename album", e));
    }
    load();
  };

  const deleteAlbum = async (album: Album) => {
    if (!confirm(`Delete album "${album.name}"? Media are not deleted. Members lose access to the items shared through this album.`))
      return;
    setError(null);
    try {
      await api.deleteAlbum(album.id);
    } catch (e) {
      console.error(e);
      setError(describeError("Could not delete album", e));
    }
    load();
  };

  // Rejecting keeps the item in the gallery, which shows the error; it stays here once the gallery closes.
  const removeItem = async (album: Album, item: MediaItem) => {
    setError(null);
    try {
      await api.removeFromAlbum(album.id, { mediaIds: [item.id] });
    } catch (e) {
      setError(describeError(`Could not remove "${item.filename}" from the album`, e));
      throw e;
    } finally {
      load();
    }
  };

  return (
    <div className="h-full overflow-y-auto p-6">
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Albums</h1>
        <button
          onClick={() => setCreating(true)}
          className="flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground"
        >
          <Plus className="h-4 w-4" />
          New album
        </button>
      </div>
      {error && <p className="mb-2 text-sm text-red-500">{error}</p>}
      {albums && albums.length === 0 && (
        <p className="text-muted-foreground">
          No albums yet. Create one to share media with other users, or ask someone to add you to theirs.
        </p>
      )}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {albums?.map((album) => (
          <article
            key={album.id}
            aria-label={album.name}
            className="cursor-pointer rounded-lg border bg-card p-4 shadow-sm transition-colors hover:bg-accent"
            onClick={() => renaming !== album.id && setGalleryId(album.id)}
          >
            {renaming === album.id ? (
              <form
                className="flex gap-2"
                onClick={(e) => e.stopPropagation()}
                onSubmit={(e) => {
                  e.preventDefault();
                  saveRename(album);
                }}
              >
                <input
                  required
                  autoFocus
                  maxLength={200}
                  aria-label="Album name"
                  value={renameValue}
                  onChange={(e) => setRenameValue(e.target.value)}
                  onKeyDown={(e) => e.key === "Escape" && setRenaming(null)}
                  className="min-w-0 flex-1 rounded border px-2 py-1 text-sm"
                />
                <button type="submit" className="rounded bg-primary px-2 py-1 text-xs text-primary-foreground">
                  Save
                </button>
                <button
                  type="button"
                  onClick={() => setRenaming(null)}
                  className="text-xs text-muted-foreground hover:text-foreground"
                >
                  Cancel
                </button>
              </form>
            ) : (
              <div className="flex items-start justify-between gap-2">
                <h2 className="min-w-0 font-medium">
                  {/* No handler: its click bubbles to the card, so keyboard users can open it too. */}
                  <button type="button" className="break-words text-left hover:underline">
                    {album.name}
                  </button>
                </h2>
                <div className="flex shrink-0 gap-2">
                  <button
                    className="text-xs text-muted-foreground hover:text-foreground"
                    aria-label={`Members of ${album.name}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setMembersId(album.id);
                    }}
                  >
                    Members
                  </button>
                  {album.is_owner && (
                    <>
                      <button
                        className="text-xs text-muted-foreground hover:text-foreground"
                        aria-label={`Rename ${album.name}`}
                        onClick={(e) => {
                          e.stopPropagation();
                          startRename(album);
                        }}
                      >
                        Rename
                      </button>
                      <button
                        className="text-xs text-destructive hover:text-destructive/80"
                        aria-label={`Delete ${album.name}`}
                        onClick={(e) => {
                          e.stopPropagation();
                          deleteAlbum(album);
                        }}
                      >
                        Delete
                      </button>
                    </>
                  )}
                </div>
              </div>
            )}
            <p className="mt-1 text-sm text-muted-foreground">
              Owner: {album.is_owner ? "You" : album.owner_name ?? "another user"}
            </p>
            <p className="text-sm text-muted-foreground">
              {plural(album.member_count, "member")} · {plural(album.media_count, "item")}
            </p>
            <p className="text-sm text-muted-foreground">{formatDateRange(album.start_date, album.end_date)}</p>
          </article>
        ))}
      </div>

      <NewAlbumDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={() => {
          setError(null);
          load();
        }}
      />

      <Dialog open={membersAlbum !== null} onOpenChange={(open) => !open && setMembersId(null)}>
        <DialogContent className="max-w-md">
          {membersAlbum && (
            <MembersPanel
              key={membersAlbum.id}
              album={membersAlbum}
              onChanged={load}
              onLeft={(id) => {
                // Another album's members may have been opened while leaving was in flight.
                setMembersId((cur) => (cur === id ? null : cur));
                load();
              }}
            />
          )}
        </DialogContent>
      </Dialog>

      {galleryAlbum && (
        <MediaGalleryOverlay
          title={galleryAlbum.name}
          subtitle={galleryAlbum.is_owner ? undefined : `by ${galleryAlbum.owner_name ?? "another user"}`}
          params={{ album_id: galleryAlbum.id }}
          onClose={() => {
            setGalleryId(null);
            load();
          }}
          // The album owner may remove any item, a member only their own.
          canRemoveFromAlbum={(item) => galleryAlbum.is_owner || item.owner_id === me.id}
          onRemoveFromAlbum={(item) => removeItem(galleryAlbum, item)}
        />
      )}
    </div>
  );
}
