package nginx

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// applyMiddleware copies all middleware fields off a config.API onto the
// generator's apiEntry, validating + accumulating http-scope artifacts as
// it goes. Failures abort the whole render (caller's responsibility).
//
// `globalMaxBody` is the Listen-level default applied when the API has no
// explicit MaxBodySize. `rlZones`, `corsOriginsSeen`, `corsOrigins` are
// shared accumulators across all APIs and Deploys.
func applyMiddleware(
	out *apiEntry,
	a config.API,
	globalMaxBody string,
	rlZones *[]rateLimitZone,
	corsOriginsSeen *map[string]struct{},
	corsOrigins *[]string,
) error {
	if mb := a.MaxBodySize; mb != "" {
		if err := validateBodySize(mb); err != nil {
			return fmt.Errorf("max_body_size: %w", err)
		}
		out.MaxBodySize = mb
	} else if globalMaxBody != "" {
		out.MaxBodySize = globalMaxBody
	}

	if h := a.Headers; h != nil {
		if err := validateHeaderMap(h.RequestSet); err != nil {
			return fmt.Errorf("headers.request_set: %w", err)
		}
		if err := validateHeaderMap(h.ResponseAdd); err != nil {
			return fmt.Errorf("headers.response_add: %w", err)
		}
		out.RequestHeaders = h.RequestSet
		out.ResponseHeaders = h.ResponseAdd
		out.ResponseHide = h.ResponseHide
	}

	if ip := a.IPRules; ip != nil {
		for _, cidr := range ip.Allow {
			if err := validateCIDR(cidr); err != nil {
				return fmt.Errorf("ip_rules.allow %q: %w", cidr, err)
			}
		}
		for _, cidr := range ip.Deny {
			if err := validateCIDR(cidr); err != nil {
				return fmt.Errorf("ip_rules.deny %q: %w", cidr, err)
			}
		}
		out.Allow = ip.Allow
		out.Deny = ip.Deny
	}

	if c := a.CORS; c != nil {
		if c.Credentials {
			for _, o := range c.Origins {
				if o == "*" {
					return fmt.Errorf("cors: credentials=true is incompatible with origins=[\"*\"]")
				}
			}
		}
		methods := c.Methods
		if len(methods) == 0 {
			methods = []string{"GET", "POST", "OPTIONS"}
		}
		headers := c.Headers
		if len(headers) == 0 {
			headers = []string{"Content-Type", "Authorization"}
		}
		maxAge := c.MaxAge
		if maxAge == 0 {
			maxAge = 86400
		}
		out.CORS = &corsEntry{
			Methods:     strings.Join(methods, ", "),
			Headers:     strings.Join(headers, ", "),
			Credentials: c.Credentials,
			MaxAge:      maxAge,
		}
		// Register origins into the shared map (one map entry per unique origin).
		for _, o := range c.Origins {
			if o == "*" {
				continue // handled inline via a static `*` add_header in the template
			}
			if _, ok := (*corsOriginsSeen)[o]; ok {
				continue
			}
			(*corsOriginsSeen)[o] = struct{}{}
			*corsOrigins = append(*corsOrigins, regexEscape(o))
		}
	}

	if ba := a.BasicAuth; ba != nil {
		if ba.Realm == "" {
			ba.Realm = "Restricted"
		}
		file := ba.File
		if file == "" {
			file = "/etc/apigw/htpasswd." + a.Name
		}
		out.BasicAuthRealm = ba.Realm
		out.BasicAuthFile = file
	}

	if rl := a.RateLimit; rl != nil {
		if rl.RPS <= 0 {
			return fmt.Errorf("rate_limit.rps must be > 0")
		}
		burst := rl.Burst
		if burst == 0 {
			burst = rl.RPS * 2
		}
		key, zoneName, err := rateLimitKey(a.Name, rl.Key)
		if err != nil {
			return err
		}
		*rlZones = append(*rlZones, rateLimitZone{
			Name: zoneName,
			Key:  key,
			RPS:  rl.RPS,
		})
		out.RateLimitZone = zoneName
		out.RateLimitBurst = burst
	}

	if fa := a.ForwardAuth; fa != nil {
		if fa.Address == "" {
			return fmt.Errorf("forward_auth.address required")
		}
		out.ForwardAuth = &forwardAuthEntry{
			UpstreamName: "_apigw_auth_" + a.Name,
			Upstream:     fa.Address,
			SignInURL:    fa.SignInURL,
			SetHeaders:   fa.SetHeaders,
		}
	}

	if r := a.Retry; r != nil && len(r.Conditions) > 0 {
		out.NextUpstream = strings.Join(r.Conditions, " ")
	} else if a.HealthCheck != nil {
		// Sensible default when a health check is configured.
		out.NextUpstream = "error timeout http_502 http_503 http_504"
	}
	return nil
}

// applyMiddlewareDeploy is the Deploy variant — same shape, different
// source type. Kept separate so the API type doesn't leak its surface into
// Deploy and vice versa.
func applyMiddlewareDeploy(
	out *deployEntry,
	d config.Deploy,
	globalMaxBody string,
	rlZones *[]rateLimitZone,
	corsOriginsSeen *map[string]struct{},
	corsOrigins *[]string,
) error {
	// Build a temporary API to reuse applyMiddleware's logic.
	tmp := apiEntry{}
	syntheticAPI := config.API{
		Name:        d.Name,
		MaxBodySize: d.MaxBodySize,
		Headers:     d.Headers,
		IPRules:     d.IPRules,
		CORS:        d.CORS,
		BasicAuth:   d.BasicAuth,
		RateLimit:   d.RateLimit,
		ForwardAuth: d.ForwardAuth,
		HealthCheck: d.HealthCheck,
		Retry:       d.Retry,
	}
	if err := applyMiddleware(&tmp, syntheticAPI, globalMaxBody, rlZones, corsOriginsSeen, corsOrigins); err != nil {
		return err
	}
	out.MaxBodySize = tmp.MaxBodySize
	out.RequestHeaders = tmp.RequestHeaders
	out.ResponseHeaders = tmp.ResponseHeaders
	out.ResponseHide = tmp.ResponseHide
	out.Allow = tmp.Allow
	out.Deny = tmp.Deny
	out.CORS = tmp.CORS
	out.BasicAuthRealm = tmp.BasicAuthRealm
	out.BasicAuthFile = tmp.BasicAuthFile
	out.RateLimitZone = tmp.RateLimitZone
	out.RateLimitBurst = tmp.RateLimitBurst
	out.ForwardAuth = tmp.ForwardAuth
	out.NextUpstream = tmp.NextUpstream
	return nil
}

// validateBodySize checks the nginx size token format: digit+ optionally
// followed by k|K|m|M|g|G.
var bodySizeRE = regexp.MustCompile(`^\d+[kKmMgG]?$`)

func validateBodySize(s string) error {
	if !bodySizeRE.MatchString(s) {
		return fmt.Errorf("%q is not a valid nginx size (e.g. 10m, 1g)", s)
	}
	return nil
}

// validateHeaderMap rejects header names containing illegal chars per RFC 7230.
// Values are allowed through (they may contain nginx variables like $host).
var headerNameRE = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+$`)

func validateHeaderMap(m map[string]string) error {
	for k, v := range m {
		if !headerNameRE.MatchString(k) {
			return fmt.Errorf("header name %q contains illegal characters", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("header value %q contains newline", v)
		}
	}
	return nil
}

// validateCIDR accepts an IP or CIDR; returns an error if neither parses.
func validateCIDR(s string) error {
	if s == "all" {
		return nil
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return nil
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return nil
	}
	return fmt.Errorf("not an IP or CIDR")
}

// rateLimitKey returns the nginx variable + zone-name suffix for the
// requested key.
//
//   - "" or "ip" → $binary_remote_addr, suffix "ip"
//   - "header:Foo" → $http_foo, suffix "hdr_foo"
func rateLimitKey(apiName, key string) (variable, zoneName string, err error) {
	if key == "" || key == "ip" {
		return "$binary_remote_addr", apiName + "_ip", nil
	}
	if strings.HasPrefix(key, "header:") {
		hdr := strings.TrimPrefix(key, "header:")
		if hdr == "" {
			return "", "", fmt.Errorf("rate_limit.key=header: must name a header")
		}
		hdrNorm := strings.ToLower(strings.ReplaceAll(hdr, "-", "_"))
		if !headerNameRE.MatchString(hdr) {
			return "", "", fmt.Errorf("rate_limit.key header name %q is invalid", hdr)
		}
		return "$http_" + hdrNorm, apiName + "_hdr_" + hdrNorm, nil
	}
	return "", "", fmt.Errorf("rate_limit.key %q unknown (use \"ip\" or \"header:<Name>\")", key)
}

// regexEscape converts an exact origin (https://foo.example.com) into an
// nginx regex token suitable for the http{} `map` block. We escape every
// regex metacharacter so `.` matches literally.
func regexEscape(s string) string {
	// nginx uses PCRE; escape per https://www.pcre.org/current/doc/html/pcre2pattern.html
	return regexp.QuoteMeta(s)
}
