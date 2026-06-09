import { useMutation, useQuery, useQueryClient, type UseQueryOptions } from "@tanstack/react-query";
import { toast } from "sonner";
import { useAdminAPI } from "./useAdminAPI";
import { APIError, ParkedError } from "../lib/api";
import type {
  API,
  AdminToken,
  Approval,
  AuditEntry,
  Consumer,
  ConfigSnapshot,
  Deploy,
  RewriteRule,
  SSOConfig,
  Stream,
  TLSCert,
  TuningConfig,
  WebhookDelivery,
} from "../lib/types";
import { useAuth } from "../auth/auth-context";

// keys defines the canonical React Query keys so invalidation in
// mutations can find the right cache slot deterministically.
export const keys = {
  status:        ["status"] as const,
  apis:          ["admin", "apis"] as const,
  api:           (name: string) => ["admin", "apis", name] as const,
  deploys:       ["admin", "deploys"] as const,
  tls:           ["admin", "tls"] as const,
  audit:         (q?: unknown) => ["admin", "audit", q ?? null] as const,
  approvals:     (status?: string) => ["admin", "approvals", status ?? "all"] as const,
  streams:       ["admin", "streams"] as const,
  consumers:     ["admin", "consumers"] as const,
  adminTokens:   ["admin", "admin-tokens"] as const,
  webhookSetup:  (deploy: string) => ["admin", "webhook-setup", deploy] as const,
  webhookActivity: ["admin", "webhook-activity"] as const,
  sshKey:        ["admin", "ssh-key"] as const,
  tuning:        ["admin", "tuning"] as const,
  sso:           ["admin", "sso"] as const,
  configHistory: ["admin", "config-history"] as const,
};

// authedQuery: skip the request entirely if the user hasn't signed in
// yet (saves a 401 → cache miss → retry loop on landing).
function useAuthedQuery<T>(
  key: readonly unknown[],
  fn: () => Promise<T>,
  extra?: Partial<UseQueryOptions<T>>,
) {
  const { isAuthed } = useAuth();
  return useQuery<T>({
    queryKey: key as unknown as string[],
    queryFn: fn,
    enabled: isAuthed,
    ...extra,
  });
}

// ----- Public, unauthed -----
export function useStatus() {
  const api = useAdminAPI();
  return useQuery({
    queryKey: keys.status as unknown as string[],
    queryFn: () => api.status(),
    refetchInterval: 5_000,
    staleTime: 2_500,
  });
}

// ----- Read -----
export function useApis() {
  const api = useAdminAPI();
  return useAuthedQuery(keys.apis, () => api.listApis());
}
export function useDeploys() {
  const api = useAdminAPI();
  return useAuthedQuery(keys.deploys, () => api.listDeploys());
}
export function useTLS() {
  const api = useAdminAPI();
  return useAuthedQuery(keys.tls, () => api.listTLS());
}
export function useStreams() {
  const api = useAdminAPI();
  return useAuthedQuery(keys.streams, () => api.listStreams());
}
export function useConsumers() {
  const api = useAdminAPI();
  return useAuthedQuery(keys.consumers, () => api.listConsumers());
}
export function useApprovals(status: string = "pending") {
  const api = useAdminAPI();
  return useAuthedQuery(keys.approvals(status), () => api.listApprovals(status));
}
export function useAudit(q?: { actor?: string; action?: string; result?: string; limit?: number }) {
  const api = useAdminAPI();
  return useAuthedQuery(keys.audit(q), () => api.listAudit(q));
}
export function useWebhookSetup(deploy: string | null) {
  const api = useAdminAPI();
  return useQuery({
    queryKey: keys.webhookSetup(deploy ?? "") as unknown as string[],
    queryFn: () => api.webhookSetup(deploy!),
    enabled: !!deploy,
    staleTime: 30_000,
  });
}
export function useWebhookActivity() {
  const api = useAdminAPI();
  return useAuthedQuery<WebhookDelivery[]>(keys.webhookActivity, () => api.listWebhookActivity());
}
export function useAdminTokens() {
  const api = useAdminAPI();
  return useAuthedQuery<AdminToken[]>(keys.adminTokens, () => api.listAdminTokens());
}
export function useTuning() {
  const api = useAdminAPI();
  return useAuthedQuery<TuningConfig>(keys.tuning, () => api.getTuning());
}
export function useSSO() {
  const api = useAdminAPI();
  return useAuthedQuery<SSOConfig | null>(keys.sso, () => api.getSSO());
}
export function useConfigHistory() {
  const api = useAdminAPI();
  return useAuthedQuery<ConfigSnapshot[]>(keys.configHistory, () => api.listConfigHistory());
}
export function useDeploySSHKey() {
  const api = useAdminAPI();
  return useAuthedQuery(keys.sshKey, () => api.deploySSHKey());
}

// ----- Mutations -----
//
// surfaceMutationError centralises the success / parked / fail toasting
// so call sites don't duplicate the pattern. `subject` is the noun in
// the action message (e.g. "foodmanager toggled.", "release rolled
// back."). For parked actions we surface the change ID so reviewers
// can find it in the Pending approvals panel.
function surfaceMutationError(err: unknown, fallback: string) {
  if (err instanceof ParkedError) {
    toast.info(`Change parked for approvals — id: ${err.id}`, { duration: 8000 });
    return;
  }
  if (err instanceof APIError) {
    toast.error(`${fallback}: ${err.message}`, { description: `HTTP ${err.status}` });
    return;
  }
  toast.error(`${fallback}: ${err instanceof Error ? err.message : String(err)}`);
}

export function useToggleApi() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (a: API) => api.updateApi(a.Name, { ...a, Enabled: !a.Enabled }),
    onMutate: async (a: API) => {
      await qc.cancelQueries({ queryKey: keys.apis as unknown as string[] });
      const prev = qc.getQueryData<API[]>(keys.apis as unknown as string[]);
      qc.setQueryData<API[]>(keys.apis as unknown as string[], (xs) =>
        xs?.map((x) => (x.Name === a.Name ? { ...x, Enabled: !x.Enabled } : x)) ?? xs);
      return { prev };
    },
    onError: (err, _a, ctx) => {
      if (ctx?.prev) qc.setQueryData(keys.apis as unknown as string[], ctx.prev);
      surfaceMutationError(err, "Toggle failed");
    },
    onSuccess: (a) => {
      toast.success(`${a.Name} ${a.Enabled ? "enabled" : "disabled"}.`);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: keys.apis as unknown as string[] });
    },
  });
}

export function useAddApi() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (a: API) => api.addApi(a),
    onSuccess: (a) => {
      toast.success(`Registered ${a.Name}.`);
      qc.invalidateQueries({ queryKey: keys.apis as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Add failed"),
  });
}

export function useUpdateApi() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, body }: { name: string; body: API }) => api.updateApi(name, body),
    onSuccess: (a) => {
      toast.success(`${a.Name} updated.`);
      qc.invalidateQueries({ queryKey: keys.apis as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Update failed"),
  });
}

export function useRemoveApi() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.removeApi(name),
    onSuccess: (_v, name) => {
      toast.success(`Removed ${name}.`);
      qc.invalidateQueries({ queryKey: keys.apis as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Remove failed"),
  });
}

export function useAddDeploy() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ body, replaceApi, replaceDeploy }: { body: Deploy; replaceApi?: string; replaceDeploy?: string }) =>
      api.addDeploy(body, { replaceApi, replaceDeploy }),
    onSuccess: (d) => {
      toast.success(`Registered deploy ${d.Name}.`);
      qc.invalidateQueries({ queryKey: keys.deploys as unknown as string[] });
      qc.invalidateQueries({ queryKey: keys.apis as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Add deploy failed"),
  });
}

export function useRemoveDeploy() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.removeDeploy(name),
    onSuccess: (_v, name) => {
      toast.success(`Removed deploy ${name}.`);
      qc.invalidateQueries({ queryKey: keys.deploys as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Remove deploy failed"),
  });
}

export function useRunDeploy() {
  const api = useAdminAPI();
  return useMutation({
    mutationFn: (name: string) => api.runDeploy(name),
    onSuccess: (_v, name) => toast.success(`Queued redeploy of ${name}.`),
    onError: (err) => surfaceMutationError(err, "Redeploy failed"),
  });
}

export function useRollbackDeploy() {
  const api = useAdminAPI();
  return useMutation({
    mutationFn: (name: string) => api.rollbackDeploy(name),
    onSuccess: (_v, name) => toast.success(`Rolled back ${name}.`),
    onError: (err) => surfaceMutationError(err, "Rollback failed"),
  });
}

export function useRotateWebhook() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.rotateWebhook(name),
    onSuccess: (r, name) => {
      toast.success("Secret rotated. Update GitHub now.", { duration: 8000 });
      qc.setQueryData(keys.webhookSetup(name) as unknown as string[], (prev: unknown) =>
        prev && typeof prev === "object" ? { ...prev, secret: r.new_secret } : prev);
    },
    onError: (err) => surfaceMutationError(err, "Rotate failed"),
  });
}

export function useRenewTLS() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) => api.renewTLS(domain),
    onSuccess: (_v, domain) => {
      toast.success(`Renewal queued for ${domain}. Refresh in ~30s.`);
      qc.invalidateQueries({ queryKey: keys.tls as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Renew failed"),
  });
}

export function useEnableTLS() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { domain: string; issuer: string; email?: string; token?: string }) =>
      api.enableTLS(body),
    onSuccess: (_v, body) => {
      toast.success(`Cert issuance started for ${body.domain}. Watch Live logs.`);
      qc.invalidateQueries({ queryKey: keys.tls as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Cert request failed"),
  });
}

export function useRemoveTLS() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (domain: string) => api.removeTLS(domain),
    onSuccess: (_v, domain) => {
      toast.success(`Removed TLS for ${domain}.`);
      qc.invalidateQueries({ queryKey: keys.tls as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Remove TLS failed"),
  });
}

export function useApprove() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, comment }: { id: string; comment?: string }) => api.approve(id, comment),
    onSuccess: (a) => {
      if (a.status === "applied") toast.success(`Applied: ${a.action} on ${a.resource}.`);
      else if (a.status === "approved") toast.success(`Approved (${a.approvals.length}/${a.threshold}).`);
      qc.invalidateQueries({ queryKey: ["admin", "approvals"] });
      qc.invalidateQueries({ queryKey: ["admin"] });
    },
    onError: (err) => surfaceMutationError(err, "Approve failed"),
  });
}

export function useReject() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason?: string }) => api.reject(id, reason),
    onSuccess: () => {
      toast.info("Rejected.");
      qc.invalidateQueries({ queryKey: ["admin", "approvals"] });
    },
    onError: (err) => surfaceMutationError(err, "Reject failed"),
  });
}

export function useAddStream() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: Stream) => api.addStream(s),
    onSuccess: (s) => {
      toast.success(`Stream ${s.Name} added.`);
      qc.invalidateQueries({ queryKey: keys.streams as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Add stream failed"),
  });
}
export function useRemoveStream() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.removeStream(name),
    onSuccess: (_v, name) => {
      toast.success(`Stream ${name} removed.`);
      qc.invalidateQueries({ queryKey: keys.streams as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Remove stream failed"),
  });
}

export function useAddConsumer() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (c: Consumer) => api.addConsumer(c),
    onSuccess: (c) => {
      toast.success(`Consumer ${c.Name} added.`);
      qc.invalidateQueries({ queryKey: keys.consumers as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Add consumer failed"),
  });
}
export function useRemoveConsumer() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.removeConsumer(name),
    onSuccess: (_v, name) => {
      toast.success(`Consumer ${name} removed.`);
      qc.invalidateQueries({ queryKey: keys.consumers as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Remove consumer failed"),
  });
}

export function useAddAdminToken() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (t: AdminToken) => api.addAdminToken(t),
    onSuccess: (t) => {
      toast.success(`Token ${t.Name} added.`);
      qc.invalidateQueries({ queryKey: keys.adminTokens as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Add token failed"),
  });
}
export function useRemoveAdminToken() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.removeAdminToken(name),
    onSuccess: (_v, name) => {
      toast.success(`Token ${name} removed.`);
      qc.invalidateQueries({ queryKey: keys.adminTokens as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Remove token failed"),
  });
}

export function useSetTuning() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (t: TuningConfig) => api.setTuning(t),
    onSuccess: () => {
      toast.success("Tuning applied + nginx reloaded.");
      qc.invalidateQueries({ queryKey: keys.tuning as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "Tuning failed"),
  });
}

export function useSetSSO() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: SSOConfig) => api.setSSO(s),
    onSuccess: () => {
      toast.success("SSO configured. Sign-in flow now goes through your IdP.");
      qc.invalidateQueries({ queryKey: keys.sso as unknown as string[] });
      qc.invalidateQueries({ queryKey: keys.status as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "SSO save failed"),
  });
}

export function useRollbackConfig() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (sha: string) => api.rollbackConfig(sha),
    onSuccess: () => {
      toast.success("Config rolled back + nginx reloaded.");
      qc.invalidateQueries({ queryKey: ["admin"] });
    },
    onError: (err) => surfaceMutationError(err, "Rollback failed"),
  });
}
export function useImportConfig() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (yaml: string) => api.importConfig(yaml),
    onSuccess: () => {
      toast.success("Config imported + nginx reloaded.");
      qc.invalidateQueries({ queryKey: ["admin"] });
    },
    onError: (err) => surfaceMutationError(err, "Import failed"),
  });
}

export function useAuditVerify() {
  const api = useAdminAPI();
  return useMutation({
    mutationFn: () => api.auditVerify(),
    onSuccess: (r) => {
      if (r.valid) toast.success(`Audit chain verified — ${r.entries} entries OK.`);
      else toast.error(`Audit chain BROKEN at entry ${r.broken_at}.`);
    },
    onError: (err) => surfaceMutationError(err, "Verify failed"),
  });
}

export function useGenerateDeploySSHKey() {
  const api = useAdminAPI();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.generateDeploySSHKey(),
    onSuccess: () => {
      toast.success("Deploy SSH key generated. Add it to GitHub.");
      qc.invalidateQueries({ queryKey: keys.sshKey as unknown as string[] });
    },
    onError: (err) => surfaceMutationError(err, "SSH key generation failed"),
  });
}

// Expose useful re-exports so panels only need one import line.
export type { AuditEntry, TLSCert, Approval, RewriteRule, Deploy, API, Stream, Consumer, AdminToken, TuningConfig, SSOConfig, ConfigSnapshot };
