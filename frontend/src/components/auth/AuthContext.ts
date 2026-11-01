import { createContext, useContext } from "react";
import type { CurrentUser } from "@/types";

export const AuthContext = createContext<CurrentUser | null>(null);

export function useCurrentUser(): CurrentUser {
  const user = useContext(AuthContext);
  if (!user) throw new Error("useCurrentUser must be used inside AuthGate");
  return user;
}
