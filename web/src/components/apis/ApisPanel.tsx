import { useState } from "react";
import { Copy, ExternalLink, Network, Pause, Play, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useApis, useRemoveApi, useToggleApi } from "../../hooks/useAdminQueries";
import type { API } from "../../lib/types";
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

export function ApisPanel({ onAdd }: { onAdd: () => void }) {
  const { isAuthed } = useAuth();
  const { data: apis, isLoading } = useApis();
  const toggle = useToggleApi();
  const remove = useRemoveApi();
  const [confirmDelete, setConfirmDelete] = useState<API | null>(null);

  if (!isAuthed) {
    return <EmptyState icon={<Network className="h-6 w-6" />} title="Sign in to view APIs" description="Bearer token from your config.yaml." />;
  }
  if (isLoading) {
    return (
      <div className="space-y-2">
        {[0, 1].map((i) => <Skeleton key={i} className="h-14 w-full" />)}
      </div>
    );
  }
  if (!apis || apis.length === 0) {
    return (
      <EmptyState
        icon={<Network className="h-6 w-6" />}
        title="No APIs configured"
        description={<>Register one with <code className="text-[11px]">apigw api add &lt;name&gt; --port &lt;n&gt;</code> or use the <strong>+ Add</strong> button.</>}
        primaryAction={{ label: "+ Add API", onClick: onAdd }}
      />
    );
  }
  return (
    <>
      <ul className="space-y-1.5">
        {apis.map((a) => (
          <li
            key={a.Name}
            className="grid grid-cols-[1fr_auto] items-center gap-x-2 gap-y-1 rounded-md border border-border bg-card px-3 py-2 sm:grid-cols-[minmax(0,1fr)_auto_auto_auto_auto_auto_auto]"
          >
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <span className="font-semibold text-sm truncate">{a.Name}</span>
                <Badge variant={a.Enabled ? "success" : "muted"}>{a.Enabled ? "enabled" : "disabled"}</Badge>
              </div>
              <div className="font-mono text-xs text-muted-foreground truncate">{a.Path || `/api/${a.Name}`} → :{a.Port}</div>
            </div>
            <Button asChild size="icon" variant="ghost" aria-label={`Open ${a.Name} in new tab`} title="Open in new tab">
              <a href={publicURL(a.Path || `/api/${a.Name}`)} target="_blank" rel="noopener noreferrer"><ExternalLink className="h-4 w-4" /></a>
            </Button>
            <Button
              size="icon" variant="ghost" aria-label={`Copy URL of ${a.Name}`} title="Copy URL"
              onClick={async () => {
                const url = publicURL(a.Path || `/api/${a.Name}`);
                const ok = await copyText(url);
                ok ? toast.success(`Copied: ${url}`, { duration: 2500 }) : toast.warning("Copy failed — copy from address bar.");
              }}
            >
              <Copy className="h-4 w-4" />
            </Button>
            <Button
              size="icon" variant="ghost"
              onClick={() => toggle.mutate(a)}
              disabled={toggle.isPending}
              aria-label={a.Enabled ? `Disable ${a.Name}` : `Enable ${a.Name}`}
              title={a.Enabled ? "Disable" : "Enable"}
            >
              {a.Enabled ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4" />}
            </Button>
            <Button
              size="icon" variant="ghost"
              onClick={() => setConfirmDelete(a)}
              aria-label={`Remove ${a.Name}`}
              title="Remove"
              className="text-destructive hover:bg-destructive/10"
            >
              <Trash2 className="h-4 w-4" />
            </Button>
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
