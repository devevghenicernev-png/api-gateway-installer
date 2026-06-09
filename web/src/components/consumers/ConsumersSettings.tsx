import { useState } from "react";
import { Plus, Trash2, User } from "lucide-react";
import { useAddConsumer, useConsumers, useRemoveConsumer } from "../../hooks/useAdminQueries";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Skeleton } from "../ui/skeleton";

export function ConsumersSettings() {
  const { data, isLoading } = useConsumers();
  const add = useAddConsumer();
  const remove = useRemoveConsumer();
  const [name, setName] = useState("");
  const [display, setDisplay] = useState("");
  const [groups, setGroups] = useState("");
  return (
    <div className="space-y-4">
      <form
        onSubmit={(e) => { e.preventDefault(); if (!name) return; add.mutate({ Name: name, DisplayName: display, Groups: groups.split(",").map((s) => s.trim()).filter(Boolean) }); setName(""); setDisplay(""); setGroups(""); }}
        className="grid gap-3 sm:grid-cols-3 rounded-md border border-border bg-card p-3"
      >
        <div className="space-y-1"><Label>Name</Label><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="alice" /></div>
        <div className="space-y-1"><Label>Display name</Label><Input value={display} onChange={(e) => setDisplay(e.target.value)} placeholder="Alice (CI)" /></div>
        <div className="space-y-1"><Label>Groups (comma-sep)</Label><Input value={groups} onChange={(e) => setGroups(e.target.value)} placeholder="ops, dev" /></div>
        <div className="sm:col-span-3 flex justify-end">
          <Button type="submit" size="sm" className="gap-1.5" disabled={add.isPending || !name}><Plus className="h-3.5 w-3.5" /> Add consumer</Button>
        </div>
      </form>
      {isLoading ? <Skeleton className="h-12 w-full" /> : !data || data.length === 0 ? (
        <div className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">No consumers registered. Consumers are identities for API-key / HMAC auth on per-API routes.</div>
      ) : (
        <ul className="space-y-1.5">
          {data.map((c) => (
            <li key={c.Name} className="flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2">
              <User className="h-4 w-4 text-muted-foreground" />
              <span className="font-semibold text-sm">{c.Name}</span>
              <span className="text-xs text-muted-foreground">{c.DisplayName}</span>
              {c.Groups.length > 0 && <span className="ml-2 font-mono text-[11px] text-muted-foreground">{c.Groups.join(", ")}</span>}
              <Button variant="ghost" size="icon" className="ml-auto text-destructive hover:bg-destructive/10" onClick={() => remove.mutate(c.Name)} aria-label="Remove">
                <Trash2 className="h-4 w-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
