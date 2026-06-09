// Admin API client. Wraps fetch with:
//   - bearer auth (token from AuthContext)
//   - automatic 202 → ParkedError surfacing so callers can show the
//     "Change parked for approvals — id: X" toast instead of treating
//     202 as success and confusing the operator (see v0.4.5 fix).
//   - typed responses (the caller knows what comes back).
//
// We expose methods rather than a generic request() because the type
// system catches typos at compile time and gives editors autocomplete
// across the codebase.

import type {
  API,
  AdminToken,
  Approval,
  AuditEntry,
  Consumer,
  ConfigSnapshot,
  CSRFResponse,
  Deploy,
  RewriteRule,
  SSOConfig,
  StatusSnapshot,
  Stream,
  TLSCert,
  TuningConfig,
  WebhookDelivery,
  WebhookSetup,
} from "./types";

export class APIError extends Error {
  constructor(public status: number, message: string, public body?: unknown) {
    super(message);
    this.name = "APIError";
  }
}

// ParkedError signals the admin handler accepted the request shape but
// the change is sitting in the approvals queue (N-of-M policy) and has
// NOT been applied. Returning this as a thrown error lets useMutation
// onError run the toast logic without the success path firing.
export class ParkedError extends Error {
  constructor(public id: string, public deploy: string | undefined, public raw: unknown) {
    super(`change parked for approvals — id: ${id}`);
    this.name = "ParkedError";
  }
}

type Method = "GET" | "POST" | "PUT" | "DELETE";

export class AdminAPI {
  constructor(private token: string | null) {}

  private async request<T>(
    method: Method,
    path: string,
    opts?: { body?: unknown; csrf?: string },
  ): Promise<T> {
    const headers: Record<string, string> = {};
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    if (opts?.body) headers["Content-Type"] = "application/json";
    if (opts?.csrf) headers["X-CSRF-Token"] = opts.csrf;

    const res = await fetch(path, {
      method,
      headers,
      body: opts?.body ? JSON.stringify(opts.body) : undefined,
    });

    if (res.status === 202) {
      const body = (await res.json().catch(() => ({}))) as { id?: string; deploy?: string };
      throw new ParkedError(body.id || "?", body.deploy, body);
    }
    if (res.status === 204) return undefined as T;
    if (!res.ok) {
      const body = (await res.json().catch(() => ({}))) as { error?: string };
      throw new APIError(res.status, body.error || res.statusText || `HTTP ${res.status}`, body);
    }
    // Some endpoints (eg auth) reply with empty body and 200. Guard.
    const ct = res.headers.get("Content-Type") || "";
    if (!ct.includes("application/json")) return undefined as T;
    return (await res.json()) as T;
  }

  // ---- Read endpoints ----
  status        = ()                                 => this.request<StatusSnapshot>("GET", "api/status");
  csrf          = ()                                 => this.request<CSRFResponse>("GET", "api/admin/csrf");

  listApis      = ()                                 => this.request<API[]>("GET", "api/admin/apis");
  getApi        = (name: string)                     => this.request<API>("GET", `api/admin/apis/${encodeURIComponent(name)}`);
  listDeploys   = ()                                 => this.request<Deploy[]>("GET", "api/admin/deploys");
  getDeploy     = (name: string)                     => this.request<Deploy>("GET", `api/admin/deploys/${encodeURIComponent(name)}`);
  listTLS       = ()                                 => this.request<TLSCert[]>("GET", "api/admin/tls");
  listStreams   = ()                                 => this.request<Stream[]>("GET", "api/admin/streams");
  listConsumers = ()                                 => this.request<Consumer[]>("GET", "api/admin/consumers");
  listAudit     = (q?: { actor?: string; action?: string; result?: string; limit?: number }) => {
    const qs = new URLSearchParams();
    if (q?.actor) qs.set("actor", q.actor);
    if (q?.action) qs.set("action", q.action);
    if (q?.result) qs.set("result", q.result);
    if (q?.limit) qs.set("limit", String(q.limit));
    const s = qs.toString();
    return this.request<AuditEntry[]>("GET", "api/admin/audit" + (s ? "?" + s : ""));
  };
  listApprovals     = (status?: string)              => this.request<Approval[]>("GET",
    "api/admin/approvals" + (status ? "?status=" + encodeURIComponent(status) : ""));
  getApproval       = (id: string)                   => this.request<Approval>("GET",
    `api/admin/approvals/${encodeURIComponent(id)}`);
  webhookSetup      = (deploy: string)               => this.request<WebhookSetup>("GET",
    `api/admin/webhook-setup/${encodeURIComponent(deploy)}`);
  listWebhookActivity = ()                           => this.request<WebhookDelivery[]>("GET", "api/admin/webhook-activity");
  deploySSHKey      = (deploy?: string)              => this.request<{ public_key: string; created_at?: string }>(
    "GET", deploy ? `api/admin/deploys/${encodeURIComponent(deploy)}/sshkey` : "api/admin/deploys/sshkey");
  listAdminTokens   = ()                             => this.request<AdminToken[]>("GET", "api/admin/admin-tokens");
  getTuning         = ()                             => this.request<TuningConfig>("GET", "api/admin/tuning");
  getSSO            = ()                             => this.request<SSOConfig | null>("GET", "api/admin/sso");
  listConfigHistory = ()                             => this.request<ConfigSnapshot[]>("GET", "api/admin/config/history");
  auditVerify       = ()                             => this.request<{ valid: boolean; broken_at?: number; entries: number }>(
    "GET", "api/admin/audit/verify");

  // ---- Mutations (all need CSRF) ----
  addApi    = async (a: API)                                                => this.request<API>("POST", "api/admin/apis", { body: a, csrf: await this.fetchCSRF() });
  updateApi = async (name: string, a: API)                                  => this.request<API>("PUT",  `api/admin/apis/${encodeURIComponent(name)}`, { body: a, csrf: await this.fetchCSRF() });
  removeApi = async (name: string)                                          => this.request<void>("DELETE", `api/admin/apis/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });

  addApiRewrite    = async (name: string, rule: RewriteRule)                => this.request<void>("POST", `api/admin/apis/${encodeURIComponent(name)}/rewrites`, { body: rule, csrf: await this.fetchCSRF() });
  removeApiRewrite = async (name: string, idx: number)                      => this.request<void>("DELETE", `api/admin/apis/${encodeURIComponent(name)}/rewrites/${idx}`, { csrf: await this.fetchCSRF() });

  addDeploy           = async (d: Deploy, opts?: { replaceApi?: string; replaceDeploy?: string }) => {
    const qs = new URLSearchParams();
    if (opts?.replaceApi)    qs.set("replace_api", opts.replaceApi);
    if (opts?.replaceDeploy) qs.set("replace_deploy", opts.replaceDeploy);
    const path = "api/admin/deploys" + (qs.toString() ? "?" + qs.toString() : "");
    return this.request<Deploy>("POST", path, { body: d, csrf: await this.fetchCSRF() });
  };
  updateDeploy        = async (name: string, d: Deploy)                     => this.request<Deploy>("PUT", `api/admin/deploys/${encodeURIComponent(name)}`, { body: d, csrf: await this.fetchCSRF() });
  removeDeploy        = async (name: string)                                => this.request<void>("DELETE", `api/admin/deploys/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });
  runDeploy           = async (name: string)                                => this.request<void>("POST", `api/admin/deploy-run/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });
  rollbackDeploy      = async (name: string)                                => this.request<void>("POST", `api/admin/deploy-rollback/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });
  generateDeploySSHKey = async ()                                           => this.request<{ public_key: string }>("POST", "api/admin/deploys/sshkey", { csrf: await this.fetchCSRF() });

  rotateWebhook = async (name: string)                                      => this.request<{ new_secret: string }>("POST", `api/admin/webhook-rotate/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });
  renewTLS      = async (domain: string)                                    => this.request<void>("POST", `api/admin/tls-renew/${encodeURIComponent(domain)}`, { csrf: await this.fetchCSRF() });
  ocspToggle    = async (domain: string, enabled: boolean)                  => this.request<void>("POST", `api/admin/tls-ocsp/${encodeURIComponent(domain)}`, { body: { enabled }, csrf: await this.fetchCSRF() });
  enableTLS     = async (body: { domain: string; issuer: string; email?: string; token?: string }) => this.request<void>("POST", "api/admin/tls", { body, csrf: await this.fetchCSRF() });
  removeTLS     = async (domain: string)                                    => this.request<void>("DELETE", `api/admin/tls/${encodeURIComponent(domain)}`, { csrf: await this.fetchCSRF() });

  addStream    = async (s: Stream)                                          => this.request<Stream>("POST", "api/admin/streams", { body: s, csrf: await this.fetchCSRF() });
  removeStream = async (name: string)                                       => this.request<void>("DELETE", `api/admin/streams/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });

  addConsumer    = async (c: Consumer)                                      => this.request<Consumer>("POST", "api/admin/consumers", { body: c, csrf: await this.fetchCSRF() });
  removeConsumer = async (name: string)                                     => this.request<void>("DELETE", `api/admin/consumers/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });

  addAdminToken    = async (t: AdminToken)                                  => this.request<AdminToken>("POST", "api/admin/admin-tokens", { body: t, csrf: await this.fetchCSRF() });
  removeAdminToken = async (name: string)                                   => this.request<void>("DELETE", `api/admin/admin-tokens/${encodeURIComponent(name)}`, { csrf: await this.fetchCSRF() });

  approve = async (id: string, comment?: string)                            => this.request<Approval>("POST", `api/admin/approvals/${encodeURIComponent(id)}/approve`, { body: { comment }, csrf: await this.fetchCSRF() });
  reject  = async (id: string, reason?: string)                             => this.request<Approval>("POST", `api/admin/approvals/${encodeURIComponent(id)}/reject`,  { body: { reason },  csrf: await this.fetchCSRF() });

  setTuning   = async (t: TuningConfig)                                     => this.request<void>("POST", "api/admin/tuning",  { body: t, csrf: await this.fetchCSRF() });
  setSSO      = async (s: SSOConfig)                                        => this.request<void>("POST", "api/admin/sso",     { body: s, csrf: await this.fetchCSRF() });
  rollbackConfig = async (sha: string)                                      => this.request<void>("POST", `api/admin/config/rollback/${encodeURIComponent(sha)}`, { csrf: await this.fetchCSRF() });
  exportConfig   = (format: "yaml" | "json" | "openapi")                    => this.request<string>("GET", `api/admin/config?format=${format}`);
  importConfig   = async (yaml: string)                                     => this.request<void>("POST", "api/admin/config/import", { body: { yaml }, csrf: await this.fetchCSRF() });
  exportAudit    = (format: "json" | "csv")                                 => `api/admin/audit/export?format=${format}`;  // download URL, not fetched here

  // Fetch + cache a single CSRF token per mutation. The dashboard
  // daemon's CSRF check is per-request idempotent for write methods,
  // and the token does not currently rotate per-call — but we fetch
  // fresh every time as a defensive belt-and-suspenders: if the daemon
  // rotates the secret server-side we won't see the failure happen at
  // a stale moment days into a session.
  private async fetchCSRF(): Promise<string> {
    const r = await this.csrf();
    return r.token;
  }
}
