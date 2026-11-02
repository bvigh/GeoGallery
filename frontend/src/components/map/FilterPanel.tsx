import { useId, useState, type ReactNode } from "react";
import type { Album, FilterState, MediaItem, Trip } from "@/types";
import { emptyFilters } from "@/lib/filters";
import { matchesSearch, SEARCH_MIN_ITEMS } from "@/lib/search";
import { groupTripsByYear } from "@/lib/trips";
import { SearchInput } from "@/components/SearchInput";

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

// Trips and albums are one filter: the backend matches media in any ticked collection.
// Ticked collections stay listed while searching, so they can always be unticked.
// Trips spanning more than one year sit under collapsible year headers, only the newest
// open by default. A search opens every year with a visible trip and hides the others,
// without touching the user's own choices, which come back when the query is cleared.
function Collections({ trips, albums, filters, onChange }: Pick<Props, "trips" | "albums" | "filters" | "onChange">) {
  const [query, setQuery] = useState("");
  const [expandedByUser, setExpandedByUser] = useState<ReadonlyMap<string, boolean>>(new Map());
  const searching = query.trim() !== "";
  const tripVisible = (t: Trip) =>
    filters.tripIds.includes(t.id) || matchesSearch(query, t.name, t.start_date, t.end_date);
  const visibleTrips = trips.filter(tripVisible);
  const yearGroups = groupTripsByYear(trips);
  const showUnassigned = filters.tripIds.includes("unassigned") || matchesSearch(query, "Unassigned");
  const visibleAlbums = albums.filter(
    (a) => filters.albumIds.includes(a.id) || matchesSearch(query, a.name, a.owner_name)
  );
  const tripsShown = trips.length > 0 && (showUnassigned || visibleTrips.length > 0);
  const tripCheckbox = (trip: Trip) => (
    <Checkbox
      key={trip.id}
      checked={filters.tripIds.includes(trip.id)}
      onChange={() => onChange({ ...filters, tripIds: toggle(filters.tripIds, trip.id) })}
      label={trip.name}
    />
  );

  return (
    <div>
      <SectionTitle>Collections</SectionTitle>
      {trips.length + albums.length >= SEARCH_MIN_ITEMS && (
        <SearchInput value={query} onChange={setQuery} label="Search collections" className="mb-2" />
      )}
      <div className="space-y-2 max-h-56 overflow-y-auto">
        {tripsShown && (
          <div role="group" aria-label="Trips" className="space-y-1">
            <p className="text-xs text-muted-foreground">Trips</p>
            {showUnassigned && (
              <Checkbox
                checked={filters.tripIds.includes("unassigned")}
                onChange={() => onChange({ ...filters, tripIds: toggle(filters.tripIds, "unassigned") })}
                label="Unassigned"
              />
            )}
            {yearGroups.length > 1
              ? yearGroups.map((group, i) => {
                  const key = group.year ?? "none";
                  const shown = group.trips.filter(tripVisible);
                  if (searching && shown.length === 0) return null;
                  const userOpen = expandedByUser.get(key) ?? i === 0;
                  const open = searching || userOpen;
                  const selected = group.trips.filter((t) => filters.tripIds.includes(t.id)).length;
                  return (
                    <div key={key} className="space-y-1">
                      <button
                        type="button"
                        aria-expanded={open}
                        disabled={searching}
                        onClick={() => setExpandedByUser((m) => new Map(m).set(key, !userOpen))}
                        className="flex w-full items-center gap-1 text-left text-sm hover:text-foreground disabled:cursor-default"
                      >
                        <span aria-hidden="true" className="w-3 text-xs text-muted-foreground">
                          {open ? "▾" : "▸"}
                        </span>
                        {group.year ?? "No date"}{" "}
                        <span className="text-xs text-muted-foreground">
                          ({group.trips.length}){selected > 0 && ` · ${selected} selected`}
                        </span>
                      </button>
                      {open && <div className="space-y-1 pl-4">{shown.map(tripCheckbox)}</div>}
                    </div>
                  );
                })
              : visibleTrips.map(tripCheckbox)}
          </div>
        )}
        {visibleAlbums.length > 0 && (
          <div role="group" aria-label="Albums" className="space-y-1">
            <p className="text-xs text-muted-foreground">Albums</p>
            {visibleAlbums.map((album) => (
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
        {!tripsShown && visibleAlbums.length === 0 && (
          <p className="text-sm text-muted-foreground">No matching trips or albums</p>
        )}
      </div>
    </div>
  );
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

          {(trips.length > 0 || albums.length > 0) && (
            <Collections trips={trips} albums={albums} filters={filters} onChange={onChange} />
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
