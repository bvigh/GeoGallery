import { useCallback, useEffect, useRef, useState } from "react";
import { ListChecks, X } from "lucide-react";
import type { Album, MediaItem, ShiftTimesResult, Trip } from "../types";
import { api, ApiError } from "../api/client";
import { MediaGalleryOverlay, type TimesChangedHint } from "@/components/MediaGalleryOverlay";
import { ShiftTimesDialog } from "@/components/ShiftTimesDialog";
import { AlbumPickerDialog } from "@/components/albums/AlbumPickerDialog";
import { useCurrentUser } from "@/components/auth/AuthContext";
import { ChecksView } from "@/components/checks/ChecksView";
import { useHistoryChange } from "@/lib/history";
import { LAST_VIEWED_CLASS, useLastViewed, useRevealLastViewed } from "@/lib/lastViewed";
import { currentRoute } from "@/lib/routes";
import { shiftedMessage } from "@/lib/timeEdit";

const byStartDateDesc = (a: Trip, b: Trip): number => {
  if (a.start_date === null && b.start_date === null) return 0;
  if (a.start_date === null) return 1;
  if (b.start_date === null) return -1;
  return b.start_date.localeCompare(a.start_date);
};

// One per "Add to album…", so concurrent adds each report their own result.
interface Notice {
  id: number;
  kind: "pending" | "success" | "error";
  text: string;
}

// Small text buttons on a mouse; finger-sized, with readable text, on touch.
const TEXT_BUTTON_CLASS =
  "text-xs text-muted-foreground hover:text-foreground touch:min-h-11 touch:min-w-11 touch:px-2 touch:text-sm";

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const INPUT_CLASS = "min-w-0 flex-1 rounded border px-2 py-1 text-sm touch:py-2 touch:text-base";

function items(n: number): string {
  return `${n} item${n !== 1 ? "s" : ""}`;
}

function addedMessage(album: Album, added: number): string {
  const text = `Added ${items(added)} to "${album.name}"`;
  // Adding a trip again only inserts what is new since last time.
  return added === 0 ? `${text}: everything in the trip is already in it` : text;
}

// 404: the caller is no longer a member of the album, or the trip is gone.
function addErrorMessage(trip: Trip, album: Album, err: unknown): string {
  const text = `Could not add "${trip.name}" to "${album.name}"`;
  return err instanceof ApiError && err.status === 404
    ? `${text}: the album or the trip is no longer available`
    : text;
}

const TRIP_GONE =
  "That trip is not available. Trips you have not edited get new links after each upload or time change.";
const TRIP_REGROUPED =
  "The time change regrouped that trip, so its gallery closed. Its items are in the trips listed here.";
const SHIFT_LOAD_FAILED = "Could not load the trip's items. Close this and try again.";
const SHIFT_OUTCOME_RELOADED =
  "The server did not answer a shift in time, and it may have been saved. The trips are loaded again: check the items' times before shifting again.";

// The items a trip's shift dialog works on, for the trip they were loaded for: a list left over
// from another trip must never reach the dialog, which takes its ticks from the first list.
interface ShiftLoad {
  tripId: string;
  items: MediaItem[] | null;
  error: string | null;
}

// The open gallery. Opened from the URL, its trip is known only once the list has loaded, and
// itemId is the item to open on.
interface Gallery {
  id: string;
  trip: Trip | null;
  itemId: string | null;
}

function galleryFromUrl(): Gallery | null {
  const route = currentRoute();
  return route.tab === "trips" && route.collectionId !== null
    ? { id: route.collectionId, trip: null, itemId: route.itemId }
    : null;
}

export function TripsPage() {
  const me = useCurrentUser();
  const [trips, setTrips] = useState<Trip[]>([]);
  const [tripsLoaded, setTripsLoaded] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [gallery, setGallery] = useState<Gallery | null>(galleryFromUrl);
  const [editName, setEditName] = useState("");
  const [editStartDate, setEditStartDate] = useState("");
  const [editEndDate, setEditEndDate] = useState("");
  const [pickerTrip, setPickerTrip] = useState<Trip | null>(null);
  const [shiftTrip, setShiftTrip] = useState<Trip | null>(null);
  const [shiftLoad, setShiftLoad] = useState<ShiftLoad | null>(null);
  // Mounted only once asked for: the checks take seconds on the server, so no visit runs them.
  const [checksOpen, setChecksOpen] = useState(false);
  const [notices, setNotices] = useState<Notice[]>([]);
  const nextNoticeId = useRef(0);
  const [lastViewed, rememberViewed] = useLastViewed(me.id, "trips");
  const lastViewedCard = useRef<HTMLElement>(null);
  useRevealLastViewed(lastViewedCard, tripsLoaded && gallery === null);
  // The open gallery as last rendered, for a time change's follow-up, which answers later.
  const galleryRef = useRef(gallery);
  galleryRef.current = gallery;

  useEffect(() => {
    api
      .get<Trip[]>("/trips")
      .then((data) => {
        setTrips(data ?? []);
        setTripsLoaded(true);
      })
      .catch(console.error);
  }, []);

  // The list again, after anything that changes trips. null when it could not be loaded.
  const reloadTrips = useCallback(async (): Promise<Trip[] | null> => {
    try {
      const data = (await api.get<Trip[]>("/trips")) ?? [];
      setTrips(data);
      return data;
    } catch (e) {
      console.error(e);
      return null;
    }
  }, []);

  // The shift dialog's items, loaded when it opens; closing it aborts the list in flight.
  useEffect(() => {
    if (shiftTrip === null) return;
    const tripId = shiftTrip.id;
    const controller = new AbortController();
    setShiftLoad({ tripId, items: null, error: null });
    api
      .listMedia({ trip_id: tripId }, { signal: controller.signal })
      .then((items) => {
        if (!controller.signal.aborted) setShiftLoad({ tripId, items, error: null });
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        console.error("Failed to load the trip's items:", e);
        setShiftLoad({ tripId, items: null, error: SHIFT_LOAD_FAILED });
      });
    return () => controller.abort();
  }, [shiftTrip]);

  const notify = useCallback((kind: Notice["kind"], text: string): number => {
    const id = nextNoticeId.current++;
    setNotices((prev) => [...prev, { id, kind, text }]);
    return id;
  }, []);

  // A gallery from the URL gets its trip's name, or closes when the trip is not the caller's (any
  // more): an upload regroups every trip nobody edited, under new IDs.
  useEffect(() => {
    if (!tripsLoaded || gallery === null || gallery.trip !== null) return;
    const trip = trips.find((t) => t.id === gallery.id);
    if (trip) {
      setGallery({ ...gallery, trip });
      return;
    }
    setGallery(null);
    notify("error", TRIP_GONE);
  }, [tripsLoaded, trips, gallery, notify]);

  // Back or forward onto a gallery's entry while none is open (forward after back): open it again
  // on the URL's item. It takes that entry over, as this runs before history is brought in line.
  // A gallery already open stays: back from a view over it (the evidence view) can land on an
  // entry still showing the trip it showed before a time change moved it to another, until the
  // history module writes the gallery's URL there again.
  useHistoryChange(() => {
    if (galleryRef.current !== null) return;
    const next = galleryFromUrl();
    if (next === null) return;
    const trip = trips.find((t) => t.id === next.id) ?? null;
    if (tripsLoaded && trip === null) {
      notify("error", TRIP_GONE);
      return;
    }
    setGallery((open) => open ?? { ...next, trip });
  });

  const deleteTrip = async (id: string) => {
    if (!confirm("Delete this trip? Its media will be unassigned and re-grouped automatically.")) return;
    setTrips((prev) => prev.filter((t) => t.id !== id));
    try {
      await api.delete(`/trips/${id}`);
      const refreshed = await api.get<Trip[]>("/trips");
      setTrips(refreshed ?? []);
    } catch (e) {
      console.error(e);
      const refreshed = await api.get<Trip[]>("/trips").catch(() => null);
      if (refreshed) setTrips(refreshed);
    }
  };

  const addTripToAlbum = async (trip: Trip, album: Album) => {
    const id = notify("pending", `Adding "${trip.name}" to "${album.name}"…`);
    const settle = (kind: Notice["kind"], text: string) =>
      setNotices((prev) => prev.map((n) => (n.id === id ? { ...n, kind, text } : n)));
    try {
      const { added } = await api.addToAlbum(album.id, { tripId: trip.id });
      settle("success", addedMessage(album, added));
    } catch (e) {
      console.error(e);
      settle("error", addErrorMessage(trip, album, e));
    }
  };

  const dismissNotice = (id: number) => setNotices((prev) => prev.filter((n) => n.id !== id));

  // Trip detection ran, so the list changes: trips nobody edited get new IDs.
  const handleShifted = (result: ShiftTimesResult, deltaSeconds: number) => {
    notify(result.followup_failed ? "error" : "success", shiftedMessage(result, deltaSeconds));
    void reloadTrips();
  };

  // A shift failed without an answer and may have been saved, so the trips may have changed too.
  const handleShiftOutcomeUnknown = () => {
    notify("error", SHIFT_OUTCOME_RELOADED);
    void reloadTrips();
  };

  // A time change in the gallery ran trip detection, which gives every trip nobody edited a new
  // ID. A trip still listed (a named one keeps its ID and its media) keeps its gallery; one that is
  // gone moves the gallery to the trip the change put the item in, on the same entry and item, or
  // closes it and says why.
  const followTimeChange = async (from: string, hint: TimesChangedHint) => {
    const fresh = await reloadTrips();
    const open = galleryRef.current;
    // Closed, or showing another trip, by now.
    if (fresh === null || open === null || open.id !== from) return;
    const still = fresh.find((t) => t.id === from);
    if (still) {
      setGallery({ ...open, trip: still });
      return;
    }
    const next = hint.tripId === null ? undefined : fresh.find((t) => t.id === hint.tripId);
    if (next) {
      setGallery({ id: next.id, trip: next, itemId: hint.itemId });
      return;
    }
    setGallery(null);
    notify("error", TRIP_REGROUPED);
  };

  const saveUpdate = (id: string) => {
    setTrips((prev) =>
      prev
        .map((t) =>
          t.id === id
            ? {
                ...t,
                name: editName || t.name,
                start_date: editStartDate || null,
                end_date: editEndDate || null,
              }
            : t
        )
        .sort(byStartDateDesc)
    );
    setEditing(null);
    api.patch(`/trips/${id}`, {
      name: editName || undefined,
      start_date: editStartDate || null,
      end_date: editEndDate || null,
    }).catch(console.error);
  };

  return (
    <div className="h-full overflow-y-auto p-4 sm:p-6">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-xl font-semibold">Trips</h1>
        <button
          type="button"
          onClick={() => setChecksOpen(true)}
          title="Find suspect track files, formats and items"
          className="flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm font-medium hover:bg-muted touch:min-h-11"
        >
          <ListChecks className="h-4 w-4" aria-hidden="true" />
          Checks
        </button>
      </div>
      {trips.length === 0 && (
        <p className="text-muted-foreground">No trips yet. Upload some media to get started.</p>
      )}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {trips.map((trip) => (
          <article
            key={trip.id}
            ref={trip.id === lastViewed ? lastViewedCard : undefined}
            aria-label={trip.name}
            aria-current={trip.id === lastViewed || undefined}
            className={`rounded-lg border bg-card p-4 shadow-sm cursor-pointer hover:bg-accent transition-colors ${
              trip.id === lastViewed ? LAST_VIEWED_CLASS : ""
            }`}
            onClick={() => editing !== trip.id && setGallery({ id: trip.id, trip, itemId: null })}
          >
            {editing === trip.id ? (
              <div className="flex flex-col gap-2">
                <div className="flex gap-2">
                  <input
                    aria-label="Trip name"
                    className={INPUT_CLASS}
                    value={editName}
                    onChange={(e) => setEditName(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") saveUpdate(trip.id);
                      else if (e.key === "Escape") setEditing(null);
                    }}
                    autoFocus
                  />
                  <button
                    className="shrink-0 rounded bg-primary px-2 py-1 text-xs text-primary-foreground touch:min-h-11 touch:px-3 touch:text-sm"
                    onClick={() => saveUpdate(trip.id)}
                  >
                    Save
                  </button>
                  {/* Escape cancels too, but a phone has no Escape key. */}
                  <button className={`shrink-0 ${TEXT_BUTTON_CLASS}`} onClick={() => setEditing(null)}>
                    Cancel
                  </button>
                </div>
                <div className="flex flex-wrap gap-2">
                  <input
                    type="date"
                    aria-label="Start date"
                    className={`basis-32 ${INPUT_CLASS}`}
                    value={editStartDate}
                    onChange={(e) => setEditStartDate(e.target.value)}
                  />
                  <input
                    type="date"
                    aria-label="End date"
                    className={`basis-32 ${INPUT_CLASS}`}
                    value={editEndDate}
                    onChange={(e) => setEditEndDate(e.target.value)}
                  />
                </div>
              </div>
            ) : (
              // A long name takes the whole line and pushes the actions below it. On a narrow phone
              // the actions of a named trip (four of them) wrap rather than overflow the card.
              <div className="flex flex-wrap items-center justify-between gap-x-2">
                <h2 className="min-w-0 font-medium [overflow-wrap:anywhere]">{trip.name}</h2>
                <div className="ml-auto flex max-w-full shrink-0 flex-wrap justify-end gap-2 touch:gap-0">
                  <button
                    className={TEXT_BUTTON_CLASS}
                    onClick={(e) => {
                      e.stopPropagation();
                      setPickerTrip(trip);
                    }}
                  >
                    Add to album…
                  </button>
                  <button
                    className={TEXT_BUTTON_CLASS}
                    onClick={(e) => {
                      e.stopPropagation();
                      setShiftLoad(null);
                      setShiftTrip(trip);
                    }}
                  >
                    Shift times…
                  </button>
                  <button
                    className={TEXT_BUTTON_CLASS}
                    onClick={(e) => {
                      e.stopPropagation();
                      setEditing(trip.id);
                      setEditName(trip.name);
                      setEditStartDate(trip.start_date ?? "");
                      setEditEndDate(trip.end_date ?? "");
                    }}
                  >
                    Edit
                  </button>
                  {trip.user_edited && (
                    <button
                      className="text-xs text-destructive hover:text-destructive/80 touch:min-h-11 touch:min-w-11 touch:px-2 touch:text-sm"
                      onClick={(e) => {
                        e.stopPropagation();
                        deleteTrip(trip.id);
                      }}
                    >
                      Delete
                    </button>
                  )}
                </div>
              </div>
            )}
            <p className="mt-1 text-sm text-muted-foreground">
              {trip.start_date} – {trip.end_date ?? "ongoing"}
            </p>
            <p className="text-sm text-muted-foreground">
              {trip.media_count} item{trip.media_count !== 1 ? "s" : ""}
            </p>
          </article>
        ))}
      </div>
      {/* Outside the cards: React events bubble out of the dialog's portal into its parent,
          and a card click opens the gallery. */}
      <AlbumPickerDialog
        open={pickerTrip !== null}
        onOpenChange={(open) => !open && setPickerTrip(null)}
        title={`Add "${pickerTrip?.name ?? ""}" to an album`}
        description="Adds the trip's current media. Adding it again later picks up new items."
        defaultName={pickerTrip?.name ?? ""}
        onPick={(album) => pickerTrip && addTripToAlbum(pickerTrip, album)}
      />
      <ShiftTimesDialog
        open={shiftTrip !== null}
        onOpenChange={(open) => !open && setShiftTrip(null)}
        title={`Shift times in "${shiftTrip?.name ?? ""}"`}
        items={shiftLoad !== null && shiftLoad.tripId === shiftTrip?.id ? shiftLoad.items : null}
        loadError={shiftLoad !== null && shiftLoad.tripId === shiftTrip?.id ? shiftLoad.error : null}
        currentUserId={me.id}
        onShifted={handleShifted}
        onOutcomeUnknown={handleShiftOutcomeUnknown}
      />
      {notices.length > 0 && (
        <div className="fixed bottom-4 left-1/2 z-[900] flex w-[min(28rem,calc(100%-2rem))] -translate-x-1/2 flex-col gap-2">
          {notices.map((n) => (
            <div
              key={n.id}
              role={n.kind === "error" ? "alert" : "status"}
              className={`flex items-start gap-2 rounded-lg border bg-background px-3 py-2 text-sm shadow-lg ${
                n.kind === "error" ? "border-red-300 text-red-500" : ""
              }`}
            >
              <span className="min-w-0 flex-1 break-words">{n.text}</span>
              {/* A pending add has no result yet; dismissing it would hide the outcome. */}
              {n.kind !== "pending" && (
                <button
                  onClick={() => dismissNotice(n.id)}
                  aria-label="Dismiss"
                  // The negative margins keep the notice one line high around a finger-sized button.
                  className="flex shrink-0 items-center justify-center text-muted-foreground hover:text-foreground touch:-my-2 touch:-mr-2 touch:h-11 touch:w-11"
                >
                  <X className="h-4 w-4" />
                </button>
              )}
            </div>
          ))}
        </div>
      )}
      {gallery && (
        <MediaGalleryOverlay
          title={gallery.trip?.name ?? ""}
          params={{ trip_id: gallery.id }}
          route={{ tab: "trips", collectionId: gallery.id }}
          initialItemId={gallery.itemId}
          onClose={() => {
            rememberViewed(gallery.id);
            setGallery(null);
          }}
          onMediaDeleted={() => void reloadTrips()}
          onTimesChanged={(hint) => void followTimeChange(gallery.id, hint)}
        />
      )}
      {/* A time changed there ran trip detection, which gives trips nobody edited new IDs: the
          cards would open galleries of trips that are gone. */}
      {checksOpen && (
        <ChecksView
          onClose={() => {
            setChecksOpen(false);
            void reloadTrips();
          }}
        />
      )}
    </div>
  );
}
