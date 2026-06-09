import { useState } from "react";
import { ClipboardCheck, ThumbsDown, ThumbsUp } from "lucide-react";
import { useApprove, useApprovals, useReject } from "../../hooks/useAdminQueries";
import { useAuth } from "../../auth/auth-context";
import type { Approval } from "../../lib/types";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
import { Skeleton } from "../ui/skeleton";
import { EmptyState } from "../ui/empty-state";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Textarea } from "../ui/textarea";

export function ApprovalsPanel() {
  const { isAuthed } = useAuth();
  const { data, isLoading } = useApprovals("pending");
  const approve = useApprove();
  const reject = useReject();
  const [active, setActive] = useState<{ a: Approval; verb: "approve" | "reject" } | null>(null);
  const [note, setNote] = useState("");

  if (!isAuthed) return <EmptyState icon={<ClipboardCheck className="h-6 w-6" />} title="Sign in to view pending approvals" />;
  if (isLoading) return <div className="space-y-2"><Skeleton className="h-14 w-full" /></div>;
  if (!data || data.length === 0) {
    return (
      <EmptyState
        icon={<ClipboardCheck className="h-6 w-6" />}
        title="Nothing waiting on approval"
        description="Dangerous actions (delete deploy, rotate secret, etc.) land here when an N-of-M approvals policy is configured."
      />
    );
  }
  return (
    <>
      <ul className="space-y-2">
        {data.map((a) => (
          <li key={a.id} className="space-y-2 rounded-md border border-border bg-card p-3">
            <div className="flex items-center gap-2">
              <span className="font-mono text-sm font-semibold">{a.action}</span>
              <span className="text-xs text-muted-foreground truncate">on {a.resource}</span>
              <Badge variant="warning" className="ml-auto">{a.approvals.length}/{a.threshold}</Badge>
            </div>
            <div className="text-xs text-muted-foreground">
              requested by <strong>{a.requested_by}</strong>{a.expires_at ? ` · expires ${a.expires_at.slice(0, 16)}` : ""}
            </div>
            <div className="flex gap-2">
              <Button size="xs" variant="success" onClick={() => { setActive({ a, verb: "approve" }); setNote(""); }}>
                <ThumbsUp className="h-3.5 w-3.5" /> Approve
              </Button>
              <Button size="xs" variant="outline" onClick={() => { setActive({ a, verb: "reject" }); setNote(""); }}>
                <ThumbsDown className="h-3.5 w-3.5" /> Reject
              </Button>
            </div>
          </li>
        ))}
      </ul>
      <Dialog open={!!active} onOpenChange={(v) => !v && setActive(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{active?.verb === "approve" ? "Approve" : "Reject"} {active?.a.action}</DialogTitle>
            <DialogDescription>
              {active?.verb === "approve"
                ? "Once the threshold is met the change applies atomically."
                : "Rejecting drops the change request; the requester gets a denied audit entry."}
            </DialogDescription>
          </DialogHeader>
          <Textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} placeholder={active?.verb === "approve" ? "comment (optional)" : "reason (optional)"} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setActive(null)}>Cancel</Button>
            <Button
              variant={active?.verb === "approve" ? "success" : "destructive"}
              onClick={() => {
                if (!active) return;
                if (active.verb === "approve") approve.mutate({ id: active.a.id, comment: note });
                else reject.mutate({ id: active.a.id, reason: note });
                setActive(null);
              }}
            >
              {active?.verb === "approve" ? "Approve" : "Reject"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
