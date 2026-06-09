import { useState } from "react";
import { AlertCircle } from "lucide-react";
import { useAddDeploy, useApis, useDeploys } from "../../hooks/useAdminQueries";
import type { Deploy, RewriteRule } from "../../lib/types";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Textarea } from "../ui/textarea";
import { Label } from "../ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Alert, AlertDescription, AlertTitle } from "../ui/alert";
import { RewritesEditor } from "../apis/ApiAddDialog";

export function DeployAddDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const add = useAddDeploy();
  const { data: apis = [] } = useApis();
  const { data: deploys = [] } = useDeploys();

  const [Name, setName] = useState("");
  const [Repo, setRepo] = useState("");
  const [Branch, setBranch] = useState("main");
  const [Port, setPort] = useState("3000");
  const [Path, setPath] = useState("");
  const [Runtime, setRuntime] = useState<Deploy["Runtime"]>("auto");
  const [Build, setBuild] = useState("");
  const [Start, setStart] = useState("");
  const [Description, setDescription] = useState("");
  const [Rewrites, setRewrites] = useState<RewriteRule[]>([]);
  const [HealthPath, setHealthPath] = useState("");

  const effectivePath = Path || (Name ? `/api/${Name}` : "");
  const conflictingApi = effectivePath ? apis.find((a) => (a.Path || `/api/${a.Name}`) === effectivePath) : undefined;
  const conflictingDeploy = effectivePath ? deploys.find((d) => d.Path === effectivePath && d.Name !== Name) : undefined;
  const conflictWith = conflictingApi ? `API ${conflictingApi.Name}` : conflictingDeploy ? `Deploy ${conflictingDeploy.Name}` : "";

  function reset() {
    setName(""); setRepo(""); setBranch("main"); setPort("3000"); setPath("");
    setRuntime("auto"); setBuild(""); setStart(""); setDescription(""); setRewrites([]); setHealthPath("");
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!Name || !Repo) return;
    const body: Deploy = {
      Name, Repo, Branch: Branch || "main",
      Port: parseInt(Port, 10) || 0,
      Path, Runtime,
      Build, Start, Description,
      Enabled: true,
      Rewrites: Rewrites.filter((r) => r.match && r.replace),
      HealthPath: HealthPath || undefined,
    };
    try {
      await add.mutateAsync({
        body,
        replaceApi: conflictingApi?.Name,
        replaceDeploy: conflictingDeploy?.Name,
      });
      reset();
      onOpenChange(false);
    } catch {
      // toast already.
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) reset(); onOpenChange(v); }}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>Add deployment</DialogTitle>
          <DialogDescription>
            apigw clones the repo, builds, supervises the upstream, and reloads nginx — all from this dialog.
            Webhook auto-deploy wires up after register from the 🪝 button on the card.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="dp-name">Name</Label>
            <Input id="dp-name" value={Name} onChange={(e) => setName(e.target.value)} placeholder="my-app" required pattern="[a-z0-9][a-z0-9_-]*" autoFocus />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="dp-branch">Branch</Label>
            <Input id="dp-branch" value={Branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label htmlFor="dp-repo">Git repo</Label>
            <Input id="dp-repo" value={Repo} onChange={(e) => setRepo(e.target.value)} placeholder="git@github.com:owner/repo.git or https://github.com/owner/repo.git" required />
            <p className="text-[11px] text-muted-foreground">SSH needs the deploy key registered in GitHub — see the SSH key dialog from the empty state.</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="dp-port">Upstream port</Label>
            <Input id="dp-port" type="number" min={0} max={65535} value={Port} onChange={(e) => setPort(e.target.value)} />
            <p className="text-[11px] text-muted-foreground">0 = no listener (static / worker)</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="dp-runtime">Runtime</Label>
            <Select value={Runtime} onValueChange={(v) => setRuntime(v as Deploy["Runtime"])}>
              <SelectTrigger id="dp-runtime"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">auto-detect</SelectItem>
                <SelectItem value="node">node</SelectItem>
                <SelectItem value="python">python</SelectItem>
                <SelectItem value="go">go</SelectItem>
                <SelectItem value="docker">docker</SelectItem>
                <SelectItem value="static">static</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label htmlFor="dp-path">Mount path</Label>
            <Input id="dp-path" value={Path} onChange={(e) => setPath(e.target.value)} placeholder={`default /api/${Name || "<name>"}`} />
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label htmlFor="dp-build">Build command</Label>
            <Textarea id="dp-build" value={Build} onChange={(e) => setBuild(e.target.value)} rows={2}
              placeholder="optional — e.g. cp /etc/myapp/.env backend/.env && cd backend && npm ci && npm run build" />
          </div>
          <div className="space-y-1.5 sm:col-span-2">
            <Label htmlFor="dp-start">Start command</Label>
            <Textarea id="dp-start" value={Start} onChange={(e) => setStart(e.target.value)} rows={2}
              placeholder="optional — e.g. cd backend && node dist/server.js (defaults to runtime auto-detection)" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="dp-health">Health probe path</Label>
            <Input id="dp-health" value={HealthPath} onChange={(e) => setHealthPath(e.target.value)} placeholder="/api/health (optional)" />
            <p className="text-[11px] text-muted-foreground">empty = lenient TCP probe</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="dp-desc">Description</Label>
            <Input id="dp-desc" value={Description} onChange={(e) => setDescription(e.target.value)} placeholder="optional" />
          </div>

          <div className="sm:col-span-2 space-y-2">
            <Label>Rewrites <span className="font-normal text-[11px] text-muted-foreground">applied before proxy_pass — useful when the backend serves at <code>/api/*</code> and you want a different public prefix</span></Label>
            <RewritesEditor value={Rewrites} onChange={setRewrites} />
          </div>

          {conflictWith && (
            <Alert variant="warning" className="sm:col-span-2">
              <AlertCircle />
              <AlertTitle>Conflict at {effectivePath}</AlertTitle>
              <AlertDescription>
                {conflictWith} already mounts there. Submitting will <strong>atomically remove it</strong> and register this deploy in its place (one nginx reload).
              </AlertDescription>
            </Alert>
          )}

          <DialogFooter className="sm:col-span-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={add.isPending || !Name || !Repo}>
              {add.isPending ? "Registering…" : conflictWith ? "Replace & register" : "Register"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
