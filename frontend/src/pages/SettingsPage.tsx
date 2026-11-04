import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError } from "@/api/client";
import type { ApiToken, CreatedApiToken } from "@/types";
import { CopyField } from "@/components/CopyField";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

const EXPIRY_OPTIONS = [30, 90, 365];
const DEFAULT_EXPIRY = 90;

function formatDate(value: string | null): string {
  return value ? new Date(value).toLocaleDateString() : "-";
}

// 16px text on touch: iOS zooms the page into a smaller focused field and stays zoomed.
const TOUCH_INPUT_CLASS = "touch:py-2 touch:text-base";

// The columns a phone has no room for: their values are folded into the name cell there.
const WIDE_CELL = "hidden py-2 sm:table-cell";

function tokenStatus(t: ApiToken): "active" | "revoked" | "expired" {
  if (t.revoked_at) return "revoked";
  if (new Date(t.expires_at).getTime() <= Date.now()) return "expired";
  return "active";
}

function CreateTokenDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [expiresInDays, setExpiresInDays] = useState(DEFAULT_EXPIRY);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<CreatedApiToken | null>(null);
  const generation = useRef(0);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      generation.current++;
      setName("");
      setExpiresInDays(DEFAULT_EXPIRY);
      setError(null);
      setCreated(null);
    }
    onOpenChange(next);
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (submitting) return;
    const gen = generation.current;
    setSubmitting(true);
    setError(null);
    try {
      const token = await api.createToken(name.trim(), expiresInDays);
      onCreated();
      if (gen === generation.current) setCreated(token);
    } catch (err) {
      console.error(err);
      if (gen !== generation.current) return;
      if (err instanceof ApiError && err.status === 409) {
        setError("Too many active tokens: revoke one first");
      } else {
        setError(err instanceof Error ? err.message : "Could not create token");
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent
        onInteractOutside={(e) => {
          if (created) e.preventDefault();
        }}
        onEscapeKeyDown={(e) => {
          if (created) e.preventDefault();
        }}
      >
        <DialogHeader>
          <DialogTitle>Create API token</DialogTitle>
          <DialogDescription>
            Tokens act as you for scripts. They cannot delete media or trips, manage tokens or use admin pages.
          </DialogDescription>
        </DialogHeader>
        {created ? (
          <div data-testid="one-time-token-panel" className="min-w-0 space-y-3 text-sm">
            <p className="font-medium text-red-600">You won't see this again. Copy the token now.</p>
            <CopyField value={created.token} label="New API token" />
            <div>
              <p className="mb-1 text-muted-foreground">Example:</p>
              <pre className="overflow-x-auto rounded bg-muted p-2 text-xs">
                {`export GG_TOKEN=<the token above>\ncurl -X POST ${window.location.origin}/api/gps-tracks/import -H "Authorization: Bearer $GG_TOKEN" -H "Content-Type: application/json" --data @points.json`}
              </pre>
            </div>
            <DialogFooter>
              <button
                type="button"
                onClick={() => handleOpenChange(false)}
                className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground"
              >
                Done
              </button>
            </DialogFooter>
          </div>
        ) : (
          <form onSubmit={handleSubmit} className="space-y-3 text-sm">
            <label className="block">
              <span className="mb-1 block font-medium">Name</span>
              <input
                required
                maxLength={100}
                value={name}
                onChange={(e) => setName(e.target.value)}
                className={`w-full rounded-md border px-2 py-1 ${TOUCH_INPUT_CLASS}`}
              />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Expires after</span>
              <select
                value={expiresInDays}
                onChange={(e) => setExpiresInDays(Number(e.target.value))}
                className={`w-full rounded-md border px-2 py-1 ${TOUCH_INPUT_CLASS}`}
              >
                {EXPIRY_OPTIONS.map((d) => (
                  <option key={d} value={d}>
                    {d} days
                  </option>
                ))}
              </select>
            </label>
            {error && <p className="text-red-500">{error}</p>}
            <DialogFooter>
              <button
                type="submit"
                disabled={submitting || name.trim() === ""}
                className="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
              >
                {submitting ? "Creating…" : "Create token"}
              </button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function SettingsPage() {
  const [tokens, setTokens] = useState<ApiToken[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);

  const load = useCallback(() => {
    api
      .listTokens()
      .then(setTokens)
      .catch((e) => {
        console.error(e);
        setError("Could not load tokens");
      });
  }, []);

  useEffect(load, [load]);

  const revoke = async (t: ApiToken) => {
    if (!confirm(`Revoke token "${t.name}"? Scripts using it will stop working.`)) return;
    setError(null);
    try {
      await api.revokeToken(t.id);
    } catch (e) {
      console.error(e);
      setError("Could not revoke token");
    }
    load();
  };

  return (
    <div className="mx-auto h-full max-w-4xl overflow-y-auto p-4 sm:p-6">
      <h1 className="mb-4 text-xl font-semibold">Settings</h1>
      <section>
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-lg font-medium">API tokens</h2>
          <button
            onClick={() => setDialogOpen(true)}
            className="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground touch:min-h-11"
          >
            Create token
          </button>
        </div>
        {error && <p className="mb-2 text-sm text-red-500">{error}</p>}
        {tokens && tokens.length === 0 && (
          <p className="text-sm text-muted-foreground">No API tokens yet.</p>
        )}
        {tokens && tokens.length > 0 && (
          // Scrolls sideways rather than widening the page when the columns do not fit.
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="hidden border-b text-muted-foreground sm:table-header-group">
                <tr>
                  <th className="py-2 font-medium">Name</th>
                  <th className="py-2 font-medium">Token</th>
                  <th className="py-2 font-medium">Created</th>
                  <th className="py-2 font-medium">Expires</th>
                  <th className="py-2 font-medium">Last used</th>
                  <th className="py-2 font-medium">Status</th>
                  <th className="py-2" />
                </tr>
              </thead>
              <tbody>
                {tokens.map((t) => {
                  const status = tokenStatus(t);
                  return (
                    <tr key={t.id} className="border-b">
                      <td className="py-2 [overflow-wrap:anywhere]">
                        {t.name}
                        <div className="mt-0.5 space-y-0.5 text-xs text-muted-foreground sm:hidden">
                          <p>
                            Token {t.display_hint}… · {status}
                          </p>
                          <p>
                            Created {formatDate(t.created_at)} · Expires {formatDate(t.expires_at)} · Last used{" "}
                            {formatDate(t.last_used_at)}
                          </p>
                        </div>
                      </td>
                      <td className={`${WIDE_CELL} font-mono text-xs`}>{t.display_hint}…</td>
                      <td className={WIDE_CELL}>{formatDate(t.created_at)}</td>
                      <td className={WIDE_CELL}>{formatDate(t.expires_at)}</td>
                      <td className={WIDE_CELL}>{formatDate(t.last_used_at)}</td>
                      <td className={WIDE_CELL}>{status}</td>
                      <td className="py-2 pl-2 text-right">
                        {status === "active" && (
                          <button
                            onClick={() => revoke(t)}
                            aria-label={`Revoke ${t.name}`}
                            className="rounded-md border px-2 py-1 text-xs font-medium text-red-600 hover:bg-red-50 touch:min-h-11 touch:px-3 touch:text-sm"
                          >
                            Revoke
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <CreateTokenDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onCreated={() => {
          setError(null);
          load();
        }}
      />
    </div>
  );
}
