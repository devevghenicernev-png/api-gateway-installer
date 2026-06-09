import { useState } from "react";
import { ArrowLeftRight, Plus, Trash2 } from "lucide-react";
import { useAddStream, useRemoveStream, useStreams } from "../../hooks/useAdminQueries";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Skeleton } from "../ui/skeleton";
import { Badge } from "../ui/badge";

export function StreamsSettings() {
  const { data, isLoading } = useStreams();
  const add = useAddStream();
  const remove = useRemoveStream();
  const [name, setName] = useState("");
  const [port, setPort] = useState("");
  const [protocol, setProtocol] = useState<"tcp" | "udp">("tcp");
  const [upstream, setUpstream] = useState("");
  return (
    <div className="space-y-4">
      <form
        onSubmit={(e) => { e.preventDefault(); if (!name || !port || !upstream) return; add.mutate({ Name: name, Port: parseInt(port, 10), Protocol: protocol, Upstream: upstream, Enabled: true }); setName(""); setPort(""); setUpstream(""); }}
        className="grid gap-3 sm:grid-cols-2 rounded-md border border-border bg-card p-3"
      >
        <div className="space-y-1"><Label>Name</Label><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="redis" /></div>
        <div className="space-y-1"><Label>Listen port</Label><Input type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} placeholder="6380" /></div>
        <div className="space-y-1">
          <Label>Protocol</Label>
          <Select value={protocol} onValueChange={(v) => setProtocol(v as "tcp" | "udp")}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="tcp">TCP</SelectItem>
              <SelectItem value="udp">UDP</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1"><Label>Upstream</Label><Input value={upstream} onChange={(e) => setUpstream(e.target.value)} placeholder="127.0.0.1:6379" /></div>
        <div className="sm:col-span-2 flex justify-end">
          <Button type="submit" size="sm" className="gap-1.5" disabled={add.isPending || !name || !port || !upstream}>
            <Plus className="h-3.5 w-3.5" /> Add stream
          </Button>
        </div>
      </form>
      {isLoading ? <Skeleton className="h-12 w-full" /> : !data || data.length === 0 ? (
        <div className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">No TCP/UDP streams configured. Streams expose a non-HTTP upstream (e.g. Redis, Postgres) on a dedicated port.</div>
      ) : (
        <ul className="space-y-1.5">
          {data.map((s) => (
            <li key={s.Name} className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2">
              <ArrowLeftRight className="h-4 w-4 text-muted-foreground" />
              <span className="font-semibold text-sm">{s.Name}</span>
              <Badge variant="muted">{s.Protocol.toUpperCase()}</Badge>
              <span className="font-mono text-xs text-muted-foreground">:{s.Port} → {s.Upstream}</span>
              <Button variant="ghost" size="icon" className="ml-auto text-destructive hover:bg-destructive/10" onClick={() => remove.mutate(s.Name)} aria-label="Remove">
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
