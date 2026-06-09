import { useEffect, useState } from "react";
import { Lock } from "lucide-react";
import { useSSO, useSetSSO } from "../../hooks/useAdminQueries";
import type { SSOConfig } from "../../lib/types";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Alert, AlertDescription, AlertTitle } from "../ui/alert";

const empty: SSOConfig = {
  provider: "oidc", issuer_url: "", client_id: "", client_secret: "",
  redirect_url: "", scopes: ["openid", "email", "profile"], cookie_secret: "",
  groups_claim: "groups", allowed_groups: [], session_ttl: "8h",
};

export function SsoSettings() {
  const { data } = useSSO();
  const set = useSetSSO();
  const [cfg, setCfg] = useState<SSOConfig>(data ?? empty);
  useEffect(() => { if (data) setCfg(data); }, [data]);

  const upd = <K extends keyof SSOConfig>(k: K, v: SSOConfig[K]) => setCfg((c) => ({ ...c, [k]: v }));

  function submit(e: React.FormEvent) {
    e.preventDefault();
    set.mutate(cfg);
  }
  return (
    <form onSubmit={submit} className="space-y-4">
      <Alert variant="info">
        <Lock />
        <AlertTitle>OIDC sign-in</AlertTitle>
        <AlertDescription>
          Configure the IdP block. apigw fetches discovery from <code className="font-mono text-[11px]">{`{issuer_url}/.well-known/openid-configuration`}</code>.
          Add the Redirect URL to your IdP&apos;s allow-list before saving.
        </AlertDescription>
      </Alert>
      <Field label="Issuer URL" value={cfg.issuer_url} onChange={(v) => upd("issuer_url", v)} placeholder="https://your-idp/realms/main" />
      <Field label="Client ID" value={cfg.client_id} onChange={(v) => upd("client_id", v)} placeholder="apigw-dashboard" />
      <Field label="Client secret" value={cfg.client_secret} type="password" onChange={(v) => upd("client_secret", v)} placeholder="REDACTED" />
      <Field label="Redirect URL" value={cfg.redirect_url} onChange={(v) => upd("redirect_url", v)} placeholder="https://your-host/dashboard/api/admin/sso/callback" />
      <Field label="Cookie secret" value={cfg.cookie_secret} onChange={(v) => upd("cookie_secret", v)} placeholder="32+ random bytes (openssl rand -base64 32)" />
      <Field label="Groups claim" value={cfg.groups_claim || ""} onChange={(v) => upd("groups_claim", v)} placeholder="groups (optional)" />
      <Field label="Allowed groups (comma-sep)" value={(cfg.allowed_groups || []).join(", ")} onChange={(v) => upd("allowed_groups", v.split(",").map((s) => s.trim()).filter(Boolean))} placeholder="apigw-admins, sre" />
      <Field label="Session TTL" value={cfg.session_ttl || "8h"} onChange={(v) => upd("session_ttl", v)} placeholder="8h" />
      <div className="flex justify-end gap-2">
        <Button type="submit" disabled={set.isPending}>{set.isPending ? "Saving…" : "Save SSO config"}</Button>
      </div>
    </form>
  );
}

function Field({ label, value, onChange, placeholder, type = "text" }: {
  label: string; value: string; onChange: (v: string) => void; placeholder?: string; type?: string;
}) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      <Input type={type} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} autoComplete="off" />
    </div>
  );
}
