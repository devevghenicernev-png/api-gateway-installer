import { Copy, KeyRound, Webhook } from "lucide-react";
import { toast } from "sonner";
import { useRotateWebhook, useWebhookSetup } from "../../hooks/useAdminQueries";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "../ui/alert-dialog";
import { useState } from "react";
import { copyText } from "../../lib/utils";

export function WebhookSetupDialog({ deploy, onClose }: { deploy: string | null; onClose: () => void }) {
  const { data, isLoading } = useWebhookSetup(deploy);
  const rotate = useRotateWebhook();
  const [confirmRotate, setConfirmRotate] = useState(false);

  async function copy(label: string, val: string) {
    const ok = await copyText(val);
    if (ok) toast.success(`Copied ${label}.`, { duration: 2500 });
    else toast.warning(`Copy ${label} failed.`);
  }

  return (
    <>
      <Dialog open={!!deploy} onOpenChange={(v) => !v && onClose()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2"><Webhook className="h-4 w-4" /> Webhook setup{deploy ? ` — ${deploy}` : ""}</DialogTitle>
            <DialogDescription>
              Paste Payload URL + Secret into the repo&apos;s Settings → Webhooks → Add webhook. Pick
              the <strong>push</strong> event with <code className="font-mono text-[11px]">application/json</code> content type.
            </DialogDescription>
          </DialogHeader>
          {isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-16 w-full" />
              <Skeleton className="h-16 w-full" />
            </div>
          ) : data ? (
            <div className="space-y-3">
              <Field label="Payload URL" value={data.url} onCopy={() => copy("URL", data.url)} />
              <Field label="Secret" value={data.secret} mono onCopy={() => copy("secret", data.secret)} extra={
                <Button variant="ghost" size="icon" onClick={() => setConfirmRotate(true)} aria-label="Rotate secret" title="Rotate secret">
                  <KeyRound className="h-4 w-4" />
                </Button>
              } />
              <div className="grid grid-cols-2 gap-3">
                <Field label="Content type" value={data.content_type} />
                <Field label="Events" value={data.events.join(", ")} />
              </div>
            </div>
          ) : (
            <div className="text-sm text-muted-foreground">No data.</div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={onClose}>Close</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={confirmRotate} onOpenChange={setConfirmRotate}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Rotate webhook secret?</AlertDialogTitle>
            <AlertDialogDescription>
              The current secret stops working immediately. You&apos;ll need to paste the new one
              into GitHub before the next push, otherwise deliveries land in the rejected bucket.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                if (deploy) rotate.mutate(deploy);
                setConfirmRotate(false);
              }}
            >
              Rotate
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function Field({ label, value, mono, onCopy, extra }: { label: string; value: string; mono?: boolean; onCopy?: () => void; extra?: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <div className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1.5">
        <div className={(mono ? "font-mono " : "") + "flex-1 overflow-x-auto whitespace-nowrap text-xs"}>{value}</div>
        {onCopy && <Button variant="ghost" size="icon" onClick={onCopy} aria-label={`Copy ${label}`} title="Copy"><Copy className="h-4 w-4" /></Button>}
        {extra}
      </div>
    </div>
  );
}
