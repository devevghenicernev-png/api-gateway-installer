import { useState } from "react";
import { Rocket, RotateCw, Trash2, Undo2, Webhook } from "lucide-react";
import {
  useDeploys, useRemoveDeploy, useRollbackDeploy, useRunDeploy,
} from "../../hooks/useAdminQueries";
import { useAuth } from "../../auth/auth-context";
import type { Deploy } from "../../lib/types";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import { EmptyState } from "../ui/empty-state";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "../ui/alert-dialog";
import { shortSHA, fmtRelative } from "../../lib/utils";

type ConfirmKind = "rollback" | "delete";
type ConfirmState = { kind: ConfirmKind; deploy: Deploy } | null;

export function DeploysPanel({ onAdd, onShowWebhook, onShowSSH }: {
  onAdd: () => void;
  onShowWebhook: (name: string) => void;
  onShowSSH: () => void;
}) {
  const { isAuthed } = useAuth();
  const { data: deploys, isLoading } = useDeploys();
  const run = useRunDeploy();
  const rollback = useRollbackDeploy();
  const remove = useRemoveDeploy();
  const [confirm, setConfirm] = useState<ConfirmState>(null);

  if (!isAuthed) {
    return <EmptyState icon={<Rocket className="h-6 w-6" />} title="Sign in to view deployments" />;
  }
  if (isLoading) {
    return <div className="space-y-2">{[0, 1].map((i) => <Skeleton key={i} className="h-16 w-full" />)}</div>;
  }
  if (!deploys || deploys.length === 0) {
    return (
      <EmptyState
        icon={<Rocket className="h-6 w-6" />}
        title="No deployments registered"
        description={<>Clone, build, and supervise a git repo as an upstream. Register one with the <strong>+ Add</strong> button.</>}
        primaryAction={{ label: "+ Add deployment", onClick: onAdd }}
        secondaryAction={{ label: "Show deploy SSH key", onClick: onShowSSH }}
      />
    );
  }

  return (
    <>
      <ul className="space-y-2">
        {deploys.map((d) => <DeployRow
          key={d.Name} d={d}
          onRedeploy={() => run.mutate(d.Name)}
          onRollback={() => setConfirm({ kind: "rollback", deploy: d })}
          onDelete={() => setConfirm({ kind: "delete", deploy: d })}
          onWebhook={() => onShowWebhook(d.Name)}
          runPending={run.isPending}
        />)}
      </ul>

      <AlertDialog open={!!confirm} onOpenChange={(v) => !v && setConfirm(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm?.kind === "rollback"
                ? `Roll back ${confirm.deploy.Name}?`
                : `Remove deployment ${confirm?.deploy.Name}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm?.kind === "rollback"
                ? <>Switches the live symlink back to the previous release. The current SHA <code className="font-mono">{shortSHA(confirm?.deploy.LastSHA)}</code> becomes inactive.</>
                : <>Stops the supervisor unit, removes the nginx mount, and clears the config entry. Cloned releases on disk are kept until <code className="font-mono">apigw deploy gc</code>.</>
              }
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className={confirm?.kind === "delete" ? "bg-destructive text-destructive-foreground hover:bg-destructive/90" : ""}
              onClick={() => {
                if (!confirm) return;
                if (confirm.kind === "rollback") rollback.mutate(confirm.deploy.Name);
                else remove.mutate(confirm.deploy.Name);
                setConfirm(null);
              }}
            >
              {confirm?.kind === "rollback" ? "Roll back" : "Remove"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function statusVariant(s: string | undefined): "success" | "building" | "destructive" | "muted" {
  if (s === "ok") return "success";
  if (s === "building" || s === "queued") return "building";
  if (s === "failed") return "destructive";
  return "muted";
}

function DeployRow({ d, onRedeploy, onRollback, onDelete, onWebhook, runPending }: {
  d: Deploy;
  onRedeploy: () => void; onRollback: () => void; onDelete: () => void; onWebhook: () => void;
  runPending: boolean;
}) {
  return (
    <li className="rounded-md border border-border bg-card p-3">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="font-semibold text-sm">{d.Name}</span>
            <Badge variant={statusVariant(d.LastStatus)}>{d.LastStatus || "idle"}</Badge>
          </div>
          <div className="mt-0.5 text-xs text-muted-foreground font-mono truncate">
            {d.Runtime || "—"} · {d.Path || "—"} · {shortSHA(d.LastSHA)} · {fmtRelative(d.LastDeploy)}
          </div>
        </div>
        <div className="flex items-center gap-1">
          <Button size="icon" variant="ghost" onClick={onWebhook} aria-label="Webhook setup" title="Webhook setup (URL + secret)">
            <Webhook className="h-4 w-4" />
          </Button>
          <Button size="icon" variant="ghost" onClick={onRedeploy} disabled={runPending} aria-label="Redeploy" title="Redeploy">
            <RotateCw className="h-4 w-4" />
          </Button>
          <Button size="icon" variant="ghost" onClick={onRollback} aria-label="Rollback" title="Rollback to previous">
            <Undo2 className="h-4 w-4" />
          </Button>
          <Button size="icon" variant="ghost" onClick={onDelete} className="text-destructive hover:bg-destructive/10" aria-label="Remove">
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>
      {d.LastError && d.LastStatus === "failed" && (
        <div className="mt-2 rounded bg-destructive/10 px-2 py-1 font-mono text-[11px] text-destructive truncate" title={d.LastError}>{d.LastError}</div>
      )}
    </li>
  );
}
