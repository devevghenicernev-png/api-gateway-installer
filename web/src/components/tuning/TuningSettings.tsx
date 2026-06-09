import { useEffect, useState } from "react";
import { Sliders } from "lucide-react";
import { useSetTuning, useTuning } from "../../hooks/useAdminQueries";
import type { TuningConfig } from "../../lib/types";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Alert, AlertDescription, AlertTitle } from "../ui/alert";

export function TuningSettings() {
  const { data } = useTuning();
  const set = useSetTuning();
  const [cfg, setCfg] = useState<TuningConfig>(data ?? {});
  useEffect(() => { if (data) setCfg(data); }, [data]);
  const upd = <K extends keyof TuningConfig>(k: K, v: TuningConfig[K]) => setCfg((c) => ({ ...c, [k]: v }));
  return (
    <form onSubmit={(e) => { e.preventDefault(); set.mutate(cfg); }} className="space-y-4">
      <Alert variant="info">
        <Sliders />
        <AlertTitle>nginx main-context tuning</AlertTitle>
        <AlertDescription>
          Writes <code className="font-mono text-[11px]">worker_processes</code> and friends into <code className="font-mono text-[11px]">nginx.conf</code>
          inside an apigw-managed marker block. Stock directives are neutralised reversibly — leaving the field empty falls back to the OS default.
        </AlertDescription>
      </Alert>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label>worker_processes</Label>
          <Input value={cfg.worker_processes ?? ""} onChange={(e) => upd("worker_processes", e.target.value || undefined)} placeholder="auto" />
        </div>
        <div className="space-y-1.5">
          <Label>worker_connections</Label>
          <Input type="number" value={cfg.worker_connections ?? ""} onChange={(e) => upd("worker_connections", e.target.value ? parseInt(e.target.value, 10) : undefined)} placeholder="1024" />
        </div>
        <div className="space-y-1.5">
          <Label>worker_rlimit_nofile</Label>
          <Input type="number" value={cfg.worker_rlimit_nofile ?? ""} onChange={(e) => upd("worker_rlimit_nofile", e.target.value ? parseInt(e.target.value, 10) : undefined)} placeholder="65535" />
        </div>
        <div className="space-y-1.5">
          <Label>worker_cpu_affinity</Label>
          <Input value={cfg.worker_cpu_affinity ?? ""} onChange={(e) => upd("worker_cpu_affinity", e.target.value || undefined)} placeholder="auto or 0001 0010 0100 1000" />
        </div>
      </div>
      <div className="flex justify-end gap-2">
        <Button type="submit" disabled={set.isPending}>{set.isPending ? "Applying…" : "Apply"}</Button>
      </div>
    </form>
  );
}
