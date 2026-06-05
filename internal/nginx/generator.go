// Package nginx renders apigw's nginx configuration from embedded templates,
// writes it atomically, validates with `nginx -t`, and reloads with rollback
// on failure.
//
// The generated file always starts with a banner that includes the version,
// timestamp, and SHA-256 hash. `apigw doctor` later verifies the hash to
// detect hand-edits.
package nginx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/devevghenicernev-png/apigw/internal/assets"
	"github.com/devevghenicernev-png/apigw/internal/build"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/deploy"
	apitls "github.com/devevghenicernev-png/apigw/internal/tls"
)

// Generator renders Config into nginx config bytes. Stateless and pure;
// safe to call from anywhere, including tests with no filesystem.
type Generator struct{}

func NewGenerator() *Generator { return &Generator{} }

// templateData is the struct the templates dereference. Keep field names
// stable — they appear in template source under assets/nginx/.
type templateData struct {
	Banner     string
	HTTPPort   int
	HTTPSPort  int
	ServerName string
	APIs       []apiEntry
	Deploys    []deployEntry
	Webhook    config.Webhook
	Dashboard  config.Dashboard

	// TLS-mode fields. Only populated when cfg.TLS.Strategy != "none".
	TLSStrategy           string
	TLSDomains            []string
	CertFile              string
	KeyFile               string
	AcmeWebroot           string
	HSTSIncludeSubDomains bool

	// mTLS at server scope — set if any API has MTLS configured.
	// nginx supports one ssl_client_certificate per server block, so we
	// use ssl_verify_client optional at server scope and let per-location
	// auth_request enforce per-API.
	MTLSAnyEnabled bool
	MTLSCAFile     string
}

// httpData drives the new _http.tmpl that renders into
// /etc/nginx/conf.d/apigw-http.conf — anything that has to live at http{}
// scope (named upstreams, rate-limit zones, gzip settings, CORS origin map).
type httpData struct {
	Banner         string
	Gzip           gzipEntry
	Upstreams      []upstreamEntry
	RateLimitZones []rateLimitZone
	CORSOriginMap  []string            // exact-match regex tokens (already escaped)
	SplitClients   []splitClientsEntry // E13 — % traffic distribution
}

// splitClientsEntry renders one nginx split_clients block.
type splitClientsEntry struct {
	Name        string // e.g. "apigw_billing_split"
	HashSource  string // typically $request_id or $remote_addr — uniform distribution
	CanaryPct   int    // 0-100; rest goes to primary
	CanaryName  string // upstream name for canary
	PrimaryName string // upstream name for primary
}

type gzipEntry struct {
	Enabled   bool
	Level     int
	MinLength int
	Types     []string
}

type upstreamEntry struct {
	Name        string // sanitised — used both here and in proxy_pass http://apigw_<name>
	LoadBalance string // "" | "least_conn" | "ip_hash" | "random"
	Servers     []upstreamServer
	Keepalive   int
	// Legacy single-port view kept until every call site is migrated; unused
	// once buildUpstream is the sole producer.
	Port        int
	MaxFails    int
	FailTimeout string // e.g. "10s"
}

type upstreamServer struct {
	Address     string // host:port
	Weight      int    // 0 = default (1)
	MaxFails    int
	FailTimeout string
	Backup      bool
	Down        bool
}

type rateLimitZone struct {
	Name string // e.g. "hello_ip"
	Key  string // e.g. "$binary_remote_addr"
	RPS  int
}

type apiEntry struct {
	Name         string
	Port         int
	Path         string
	Description  string
	Enabled      bool
	UpstreamName string // "" = no upstream block; we fall back to proxy_pass http://127.0.0.1:port

	// Per-API middleware (F2-F6 fields). Empty values render no extra directives.
	MaxBodySize      string            // F2
	RequestHeaders   map[string]string // F2
	ResponseHeaders  map[string]string // F2
	ResponseHide     []string          // F2
	Allow            []string          // F2 (IP rules)
	Deny             []string          // F2
	CORS             *corsEntry        // F2
	BasicAuthRealm   string            // F2 (empty = no basic auth)
	BasicAuthFile    string            // F2 — path to htpasswd file
	RateLimitZone    string            // F4 — zone name
	RateLimitBurst   int               // F4
	ForwardAuth      *forwardAuthEntry // F6
	NextUpstream     string            // F5 — proxy_next_upstream conditions
	GRPC             bool              // F9 — emit grpc_pass instead of proxy_pass
	JWT              bool              // F12 — emit auth_request /_apigw_jwt/<api>
	JWTDashboardPort int               // F12 — port the dashboard daemon listens on
	CustomLocation   string            // F14 — raw nginx directives, location-scoped
	CustomServer     string            // F14 — raw nginx directives, server-scoped (rendered server.tmpl)
	MTLS             bool              // E1 — emit ssl_verify_client + auth_request /_apigw_mtls/<api>
	MTLSCAFile       string            // E1 — path to the CA bundle nginx loads
	MTLSOptional     bool              // E1 — ssl_verify_client optional vs on
}

type corsEntry struct {
	Methods     string // pre-joined "GET, POST, OPTIONS"
	Headers     string // pre-joined "Content-Type, Authorization"
	Credentials bool
	MaxAge      int
}

type forwardAuthEntry struct {
	UpstreamName string // location /_apigw_auth_<name>
	Upstream     string // proxy_pass target (URL form)
	SignInURL    string
	SetHeaders   []string // auth_request_set $var $upstream_http_<header>
}

type deployEntry struct {
	Name       string
	Port       int
	Path       string
	Runtime    string
	SHA        string
	CurrentDir string // /var/lib/apigw/<name>/current
	Enabled    bool

	// Mirrors of apiEntry middleware so deploys share the same surface.
	MaxBodySize      string
	RequestHeaders   map[string]string
	ResponseHeaders  map[string]string
	ResponseHide     []string
	Allow            []string
	Deny             []string
	CORS             *corsEntry
	BasicAuthRealm   string
	BasicAuthFile    string
	RateLimitZone    string
	RateLimitBurst   int
	ForwardAuth      *forwardAuthEntry
	UpstreamName     string
	NextUpstream     string
	GRPC             bool
	JWT              bool
	JWTDashboardPort int
	CustomLocation   string
	CustomServer     string
}

// Render produces both nginx config files apigw owns: the server-block
// payload (returned as `serverBytes`) and the http-scope payload that lands
// in /etc/nginx/conf.d/apigw-http.conf (returned as `httpBytes`). The
// caller writes both atomically and runs `nginx -t` once per pair.
//
// Template selection for server bytes:
//   - cfg.TLS.Strategy is "" or "none" → server.tmpl (HTTP-only)
//   - otherwise                        → tls-server.tmpl (HTTPS + HTTP redirect)
func (g *Generator) Render(cfg *config.Config) (serverBytes, httpBytes []byte, err error) {
	tmpl, err := template.New("apigw").ParseFS(
		assets.Nginx(),
		"nginx/server.tmpl",
		"nginx/tls-server.tmpl",
		"nginx/_locations.tmpl",
		"nginx/_http.tmpl",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("parse templates: %w", err)
	}

	// Build the master upstream + rate-limit-zone lists FIRST — each
	// API/Deploy below references them by name.
	upstreams := make([]upstreamEntry, 0, len(cfg.APIs)+len(cfg.Deploys))
	rlZones := make([]rateLimitZone, 0)
	corsOriginsSeen := map[string]struct{}{}
	corsOrigins := []string{}

	// Scan once for mTLS server-scope state.
	mtlsAny := false
	mtlsCAFile := ""
	for _, a := range cfg.APIs {
		if a.MTLS != nil && a.MTLS.CAFile != "" {
			mtlsAny = true
			if mtlsCAFile == "" {
				mtlsCAFile = a.MTLS.CAFile
			}
		}
	}

	apis := make([]apiEntry, 0, len(cfg.APIs))
	for _, a := range cfg.APIs {
		if err := sanitizeName(a.Name); err != nil {
			return nil, nil, fmt.Errorf("api: %w", err)
		}
		path := a.Path
		if path == "" {
			path = "/api/" + a.Name
		}
		if err := sanitizePath(path); err != nil {
			return nil, nil, fmt.Errorf("api %s: %w", a.Name, err)
		}
		// Every enabled API with a real port gets a named upstream block so
		// passive health checks (max_fails/fail_timeout) and keepalive can
		// be applied without per-location duplication.
		entry := apiEntry{
			Name:        a.Name,
			Port:        a.Port,
			Path:        path,
			Description: a.Description,
			Enabled:     a.Enabled,
		}
		entry.GRPC = a.GRPC
		if a.JWT != nil {
			entry.JWT = true
			entry.JWTDashboardPort = cfg.Dashboard.Port
		}
		if a.MTLS != nil {
			entry.MTLS = true
			entry.MTLSCAFile = a.MTLS.CAFile
			entry.MTLSOptional = a.MTLS.Optional
			if entry.JWTDashboardPort == 0 {
				entry.JWTDashboardPort = cfg.Dashboard.Port
			}
		}
		entry.CustomLocation = a.CustomLocation
		entry.CustomServer = a.CustomServer
		if up, ok := buildUpstream(a.Name, a.Port, a.Upstreams, a.LoadBalance, a.HealthCheck); ok {
			entry.UpstreamName = "apigw_" + a.Name
			upstreams = append(upstreams, up)
		}
		// Legacy fallback path retained for safety — replaced once every
		// API/Deploy migrates to the buildUpstream output above.
		if false && a.Port > 0 {
			entry.UpstreamName = "apigw_" + a.Name
			maxFails, failTimeout := 3, "10s"
			if a.HealthCheck != nil {
				if a.HealthCheck.MaxFails > 0 {
					maxFails = a.HealthCheck.MaxFails
				}
				if a.HealthCheck.FailTimeout != "" {
					failTimeout = a.HealthCheck.FailTimeout
				}
			}
			upstreams = append(upstreams, upstreamEntry{
				Name:        a.Name,
				Port:        a.Port,
				MaxFails:    maxFails,
				FailTimeout: failTimeout,
			})
		}
		if err := applyMiddleware(&entry, a, cfg.Listen.MaxBodySize, &rlZones, &corsOriginsSeen, &corsOrigins); err != nil {
			return nil, nil, fmt.Errorf("api %s: %w", a.Name, err)
		}
		apis = append(apis, entry)
	}

	deploys := make([]deployEntry, 0, len(cfg.Deploys))
	for _, d := range cfg.Deploys {
		if err := sanitizeName(d.Name); err != nil {
			return nil, nil, fmt.Errorf("deploy: %w", err)
		}
		path := d.Path
		if path == "" {
			path = "/apps/" + d.Name
		}
		if err := sanitizePath(path); err != nil {
			return nil, nil, fmt.Errorf("deploy %s: %w", d.Name, err)
		}
		entry := deployEntry{
			Name:       d.Name,
			Port:       d.Port,
			Path:       path,
			Runtime:    d.Runtime,
			SHA:        shortSHA(d.LastSHA),
			CurrentDir: deploy.CurrentSymlink(d.Name),
			Enabled:    d.Enabled,
		}
		entry.GRPC = d.GRPC
		// Deploy-level JWT / Custom fields mirror API. Deploy struct doesn't
		// have them yet — added below in config.go. Read defensively in case
		// koanf zero-values are present.
		if d.JWT != nil {
			entry.JWT = true
			entry.JWTDashboardPort = cfg.Dashboard.Port
		}
		entry.CustomLocation = d.CustomLocation
		entry.CustomServer = d.CustomServer
		if d.Runtime != "static" {
			if up, ok := buildUpstream(d.Name, d.Port, d.Upstreams, d.LoadBalance, d.HealthCheck); ok {
				entry.UpstreamName = "apigw_" + d.Name
				upstreams = append(upstreams, up)
			}
		}
		if err := applyMiddlewareDeploy(&entry, d, cfg.Listen.MaxBodySize, &rlZones, &corsOriginsSeen, &corsOrigins); err != nil {
			return nil, nil, fmt.Errorf("deploy %s: %w", d.Name, err)
		}
		deploys = append(deploys, entry)
	}

	serverName := nonEmpty(cfg.Listen.ServerName, "_")
	// Validate each token (space-separated) — accepts hostnames, `_` catch-all,
	// and `*` wildcards. Anything else aborts the render.
	for _, n := range strings.Fields(serverName) {
		if err := sanitizeServerName(n); err != nil {
			return nil, nil, fmt.Errorf("listen.server_name: %w", err)
		}
	}

	data := templateData{
		HTTPPort:       nonZero(cfg.Listen.HTTPPort, 80),
		HTTPSPort:      nonZero(cfg.Listen.HTTPSPort, 443),
		ServerName:     serverName,
		APIs:           apis,
		Deploys:        deploys,
		Webhook:        cfg.Webhook,
		Dashboard:      cfg.Dashboard,
		AcmeWebroot:    apitls.AcmeWebrootDir,
		Banner:         "PLACEHOLDER", // substituted post-render
		MTLSAnyEnabled: mtlsAny,
		MTLSCAFile:     mtlsCAFile,
	}

	templateName := "server.tmpl"
	strategy := apitls.Strategy(cfg.TLS.Strategy)
	if strategy != "" && strategy != apitls.StrategyNone {
		templateName = "tls-server.tmpl"
		domains := cfg.TLS.Domains
		if len(domains) == 0 {
			return nil, nil, fmt.Errorf("tls strategy %q but no domains configured", strategy)
		}
		primary := domains[0]
		certFile, keyFile, _ := apitls.CertPaths(primary)
		data.TLSStrategy = string(strategy)
		data.TLSDomains = domains
		data.CertFile = certFile
		data.KeyFile = keyFile
		data.AcmeWebroot = apitls.AcmeWebrootDir
		// DuckDNS: omit includeSubDomains — we don't own siblings under
		// *.duckdns.org. Same trap noted in ARCHITECTURE.md §"HSTS trap".
		data.HSTSIncludeSubDomains = strategy != apitls.StrategyDuckDNS
		if data.ServerName == "_" {
			data.ServerName = strings.Join(domains, " ")
		}
	}

	var body bytes.Buffer
	if err := tmpl.ExecuteTemplate(&body, templateName, data); err != nil {
		return nil, nil, fmt.Errorf("execute %s: %w", templateName, err)
	}

	// Hash the canonical body (banner-stripped) and substitute into the banner.
	canonical := stripBanner(body.Bytes())
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])[:16]
	banner := fmt.Sprintf("MANAGED BY apigw — do not edit. version=%s commit=%s generated=%s hash=%s",
		build.Version, build.Commit, time.Now().UTC().Format(time.RFC3339), hash)
	serverBytes = bytes.Replace(body.Bytes(),
		[]byte("# PLACEHOLDER"),
		[]byte("# "+banner),
		1,
	)

	// Now render the http-scope payload.
	// E13 — build split_clients blocks for each API with canary set.
	splits := []splitClientsEntry{}
	for _, a := range cfg.APIs {
		if a.Canary == nil || a.Canary.Weight <= 0 {
			continue
		}
		canaryUp := "apigw_" + a.Name + "_canary"
		if up, ok := buildUpstream(a.Name+"_canary", 0, a.Canary.Upstreams, a.LoadBalance, a.HealthCheck); ok {
			upstreams = append(upstreams, up)
		}
		splits = append(splits, splitClientsEntry{
			Name:        "apigw_" + a.Name + "_split",
			HashSource:  "$request_id",
			CanaryPct:   a.Canary.Weight,
			CanaryName:  canaryUp,
			PrimaryName: "apigw_" + a.Name,
		})
	}

	hd := httpData{
		Banner:         "PLACEHOLDER",
		Gzip:           resolveGzip(cfg),
		Upstreams:      upstreams,
		RateLimitZones: rlZones,
		CORSOriginMap:  corsOrigins,
		SplitClients:   splits,
	}
	var httpBuf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&httpBuf, "_http.tmpl", hd); err != nil {
		return nil, nil, fmt.Errorf("execute _http.tmpl: %w", err)
	}
	hSum := sha256.Sum256(stripBanner(httpBuf.Bytes()))
	hHash := hex.EncodeToString(hSum[:])[:16]
	hBanner := fmt.Sprintf("MANAGED BY apigw — http{} scope. version=%s commit=%s generated=%s hash=%s",
		build.Version, build.Commit, time.Now().UTC().Format(time.RFC3339), hHash)
	httpBytes = bytes.Replace(httpBuf.Bytes(),
		[]byte("# PLACEHOLDER"),
		[]byte("# "+hBanner),
		1,
	)
	return serverBytes, httpBytes, nil
}

// resolveGzip merges cfg.Listen.Gzip with sane production defaults so an
// empty config still ships a useful gzip block. Set cfg.Listen.Gzip.Enabled
// to false explicitly to suppress.
func resolveGzip(cfg *config.Config) gzipEntry {
	def := gzipEntry{
		Enabled:   true,
		Level:     5,
		MinLength: 1024,
		Types: []string{
			"text/plain", "text/css", "text/xml",
			"application/json", "application/javascript", "application/xml",
			"application/xml+rss", "image/svg+xml",
		},
	}
	g := cfg.Listen.Gzip
	// Zero-value Listen.Gzip means "user didn't touch it" → ship defaults.
	if !g.Enabled && g.Level == 0 && g.MinLength == 0 && len(g.Types) == 0 {
		return def
	}
	out := gzipEntry{
		Enabled:   g.Enabled,
		Level:     g.Level,
		MinLength: g.MinLength,
		Types:     g.Types,
	}
	if out.Level == 0 {
		out.Level = def.Level
	}
	if out.MinLength == 0 {
		out.MinLength = def.MinLength
	}
	if len(out.Types) == 0 {
		out.Types = def.Types
	}
	return out
}

// stripBanner returns the body with the leading "# MANAGED BY ..." line
// removed so a second renderer with a different timestamp/version produces
// the same canonical bytes. Used for hashing only.
func stripBanner(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 && bytes.HasPrefix(b, []byte("# ")) {
		return b[i+1:]
	}
	return b
}

func nonZero(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
func nonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func shortSHA(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:7]
}
