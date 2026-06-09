import { useEffect, useRef, useState } from "react";
import { CircleDot, Download, Eraser, Terminal } from "lucide-react";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Checkbox } from "../ui/checkbox";
import { Label } from "../ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { EmptyState } from "../ui/empty-state";
import { useDeploys } from "../../hooks/useAdminQueries";
import { useSSE, type SSEStatus } from "../../hooks/useSSE";
import { cn } from "../../lib/utils";

// Each line on the wire is { deploy: string, stream: "stdout"|"stderr", line: string }
// from the deploy.*.stdout topic, plus state ticks from deploy.*.state.
// We buffer the last N (configurable up to 5000) in a ring. Render only
// the trailing window to avoid a 5000-DOM-node blowup.
const MAX_LINES = 5000;
const VIEW_WINDOW = 800; // last 800 rendered, scrolls to bottom

interface LogLine {
  id: number;
  ts: string;
  stream: "stdout" | "stderr" | "system";
  text: string;
}

export function LogsPanel({ onSSEStatus }: { onSSEStatus: (s: SSEStatus) => void }) {
  const { data: deploys = [] } = useDeploys();
  const [current, setCurrent] = useState<string>(""); // empty = all
  const [grep, setGrep] = useState("");
  const [showTs, setShowTs] = useState(false);
  const [highlight, setHighlight] = useState(true);
  const [lines, setLines] = useState<LogLine[]>([]);
  const idCounter = useRef(0);
  const preRef = useRef<HTMLPreElement>(null);
  const stickyBottom = useRef(true);

  useEffect(() => { if (deploys[0] && !current) setCurrent(deploys[0].Name); }, [deploys, current]);

  function push(text: string, stream: LogLine["stream"]) {
    setLines((prev) => {
      const next = prev.length >= MAX_LINES ? prev.slice(prev.length - MAX_LINES + 1) : prev;
      idCounter.current += 1;
      return [...next, { id: idCounter.current, ts: new Date().toISOString().slice(11, 19), stream, text }];
    });
  }

  const { status, lastEventAt } = useSSE(
    ["webhook.recv", "tls.expiry", "status.deploy", "deploy.*.stdout", "deploy.*.state"],
    {
      stdout: (data) => {
        const d = data as { deploy: string; stream: "stdout" | "stderr"; line: string };
        if (current && d.deploy !== current) return;
        push(d.line || "", d.stream || "stdout");
      },
      state: (data) => {
        const d = data as { deploy: string; status: string };
        if (current && d.deploy !== current) return;
        push(`[state] ${d.deploy}: ${d.status}`, "system");
      },
      "webhook.recv": (data) => {
        const d = data as { deploy?: string; result?: string };
        push(`[webhook] ${d.deploy || "—"}: ${d.result || "received"}`, "system");
      },
    },
  );
  useEffect(() => onSSEStatus(status), [status, onSSEStatus]);

  useEffect(() => {
    if (stickyBottom.current && preRef.current) preRef.current.scrollTop = preRef.current.scrollHeight;
  }, [lines.length]);

  function onScroll() {
    const el = preRef.current;
    if (!el) return;
    stickyBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  }

  function download() {
    const text = lines.map((l) => (showTs ? `${l.ts} ` : "") + l.text).join("\n");
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    a.download = `apigw-logs-${new Date().toISOString().slice(0, 19)}.log`;
    a.click();
    URL.revokeObjectURL(a.href);
  }

  const filtered = grep
    ? lines.filter((l) => l.text.toLowerCase().includes(grep.toLowerCase()))
    : lines;
  const view = filtered.slice(-VIEW_WINDOW);

  const sseColor = { open: "text-success", connecting: "text-muted-foreground", reconnecting: "text-warning", closed: "text-destructive" }[status];

  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={current || "all"} onValueChange={(v) => setCurrent(v === "all" ? "" : v)}>
          <SelectTrigger className="h-8 w-44"><SelectValue placeholder="all deployments" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">all deployments</SelectItem>
            {deploys.length === 0 && <SelectItem value="__none__" disabled>no deployments</SelectItem>}
            {deploys.map((d) => <SelectItem key={d.Name} value={d.Name}>{d.Name}</SelectItem>)}
          </SelectContent>
        </Select>
        <Input className="h-8 w-48" placeholder="grep…" value={grep} onChange={(e) => setGrep(e.target.value)} />
        <Label className="flex items-center gap-1.5 text-xs cursor-pointer">
          <Checkbox checked={showTs} onCheckedChange={(v) => setShowTs(!!v)} /> ts
        </Label>
        <Label className="flex items-center gap-1.5 text-xs cursor-pointer">
          <Checkbox checked={highlight} onCheckedChange={(v) => setHighlight(!!v)} /> hi
        </Label>
        <Button variant="ghost" size="icon" onClick={download} aria-label="Download buffer" title="Download buffer">
          <Download className="h-4 w-4" />
        </Button>
        <Button variant="ghost" size="icon" onClick={() => setLines([])} aria-label="Clear" title="Clear">
          <Eraser className="h-4 w-4" />
        </Button>
        <span className={cn("ml-auto flex items-center gap-1 text-xs", sseColor)} title={`SSE: ${status}`}>
          <CircleDot className="h-3 w-3" /> {status}
        </span>
      </div>
      <pre
        ref={preRef}
        onScroll={onScroll}
        className="font-mono text-[11px] leading-relaxed flex-1 min-h-0 overflow-auto rounded-md border border-border bg-[hsl(var(--card))] p-3 text-foreground/90 no-scrollbar"
      >
        {view.length === 0 ? null : view.map((l) => (
          <div key={l.id} className={cn(
            highlight && l.stream === "stderr" && "text-destructive",
            highlight && l.stream === "system" && "text-primary",
          )}>
            {showTs && <span className="opacity-60 mr-2">{l.ts}</span>}
            {l.text}
          </div>
        ))}
      </pre>
      {view.length === 0 && (
        <EmptyState
          icon={<Terminal className="h-6 w-6" />}
          title="Waiting for events"
          description={
            <>
              Live logs stream over SSE. Events appear when a deploy build prints to stdout / stderr,
              a webhook arrives, a deploy status flips, or TLS expires. With no deploys configured
              nothing fires — that&apos;s expected.
              {lastEventAt && <div className="mt-1 opacity-70">last event {new Date(lastEventAt).toLocaleTimeString()}</div>}
            </>
          }
        />
      )}
    </div>
  );
}
