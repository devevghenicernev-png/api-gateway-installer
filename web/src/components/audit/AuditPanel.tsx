import { useMemo, useState } from "react";
import { Check, Download, FileText, ShieldCheck } from "lucide-react";
import { useAudit, useAuditVerify } from "../../hooks/useAdminQueries";
import { useAuth } from "../../auth/auth-context";
import type { AuditEntry } from "../../lib/types";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Badge } from "../ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Skeleton } from "../ui/skeleton";
import { EmptyState } from "../ui/empty-state";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { ScrollArea } from "../ui/scroll-area";

export function AuditPanel() {
  const { isAuthed } = useAuth();
  const [actor, setActor] = useState("");
  const [action, setAction] = useState("");
  const [result, setResult] = useState("");
  const [expanded, setExpanded] = useState<AuditEntry | null>(null);
  const verify = useAuditVerify();
  const { data, isLoading } = useAudit({ actor, action, result, limit: 200 });

  if (!isAuthed) return <EmptyState icon={<FileText className="h-6 w-6" />} title="Sign in to read audit log" />;

  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-[1fr_1fr_140px_auto]">
        <Input className="h-8" placeholder="actor…" value={actor} onChange={(e) => setActor(e.target.value)} />
        <Input className="h-8" placeholder="action…" value={action} onChange={(e) => setAction(e.target.value)} />
        <Select value={result || "all"} onValueChange={(v) => setResult(v === "all" ? "" : v)}>
          <SelectTrigger className="h-8"><SelectValue placeholder="all results" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">all results</SelectItem>
            <SelectItem value="ok">ok</SelectItem>
            <SelectItem value="denied">denied</SelectItem>
            <SelectItem value="failed">failed</SelectItem>
            <SelectItem value="parked">parked</SelectItem>
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" onClick={() => { setActor(""); setAction(""); setResult(""); }}>clear</Button>
      </div>
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
        <Button
          variant="ghost" size="xs" onClick={() => verify.mutate()} disabled={verify.isPending}
          className="gap-1"
        >
          <ShieldCheck className="h-3 w-3" /> Verify chain
        </Button>
        <Button asChild variant="ghost" size="xs" className="gap-1">
          <a href="api/admin/audit/export?format=json" download="audit.json"><Download className="h-3 w-3" /> JSON</a>
        </Button>
        <Button asChild variant="ghost" size="xs" className="gap-1">
          <a href="api/admin/audit/export?format=csv" download="audit.csv"><Download className="h-3 w-3" /> CSV</a>
        </Button>
        <span className="ml-auto">{data ? `${data.length} entries` : ""}</span>
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        {isLoading ? (
          <div className="space-y-1">{Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-9 w-full" />)}</div>
        ) : !data || data.length === 0 ? (
          <p className="text-xs text-muted-foreground px-2">Audit log is empty under current filters.</p>
        ) : (
          <ul className="space-y-1">
            {data.slice().reverse().map((e) => <AuditRow key={e.id} entry={e} onClick={() => setExpanded(e)} />)}
          </ul>
        )}
      </div>
      <AuditDetail entry={expanded} onClose={() => setExpanded(null)} />
    </div>
  );
}

function resultVariant(r: string): "success" | "destructive" | "muted" | "warning" {
  if (r === "ok") return "success";
  if (r === "denied" || r === "failed") return "destructive";
  if (r === "parked") return "warning";
  return "muted";
}

function AuditRow({ entry, onClick }: { entry: AuditEntry; onClick: () => void }) {
  const diff = useMemo(() => inlineDiff(entry.before, entry.after), [entry.before, entry.after]);
  return (
    <li
      onClick={onClick}
      className="grid cursor-pointer grid-cols-[auto_auto_auto_minmax(0,1fr)_minmax(0,auto)_auto] items-center gap-2 rounded-md border border-border bg-card px-3 py-1.5 text-xs hover:border-primary"
    >
      <span className="font-mono text-muted-foreground tabular-nums">{(entry.timestamp || "").slice(11, 19)}</span>
      <span className="font-semibold">{entry.actor || "—"}</span>
      <span className="font-mono text-primary">{entry.action || "—"}</span>
      <span className="font-mono text-muted-foreground truncate">{entry.resource || ""}</span>
      <span className="font-mono text-[11px] hidden sm:block truncate" dangerouslySetInnerHTML={{ __html: diff }} />
      <Badge variant={resultVariant(entry.result)}>{entry.result || "—"}</Badge>
    </li>
  );
}

function escapeHtml(s: string) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
}
function summarizeVal(v: unknown): string {
  if (v == null) return "—";
  if (typeof v === "string") return v.length > 24 ? v.slice(0, 21) + "…" : v;
  if (typeof v === "boolean" || typeof v === "number") return String(v);
  if (Array.isArray(v)) return `[${v.length}]`;
  return "{…}";
}
function inlineDiff(before: Record<string, unknown> | null | undefined, after: Record<string, unknown> | null | undefined): string {
  if (!before && after) return `<em class="opacity-70">created</em>`;
  if (before && !after) return `<em class="opacity-70">removed</em>`;
  if (!before || !after) return "";
  const keys = new Set([...Object.keys(before), ...Object.keys(after)]);
  const changes: Array<{ k: string; b: unknown; a: unknown }> = [];
  for (const k of keys) {
    const b = before[k], a = after[k];
    if (JSON.stringify(b) === JSON.stringify(a)) continue;
    changes.push({ k, b, a });
  }
  if (changes.length === 0) return `<em class="opacity-70">no-op</em>`;
  if (changes.length === 1) {
    const { k, b, a } = changes[0];
    return `${escapeHtml(k)}: <s class="text-destructive opacity-70">${escapeHtml(summarizeVal(b))}</s> <span class="opacity-60">→</span> <strong class="text-success">${escapeHtml(summarizeVal(a))}</strong>`;
  }
  return `${escapeHtml(changes[0].k)} <span class="opacity-60">+ ${changes.length - 1} more</span>`;
}

function AuditDetail({ entry, onClose }: { entry: AuditEntry | null; onClose: () => void }) {
  return (
    <Dialog open={!!entry} onOpenChange={(v) => !v && onClose()}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">{entry?.action}</DialogTitle>
          <DialogDescription>{entry?.actor} · {entry?.resource} · {entry?.timestamp}</DialogDescription>
        </DialogHeader>
        {entry && (
          <ScrollArea className="max-h-[55vh]">
            <div className="space-y-3 pr-3">
              {entry.reason && (
                <Section title="Reason">{entry.reason}</Section>
              )}
              {entry.before && (
                <Section title="Before"><pre className="overflow-auto rounded bg-card p-2 font-mono text-[11px]">{JSON.stringify(entry.before, null, 2)}</pre></Section>
              )}
              {entry.after && (
                <Section title="After"><pre className="overflow-auto rounded bg-card p-2 font-mono text-[11px]">{JSON.stringify(entry.after, null, 2)}</pre></Section>
              )}
              {entry.hash && (
                <Section title="Hash">
                  <div className="font-mono text-[11px] break-all">{entry.hash}</div>
                  {entry.prev_hash && <div className="font-mono text-[10px] text-muted-foreground break-all mt-0.5">prev {entry.prev_hash}</div>}
                </Section>
              )}
            </div>
          </ScrollArea>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}><Check className="h-4 w-4" /> Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">{title}</div>
      <div className="mt-1">{children}</div>
    </div>
  );
}
