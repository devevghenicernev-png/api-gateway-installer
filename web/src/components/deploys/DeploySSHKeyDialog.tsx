import { Copy, KeyRound, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { useDeploySSHKey, useGenerateDeploySSHKey } from "../../hooks/useAdminQueries";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import { copyText } from "../../lib/utils";

export function DeploySSHKeyDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { data, isLoading, isError } = useDeploySSHKey();
  const generate = useGenerateDeploySSHKey();
  const pub = data?.public_key || "";

  async function copy() {
    if (!pub) return;
    const ok = await copyText(pub);
    if (ok) toast.success("Copied to clipboard.", { duration: 2500 });
    else toast.warning("Copy failed — select & ⌘C.");
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2"><KeyRound className="h-4 w-4" /> Deploy SSH key</DialogTitle>
          <DialogDescription>
            apigw clones repos as this key. Add it to your GitHub / GitLab / Bitbucket repo as a deploy key
            (read-only is enough) so SSH clone URLs resolve from the host.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          {isLoading && <Skeleton className="h-20 w-full" />}
          {isError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-xs text-destructive">
              No key on disk yet — generate one to start.
            </div>
          )}
          {pub && (
            <div className="rounded-md border border-border bg-card p-3 font-mono text-[11px] break-all">{pub}</div>
          )}
          <div className="flex justify-between gap-2">
            <Button
              variant="outline" size="sm"
              onClick={() => generate.mutate()}
              disabled={generate.isPending}
              className="gap-1.5"
            >
              <RefreshCw className="h-3.5 w-3.5" /> {pub ? "Re-generate" : "Generate key"}
            </Button>
            <Button onClick={copy} disabled={!pub} size="sm" className="gap-1.5">
              <Copy className="h-3.5 w-3.5" /> Copy public key
            </Button>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
