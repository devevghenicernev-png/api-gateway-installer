import { useState } from "react";
import { RefreshCw, ShieldAlert, ShieldCheck, ShieldOff, Trash2 } from "lucide-react";
import { useTLS, useRenewTLS, useRemoveTLS } from "../../hooks/useAdminQueries";
import { useAuth } from "../../auth/auth-context";
import { Button } from "../ui/button";
import { Badge } from "../ui/badge";
import { Skeleton } from "../ui/skeleton";
import { EmptyState } from "../ui/empty-state";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "../ui/alert-dialog";

export function TlsPanel({ onAdd }: { onAdd: () => void }) {
  const { isAuthed } = useAuth();
  const { data, isLoading } = useTLS();
  const renew = useRenewTLS();
  const remove = useRemoveTLS();
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);

  if (!isAuthed) {
    return <EmptyState icon={<ShieldCheck className="h-6 w-6" />} title="Sign in to view certificates" />;
  }
  if (isLoading) return <Skeleton className="h-20 w-full" />;
  if (!data || data.length === 0) {
    return (
      <EmptyState
        icon={<ShieldOff className="h-6 w-6" />}
        title="No certificates registered"
        description={<>Issue one with the <strong>+ Add</strong> button below, or via <code className="text-[11px]">apigw tls enable letsencrypt --domain ...</code>.</>}
        primaryAction={{ label: "+ Add domain", onClick: onAdd }}
      />
    );
  }
  return (
    <>
      <ul className="space-y-2">
        {data.map((c) => {
          const cls = c.days_left < 0 ? "destructive" : c.days_left < 30 ? "warning" : "success";
          const icon = c.days_left < 0 ? <ShieldAlert className="h-4 w-4" /> : <ShieldCheck className="h-4 w-4" />;
          return (
            <li key={c.domain} className="flex items-center gap-3 rounded-md border border-border bg-card p-3">
              <span className={`text-${cls}`}>{icon}</span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-semibold text-sm truncate">{c.domain}</span>
                  <Badge variant={cls === "warning" ? "warning" : cls === "destructive" ? "destructive" : "success"}>
                    {c.days_left < 0 ? `expired ${-c.days_left}d ago` : `${c.days_left}d left`}
                  </Badge>
                </div>
                <div className="text-xs text-muted-foreground font-mono">{c.strategy || "—"}{c.issuer ? ` · ${c.issuer}` : ""}</div>
              </div>
              <Button size="icon" variant="ghost" onClick={() => renew.mutate(c.domain)} disabled={renew.isPending} aria-label={`Renew ${c.domain}`} title="Renew now">
                <RefreshCw className="h-4 w-4" />
              </Button>
              <Button size="icon" variant="ghost" onClick={() => setConfirmRemove(c.domain)} className="text-destructive hover:bg-destructive/10" aria-label={`Remove TLS for ${c.domain}`} title="Remove">
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          );
        })}
      </ul>
      <AlertDialog open={!!confirmRemove} onOpenChange={(v) => !v && setConfirmRemove(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove TLS for {confirmRemove}?</AlertDialogTitle>
            <AlertDialogDescription>
              Stops auto-renewal and removes the listener from the nginx config. The cert files on disk
              (<code className="font-mono text-[11px]">/etc/letsencrypt/live/&lt;domain&gt;/</code> for LE) are kept until
              <code className="font-mono text-[11px]"> certbot delete --cert-name &lt;domain&gt;</code>.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => { if (confirmRemove) remove.mutate(confirmRemove); setConfirmRemove(null); }}
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
