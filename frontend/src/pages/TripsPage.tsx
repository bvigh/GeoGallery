import { useEffect, useState } from "react";
import type { Trip } from "../types";
import { api } from "../api/client";
import { TripGalleryOverlay } from "../components/TripGalleryOverlay";

export function TripsPage() {
  const [trips, setTrips] = useState<Trip[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [selectedTrip, setSelectedTrip] = useState<Trip | null>(null);
  const [editName, setEditName] = useState("");
  const [editStartDate, setEditStartDate] = useState("");
  const [editEndDate, setEditEndDate] = useState("");

  useEffect(() => {
    api.get<Trip[]>("/trips").then((data) => setTrips(data ?? [])).catch(console.error);
  }, []);

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

  const saveUpdate = (id: string) => {
    setTrips((prev) =>
      prev.map((t) =>
        t.id === id
          ? {
              ...t,
              name: editName || t.name,
              start_date: editStartDate || null,
              end_date: editEndDate || null,
            }
          : t
      )
    );
    setEditing(null);
    api.patch(`/trips/${id}`, {
      name: editName || undefined,
      start_date: editStartDate || null,
      end_date: editEndDate || null,
    }).catch(console.error);
  };

  return (
    <div className="p-6">
      <h1 className="mb-4 text-xl font-semibold">Trips</h1>
      {trips.length === 0 && (
        <p className="text-muted-foreground">No trips yet. Upload some media to get started.</p>
      )}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {trips.map((trip) => (
          <div
            key={trip.id}
            className="rounded-lg border bg-card p-4 shadow-sm cursor-pointer hover:bg-accent transition-colors"
            onClick={() => editing !== trip.id && setSelectedTrip(trip)}
          >
            {editing === trip.id ? (
              <div className="flex flex-col gap-2">
                <div className="flex gap-2">
                  <input
                    className="flex-1 rounded border px-2 py-1 text-sm"
                    value={editName}
                    onChange={(e) => setEditName(e.target.value)}
                    onKeyDown={(e) => e.key === "Enter" && saveUpdate(trip.id)}
                    autoFocus
                  />
                  <button
                    className="rounded bg-primary px-2 py-1 text-xs text-primary-foreground"
                    onClick={() => saveUpdate(trip.id)}
                  >
                    Save
                  </button>
                </div>
                <div className="flex gap-2">
                  <input
                    type="date"
                    className="flex-1 rounded border px-2 py-1 text-sm"
                    value={editStartDate}
                    onChange={(e) => setEditStartDate(e.target.value)}
                  />
                  <input
                    type="date"
                    className="flex-1 rounded border px-2 py-1 text-sm"
                    value={editEndDate}
                    onChange={(e) => setEditEndDate(e.target.value)}
                  />
                </div>
              </div>
            ) : (
              <div className="flex items-center justify-between">
                <h2 className="font-medium">{trip.name}</h2>
                <div className="flex gap-2">
                  <button
                    className="text-xs text-muted-foreground hover:text-foreground"
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
                      className="text-xs text-destructive hover:text-destructive/80"
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
          </div>
        ))}
      </div>
      {selectedTrip && (
        <TripGalleryOverlay
          trip={selectedTrip}
          onClose={() => setSelectedTrip(null)}
        />
      )}
    </div>
  );
}
