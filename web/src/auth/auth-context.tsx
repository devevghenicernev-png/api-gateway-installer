import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

// Bearer-token storage + signin / signout. Token is persisted in
// localStorage under a stable key — clearing the key (or clicking
// Sign out) is the only way to drop the session client-side. The
// dashboard daemon validates the token on every request, so even a
// leaked localStorage entry is bounded to the cfg.Security.AdminTokens
// life cycle.
//
// SessionResolver lives server-side (see internal/dashboard/server.go:
// SessionIdentity) and bridges the OIDC cookie path; this client only
// needs to know about bearer.
type AuthCtx = {
  token: string | null;
  signIn: (token: string) => void;
  signOut: () => void;
  isAuthed: boolean;
};
const Ctx = createContext<AuthCtx | null>(null);

const TOKEN_KEY = "apigw.token";

function readToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(readToken);

  // Cross-tab sync: another tab clicking Sign out should de-auth this
  // tab too. The `storage` event fires when localStorage changes from
  // a different document.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key === TOKEN_KEY) setToken(e.newValue);
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const signIn = useCallback((t: string) => {
    try {
      localStorage.setItem(TOKEN_KEY, t);
    } catch {
      // Ignore — token still works for this tab session.
    }
    setToken(t);
  }, []);

  const signOut = useCallback(() => {
    try {
      localStorage.removeItem(TOKEN_KEY);
    } catch {
      // Ignore.
    }
    setToken(null);
  }, []);

  const value = useMemo<AuthCtx>(
    () => ({ token, signIn, signOut, isAuthed: !!token }),
    [token, signIn, signOut],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth() {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider");
  return ctx;
}
