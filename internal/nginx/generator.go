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

// resolveSampleRate clamps the operator's sample percent into the
// 1-100 range nginx's split_clients can handle. 0 means "default", which
// for log sampling means "no sampling = 100%".
func resolveSampleRate(pct int) int {
	if pct <= 0 {
		return 100
	}
	if pct > 100 {
		return 100
	}
	return pct
}

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

	// OCSPStapling, when true, emits ssl_stapling + ssl_stapling_verify
	// and a resolver directive. Off by default — Let's Encrypt killed
	// their OCSP responders in Aug 2025, so flipping this on for LE
	// certs is worse than useless. Operators with non-LE certs
	// (DigiCert, Sectigo, internal CA) opt in via `apigw tls ocsp enable`.
	OCSPStapling bool
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

	// v0.2.0
	CacheZones    []cacheZoneEntry
	JSONLogFormat bool
	LogSampleRate int // 1-100

	// IPReputationFeed — when non-empty, emit a global geo
	// $apigw_ip_blocked block that includes the file. Set from
	// Security.IPReputationFeed when at least one API opts in via
	// BotGuard.UseIPReputation; otherwise empty (no emission).
	IPReputationFeed string

	// TLSPatternMaps — one per-API map for BotGuard.BlockTLSPatterns.
	TLSPatternMaps []tlsPatternMapEntry
}

type cacheZoneEntry struct {
	Name    string
	Path    string
	MaxSize string // "256m"
}

// streamData drives the stream{} fragment: TCP/UDP servers + their
// upstreams. Lives in a separate include file so operators who don't
// use any TCP/UDP forwarding don't get a stream{} block at all.
type streamData struct {
	Banner          string
	Streams         []streamEntry
	StreamUpstreams []streamUpstream
}

type streamEntry struct {
	Name         string
	Protocol     string // "tcp" | "udp"
	ListenPort   int
	ProxyTimeout string
	Enabled      bool
}

type streamUpstream struct {
	Name    string
	Servers []string // "host:port"
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

	// v0.2.0 additions.
	APIKey        bool           // emit auth_request /_apigw_apikey_<name>
	HMAC          bool           // emit auth_request /_apigw_hmac_<name>
	Session       bool           // emit auth_request /_apigw_session_<name>
	OAuth2        bool           // emit auth_request /_apigw_oauth2_<name>
	Mock          bool           // route hits internal mock handler instead of upstream
	Cache         *cacheEntry    // proxy_cache directives
	Timeouts      *timeoutsEntry // per-route timeouts
	Mirror        *mirrorEntry   // shadow traffic copy
	GRPCWeb       bool           // wraps gRPC for browsers via grpc_web_proxy_*
	StickyMode    string         // "ip_hash" or "cookie:<name>"
	BotGuard      *botGuardEntry
	AccessLogMode string // "" (default) | "json" | "off"
}

type cacheEntry struct {
	Duration     string
	Methods      []string
	Key          string
	BypassHeader string
	VaryHeaders  []string
	ZoneName     string
}

type timeoutsEntry struct {
	Connect string
	Read    string
	Send    string
}

type mirrorEntry struct {
	Path          string // internal location name we proxy_pass to
	UpstreamURL   string
	SamplePercent int
	IgnoreBody    bool
}

type botGuardEntry struct {
	BlockedAgents         []string
	AllowedAgents         []string
	RequireUserAgent      bool
	BlockEmptyReferer     bool
	BlockCommonScanners   bool
	UseIPReputation       bool // emit `if ($apigw_ip_blocked = 1) { return 403; }`
	HasTLSPatterns        bool // emit `if ($apigw_tls_blocked_<name> = 1) { return 403; }`
	ForwardTLSFingerprint bool // emit proxy_set_header X-Apigw-TLS-Profile
}

// tlsPatternMapEntry drives the http{}-scope `map` for per-API TLS
// pattern blocking.
type tlsPatternMapEntry struct {
	APIName  string
	Patterns []string // regex fragments — already operator-supplied, emitted as `~`-prefixed
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

// RenderStream produces the TCP/UDP stream{} include. Empty when no
// streams are configured — caller writes "" to the file so the include
// directive parses but yields no servers.
func (g *Generator) RenderStream(cfg *config.Config) ([]byte, error) {
	if len(cfg.Streams) == 0 {
		return nil, nil
	}
	tmpl, err := template.New("stream").ParseFS(assets.Nginx(), "nginx/stream.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse stream template: %w", err)
	}
	sd := streamData{Banner: "MANAGED BY apigw — stream{} scope"}
	for _, s := range cfg.Streams {
		if !s.Enabled {
			continue
		}
		sd.Streams = append(sd.Streams, streamEntry{
			Name:         s.Name,
			Protocol:     s.Protocol,
			ListenPort:   s.ListenPort,
			ProxyTimeout: s.ProxyTimeout,
			Enabled:      true,
		})
		su := streamUpstream{Name: s.Name}
		for _, u := range s.Upstreams {
			su.Servers = append(su.Servers, u.Address)
		}
		sd.StreamUpstreams = append(sd.StreamUpstreams, su)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "stream", sd); err != nil {
		return nil, fmt.Errorf("execute stream: %w", err)
	}
	return buf.Bytes(), nil
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

		// v0.2.0 — populate the new middleware fields.
		if a.APIKey != nil && len(a.APIKey.Keys) > 0 {
			entry.APIKey = true
			if entry.JWTDashboardPort == 0 {
				entry.JWTDashboardPort = cfg.Dashboard.Port
			}
		}
		if a.HMAC != nil && len(a.HMAC.Keys) > 0 {
			entry.HMAC = true
			if entry.JWTDashboardPort == 0 {
				entry.JWTDashboardPort = cfg.Dashboard.Port
			}
		}
		if a.Session {
			entry.Session = true
			if entry.JWTDashboardPort == 0 {
				entry.JWTDashboardPort = cfg.Dashboard.Port
			}
		}
		if a.OAuth2 != nil && a.OAuth2.IntrospectionURL != "" {
			entry.OAuth2 = true
			if entry.JWTDashboardPort == 0 {
				entry.JWTDashboardPort = cfg.Dashboard.Port
			}
		}
		if a.Mock != nil {
			entry.Mock = true
			if entry.JWTDashboardPort == 0 {
				entry.JWTDashboardPort = cfg.Dashboard.Port
			}
		}
		if a.Cache != nil && a.Cache.Duration != "" {
			methods := a.Cache.Methods
			if len(methods) == 0 {
				methods = []string{"GET", "HEAD"}
			}
			key := a.Cache.Key
			if key == "" {
				key = "$scheme$request_method$host$request_uri"
			}
			entry.Cache = &cacheEntry{
				Duration: a.Cache.Duration, Methods: methods, Key: key,
				BypassHeader: a.Cache.BypassHeader,
				VaryHeaders:  a.Cache.VaryHeaders,
				ZoneName:     "apigw_cache_" + a.Name,
			}
		}
		if a.Timeouts != nil {
			entry.Timeouts = &timeoutsEntry{
				Connect: a.Timeouts.Connect,
				Read:    a.Timeouts.Read,
				Send:    a.Timeouts.Send,
			}
		}
		if a.Mirror != nil && a.Mirror.Target != "" {
			pct := a.Mirror.SamplePercent
			if pct <= 0 || pct > 100 {
				pct = 100
			}
			entry.Mirror = &mirrorEntry{
				Path:          "/_apigw_mirror_" + a.Name,
				UpstreamURL:   "http://" + a.Mirror.Target,
				SamplePercent: pct,
				IgnoreBody:    a.Mirror.IgnoreBody,
			}
		}
		entry.GRPCWeb = a.GRPCWeb
		entry.StickyMode = a.StickySession
		if a.BotGuard != nil {
			entry.BotGuard = &botGuardEntry{
				BlockedAgents:         a.BotGuard.BlockUserAgents,
				AllowedAgents:         a.BotGuard.AllowUserAgents,
				RequireUserAgent:      a.BotGuard.RequireUserAgent,
				BlockEmptyReferer:     a.BotGuard.BlockEmptyReferer,
				BlockCommonScanners:   a.BotGuard.BlockCommonScanners,
				UseIPReputation:       a.BotGuard.UseIPReputation && cfg.Security.IPReputationFeed != "",
				HasTLSPatterns:        len(a.BotGuard.BlockTLSPatterns) > 0,
				ForwardTLSFingerprint: a.BotGuard.ForwardTLSFingerprint,
			}
		}
		entry.AccessLogMode = a.AccessLog

		if up, ok := buildUpstream(a.Name, a.Port, a.Upstreams, a.LoadBalance, a.HealthCheck); ok {
			// Wire LB strategy extensions: consistent_hash + sticky cookie
			// override the basic least_conn/ip_hash/random returned by
			// buildUpstream.
			if a.LoadBalance == "consistent_hash" && a.HashKey != "" {
				up.LoadBalance = "hash " + a.HashKey + " consistent"
			} else if strings.HasPrefix(a.StickySession, "cookie:") {
				up.LoadBalance = "hash $cookie_" +
					strings.TrimPrefix(a.StickySession, "cookie:") + " consistent"
			} else if a.StickySession == "ip_hash" {
				up.LoadBalance = "ip_hash"
			}
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
		OCSPStapling:   cfg.TLS.OCSPStapling,
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

	// Collect cache zones from any API with response caching configured.
	var cacheZones []cacheZoneEntry
	for _, a := range apis {
		if a.Cache != nil {
			cacheZones = append(cacheZones, cacheZoneEntry{
				Name: a.Cache.ZoneName,
				Path: "/var/cache/nginx/" + a.Cache.ZoneName,
				MaxSize: func() string {
					return "256m"
				}(),
			})
		}
	}
	// Collect TLS pattern maps + decide if the IP reputation block should
	// emit. The feed only renders when at least one API opts in — empty
	// http{} otherwise.
	var tlsMaps []tlsPatternMapEntry
	ipRepNeeded := false
	for _, a := range cfg.APIs {
		if a.BotGuard == nil {
			continue
		}
		if a.BotGuard.UseIPReputation && cfg.Security.IPReputationFeed != "" {
			ipRepNeeded = true
		}
		if len(a.BotGuard.BlockTLSPatterns) > 0 {
			tlsMaps = append(tlsMaps, tlsPatternMapEntry{
				APIName:  a.Name,
				Patterns: a.BotGuard.BlockTLSPatterns,
			})
		}
	}
	ipFeed := ""
	if ipRepNeeded {
		ipFeed = cfg.Security.IPReputationFeed
	}

	hd := httpData{
		Banner:           "PLACEHOLDER",
		Gzip:             resolveGzip(cfg),
		Upstreams:        upstreams,
		RateLimitZones:   rlZones,
		CORSOriginMap:    corsOrigins,
		SplitClients:     splits,
		CacheZones:       cacheZones,
		JSONLogFormat:    cfg.Logging.Format == "json",
		LogSampleRate:    resolveSampleRate(cfg.Logging.SamplePercent),
		IPReputationFeed: ipFeed,
		TLSPatternMaps:   tlsMaps,
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
