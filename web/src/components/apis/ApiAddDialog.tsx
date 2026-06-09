import { useState } from "react";
import { AlertCircle, Plus, X } from "lucide-react";
import { useAddApi, useApis, useDeploys, useRemoveApi, useRemoveDeploy } from "../../hooks/useAdminQueries";
import type { API, RewriteRule } from "../../lib/types";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Alert, AlertDescription, AlertTitle } from "../ui/alert";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";

export function ApiAddDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const add = useAddApi();
  const { data: apis = [] } = useApis();
  const { data: deploys = [] } = useDeploys();
  const removeApi = useRemoveApi();
  const removeDeploy = useRemoveDeploy();

  const [Name, setName] = useState("");
  const [Path, setPath] = useState("");
  const [Port, setPort] = useState("3000");
  const [Description, setDescription] = useState("");
  const [Rewrites, setRewrites] = useState<RewriteRule[]>([]);

  const effectivePath = Path || (Name ? `/api/${Name}` : "");
  const conflictingApi = effectivePath ? apis.find((a) => (a.Path || `/api/${a.Name}`) === effectivePath && a.Name !== Name) : undefined;
  const conflictingDeploy = effectivePath ? deploys.find((d) => d.Path === effectivePath) : undefined;
  const hasConflict = !!(conflictingApi || conflictingDeploy);
  const conflictWith = conflictingApi ? `API ${conflictingApi.Name}` : conflictingDeploy ? `Deploy ${conflictingDeploy.Name}` : "";

  function reset() {
    setName(""); setPath(""); setPort("3000"); setDescription(""); setRewrites([]);
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!Name) return;
    if (hasConflict) {
      if (conflictingApi) await removeApi.mutateAsync(conflictingApi.Name);
      if (conflictingDeploy) await removeDeploy.mutateAsync(conflictingDeploy.Name);
    }
    const body: API = {
      Name, Port: parseInt(Port, 10) || 0, Path,
      Description, Enabled: true, Rewrites,
    };
    try {
      await add.mutateAsync(body);
      reset();
      onOpenChange(false);
    } catch {
      // toast surfaced via mutation hook.
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) reset(); onOpenChange(v); }}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>Add API</DialogTitle>
          <DialogDescription>
            Register an upstream that apigw will proxy traffic to. Lifecycle (start/stop/build) is owned by you;
            for clone+build use a Deploy instead.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="api-name">Name</Label>
            <Input id="api-name" value={Name} onChange={(e) => setName(e.target.value)} placeholder="my-service" required pattern="[a-z0-9][a-z0-9_-]*" autoFocus />
            <p className="text-[11px] text-muted-foreground">lowercase letters, digits, dashes</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="api-port">Upstream port</Label>
            <Input id="api-port" type="number" min={1} max={65535} value={Port} onChange={(e) => setPort(e.target.value)} required />
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label htmlFor="api-path">Mount path</Label>
            <Input id="api-path" value={Path} onChange={(e) => setPath(e.target.value)} placeholder={`default /api/${Name || "<name>"}`} />
            <p className="text-[11px] text-muted-foreground">absolute path under the gateway, e.g. <code>/api/myservice</code> or <code>/foo</code></p>
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label htmlFor="api-desc">Description</Label>
            <Input id="api-desc" value={Description} onChange={(e) => setDescription(e.target.value)} placeholder="optional, shown in api list" />
          </div>

          <div className="sm:col-span-2 space-y-2">
            <Label>Rewrites <span className="font-normal text-[11px] text-muted-foreground">nginx <code>rewrite</code> directives, applied in order before proxy_pass</span></Label>
            <RewritesEditor value={Rewrites} onChange={setRewrites} />
          </div>

          {hasConflict && (
            <Alert variant="warning" className="sm:col-span-2">
              <AlertCircle />
              <AlertTitle>Conflict at {effectivePath}</AlertTitle>
              <AlertDescription>
                {conflictWith} already mounts there. Submitting will <strong>remove it</strong> and replace with this API.
              </AlertDescription>
            </Alert>
          )}

          <DialogFooter className="sm:col-span-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={add.isPending || !Name}>
              {add.isPending ? "Registering…" : hasConflict ? "Replace & register" : "Register"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function RewritesEditor({ value, onChange }: { value: RewriteRule[]; onChange: (next: RewriteRule[]) => void }) {
  function update(idx: number, patch: Partial<RewriteRule>) {
    onChange(value.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
  }
  return (
    <div className="space-y-2">
      {value.map((r, i) => (
        <div key={i} className="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_1fr_120px_auto]">
          <Input
            placeholder="^/api/foo/(.*)$"
            value={r.match}
            onChange={(e) => update(i, { match: e.target.value })}
            aria-label={`match #${i + 1}`}
          />
          <Input
            placeholder="/api/$1"
            value={r.replace}
            onChange={(e) => update(i, { replace: e.target.value })}
            aria-label={`replace #${i + 1}`}
          />
          <Select value={r.flag || "last"} onValueChange={(flag) => update(i, { flag: flag as RewriteRule["flag"] })}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="last">last</SelectItem>
              <SelectItem value="break">break</SelectItem>
              <SelectItem value="redirect">redirect</SelectItem>
              <SelectItem value="permanent">permanent</SelectItem>
            </SelectContent>
          </Select>
          <Button type="button" variant="ghost" size="icon" onClick={() => onChange(value.filter((_, j) => j !== i))} aria-label="Remove rewrite">
            <X className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <Button
        type="button" variant="outline" size="sm" className="gap-1"
        onClick={() => onChange([...value, { match: "", replace: "", flag: "last" }])}
      >
        <Plus className="h-3.5 w-3.5" /> Add rewrite
      </Button>
    </div>
  );
}
