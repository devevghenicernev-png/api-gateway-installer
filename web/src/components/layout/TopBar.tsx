import React, { useEffect, useState } from "react";
import { CircleDot, LogIn, LogOut, MonitorCog, Moon, Settings as SettingsIcon, Sun, MonitorSmartphone } from "lucide-react";
import { useAuth } from "../../auth/auth-context";
import { useStatus } from "../../hooks/useAdminQueries";
import { Button } from "../ui/button";
import { Badge } from "../ui/badge";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import { useTheme } from "./ThemeProvider";
import { fmtDuration } from "../../lib/utils";

export function TopBar({ sseStatus, onOpenSignIn, onOpenSettings }: {
  sseStatus: "connecting" | "open" | "reconnecting" | "closed";
  onOpenSignIn: () => void;
  onOpenSettings: () => void;
}) {
  const { token, signOut } = useAuth();
  const { data: status } = useStatus();
  const { theme, setTheme } = useTheme();

  // Tick once per second to keep the uptime label live without
  // re-fetching /api/status more than the existing 5s polling. We
  // snapshot Date.now() at the moment status?.uptime_sec changes (i.e.
  // each refetch) so the live offset is "seconds elapsed since the
  // last server-reported uptime" rather than relying on an undefined
  // sidecar field that produced `NaNs` in v0.5.0–v0.5.2.
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);
  const baseUptimeRef = React.useRef<{ value: number; at: number } | null>(null);
  if (status?.uptime_sec != null) {
    if (!baseUptimeRef.current || baseUptimeRef.current.value !== status.uptime_sec) {
      baseUptimeRef.current = { value: status.uptime_sec, at: Date.now() };
    }
  }
  const uptimeLabel = baseUptimeRef.current
    ? fmtDuration(baseUptimeRef.current.value + Math.floor((Date.now() - baseUptimeRef.current.at) / 1000))
    : "—";
  void tick; // consumed via Date.now() at render time

  const version = (status?.version || "").replace(/^v+/, "");
  const commit = status?.commit && status.commit !== "none" ? status.commit.slice(0, 7) : "";

  const connColor = {
    open: "text-success",
    connecting: "text-muted-foreground",
    reconnecting: "text-warning",
    closed: "text-destructive",
  }[sseStatus];

  return (
    <header className="flex h-14 items-center gap-3 border-b border-border bg-card px-4">
      <div className="flex items-center gap-2 font-semibold tracking-tight">
        <span className="text-primary">apigw</span>
        {version && (
          <span className="text-xs font-mono text-muted-foreground">
            v{version}{commit && <span> ({commit})</span>}
          </span>
        )}
      </div>

      <div className="hidden sm:flex items-center gap-3 ml-4 text-xs text-muted-foreground">
        <span className="tabular">
          <span className="opacity-70">uptime</span> <span className="text-foreground tabular-nums">{uptimeLabel}</span>
        </span>
        <span className="tabular">
          <span className="opacity-70">clients</span> <span className="text-foreground tabular-nums">{status?.sse_clients ?? 0}</span>
        </span>
        <span title={`SSE: ${sseStatus}`} className={connColor}>
          <CircleDot className="h-3.5 w-3.5" />
        </span>
      </div>

      <div className="ml-auto flex items-center gap-2">
        {token ? (
          <>
            <Badge variant="success">signed in</Badge>
            <Button variant="ghost" size="icon" onClick={onOpenSettings} aria-label="Settings">
              <SettingsIcon className="h-4 w-4" />
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" className="gap-1">
                  {theme === "system" ? <MonitorCog className="h-3.5 w-3.5" /> : theme === "dark" ? <Moon className="h-3.5 w-3.5" /> : <Sun className="h-3.5 w-3.5" />}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent>
                <DropdownMenuLabel>Theme</DropdownMenuLabel>
                <DropdownMenuItem onClick={() => setTheme("system")}>
                  <MonitorSmartphone className="h-4 w-4" /> System
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setTheme("light")}>
                  <Sun className="h-4 w-4" /> Light
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setTheme("dark")}>
                  <Moon className="h-4 w-4" /> Dark
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem onClick={signOut} destructive>
                  <LogOut className="h-4 w-4" /> Sign out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        ) : (
          <>
            {status?.sso_enabled && (
              <Button asChild variant="outline" size="sm">
                <a href="api/admin/sso/login">Sign in with SSO</a>
              </Button>
            )}
            <Button onClick={onOpenSignIn} size="sm" className="gap-1.5">
              <LogIn className="h-3.5 w-3.5" />
              Sign in
            </Button>
          </>
        )}
      </div>
    </header>
  );
}
