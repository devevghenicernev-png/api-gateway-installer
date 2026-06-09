import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// shadcn standard cn() helper.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// fmtDuration: 12345 → "3h 25m" / 90 → "1m 30s".
export function fmtDuration(seconds: number): string {
  if (seconds < 0) return "—";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

const RTF = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

// fmtRelative: ISO string → "3 minutes ago" / "in 2 days".
export function fmtRelative(iso: string | null | undefined): string {
  if (!iso) return "—";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return iso;
  const now = Date.now();
  const sec = Math.round((then - now) / 1000);
  const abs = Math.abs(sec);
  if (abs < 60) return RTF.format(sec, "second");
  if (abs < 3600) return RTF.format(Math.round(sec / 60), "minute");
  if (abs < 86400) return RTF.format(Math.round(sec / 3600), "hour");
  return RTF.format(Math.round(sec / 86400), "day");
}

// shortSHA: "abc123def…" → "abc123d" (7 chars, matches `git log --oneline`).
export function shortSHA(sha: string | null | undefined): string {
  if (!sha) return "—";
  return sha.slice(0, 7);
}

// Reliable copy-to-clipboard with execCommand fallback for non-secure
// contexts (LAN dashboards over plain HTTP).
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try {
      // execCommand is deprecated but is the only fallback that works
      // on http:// dashboards. We accept the deprecation warning rather
      // than failing silently.
      const ok = document.execCommand("copy");
      return ok;
    } catch {
      return false;
    } finally {
      ta.remove();
    }
  }
}

// Compute the API's public URL from current location + path. Used by
// the "open" and "copy" buttons on ApiCard / DeployCard.
export function publicURL(path: string): string {
  if (!path) return "";
  const { protocol, host } = window.location;
  return `${protocol}//${host}${path.startsWith("/") ? path : "/" + path}`;
}
