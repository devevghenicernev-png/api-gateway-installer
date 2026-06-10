import { useState } from "react";
import { Copy, ExternalLink, Network, Pause, Play, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useApis, useDeploys, useRemoveApi, useToggleApi } from "../../hooks/useAdminQueries";
import type { API, Deploy } from "../../lib/types";
import { copyText, publicURL } from "../../lib/utils";
import { Button } from "../ui/button";
import { Badge } from "../ui/badge";
import { Skeleton } from "../ui/skeleton";
import { EmptyState } from "../ui/empty-state";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "../ui/alert-dialog";
import { useAuth } from "../../auth/auth-context";

// Unified row used both for plain APIs (proxy-only) and for Deploys
// (git-backed services). Operators think of both as "an API I expose at
// some path", so the panel merges them into one list — deploys carry a
// `deploy` badge and skip the toggle/remove actions (those live in the
// Deployments panel since they have build/SSH/secrets consequences).
type Row =
  | { kind: "api"; name: string; path: string; port: number; enabled: boolean; raw: API }
  | { kind: "deploy"; name: string; path: string; port: number; enabled: boolean; status?: string };

export function ApisPanel({ onAdd }: { onAdd: () => void }) {
  const { isAuthed } = useAuth();
  const { data: apis, isLoading: apisLoading } = useApis();
  const { data: deploys, isLoading: deploysLoading } = useDeploys();
  const toggle = useToggleApi();
  const remove = useRemoveApi();
  const [confirmDelete, setConfirmDelete] = useState<API | null>(null);

  if (!isAuthed) {
    return <EmptyState icon={<Network className="h-6 w-6" />} title="Sign in to view APIs" description="Bearer token from your config.yaml." />;
  }
  if (apisLoading || deploysLoading) {
    return (
      <div className="space-y-2">
        {[0, 1].map((i) => <Skeleton key={i} className="h-14 w-full" />)}
      </div>
    );
  }
  const rows: Row[] = [
    ...(apis ?? []).map<Row>((a) => ({
      kind: "api" as const, name: a.Name, path: a.Path || `/api/${a.Name}`,
      port: a.Port, enabled: a.Enabled, raw: a,
    })),
    ...(deploys ?? []).map<Row>((d: Deploy) => ({
      kind: "deploy" as const, name: d.Name, path: d.Path || `/apps/${d.Name}`,
      port: d.Port, enabled: d.Enabled, status: d.LastStatus,
    })),
  ];
  if (rows.length === 0) {
    return (
      <EmptyState
        icon={<Network className="h-6 w-6" />}
        title="No APIs configured"
        description={<>Register a proxy API with <code className="text-[11px]">apigw api add</code>, or a git-backed deployment with <code className="text-[11px]">apigw deploy add</code> — or use <strong>+ Add</strong>.</>}
        primaryAction={{ label: "+ Add API", onClick: onAdd }}
      />
    );
  }
  return (
    <>
      <ul className="space-y-1.5">
        {rows.map((r) => (
          <li
            key={`${r.kind}:${r.name}`}
            className="grid grid-cols-[1fr_auto] items-center gap-x-2 gap-y-1 rounded-md border border-border bg-card px-3 py-2 sm:grid-cols-[minmax(0,1fr)_auto_auto_auto_auto_auto_auto]"
          >
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="font-semibold text-sm truncate">{r.name}</span>
                <Badge variant={r.enabled ? "success" : "muted"}>{r.enabled ? "enabled" : "disabled"}</Badge>
                {r.kind === "deploy" && <Badge variant="muted">deploy</Badge>}
              </div>
              <div className="font-mono text-xs text-muted-foreground truncate">{r.path} → :{r.port}</div>
            </div>
            <Button asChild size="icon" variant="ghost" aria-label={`Open ${r.name} in new tab`} title="Open in new tab">
              <a href={publicURL(r.path)} target="_blank" rel="noopener noreferrer"><ExternalLink className="h-4 w-4" /></a>
            </Button>
            <Button
              size="icon" variant="ghost" aria-label={`Copy URL of ${r.name}`} title="Copy URL"
              onClick={async () => {
                const url = publicURL(r.path);
                const ok = await copyText(url);
                ok ? toast.success(`Copied: ${url}`, { duration: 2500 }) : toast.warning("Copy failed — copy from address bar.");
              }}
            >
              <Copy className="h-4 w-4" />
            </Button>
            {r.kind === "api" ? (
              <>
                <Button
                  size="icon" variant="ghost"
                  onClick={() => toggle.mutate(r.raw)}
                  disabled={toggle.isPending}
                  aria-label={r.enabled ? `Disable ${r.name}` : `Enable ${r.name}`}
                  title={r.enabled ? "Disable" : "Enable"}
                >
                  {r.enabled ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}
                </Button>
                <Button
                  size="icon" variant="ghost"
                  onClick={() => setConfirmDelete(r.raw)}
                  aria-label={`Remove ${r.name}`}
                  title="Remove"
                  className="text-destructive hover:bg-destructive/10"
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </>
            ) : (
              // Deploys are managed in the Deployments panel — only show
              // a hint pointing the operator there so this list stays
              // single-purpose (a unified "what's exposed" view).
              <span className="col-span-2 text-[10px] text-muted-foreground italic px-2">manage ↓</span>
            )}
          </li>
        ))}
      </ul>
      <AlertDialog open={!!confirmDelete} onOpenChange={(v) => !v && setConfirmDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove API {confirmDelete?.Name}?</AlertDialogTitle>
            <AlertDialogDescription>
              This unmounts <code className="font-mono">{confirmDelete?.Path || `/api/${confirmDelete?.Name}`}</code> and regenerates the nginx config.
              External clients will start receiving 404s for that path immediately.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                if (confirmDelete) remove.mutate(confirmDelete.Name);
                setConfirmDelete(null);
              }}
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
