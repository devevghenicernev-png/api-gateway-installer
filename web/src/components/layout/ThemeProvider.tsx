import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

// Three theme states: "system" (follow OS), "light" (force), "dark"
// (force). We persist the explicit choice in localStorage; "system"
// means no override → CSS `@media (prefers-color-scheme: dark)` rule in
// globals.css handles the switch. The data-theme attribute on <html>
// drives shadcn primitives that read it directly (a few of them do via
// `dark:` Tailwind variants which we map through tailwind.config).
type Theme = "system" | "light" | "dark";
type Ctx = { theme: Theme; setTheme: (t: Theme) => void; resolved: "light" | "dark" };
const ThemeCtx = createContext<Ctx | null>(null);

const STORAGE_KEY = "apigw.theme";

function getInitial(): Theme {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    // localStorage can be unavailable in third-party iframe / private
    // browsing → fall through to system default. Nothing breaks.
  }
  return "system";
}

function resolveSystem(): "light" | "dark" {
  if (typeof window === "undefined") return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(getInitial);
  const [systemResolved, setSystemResolved] = useState<"light" | "dark">(resolveSystem);

  // Watch OS-level theme changes when in "system" mode so the dashboard
  // flips alongside the rest of the desktop in real time.
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = () => setSystemResolved(mq.matches ? "dark" : "light");
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  const resolved = theme === "system" ? systemResolved : theme;

  // Apply data-theme to <html> for every render. We set it on <html>
  // (not <body>) so any portaled Radix popper that mounts to document
  // root still inherits the same palette.
  useEffect(() => {
    const root = document.documentElement;
    if (theme === "system") {
      root.removeAttribute("data-theme");
    } else {
      root.setAttribute("data-theme", theme);
    }
  }, [theme]);

  const setTheme = (t: Theme) => {
    setThemeState(t);
    try {
      if (t === "system") localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, t);
    } catch {
      // Ignore — explicit choice is still applied for this session.
    }
  };

  return <ThemeCtx.Provider value={{ theme, setTheme, resolved }}>{children}</ThemeCtx.Provider>;
}

export function useTheme() {
  const ctx = useContext(ThemeCtx);
  if (!ctx) throw new Error("useTheme must be used inside ThemeProvider");
  return ctx;
}
