// Package config owns the apigw configuration: typed Go struct, atomic save
// with flock, koanf-based load with documented precedence.
//
// Precedence (last wins): defaults → /etc/apigw/config.yaml (or XDG) → env
// (APIGW_*) → flags (wired by the cobra command).
//
// Atomic save: write tmp file, fsync, rename onto live path. The flock guards
// against concurrent writers (two `apigw api add` racing the same /etc/apigw).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

// SchemaVersion is bumped on every breaking change to the on-disk format.
// Migrations live in migrate.go, keyed on this number.
const SchemaVersion = 1

// Config is the canonical in-memory representation of apigw's state.
// Each field maps 1:1 to a top-level key in config.yaml.
type Config struct {
	Version   int       `koanf:"version" yaml:"version"`
	Listen    Listen    `koanf:"listen" yaml:"listen"`
	APIs      []API     `koanf:"apis" yaml:"apis"`
	Deploys   []Deploy  `koanf:"deployments" yaml:"deployments"`
	Webhook   Webhook   `koanf:"webhook" yaml:"webhook"`
	Dashboard Dashboard `koanf:"dashboard" yaml:"dashboard"`
	TLS       TLS       `koanf:"tls" yaml:"tls"`

	// E1+ enterprise features. All optional; zero-value = feature off.
	Security Security `koanf:"security" yaml:"security,omitempty"`
	Tenants  []Tenant `koanf:"tenants" yaml:"tenants,omitempty"`
	Alerts   Alerts   `koanf:"alerts" yaml:"alerts,omitempty"`
	GitOps   GitOps   `koanf:"gitops" yaml:"gitops,omitempty"`
	Cluster  Cluster  `koanf:"cluster" yaml:"cluster,omitempty"`

	// path is where this config was loaded from / will be saved to. Not in yaml.
	path string `koanf:"-" yaml:"-"`
}

// Security bundles RBAC, audit, OPA-policy, approvals, and admin-API auth.
// Empty Security = legacy mode: admin API requires no auth (preserved for
// upgrade paths), and audit/policy/approvals are inert.
type Security struct {
	// AdminTokens is the static API-token allow-list for /api/admin/*.
	// Each token grants the listed roles (subject to the RBAC engine below).
	// Tokens are matched in constant time against the request's
	// `Authorization: Bearer <token>` header.
	//
	// At least one entry with role "owner" is required to fully enable
	// security mode — otherwise nobody can ever modify config.
	AdminTokens []AdminToken `koanf:"admin_tokens" yaml:"admin_tokens,omitempty"`

	// RBAC enforcement. enforce=false (default for soft-rollout) means every
	// denied permission is still logged via audit but the request goes
	// through. Flip to true once you've watched the audit log for a week.
	RBACEnforce bool         `koanf:"rbac_enforce" yaml:"rbac_enforce,omitempty"`
	Roles       []Role       `koanf:"roles" yaml:"roles,omitempty"`
	Assignments []Assignment `koanf:"assignments" yaml:"assignments,omitempty"`

	// OPA policy directory. Each *.rego file is loaded and evaluated on every
	// config mutation. Empty = no policies = fail-open.
	PolicyDir string `koanf:"policy_dir" yaml:"policy_dir,omitempty"`

	// Approvals threshold for "dangerous" actions. 0 = no approvals required.
	// >0 = the action is parked, ID is returned, and N other operators must
	// approve via `apigw approvals approve <id>`.
	ApprovalsThreshold int `koanf:"approvals_threshold" yaml:"approvals_threshold,omitempty"`

	// StateDir is where audit.db / approvals.db live. Defaults to
	// /var/lib/apigw when empty.
	StateDir string `koanf:"state_dir" yaml:"state_dir,omitempty"`

	// CSRFSecret is the HMAC key used to sign anti-CSRF tokens issued to the
	// embedded dashboard. Empty = generated on first start and cached in
	// StateDir. Rotate by removing the cache file.
	CSRFSecret string `koanf:"csrf_secret" yaml:"csrf_secret,omitempty"`

	// MaxRequestBytes caps the body size accepted by /api/admin/* endpoints
	// (defence-in-depth against memory-DoS). 0 = 1 MiB default.
	MaxRequestBytes int64 `koanf:"max_request_bytes" yaml:"max_request_bytes,omitempty"`

	// AuditReads, when true, logs even successful read-only operations
	// (list / show / query / status). Default false — these are noisy
	// and SOX/PCI/SOC2 care about mutations and denials, not lookups.
	// Flip to true for highest-paranoia / breach-investigation mode.
	AuditReads bool `koanf:"audit_reads" yaml:"audit_reads,omitempty"`
}

// AdminToken associates an opaque secret with a user identity + role set.
// Tokens are stored verbatim — file mode 0o600 on /etc/apigw/config.yaml
// is the protection. Rotate via `apigw auth rotate-token`.
type AdminToken struct {
	Name   string   `koanf:"name" yaml:"name"`   // human label, e.g. "ops-deploy-bot"
	User   string   `koanf:"user" yaml:"user"`   // RBAC subject identity
	Token  string   `koanf:"token" yaml:"token"` // bearer secret; min 32 chars
	Groups []string `koanf:"groups" yaml:"groups,omitempty"`
}

// Role mirrors rbac.Role for YAML loading. Kept here to avoid a config →
// rbac import cycle.
type Role struct {
	Name        string   `koanf:"name" yaml:"name"`
	Description string   `koanf:"description" yaml:"description,omitempty"`
	Permissions []string `koanf:"permissions" yaml:"permissions"`
}

// Assignment mirrors rbac.Assignment.
type Assignment struct {
	User  string   `koanf:"user" yaml:"user,omitempty"`
	Group string   `koanf:"group" yaml:"group,omitempty"`
	Roles []string `koanf:"roles" yaml:"roles"`
}

// Tenant mirrors tenant.Tenant.
type Tenant struct {
	ID          string   `koanf:"id" yaml:"id"`
	Name        string   `koanf:"name" yaml:"name"`
	Description string   `koanf:"description" yaml:"description,omitempty"`
	PathPrefix  string   `koanf:"path_prefix" yaml:"path_prefix,omitempty"`
	Admins      []string `koanf:"admins" yaml:"admins,omitempty"`
	Enabled     bool     `koanf:"enabled" yaml:"enabled"`
	MaxAPIs     int      `koanf:"max_apis" yaml:"max_apis,omitempty"`
	MaxDeploys  int      `koanf:"max_deploys" yaml:"max_deploys,omitempty"`
}

// Alerts wires Slack/Teams/PagerDuty/Webhook/SMTP notifiers. Every notifier
// is optional; missing = silently disabled.
type Alerts struct {
	Slack     string        `koanf:"slack_webhook" yaml:"slack_webhook,omitempty"`
	Teams     string        `koanf:"teams_webhook" yaml:"teams_webhook,omitempty"`
	PagerDuty string        `koanf:"pagerduty_key" yaml:"pagerduty_key,omitempty"`
	Webhook   AlertsWebhook `koanf:"webhook" yaml:"webhook,omitempty"`
	Email     AlertsEmail   `koanf:"email" yaml:"email,omitempty"`

	// TLSExpiryWarnDays — fires cert.expiring N days before NotAfter (default 30).
	TLSExpiryWarnDays int `koanf:"tls_expiry_warn_days" yaml:"tls_expiry_warn_days,omitempty"`
}

type AlertsWebhook struct {
	URL     string            `koanf:"url" yaml:"url"`
	Headers map[string]string `koanf:"headers" yaml:"headers,omitempty"`
}

type AlertsEmail struct {
	Host     string   `koanf:"host" yaml:"host"`
	Port     int      `koanf:"port" yaml:"port,omitempty"`
	Username string   `koanf:"username" yaml:"username,omitempty"`
	Password string   `koanf:"password" yaml:"password,omitempty"`
	From     string   `koanf:"from" yaml:"from"`
	To       []string `koanf:"to" yaml:"to"`
}

// GitOps configures the pull-mode reconciler. Empty RepoURL = disabled.
type GitOps struct {
	RepoURL     string `koanf:"repo_url" yaml:"repo_url,omitempty"`
	Branch      string `koanf:"branch" yaml:"branch,omitempty"`
	Path        string `koanf:"path" yaml:"path,omitempty"`
	IntervalSec int    `koanf:"interval_sec" yaml:"interval_sec,omitempty"`
	HTTPToken   string `koanf:"http_token" yaml:"http_token,omitempty"`
	SSHKeyFile  string `koanf:"ssh_key_file" yaml:"ssh_key_file,omitempty"`
	SSHKeyPass  string `koanf:"ssh_key_pass" yaml:"ssh_key_pass,omitempty"`
}

// Cluster turns on Raft-replicated config. Empty NodeID = disabled.
type Cluster struct {
	NodeID      string `koanf:"node_id" yaml:"node_id,omitempty"`
	BindAddr    string `koanf:"bind_addr" yaml:"bind_addr,omitempty"`
	DataDir     string `koanf:"data_dir" yaml:"data_dir,omitempty"`
	Bootstrap   bool   `koanf:"bootstrap" yaml:"bootstrap,omitempty"`
	Peers       []Peer `koanf:"peers" yaml:"peers,omitempty"`
	HeartbeatMS int    `koanf:"heartbeat_ms" yaml:"heartbeat_ms,omitempty"`
}

type Peer struct {
	NodeID  string `koanf:"node_id" yaml:"node_id"`
	Address string `koanf:"address" yaml:"address"`
}

// Listen describes the public-facing nginx listener.
type Listen struct {
	HTTPPort   int    `koanf:"http_port" yaml:"http_port"`
	HTTPSPort  int    `koanf:"https_port" yaml:"https_port"`
	ServerName string `koanf:"server_name" yaml:"server_name"`

	// MaxBodySize is the global default `client_max_body_size`. nginx's
	// default is 1m; we mirror that. Per-API/Deploy values override.
	MaxBodySize string `koanf:"max_body_size" yaml:"max_body_size,omitempty"`

	// Gzip configures the http{}-scope compression block. Defaults are
	// production-sane (level 5, min_length 1024, common MIME types).
	Gzip Gzip `koanf:"gzip" yaml:"gzip"`
}

// Gzip mirrors nginx's gzip module directives. Empty = "use defaults"; set
// Enabled=false to suppress the block entirely.
type Gzip struct {
	Enabled   bool     `koanf:"enabled" yaml:"enabled"`
	Level     int      `koanf:"level" yaml:"level,omitempty"`           // 1-9; default 5
	MinLength int      `koanf:"min_length" yaml:"min_length,omitempty"` // bytes; default 1024
	Types     []string `koanf:"types" yaml:"types,omitempty"`           // MIME list; default text/* + json + xml + svg
}

// API is an upstream service registered with apigw.
//
// The bash version stored these in apis.json; the migration step (Phase 8)
// converts that to this struct. Middleware fields (RateLimit, CORS,
// Headers, …) are all optional — when nil/empty the generator emits no
// extra nginx directives.
type API struct {
	Name        string `koanf:"name" yaml:"name"`
	Port        int    `koanf:"port" yaml:"port"`
	Path        string `koanf:"path" yaml:"path"`               // mount point, default /api/<name>
	Description string `koanf:"description" yaml:"description"` // free-form, shown in `api list`
	Enabled     bool   `koanf:"enabled" yaml:"enabled"`

	// Multi-upstream pool. When non-empty, overrides Port.
	Upstreams   []Upstream `koanf:"upstreams" yaml:"upstreams,omitempty"`
	LoadBalance string     `koanf:"load_balance" yaml:"load_balance,omitempty"` // round_robin (default) | least_conn | ip_hash | random
	// GRPC switches the location to grpc_pass instead of proxy_pass. Requires
	// HTTP/2 on the listener — apigw's tls-server.tmpl already sets `http2 on;`.
	GRPC bool `koanf:"grpc" yaml:"grpc,omitempty"`

	// Middleware — all optional.
	MaxBodySize string       `koanf:"max_body_size" yaml:"max_body_size,omitempty"` // "10m", "1g"; default = global
	Headers     *Headers     `koanf:"headers" yaml:"headers,omitempty"`
	IPRules     *IPRules     `koanf:"ip_rules" yaml:"ip_rules,omitempty"`
	CORS        *CORS        `koanf:"cors" yaml:"cors,omitempty"`
	BasicAuth   *BasicAuth   `koanf:"basic_auth" yaml:"basic_auth,omitempty"`
	RateLimit   *RateLimit   `koanf:"rate_limit" yaml:"rate_limit,omitempty"`
	ForwardAuth *ForwardAuth `koanf:"forward_auth" yaml:"forward_auth,omitempty"`
	HealthCheck *HealthCheck `koanf:"health_check" yaml:"health_check,omitempty"`
	Retry       *Retry       `koanf:"retry" yaml:"retry,omitempty"`
	JWT         *JWT         `koanf:"jwt" yaml:"jwt,omitempty"`
	MTLS        *MTLS        `koanf:"mtls" yaml:"mtls,omitempty"`
	Canary      *Canary      `koanf:"canary" yaml:"canary,omitempty"`
	Transform   *Transform   `koanf:"transform" yaml:"transform,omitempty"`
	Versioning  *Versioning  `koanf:"versioning" yaml:"versioning,omitempty"`

	// CustomLocation / CustomServer (F14) inject raw nginx directives into
	// the generated config. CustomLocation lands inside the `location {…}`
	// block; CustomServer lands at server scope (after listen, before
	// includes). Useful for nginx features we don't model — `access_log`
	// per API, `proxy_intercept_errors`, custom `error_page`, etc.
	//
	// Sanitization: we block `}` to prevent escaping the location/server
	// block, but otherwise pass directives verbatim. `nginx -t` will catch
	// syntax errors at apply time.
	CustomLocation string `koanf:"custom_location" yaml:"custom_location,omitempty"`
	CustomServer   string `koanf:"custom_server" yaml:"custom_server,omitempty"`
}

// Upstream is one server in the API/Deploy upstream pool. Weight, Backup,
// MaxFails, FailTimeout map onto nginx `server` directive parameters.
type Upstream struct {
	Address     string `koanf:"address" yaml:"address"`         // host:port — required
	Weight      int    `koanf:"weight" yaml:"weight,omitempty"` // weighted round-robin; default 1
	Backup      bool   `koanf:"backup" yaml:"backup,omitempty"` // only used when others are down
	MaxFails    int    `koanf:"max_fails" yaml:"max_fails,omitempty"`
	FailTimeout string `koanf:"fail_timeout" yaml:"fail_timeout,omitempty"`
	Down        bool   `koanf:"down" yaml:"down,omitempty"` // marks server permanently down
}

// Headers describes per-route header manipulation. Empty maps render no
// directives.
type Headers struct {
	// RequestSet emits `proxy_set_header K V;`. Values support nginx
	// variables ($host, $remote_addr, ...) — they pass through verbatim.
	RequestSet map[string]string `koanf:"request_set" yaml:"request_set,omitempty"`
	// ResponseAdd emits `add_header K V always;`. The `always` flag means
	// nginx adds the header on every response, including 4xx/5xx.
	ResponseAdd map[string]string `koanf:"response_add" yaml:"response_add,omitempty"`
	// ResponseHide emits `proxy_hide_header K;` — strips backend-set
	// headers like `X-Powered-By` or `Server`.
	ResponseHide []string `koanf:"response_hide" yaml:"response_hide,omitempty"`
}

// IPRules controls per-route allow/deny.
//
// Order: emit Allow entries first, then `deny all;` as implicit fall-through
// when Allow is non-empty. Deny entries always emit verbatim.
type IPRules struct {
	Allow []string `koanf:"allow" yaml:"allow,omitempty"` // CIDRs or single IPs
	Deny  []string `koanf:"deny" yaml:"deny,omitempty"`
}

// CORS configures cross-origin requests. Multiple origins use a map at
// http{} scope to echo back the exact request origin (nginx can't send a
// list in Access-Control-Allow-Origin).
type CORS struct {
	Origins     []string `koanf:"origins" yaml:"origins"`           // exact match; "*" allowed for non-credentialed
	Methods     []string `koanf:"methods" yaml:"methods,omitempty"` // default GET, POST, OPTIONS
	Headers     []string `koanf:"headers" yaml:"headers,omitempty"` // default Content-Type, Authorization
	Credentials bool     `koanf:"credentials" yaml:"credentials"`   // incompatible with Origins=["*"]
	MaxAge      int      `koanf:"max_age" yaml:"max_age,omitempty"` // preflight cache seconds; default 86400
}

// BasicAuth configures HTTP Basic auth via nginx `auth_basic_user_file`.
// The htpasswd file lives at /etc/apigw/htpasswd.<api-name> with mode 0640.
type BasicAuth struct {
	Realm string `koanf:"realm" yaml:"realm"`
	// File overrides the default path. Empty = /etc/apigw/htpasswd.<name>.
	File string `koanf:"file" yaml:"file,omitempty"`
}

// RateLimit applies nginx `limit_req` with a per-API zone.
type RateLimit struct {
	RPS   int    `koanf:"rps" yaml:"rps"`               // sustained rate
	Burst int    `koanf:"burst" yaml:"burst,omitempty"` // default 2*RPS
	Key   string `koanf:"key" yaml:"key,omitempty"`     // "ip" (default) | "header:<name>"
}

// ForwardAuth proxies the inbound request through an external auth service
// (Authelia, oauth2-proxy, Pomerium) before forwarding to the upstream.
type ForwardAuth struct {
	Address    string   `koanf:"address" yaml:"address"`                   // full URL of the auth check endpoint
	SignInURL  string   `koanf:"sign_in_url" yaml:"sign_in_url,omitempty"` // 401 → redirect here
	SetHeaders []string `koanf:"set_headers" yaml:"set_headers,omitempty"` // upstream response headers to copy forward (X-Remote-User, …)
}

// HealthCheck is currently passive (nginx OSS): MaxFails + FailTimeout on
// the upstream block. Active probing (HTTP GET every Interval) is added by
// the dashboard daemon.
type HealthCheck struct {
	Path        string `koanf:"path" yaml:"path,omitempty"`                 // active probe path; "" disables active probing
	Interval    string `koanf:"interval" yaml:"interval,omitempty"`         // active probe period; "10s"
	MaxFails    int    `koanf:"max_fails" yaml:"max_fails,omitempty"`       // nginx default 1; we default 3
	FailTimeout string `koanf:"fail_timeout" yaml:"fail_timeout,omitempty"` // nginx default 10s
}

// Retry maps to nginx `proxy_next_upstream`. The default set retries on
// connect/read errors + 502/503/504; users can extend.
type Retry struct {
	Conditions []string `koanf:"conditions" yaml:"conditions,omitempty"` // e.g. ["error", "timeout", "http_502", "http_503", "http_504"]
}

// Canary describes a percentage-based traffic split — N% of requests go
// to CanaryUpstreams, the rest to the primary Upstreams pool. Used for
// blue/green and progressive rollouts at the gateway layer.
//
// Pinning: when PinHeader is set, clients with that header (e.g.
// `X-Canary: 1`) always hit the canary regardless of percentage.
//
// nginx implementation: split_clients $client_id $upstream_pick — the
// generator emits a split_clients map + uses $upstream_pick in proxy_pass.
type Canary struct {
	Weight    int        `koanf:"weight" yaml:"weight"`                   // 0-100, percent to canary
	PinHeader string     `koanf:"pin_header" yaml:"pin_header,omitempty"` // e.g. "X-Canary"
	Upstreams []Upstream `koanf:"upstreams" yaml:"upstreams"`
}

// Transform describes per-API request/response body manipulation. We
// support a small set of JSON field operations evaluated in the dashboard
// daemon via /transform/<api>. For richer logic operators wire forward_auth
// to a real service.
type Transform struct {
	// RequestSetJSON injects/overrides a top-level JSON field on the request
	// body before forwarding to upstream. Map of jq-style path → value.
	// Example: {".user_id": "$claims.sub"}.
	RequestSetJSON map[string]string `koanf:"request_set_json" yaml:"request_set_json,omitempty"`

	// RequestStripJSON removes top-level fields. Example: [".password"].
	RequestStripJSON []string `koanf:"request_strip_json" yaml:"request_strip_json,omitempty"`

	// ResponseStripJSON removes top-level fields from the upstream response.
	ResponseStripJSON []string `koanf:"response_strip_json" yaml:"response_strip_json,omitempty"`
}

// Versioning sets how a /v1/x vs /v2/x split is routed. Strategy:
//
//	path    — /v1/api → upstream-v1, /v2/api → upstream-v2 (default)
//	header  — Accept-Version: v2 → upstream-v2
//	query   — ?version=v2 → upstream-v2
//
// SunsetDate, when set, emits an RFC 8594 Sunset header on every
// response so clients see deprecation.
type Versioning struct {
	Strategy   string `koanf:"strategy" yaml:"strategy"`                 // path | header | query
	SunsetDate string `koanf:"sunset_date" yaml:"sunset_date,omitempty"` // RFC 3339
}

// MTLS configures per-API mutual TLS (client cert auth). nginx terminates
// the handshake with ssl_verify_client; the dashboard re-validates against
// per-API allow-lists (CN, SAN, sha256 fingerprint) via auth_request.
//
// CAFile is required — it's the bundle of CAs nginx trusts. AllowCNs /
// AllowSANs / AllowFingerprints are AND-of-OR: provide any combination,
// and a cert passes if it matches at least one rule from each provided list.
type MTLS struct {
	CAFile            string   `koanf:"ca_file" yaml:"ca_file"`
	AllowCNs          []string `koanf:"allow_cns" yaml:"allow_cns,omitempty"`
	AllowSANs         []string `koanf:"allow_sans" yaml:"allow_sans,omitempty"`
	AllowFingerprints []string `koanf:"allow_fingerprints" yaml:"allow_fingerprints,omitempty"`
	Optional          bool     `koanf:"optional" yaml:"optional,omitempty"`
}

// JWT configures per-API JWT validation. The dashboard daemon serves an
// internal /auth/jwt/<api> endpoint; nginx auth_request calls it on every
// request. Algorithm + (HMACSecret OR JWKSURL) is the minimum.
//
// See internal/auth/jwt.go for the verifier implementation. The same struct
// is mirrored as internal/auth.JWTConfig — they're kept in sync deliberately
// (config = serialized form, auth = in-process form).
type JWT struct {
	Algorithm     string            `koanf:"algorithm" yaml:"algorithm"`               // HS256/RS256/ES256/EdDSA/...
	HMACSecret    string            `koanf:"hmac_secret" yaml:"hmac_secret,omitempty"` // required for HS*
	JWKSURL       string            `koanf:"jwks_url" yaml:"jwks_url,omitempty"`       // required for RS*/ES*/EdDSA
	Issuer        string            `koanf:"issuer" yaml:"issuer,omitempty"`           // optional iss check
	Audience      string            `koanf:"audience" yaml:"audience,omitempty"`       // optional aud check
	RequireClaims map[string]string `koanf:"require_claims" yaml:"require_claims,omitempty"`
}

// Mirror of the API middleware on Deploy — when both are set they apply at
// the deploy's nginx location. Keeping the shape identical means apigw api
// and apigw deploy share the same docs + generator logic.

// Deploy is a git-backed deployment.
//
// The actual on-disk layout lives in /var/lib/apigw/<name>/{releases/<sha>,current}
// — see internal/deploy/swap.go. The fields here are configuration; runtime
// state (last SHA, last deploy time, status) is recorded after each successful
// pass so `apigw deploy list` doesn't have to shell out to git/journald.
type Deploy struct {
	Name        string `koanf:"name" yaml:"name"`
	Repo        string `koanf:"repo" yaml:"repo"`
	Branch      string `koanf:"branch" yaml:"branch"`
	Port        int    `koanf:"port" yaml:"port"`       // upstream port nginx proxies to
	Path        string `koanf:"path" yaml:"path"`       // nginx mount, default /apps/<name>
	Runtime     string `koanf:"runtime" yaml:"runtime"` // auto|node|python|go|docker|static
	Build       string `koanf:"build" yaml:"build"`     // override build cmd (empty = runtime default)
	Start       string `koanf:"start" yaml:"start"`     // override start cmd
	Description string `koanf:"description" yaml:"description"`
	Enabled     bool   `koanf:"enabled" yaml:"enabled"`

	// Recorded after each pass.
	LastSHA    string `koanf:"last_sha" yaml:"last_sha"`
	LastDeploy string `koanf:"last_deploy" yaml:"last_deploy"` // RFC3339
	LastStatus string `koanf:"last_status" yaml:"last_status"` // ok|failed|building|stopped
	LastError  string `koanf:"last_error" yaml:"last_error,omitempty"`

	// Multi-upstream + LB mirror of API.
	Upstreams   []Upstream `koanf:"upstreams" yaml:"upstreams,omitempty"`
	LoadBalance string     `koanf:"load_balance" yaml:"load_balance,omitempty"`
	GRPC        bool       `koanf:"grpc" yaml:"grpc,omitempty"`

	// Middleware mirror of API. See API field comments for semantics.
	MaxBodySize string       `koanf:"max_body_size" yaml:"max_body_size,omitempty"`
	Headers     *Headers     `koanf:"headers" yaml:"headers,omitempty"`
	IPRules     *IPRules     `koanf:"ip_rules" yaml:"ip_rules,omitempty"`
	CORS        *CORS        `koanf:"cors" yaml:"cors,omitempty"`
	BasicAuth   *BasicAuth   `koanf:"basic_auth" yaml:"basic_auth,omitempty"`
	RateLimit   *RateLimit   `koanf:"rate_limit" yaml:"rate_limit,omitempty"`
	ForwardAuth *ForwardAuth `koanf:"forward_auth" yaml:"forward_auth,omitempty"`
	HealthCheck *HealthCheck `koanf:"health_check" yaml:"health_check,omitempty"`
	Retry       *Retry       `koanf:"retry" yaml:"retry,omitempty"`
	JWT         *JWT         `koanf:"jwt" yaml:"jwt,omitempty"`
	MTLS        *MTLS        `koanf:"mtls" yaml:"mtls,omitempty"`

	CustomLocation string `koanf:"custom_location" yaml:"custom_location,omitempty"`
	CustomServer   string `koanf:"custom_server" yaml:"custom_server,omitempty"`
}

type Webhook struct {
	Enabled bool   `koanf:"enabled" yaml:"enabled"`
	Port    int    `koanf:"port" yaml:"port"`
	Path    string `koanf:"path" yaml:"path"` // default /webhook
}

type Dashboard struct {
	Enabled bool   `koanf:"enabled" yaml:"enabled"`
	Port    int    `koanf:"port" yaml:"port"`
	Path    string `koanf:"path" yaml:"path"` // default /dashboard
}

type TLS struct {
	Strategy     string   `koanf:"strategy" yaml:"strategy"` // none|letsencrypt|duckdns|self-signed
	Domains      []string `koanf:"domains" yaml:"domains"`
	Email        string   `koanf:"email" yaml:"email"`
	Staging      bool     `koanf:"staging" yaml:"staging"`
	DuckDNSToken string   `koanf:"duckdns_token" yaml:"duckdns_token"`
}

// Defaults returns a config preloaded with sensible defaults. Save-only fields
// (path) are left empty; the caller sets them.
func Defaults() Config {
	return Config{
		Version: SchemaVersion,
		Listen: Listen{
			HTTPPort:   80,
			HTTPSPort:  443,
			ServerName: "_",
		},
		APIs:    []API{},
		Deploys: []Deploy{},
		Webhook: Webhook{
			Enabled: false,
			Port:    9000,
			Path:    "/webhook",
		},
		Dashboard: Dashboard{
			Enabled: true,
			Port:    9080,
			Path:    "/dashboard",
		},
		TLS: TLS{Strategy: "none"},
	}
}

// SearchPaths is the ordered list of locations Load() probes. First hit wins.
// XDG first (per-user), then system-wide /etc.
func SearchPaths() []string {
	paths := []string{}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "apigw", "config.yaml"))
	} else if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "apigw", "config.yaml"))
	}
	paths = append(paths, "/etc/apigw/config.yaml")
	return paths
}

// Load reads config from the first SearchPaths() entry that exists, applies
// env overrides (APIGW_*), and returns a populated Config.
//
// If no config file exists, returns Defaults() with path set to the preferred
// XDG location — apigw install then writes /etc/apigw/config.yaml.
func Load() (cmdutil.Config, error) {
	return LoadFrom("")
}

// LoadFrom is Load with an explicit path; used by --config flag.
func LoadFrom(explicit string) (cmdutil.Config, error) {
	k := koanf.New(".")
	def := Defaults()

	// 1. defaults
	if err := k.Load(structs.Provider(def, "koanf"), nil); err != nil {
		return nil, cmdutil.NewConfigError(fmt.Errorf("load defaults: %w", err))
	}

	// 2. file
	path := explicit
	if path == "" {
		for _, p := range SearchPaths() {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, cmdutil.NewConfigError(fmt.Errorf("read %s: %w", path, err))
		}
	} else {
		// No file present — use the system-wide path for future Save().
		path = "/etc/apigw/config.yaml"
	}

	// 3. env (APIGW_LISTEN_HTTP_PORT=8080 -> listen.http_port)
	if err := k.Load(env.Provider("APIGW_", ".", envMap), nil); err != nil {
		return nil, cmdutil.NewConfigError(fmt.Errorf("load env: %w", err))
	}

	cfg := Defaults()
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, cmdutil.NewConfigError(fmt.Errorf("decode: %w", err))
	}
	cfg.path = path
	return &cfg, nil
}

// envMap turns APIGW_LISTEN_HTTP_PORT into listen.http_port.
func envMap(s string) string {
	// strip APIGW_ prefix
	s = s[len("APIGW_"):]
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_':
			out = append(out, '.')
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// Path returns the on-disk location this config will be saved to.
func (c *Config) Path() string { return c.path }

// SetPath overrides the save target. Used by `apigw install` when it decides
// between XDG and /etc.
func (c *Config) SetPath(p string) { c.path = p }

// Save writes the config atomically: tmp + fsync + rename, under a flock.
//
// Always writes the schema version + a managed-by banner via YAML doc comment
// in the future — for now a clean YAML dump is sufficient.
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("config has no path; call SetPath first")
	}
	if c.Version == 0 {
		c.Version = SchemaVersion
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	lock := flock.New(filepath.Join(dir, ".lock"))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	tmp, err := os.CreateTemp(dir, "config.yaml.*.tmp")
	if err != nil {
		return fmt.Errorf("tmp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // best-effort cleanup if anything below fails

	// Encode via koanf-yaml for stable formatting.
	k := koanf.New(".")
	if err := k.Load(structs.Provider(*c, "koanf"), nil); err != nil {
		tmp.Close()
		return fmt.Errorf("marshal: %w", err)
	}
	b, err := k.Marshal(yaml.Parser())
	if err != nil {
		tmp.Close()
		return fmt.Errorf("encode yaml: %w", err)
	}
	banner := fmt.Sprintf("# MANAGED BY apigw — do not edit.\n"+
		"# Use `apigw api add|remove` or run `apigw install`.\n"+
		"# Generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	if _, err := tmp.WriteString(banner); err != nil {
		tmp.Close()
		return fmt.Errorf("write banner: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Snapshot returns the current YAML bytes — used by `apigw backup`.
func (c *Config) Snapshot() ([]byte, error) {
	k := koanf.New(".")
	if err := k.Load(structs.Provider(*c, "koanf"), nil); err != nil {
		return nil, err
	}
	return k.Marshal(yaml.Parser())
}

// FindAPI returns a pointer to the named API entry, or nil if absent.
func (c *Config) FindAPI(name string) *API {
	for i := range c.APIs {
		if c.APIs[i].Name == name {
			return &c.APIs[i]
		}
	}
	return nil
}

// AddAPI registers a new API; returns an error if a duplicate name exists.
func (c *Config) AddAPI(a API) error {
	if c.FindAPI(a.Name) != nil {
		return fmt.Errorf("api %q already exists", a.Name)
	}
	c.APIs = append(c.APIs, a)
	return nil
}

// RemoveAPI deletes the named API; returns an error if not found.
func (c *Config) RemoveAPI(name string) error {
	for i := range c.APIs {
		if c.APIs[i].Name == name {
			c.APIs = append(c.APIs[:i], c.APIs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("api %q not found", name)
}

// SetEnabled toggles an API on or off; returns an error if not found.
func (c *Config) SetEnabled(name string, enabled bool) error {
	a := c.FindAPI(name)
	if a == nil {
		return fmt.Errorf("api %q not found", name)
	}
	a.Enabled = enabled
	return nil
}
