import { useRef, useState } from "react";
import { FileText, History, Undo2, Upload } from "lucide-react";
import { useConfigHistory, useImportConfig, useRollbackConfig } from "../../hooks/useAdminQueries";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "../ui/alert-dialog";
import { fmtRelative } from "../../lib/utils";

export function ConfigSettings() {
  const { data, isLoading } = useConfigHistory();
  const rollback = useRollbackConfig();
  const importCfg = useImportConfig();
  const fileRef = useRef<HTMLInputElement>(null);
  const [confirm, setConfirm] = useState<string | null>(null);

  function onFile(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    const reader = new FileReader();
    reader.onload = () => importCfg.mutate(String(reader.result));
    reader.readAsText(f);
  }
  return (
    <div className="space-y-4">
      <div className="rounded-md border border-border bg-card p-3 space-y-3">
        <div className="flex items-center gap-2">
          <FileText className="h-4 w-4 text-muted-foreground" />
          <h4 className="text-sm font-semibold">Import / export</h4>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button asChild variant="outline" size="sm" className="gap-1.5">
            <a href="api/admin/config?format=yaml" download="apigw-config.yaml">Export YAML</a>
          </Button>
          <Button asChild variant="outline" size="sm" className="gap-1.5">
            <a href="api/admin/config?format=json" download="apigw-config.json">Export JSON</a>
          </Button>
          <Button asChild variant="outline" size="sm" className="gap-1.5">
            <a href="api/admin/config?format=openapi" download="apigw-openapi.yaml">Export OpenAPI</a>
          </Button>
          <Button variant="default" size="sm" className="gap-1.5" onClick={() => fileRef.current?.click()}>
            <Upload className="h-3.5 w-3.5" /> Import YAML
          </Button>
          <input ref={fileRef} type="file" accept=".yaml,.yml" onChange={onFile} hidden />
        </div>
      </div>

      <div className="rounded-md border border-border bg-card p-3 space-y-3">
        <div className="flex items-center gap-2">
          <History className="h-4 w-4 text-muted-foreground" />
          <h4 className="text-sm font-semibold">Snapshot history</h4>
        </div>
        {isLoading ? <Skeleton className="h-16 w-full" /> : !data || data.length === 0 ? (
          <p className="text-xs text-muted-foreground">No snapshots yet. apigw snapshots config.yaml on every mutating CLI/admin operation.</p>
        ) : (
          <ul className="space-y-1.5">
            {data.map((s) => (
              <li key={s.sha} className="flex items-center gap-2 rounded-md border border-border bg-background px-3 py-1.5 text-xs">
                <span className="font-mono">{s.sha.slice(0, 9)}</span>
                <span className="font-semibold">{s.actor}</span>
                <span className="text-muted-foreground truncate">{s.message || "—"}</span>
                <span className="ml-auto text-muted-foreground">{fmtRelative(s.timestamp)}</span>
                <Button size="xs" variant="outline" className="gap-1" onClick={() => setConfirm(s.sha)}>
                  <Undo2 className="h-3 w-3" /> Rollback
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <AlertDialog open={!!confirm} onOpenChange={(v) => !v && setConfirm(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Roll back config to {confirm?.slice(0, 9)}?</AlertDialogTitle>
            <AlertDialogDescription>
              Replaces the live config.yaml with the snapshot content and reloads nginx. APIs, deploys,
              and TLS state may all flip back. The current snapshot stays in history for re-rollback.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => { if (confirm) rollback.mutate(confirm); setConfirm(null); }}
            >
              Roll back
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
