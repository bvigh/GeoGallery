import { useRef, useState } from "react";

type CopyStatus = "idle" | "copied" | "manual";

export function CopyField({ value, label }: { value: string; label: string }) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [status, setStatus] = useState<CopyStatus>("idle");

  const copy = async () => {
    const copied = navigator.clipboard?.writeText
      ? await navigator.clipboard.writeText(value).then(() => true, () => false)
      : false;
    if (copied) {
      setStatus("copied");
      return;
    }
    inputRef.current?.focus();
    inputRef.current?.select();
    setStatus("manual");
  };

  return (
    <div>
      <div className="flex gap-2">
        <input
          ref={inputRef}
          aria-label={label}
          readOnly
          autoComplete="off"
          spellCheck={false}
          value={value}
          onFocus={(e) => e.currentTarget.select()}
          className="min-w-0 flex-1 rounded-md border bg-muted px-2 py-1 font-mono text-xs"
        />
        <button
          type="button"
          onClick={copy}
          className="shrink-0 rounded-md border px-3 py-1 text-sm font-medium hover:bg-accent"
        >
          Copy
        </button>
      </div>
      {status === "copied" && <p className="mt-1 text-xs text-green-600">Copied</p>}
      {status === "manual" && <p className="mt-1 text-xs text-muted-foreground">Press Ctrl/Cmd+C to copy</p>}
    </div>
  );
}
