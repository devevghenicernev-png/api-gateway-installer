// Type mirror of internal/config Go structs. Sync manually for now —
// the alternative (codegen via reflection) is more machinery than the
// ~20-struct surface warrants. Field names use the Go PascalCase
// shape because the admin handlers JSON-decode without struct tags →
// JSON marshalling matches struct field names exactly.

export interface RewriteRule {
  match: string;
  replace: string;
  flag?: "" | "last" | "break" | "redirect" | "permanent";
}

export interface API {
  Name: string;
  Port: number;
  Path: string;
  Description: string;
  Enabled: boolean;
  Rewrites?: RewriteRule[] | null;
  // Lower-priority fields the dashboard reads but doesn't currently
  // surface beyond display. Keeping them on the type so PUT round-trip
  // doesn't drop them.
  MaxBodySize?: string;
  AccessLog?: string;
}

export interface Deploy {
  Name: string;
  Repo: string;
  Branch: string;
  Port: number;
  Path: string;
  Runtime: "auto" | "node" | "python" | "go" | "docker" | "static" | string;
  Build: string;
  Start: string;
  Description: string;
  Enabled: boolean;
  Rewrites?: RewriteRule[] | null;
  HealthPath?: string;
  LastSHA?: string;
  LastDeploy?: string;
  LastStatus?: "ok" | "failed" | "building" | "stopped" | "idle" | string;
  LastError?: string;
}

// Mirrors apitls.CertInfo on the Go side — keep these in lockstep when
// touching either side. Field names are JSON-case (snake) because the
// Go struct tags JSON-encode that way.
export interface TLSCert {
  domain: string;
  strategy: string;        // letsencrypt | duckdns | selfsigned
  subject?: string;
  issuer?: string;
  not_before?: string;
  not_after?: string;
  days_left: number;
  serial_hex?: string;
  fingerprint_sha256?: string;
  san?: string[];
  ocsp_enabled?: boolean;
}

export interface AuditEntry {
  id: number;
  timestamp: string;       // ISO
  actor: string;
  action: string;          // e.g. "api.edit"
  resource: string;        // e.g. "api/foodmanager"
  result: "ok" | "denied" | "failed" | "parked";
  reason?: string;
  before?: Record<string, unknown> | null;
  after?: Record<string, unknown> | null;
  hash?: string;
  prev_hash?: string;
}

export interface Approval {
  id: string;
  action: string;
  resource: string;
  requested_by: string;
  requested_at: string;
  threshold: number;
  approvals: Array<{ by: string; at: string; comment?: string }>;
  rejected_by?: string;
  rejected_at?: string;
  rejected_reason?: string;
  status: "pending" | "approved" | "applied" | "rejected" | "expired";
  payload?: { before?: unknown; after?: unknown };
  expires_at?: string;
}

export interface WebhookDelivery {
  deploy: string;
  ts: string;
  result: "received" | "verified" | "dispatched" | "rejected" | "failed";
  reason?: string;
  sha?: string;
}

export interface WebhookSetup {
  deploy: string;
  repo: string;
  url: string;
  secret: string;
  content_type: string;
  events: string[];
  note?: string;
}

export interface Stream {
  Name: string;
  Port: number;
  Protocol: "tcp" | "udp";
  Upstream: string;
  Enabled: boolean;
  Description?: string;
}

export interface Consumer {
  Name: string;
  DisplayName?: string;
  Groups: string[];
  Description?: string;
  Disabled?: boolean;
}

export interface AdminToken {
  Name: string;
  User: string;
  Token: string;
}

export interface StatusSnapshot {
  version: string;
  commit: string;
  uptime_sec: number;
  sse_clients: number;
  sso_enabled?: boolean;
  deploys?: Deploy[];
  tls?: TLSCert[];
  apis_count?: number;
  apis?: API[];
}

export interface CSRFResponse {
  token: string;
}

export interface SSOConfig {
  provider: "" | "oidc" | "saml";
  issuer_url: string;
  client_id: string;
  client_secret: string;
  redirect_url: string;
  scopes: string[];
  cookie_secret: string;
  groups_claim?: string;
  allowed_groups?: string[];
  session_ttl?: string;
}

export interface TuningConfig {
  worker_processes?: number | string;
  worker_connections?: number;
  worker_rlimit_nofile?: number;
  worker_cpu_affinity?: string;
}

export interface ConfigSnapshot {
  id: string;
  sha: string;
  actor: string;
  timestamp: string;
  message?: string;
}
