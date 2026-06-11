import { useState } from "react";
import type { Trip, FilterState, MediaItem } from "../../types";

const ALL_MEDIA_TYPES: MediaItem["media_type"][] = ["photo", "video", "photo_360", "video_360"];

interface Props {
  trips: Trip[];
  filters: FilterState;
  onChange: (f: FilterState) => void;
  activeCount: number;
}

function Checkbox({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
}) {
  return (
    <label className="flex items-center gap-2 text-sm cursor-pointer">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="rounded"
      />
      {label}
    </label>
  );
}

export function FilterPanel({ trips, filters, onChange, activeCount }: Props) {
  const [open, setOpen] = useState(false);

  function toggleTrip(id: string) {
    const next = filters.tripIds.includes(id)
      ? filters.tripIds.filter((t) => t !== id)
      : [...filters.tripIds, id];
    onChange({ ...filters, tripIds: next });
  }

  function toggleMediaType(type: MediaItem["media_type"]) {
    const next = filters.mediaTypes.includes(type)
      ? filters.mediaTypes.filter((t) => t !== type)
      : [...filters.mediaTypes, type];
    onChange({ ...filters, mediaTypes: next });
  }

  function reset() {
    onChange({ fromDate: "", toDate: "", tripIds: [], mediaTypes: [] });
  }

  return (
    <div className="absolute left-4 top-4 z-[800]">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex items-center gap-1 rounded-lg border bg-background px-3 py-1.5 text-sm shadow-lg hover:bg-muted"
        aria-expanded={open}
      >
        ≡ Filters
        {activeCount > 0 && (
          <span className="ml-1 rounded-full bg-primary px-1.5 py-0.5 text-xs text-white leading-none">
            {activeCount}
          </span>
        )}
      </button>

      {open && (
        <div className="mt-1 w-64 rounded-lg border bg-background p-3 shadow-lg space-y-3">
          <div>
            <p className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">
              Date range
            </p>
            <div className="space-y-1">
              <input
                type="date"
                aria-label="From date"
                value={filters.fromDate}
                onChange={(e) => onChange({ ...filters, fromDate: e.target.value })}
                className="w-full rounded border px-2 py-1 text-sm"
              />
              <input
                type="date"
                aria-label="To date"
                value={filters.toDate}
                onChange={(e) => onChange({ ...filters, toDate: e.target.value })}
                className="w-full rounded border px-2 py-1 text-sm"
              />
            </div>
          </div>

          {trips.length > 0 && (
            <div>
              <p className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">
                Trips
              </p>
              <div className="space-y-1 max-h-40 overflow-y-auto">
                <Checkbox
                  checked={filters.tripIds.includes("unassigned")}
                  onChange={() => toggleTrip("unassigned")}
                  label="Unassigned"
                />
                {trips.map((trip) => (
                  <Checkbox
                    key={trip.id}
                    checked={filters.tripIds.includes(trip.id)}
                    onChange={() => toggleTrip(trip.id)}
                    label={trip.name}
                  />
                ))}
              </div>
            </div>
          )}

          <div>
            <p className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">
              Media type
            </p>
            <div className="space-y-1">
              {ALL_MEDIA_TYPES.map((type) => (
                <Checkbox
                  key={type}
                  checked={filters.mediaTypes.includes(type)}
                  onChange={() => toggleMediaType(type)}
                  label={type}
                />
              ))}
            </div>
          </div>

          {activeCount > 0 && (
            <button
              onClick={reset}
              className="w-full rounded border px-2 py-1 text-sm hover:bg-muted"
            >
              Reset filters
            </button>
          )}
        </div>
      )}
    </div>
  );
}
