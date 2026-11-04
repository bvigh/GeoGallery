import { Search, X } from "lucide-react";
import { cn } from "@/lib/utils";

interface Props {
  value: string;
  onChange: (value: string) => void;
  label: string;
  placeholder?: string;
  autoFocus?: boolean;
  className?: string;
}

// A filter field for long lists. It filters as you type, so Enter does nothing:
// inside a form it would otherwise submit whatever happens to be chosen.
export function SearchInput({ value, onChange, label, placeholder = "Search…", autoFocus, className }: Props) {
  return (
    <div className={cn("relative", className)}>
      <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
      <input
        type="search"
        aria-label={label}
        placeholder={placeholder}
        autoFocus={autoFocus}
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") e.preventDefault();
        }}
        // 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
        className="w-full rounded-md border bg-background py-1 pl-7 pr-7 text-sm [&::-webkit-search-cancel-button]:hidden touch:py-2 touch:pr-11 touch:text-base"
      />
      {value && (
        <button
          type="button"
          onClick={() => onChange("")}
          aria-label="Clear search"
          className="absolute right-1.5 top-1/2 flex -translate-y-1/2 items-center justify-center rounded-sm p-0.5 text-muted-foreground hover:text-foreground touch:right-0 touch:h-11 touch:w-11"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  );
}
