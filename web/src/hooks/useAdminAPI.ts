import { useMemo } from "react";
import { useAuth } from "../auth/auth-context";
import { AdminAPI } from "../lib/api";

// Returns a fresh AdminAPI bound to the current bearer token. Memoized
// per-token so we don't churn the instance on unrelated re-renders.
export function useAdminAPI() {
  const { token } = useAuth();
  return useMemo(() => new AdminAPI(token), [token]);
}
