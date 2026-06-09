import { Webhook } from "lucide-react";
import { useAuth } from "../../auth/auth-context";
import { useWebhookActivity } from "../../hooks/useAdminQueries";
import { Badge } from "../ui/badge";
import { Skeleton } from "../ui/skeleton";
import { EmptyState } from "../ui/empty-state";
import { fmtRelative, shortSHA } from "../../lib/utils";

export function WebhookActivityPanel({ onAddDeploy }: { onAddDeploy: () => void }) {
  const { isAuthed } = useAuth();
  const { data, isLoading } = useWebhookActivity();

  if (!isAuthed) return <EmptyState icon={<Webhook className="h-6 w-6" />} title="Sign in to view webhook activity" />;
  if (isLoading) return <Skeleton className="h-16 w-full" />;
  if (!data || data.length === 0) {
    return (
      <EmptyState
        icon={<Webhook className="h-6 w-6" />}
        title="No webhook deliveries yet"
        description={<>The receiver listens at <code className="text-[11px]">/webhook</code> for GitHub push events. On a valid HMAC payload it enqueues a deploy job (clone → build → swap symlink). Register a deployment first, then click <strong>🪝 Webhook setup</strong> on its card for the URL + secret.</>}
        primaryAction={{ label: "+ Add deployment", onClick: onAddDeploy }}
      />
    );
  }
  return (
    <ul className="space-y-1.5">
      {data.map((d, i) => {
        const variant = d.result === "dispatched" || d.result === "verified" ? "success" : d.result === "rejected" || d.result === "failed" ? "destructive" : "muted";
        return (
          <li key={i} className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-xs">
            <Badge variant={variant}>{d.result}</Badge>
            <span className="font-mono">{d.deploy}</span>
            {d.sha && <span className="font-mono text-muted-foreground">{shortSHA(d.sha)}</span>}
            <span className="ml-auto text-muted-foreground">{fmtRelative(d.ts)}</span>
          </li>
        );
      })}
    </ul>
  );
}
