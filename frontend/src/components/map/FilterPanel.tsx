import { useId, useState, type ReactNode } from "react";
import type { Album, FilterState, MediaItem, Trip } from "@/types";
import { emptyFilters } from "@/lib/filters";

const ALL_MEDIA_TYPES: MediaItem["media_type"][] = ["photo", "video", "photo_360", "video_360"];

const OWNER_OPTIONS: { value: FilterState["owner"]; label: string }[] = [
  { value: "all", label: "All" },
  { value: "mine", label: "Mine" },
  { value: "shared", label: "Shared with me" },
];

interface Props {
  trips: Trip[];
  albums: Album[];
  filters: FilterState;
  onChange: (f: FilterState) => void;
  activeCount: number;
}

function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <p className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">
      {children}
    </p>
  );
}

function Checkbox({
  checked,
  onChange,
  label,
  hint,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  hint?: string;
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
      {hint && <span className="text-xs text-muted-foreground"> {hint}</span>}
    </label>
  );
}

function toggle<T>(list: T[], value: T): T[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value];
}

export function FilterPanel({ trips, albums, filters, onChange, activeCount }: Props) {
  const [open, setOpen] = useState(false);
  const ownerRadioName = useId();

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
          <fieldset>
            <legend className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">
              Show
            </legend>
            <div className="flex flex-wrap gap-x-3 gap-y-1">
              {OWNER_OPTIONS.map((option) => (
                <label key={option.value} className="flex items-center gap-1.5 text-sm cursor-pointer">
                  <input
                    type="radio"
                    name={ownerRadioName}
                    value={option.value}
                    checked={filters.owner === option.value}
                    onChange={() => onChange({ ...filters, owner: option.value })}
                  />
                  {option.label}
                </label>
              ))}
            </div>
          </fieldset>

          <div>
            <SectionTitle>Date range</SectionTitle>
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

          {/* Trips and albums are one filter: the backend matches media in any ticked collection. */}
          {(trips.length > 0 || albums.length > 0) && (
            <div>
              <SectionTitle>Collections</SectionTitle>
              <div className="space-y-2 max-h-48 overflow-y-auto">
                {trips.length > 0 && (
                  <div role="group" aria-label="Trips" className="space-y-1">
                    <p className="text-xs text-muted-foreground">Trips</p>
                    <Checkbox
                      checked={filters.tripIds.includes("unassigned")}
                      onChange={() => onChange({ ...filters, tripIds: toggle(filters.tripIds, "unassigned") })}
                      label="Unassigned"
                    />
                    {trips.map((trip) => (
                      <Checkbox
                        key={trip.id}
                        checked={filters.tripIds.includes(trip.id)}
                        onChange={() => onChange({ ...filters, tripIds: toggle(filters.tripIds, trip.id) })}
                        label={trip.name}
                      />
                    ))}
                  </div>
                )}
                {albums.length > 0 && (
                  <div role="group" aria-label="Albums" className="space-y-1">
                    <p className="text-xs text-muted-foreground">Albums</p>
                    {albums.map((album) => (
                      <Checkbox
                        key={album.id}
                        checked={filters.albumIds.includes(album.id)}
                        onChange={() => onChange({ ...filters, albumIds: toggle(filters.albumIds, album.id) })}
                        label={album.name}
                        hint={album.is_owner ? undefined : `by ${album.owner_name ?? "another user"}`}
                      />
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          <div>
            <SectionTitle>Media type</SectionTitle>
            <div className="space-y-1">
              {ALL_MEDIA_TYPES.map((type) => (
                <Checkbox
                  key={type}
                  checked={filters.mediaTypes.includes(type)}
                  onChange={() => onChange({ ...filters, mediaTypes: toggle(filters.mediaTypes, type) })}
                  label={type}
                />
              ))}
            </div>
          </div>

          {activeCount > 0 && (
            <button
              onClick={() => onChange(emptyFilters)}
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
