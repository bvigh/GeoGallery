import { useEffect, useState, type ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { api } from "@/api/client";
import type { CurrentUser } from "@/types";
import { AuthContext } from "@/components/auth/AuthContext";
import { LoginScreen } from "@/components/auth/LoginScreen";

type GateState =
  | { status: "loading" }
  | { status: "anonymous" }
  | { status: "error" }
  | { status: "authenticated"; user: CurrentUser };

export function AuthGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<GateState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    api
      .me()
      .then((user) => {
        if (!cancelled) setState(user ? { status: "authenticated", user } : { status: "anonymous" });
      })
      .catch((e) => {
        console.error(e);
        if (!cancelled) setState({ status: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (state.status === "loading") {
    return (
      <div className="flex h-screen items-center justify-center" role="status" aria-label="Loading">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }
  if (state.status === "error") {
    return (
      <div className="flex h-screen items-center justify-center text-sm text-red-500">
        Could not reach the server. Reload the page to try again.
      </div>
    );
  }
  if (state.status === "anonymous") return <LoginScreen />;
  return <AuthContext.Provider value={state.user}>{children}</AuthContext.Provider>;
}
