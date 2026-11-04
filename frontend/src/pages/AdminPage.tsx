import { useCallback, useEffect, useState } from "react";
import { api } from "@/api/client";
import type { AdminUser } from "@/types";
import { CopyField } from "@/components/CopyField";
import { useCurrentUser } from "@/components/auth/AuthContext";

// The columns a phone has no room for: their values are folded into the name cell there.
const WIDE_CELL = "hidden py-2 sm:table-cell";

function formatDate(value: string | null): string {
  return value ? new Date(value).toLocaleString() : "-";
}

export function AdminPage() {
  const me = useCurrentUser();
  const [users, setUsers] = useState<AdminUser[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [inviteRole, setInviteRole] = useState<"user" | "admin">("user");
  const [inviteUrl, setInviteUrl] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(() => {
    api
      .adminListUsers()
      .then(setUsers)
      .catch((e) => {
        console.error(e);
        setError("Could not load users");
      });
  }, []);

  useEffect(load, [load]);

  const toggleDisabled = async (u: AdminUser) => {
    if (
      !u.disabled &&
      !confirm(`Disable ${u.email ?? u.display_name ?? "this user"}? Their sessions end and all their API tokens are revoked permanently.`)
    )
      return;
    setError(null);
    try {
      await api.adminSetDisabled(u.id, !u.disabled);
    } catch (e) {
      console.error(e);
      setError(`Could not ${u.disabled ? "enable" : "disable"} user`);
    }
    load();
  };

  const createInvite = async () => {
    setCreating(true);
    setError(null);
    setInviteUrl(null);
    try {
      setInviteUrl(await api.adminCreateInvite(inviteRole));
    } catch (e) {
      console.error(e);
      setError("Could not create invite");
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className="mx-auto h-full max-w-4xl overflow-y-auto p-4 sm:p-6">
      <h1 className="mb-4 text-xl font-semibold">Admin</h1>
      {error && <p className="mb-2 text-sm text-red-500">{error}</p>}

      <section className="mb-8">
        <h2 className="mb-2 text-lg font-medium">Invite</h2>
        <div className="flex flex-wrap items-center gap-2">
          <select
            aria-label="Invite role"
            value={inviteRole}
            onChange={(e) => setInviteRole(e.target.value as "user" | "admin")}
            // 16px on touch: iOS zooms the page into a smaller focused field and stays zoomed.
            className="rounded-md border px-2 py-1 text-sm touch:min-h-11 touch:text-base"
          >
            <option value="user">user</option>
            <option value="admin">admin</option>
          </select>
          <button
            onClick={createInvite}
            disabled={creating}
            className="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground disabled:opacity-50 touch:min-h-11"
          >
            {creating ? "Creating…" : "Create invite"}
          </button>
        </div>
        {inviteUrl && (
          <div className="mt-3 max-w-xl">
            <CopyField value={inviteUrl} label="Invite link" />
          </div>
        )}
      </section>

      <section>
        <h2 className="mb-2 text-lg font-medium">Users</h2>
        {users && (
          // Scrolls sideways rather than widening the page when the columns do not fit.
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="hidden border-b text-muted-foreground sm:table-header-group">
                <tr>
                  <th className="py-2 font-medium">Name</th>
                  <th className="py-2 font-medium">Email</th>
                  <th className="py-2 font-medium">Role</th>
                  <th className="py-2 font-medium">Last login</th>
                  <th className="py-2 font-medium">Status</th>
                  <th className="py-2" />
                </tr>
              </thead>
              <tbody>
                {users.map((u) => {
                  const status = u.disabled ? "disabled" : "active";
                  return (
                    <tr key={u.id} className="border-b">
                      <td className="py-2 [overflow-wrap:anywhere]">
                        {u.display_name ?? "-"}
                        <div className="mt-0.5 space-y-0.5 text-xs text-muted-foreground sm:hidden">
                          <p>
                            {u.email ?? "No email"} · {u.role} · {status}
                          </p>
                          <p>Last login {formatDate(u.last_login_at)}</p>
                        </div>
                      </td>
                      <td className={`${WIDE_CELL} [overflow-wrap:anywhere]`}>{u.email ?? "-"}</td>
                      <td className={WIDE_CELL}>{u.role}</td>
                      <td className={WIDE_CELL}>{formatDate(u.last_login_at)}</td>
                      <td className={WIDE_CELL}>{status}</td>
                      <td className="py-2 pl-2 text-right">
                        {u.id !== me.id && (
                          <button
                            onClick={() => toggleDisabled(u)}
                            aria-label={`${u.disabled ? "Enable" : "Disable"} ${u.email ?? u.display_name ?? u.id}`}
                            className="rounded-md border px-2 py-1 text-xs font-medium hover:bg-accent touch:min-h-11 touch:px-3 touch:text-sm"
                          >
                            {u.disabled ? "Enable" : "Disable"}
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
    </div>
  );
}
