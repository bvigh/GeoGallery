import { Fragment, memo, useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react";
import { SearchInput } from "@/components/SearchInput";
import {
  agreementCell,
  canResume,
  COMPARED_COLUMN_NOTES,
  compareStage,
  compareStatusText,
  DEFAULT_FILE_SORT,
  DISTANCE_COLUMN,
  FILE_COLUMN_NOTES,
  fileName,
  fileRows,
  kindName,
  matchesFile,
  nameParts,
  nextFileSort,
  sortFiles,
  spacingCell,
  type AgreementState,
  type CompareStage,
  type FileColumn,
  type FileRow,
  type FileSort,
  type SortDirection,
} from "@/lib/checks";
import { formatCount } from "@/lib/numbers";
import { SEARCH_MIN_ITEMS } from "@/lib/search";
import { formatInstant, YEAR_OUT_OF_RANGE } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { ChecksFile } from "@/types";

// Rows rendered at a time: a library can hold thousands of track files.
export const FILES_PAGE = 200;

// Compare files is busy while its comparison runs, never disabled: a disabled button would drop the
// keyboard focus to <body>.
const actionButton =
  "rounded border border-border px-3 py-1.5 text-sm hover:bg-muted aria-disabled:cursor-default aria-disabled:opacity-50 aria-disabled:hover:bg-transparent touch:min-h-11";
const inlineButton = "rounded border border-border px-2 py-1 text-xs hover:bg-muted touch:min-h-11 touch:px-3 touch:text-sm";

interface Column {
  id: FileColumn;
  label: string;
  numeric?: boolean;
}

const COLUMNS: Column[] = [
  { id: "file", label: "File" },
  { id: "format", label: "Format" },
  { id: "kind", label: "Kind" },
  { id: "points", label: "Points", numeric: true },
  { id: "first", label: "First" },
  { id: "last", label: "Last" },
  { id: "items_placed", label: "Items placed", numeric: true },
];
const COMPARED_COLUMNS: Column[] = [
  { id: "agreement", label: DISTANCE_COLUMN },
  { id: "spacing", label: "Spacing" },
];

type InstantText = (iso: string, timeZone: string) => string;

// formatInstant builds an Intl formatter on every call (about 70 µs), so formatting the first and
// last times of thousands of files at once would take more than a second. Each one is formatted
// when its row is first shown, and kept.
function useInstantText(): InstantText {
  const cache = useRef(new Map<string, string>());
  return useCallback((iso: string, timeZone: string) => {
    const key = `${timeZone} ${iso}`;
    let text = cache.current.get(key);
    if (text === undefined) {
      text = formatInstant(iso, timeZone);
      cache.current.set(key, text);
    }
    return text;
  }, []);
}

function SortIcon({ direction }: { direction: SortDirection | null }) {
  const Icon = direction === null ? ArrowUpDown : direction === "ascending" ? ArrowUp : ArrowDown;
  return <Icon className={cn("h-3 w-3 shrink-0", direction === null && "opacity-40")} aria-hidden="true" />;
}

// On a phone the table's rows stack: each cell is a line with its column's name.
function Cell({ label, numeric = false, children }: { label: string; numeric?: boolean; children: ReactNode }) {
  return (
    <td
      className={cn(
        "flex justify-between gap-3 py-0.5 lg:table-cell lg:px-2 lg:py-1 lg:align-top",
        numeric && "tabular-nums lg:text-right"
      )}
    >
      <span className="shrink-0 text-muted-foreground lg:hidden">{label}</span>
      <div className={cn("min-w-0 break-words text-right", !numeric && "lg:text-left")}>{children}</div>
    </td>
  );
}

// A name that wraps after its separators ("_", ".", "-") as well as at spaces, rather than inside a
// word. A part that still does not fit breaks anywhere (the cell's overflow-wrap), so no name can
// widen the table.
function BreakableName({ name }: { name: string }) {
  return (
    <>
      {nameParts(name).map((part, i) => (
        <Fragment key={i}>
          {i > 0 && <wbr />}
          {part}
        </Fragment>
      ))}
    </>
  );
}

// A formatted instant that never wraps inside its time. An Area/City zone name may go to a line of
// its own; a one-word zone ("UTC") stays with the time, where it does not read as a stray word.
function ZonedTime({ text, timeZone }: { text: string; timeZone: string }) {
  const zone = ` ${timeZone}`;
  if (!text.endsWith(zone) || !timeZone.includes("/")) return <span className="whitespace-nowrap">{text}</span>;
  return (
    <>
      <span className="whitespace-nowrap">{text.slice(0, -zone.length)}</span>{" "}
      <span className="whitespace-nowrap">{timeZone}</span>
    </>
  );
}

// An instant with no camera clock: UTC and the browser zone, each labelled. In the table each label
// sits above its time, so the two time columns stay narrow.
function InstantCell({ iso, text, timeZone }: { iso: string | null; text: InstantText; timeZone: string }) {
  if (iso === null) return <>{YEAR_OUT_OF_RANGE}</>;
  return (
    <dl className="space-y-0.5">
      <div className="flex justify-end gap-2 lg:flex-col lg:gap-0">
        <dt className="shrink-0 text-muted-foreground">UTC</dt>
        <dd>
          <ZonedTime text={text(iso, "UTC")} timeZone="UTC" />
        </dd>
      </div>
      <div className="flex justify-end gap-2 lg:flex-col lg:gap-0">
        <dt className="shrink-0 text-muted-foreground">Your browser</dt>
        <dd>
          <ZonedTime text={text(iso, timeZone)} timeZone={timeZone} />
        </dd>
      </div>
    </dl>
  );
}

interface FileLineProps {
  row: FileRow;
  compared: boolean; // whether the comparison's columns are shown
  stage: CompareStage;
  text: InstantText;
  timeZone: string;
}

// The source label holds an uploaded file name: it is only ever rendered as text. Memoised, so a
// keystroke in the search field leaves the rows it keeps alone.
const FileLine = memo(function FileLine({ row, compared, stage, text, timeZone }: FileLineProps) {
  const { file } = row;
  const distance = compared ? agreementCell(row, stage) : null;
  return (
    <tr className="block border-t border-border py-2 lg:table-row lg:py-0">
      <td
        // Not a tab stop: the focus comes here from the last Show more, which goes away.
        tabIndex={-1}
        className="block pb-1 font-medium [overflow-wrap:anywhere] lg:table-cell lg:min-w-[11rem] lg:px-2 lg:py-1 lg:align-top xl:min-w-[16rem]"
        title={file.source}
      >
        <BreakableName name={fileName(file.source)} />
      </td>
      <Cell label="Format">
        <BreakableName name={file.format} />
      </Cell>
      <Cell label="Kind">{kindName(file.kind)}</Cell>
      <Cell label="Points" numeric>
        {formatCount(file.points)}
      </Cell>
      <Cell label="First">
        <InstantCell iso={file.first} text={text} timeZone={timeZone} />
      </Cell>
      <Cell label="Last">
        <InstantCell iso={file.last} text={text} timeZone={timeZone} />
      </Cell>
      <Cell label="Items placed" numeric>
        {formatCount(file.items_placed)}
      </Cell>
      {distance && (
        <Cell label={DISTANCE_COLUMN}>
          {/* A distance stays on one line; "too few overlaps" and the like may wrap. */}
          <span className={cn(row.agreement?.median_m != null && "whitespace-nowrap")}>{distance.text}</span>
          {distance.detail && <span className="block text-muted-foreground">{distance.detail}</span>}
        </Cell>
      )}
      {compared && <Cell label="Spacing">{spacingCell(row, stage)}</Cell>}
    </tr>
  );
});

interface TrackFilesProps {
  files: ChecksFile[];
  agreement: AgreementState;
  onCompare: () => void; // compares every log file, from the first
  onResume: () => void; // goes on from where the comparison on screen stopped
  timeZone: string; // the browser's
}

// Every track file of the owner, sortable by any column. Sorting by Last brings logs dated far in
// the future to the top, and by the distance to other formats files that recorded a device
// somewhere else.
export function TrackFiles({ files, agreement, onCompare, onResume, timeZone }: TrackFilesProps) {
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<FileSort>(DEFAULT_FILE_SORT);
  const [limit, setLimit] = useState(FILES_PAGE);
  const text = useInstantText();
  const headingId = useId();
  const compareButton = useRef<HTMLButtonElement>(null);
  const body = useRef<HTMLTableSectionElement>(null);
  // The row the last Show more's focus goes to, once it is shown.
  const focusRow = useRef<number | null>(null);
  const compared = agreement.data;
  const comparing = agreement.status === "loading";
  const stage = compareStage(agreement);
  // The same row objects whatever the order and the search, so the memoised rows can skip a render.
  const rows = useMemo(() => fileRows(files, compared), [files, compared]);
  // Sorted once per order; a search only filters, which keeps it.
  const sorted = useMemo(() => sortFiles(rows, sort), [rows, sort]);
  const matching = useMemo(() => sorted.filter((row) => matchesFile(row.file, query)), [sorted, query]);
  const shown = matching.slice(0, limit);
  const columns = compared ? [...COLUMNS, ...COMPARED_COLUMNS] : COLUMNS;
  const notes = compared ? [...FILE_COLUMN_NOTES, ...COMPARED_COLUMN_NOTES] : FILE_COLUMN_NOTES;

  useEffect(() => {
    if (focusRow.current === null) return;
    const cell = body.current?.rows[focusRow.current]?.cells[0];
    focusRow.current = null;
    cell?.focus();
  }, [limit]);

  function showMore() {
    // The last page removes the button, and the focus would fall to <body>: it goes to the first row
    // the page added instead, where reading goes on.
    if (limit + FILES_PAGE >= matching.length) focusRow.current = limit;
    setLimit(limit + FILES_PAGE);
  }

  function resume() {
    // This button goes while the comparison runs; Compare files, busy meanwhile, keeps the focus.
    compareButton.current?.focus();
    onResume();
  }

  return (
    <section aria-labelledby={headingId} className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 id={headingId} className="text-sm font-semibold">
          Track files
        </h3>
        {files.length > 0 && (
          <button
            ref={compareButton}
            type="button"
            onClick={comparing ? undefined : onCompare}
            aria-disabled={comparing}
            className={actionButton}
          >
            {compared ? "Compare again" : "Compare files"}
          </button>
        )}
      </div>

      {files.length === 0 ? (
        <p className="text-sm text-muted-foreground">You have no track points.</p>
      ) : (
        <>
          {/* Prose keeps a readable width; the table below takes the whole section. */}
          <div className="max-w-6xl space-y-2">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              {/* One live region for the whole comparison: its progress, its end and why it stopped. */}
              <p role="status" className={cn("text-xs", agreement.status === "idle" && "text-muted-foreground")}>
                {compareStatusText(agreement)}
              </p>
              {canResume(agreement) && (
                <button type="button" onClick={resume} className={inlineButton}>
                  Compare the rest
                </button>
              )}
            </div>
            {/* Above the table: below it, the notes would sit after every row. */}
            <dl className="space-y-1 text-xs text-muted-foreground">
              {notes.map(([column, note]) => (
                <div key={column}>
                  <dt className="inline font-medium text-foreground">{column}: </dt>
                  <dd className="inline">{note}</dd>
                </div>
              ))}
            </dl>
          </div>
          {files.length >= SEARCH_MIN_ITEMS && (
            <SearchInput value={query} onChange={setQuery} label="Search track files" className="max-w-sm" />
          )}
          {matching.length === 0 ? (
            <p className="text-sm text-muted-foreground">No track file matches.</p>
          ) : (
            <div className="overflow-x-auto">
              <p className="pb-1 text-xs text-muted-foreground lg:hidden">Sort by</p>
              <table aria-labelledby={headingId} className="w-full text-left text-xs">
                <thead className="block lg:table-header-group">
                  <tr className="flex flex-wrap gap-1 pb-2 lg:table-row lg:pb-0">
                    {columns.map((column) => {
                      const direction = sort.column === column.id ? sort.direction : null;
                      return (
                        <th
                          key={column.id}
                          scope="col"
                          aria-sort={direction ?? undefined}
                          className={cn(
                            "font-normal lg:border-b lg:border-border lg:px-2 lg:py-1 lg:align-bottom",
                            column.numeric && "lg:text-right"
                          )}
                        >
                          <button
                            type="button"
                            onClick={() => setSort(nextFileSort(sort, column.id))}
                            className="inline-flex items-center gap-1 rounded border border-border px-2 py-1 text-left text-muted-foreground hover:text-foreground touch:min-h-11 lg:border-0 lg:p-0"
                          >
                            {column.label}
                            <SortIcon direction={direction} />
                          </button>
                        </th>
                      );
                    })}
                  </tr>
                </thead>
                <tbody ref={body} className="block lg:table-row-group">
                  {shown.map((row) => (
                    <FileLine
                      key={row.file.source}
                      row={row}
                      compared={compared !== null}
                      stage={stage}
                      text={text}
                      timeZone={timeZone}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {matching.length > shown.length && (
            <div className="flex flex-wrap items-center gap-2">
              <button type="button" onClick={showMore} className={actionButton}>
                Show more
              </button>
              <span className="text-xs text-muted-foreground">
                {formatCount(shown.length)} of {formatCount(matching.length)} shown
              </span>
            </div>
          )}
        </>
      )}
    </section>
  );
}
