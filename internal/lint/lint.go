// Package lint runs static checks over a loaded config and reports
// findings — wiring mistakes nginx -t can't catch (semantic conflicts,
// references to missing entries, easy-to-miss empty fields).
//
// `apigw config lint` calls Lint() and renders the findings; CI can
// gate config commits on a clean result.
package lint

import (
	"fmt"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// Severity classifies a finding. Lint exits non-zero only on Errors;
// Warnings are advisory but printed.
type Severity int

const (
	SeverityWarning Severity = iota
	SeverityError
)

func (s Severity) String() string {
	if s == SeverityError {
		return "error"
	}
	return "warning"
}

// Finding is one diagnostic produced by Lint.
type Finding struct {
	Severity Severity
	Code     string // short stable ID, e.g. "L001" — for `// lint:ignore` comments later
	Resource string // free-form scope, e.g. "api[billing]" or "tls"
	Message  string
}

// Lint runs every check over cfg and returns findings in stable
// order (errors before warnings, then by code).
func Lint(cfg *config.Config) []Finding {
	var out []Finding
	out = append(out, lintAPIs(cfg)...)
	out = append(out, lintTLS(cfg)...)
	out = append(out, lintWebhook(cfg)...)
	out = append(out, lintAuth(cfg)...)
	out = append(out, lintSecurity(cfg)...)
	sortFindings(out)
	return out
}

// HasErrors reports whether any finding is at error severity.
func HasErrors(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

func lintAPIs(cfg *config.Config) []Finding {
	var out []Finding
	pathSeen := map[string]string{} // path → first api name
	nameSeen := map[string]bool{}
	for _, a := range cfg.APIs {
		res := fmt.Sprintf("api[%s]", a.Name)

		if nameSeen[a.Name] {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L101", Resource: res,
				Message: "duplicate API name",
			})
		}
		nameSeen[a.Name] = true

		// No upstream at all = nothing to proxy to.
		if a.Port == 0 && len(a.Upstreams) == 0 &&
			(a.BlueGreen == nil || (len(a.BlueGreen.Blue) == 0 && len(a.BlueGreen.Green) == 0)) &&
			len(a.Variants) == 0 && a.Mock == nil {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L102", Resource: res,
				Message: "no upstream: set Port, Upstreams, Variants, BlueGreen, or Mock",
			})
		}

		// Path collisions — first wins; subsequent are flagged.
		path := a.Path
		if path == "" {
			path = "/api/" + a.Name
		}
		if first, ok := pathSeen[path]; ok && first != a.Name {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L103", Resource: res,
				Message: fmt.Sprintf("path %q already used by api[%s]", path, first),
			})
		} else {
			pathSeen[path] = a.Name
		}

		// Canary weight sanity.
		if a.Canary != nil && (a.Canary.Weight < 0 || a.Canary.Weight > 100) {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L104", Resource: res,
				Message: fmt.Sprintf("canary.weight %d out of 0..100", a.Canary.Weight),
			})
		}
		if a.Canary != nil && a.Canary.Weight > 0 && len(a.Canary.Upstreams) == 0 {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L105", Resource: res,
				Message: "canary has weight but no upstreams",
			})
		}

		// BlueGreen sanity.
		if a.BlueGreen != nil {
			if a.BlueGreen.Active != "blue" && a.BlueGreen.Active != "green" {
				out = append(out, Finding{
					Severity: SeverityError, Code: "L106", Resource: res,
					Message: fmt.Sprintf("blue_green.active %q must be 'blue' or 'green'", a.BlueGreen.Active),
				})
			}
			if len(a.BlueGreen.Blue) == 0 || len(a.BlueGreen.Green) == 0 {
				out = append(out, Finding{
					Severity: SeverityError, Code: "L107", Resource: res,
					Message: "blue_green requires both blue and green pools non-empty",
				})
			}
		}

		// Variants weight sum.
		if len(a.Variants) > 0 {
			total := 0
			seen := map[string]bool{}
			for _, v := range a.Variants {
				total += v.Weight
				if seen[v.Name] {
					out = append(out, Finding{
						Severity: SeverityError, Code: "L108", Resource: res,
						Message: fmt.Sprintf("duplicate variant name %q", v.Name),
					})
				}
				seen[v.Name] = true
			}
			if total > 100 {
				out = append(out, Finding{
					Severity: SeverityWarning, Code: "L109", Resource: res,
					Message: fmt.Sprintf("variant weights sum to %d (>100); last variant becomes the catchall", total),
				})
			}
		}
	}
	return out
}

func lintTLS(cfg *config.Config) []Finding {
	var out []Finding
	if cfg.TLS.Strategy == "" || cfg.TLS.Strategy == "none" {
		return out
	}
	res := "tls"
	if cfg.TLS.Strategy == "letsencrypt" {
		if cfg.TLS.Email == "" {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L201", Resource: res,
				Message: "letsencrypt strategy requires tls.email",
			})
		}
		if len(cfg.TLS.Domains) == 0 {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L202", Resource: res,
				Message: "letsencrypt strategy requires at least one tls.domain",
			})
		}
		if cfg.TLS.OCSPStapling {
			out = append(out, Finding{
				Severity: SeverityWarning, Code: "L203", Resource: res,
				Message: "OCSP stapling is on but Let's Encrypt killed their OCSP responders in Aug 2025 — stapling yields warnings + zero benefit for LE certs",
			})
		}
	}
	if cfg.TLS.Strategy == "duckdns" && cfg.TLS.DuckDNSToken == "" {
		out = append(out, Finding{
			Severity: SeverityError, Code: "L204", Resource: res,
			Message: "duckdns strategy requires tls.duckdns_token",
		})
	}
	return out
}

func lintWebhook(cfg *config.Config) []Finding {
	var out []Finding
	if cfg.Webhook.Enabled && cfg.Webhook.Port == 0 {
		out = append(out, Finding{
			Severity: SeverityWarning, Code: "L301", Resource: "webhook",
			Message: "webhook.enabled=true but port=0 — pick an explicit port (default usually 9000)",
		})
	}
	if cfg.Webhook.Enabled && cfg.Dashboard.Enabled && cfg.Webhook.Port != 0 && cfg.Webhook.Port == cfg.Dashboard.Port {
		out = append(out, Finding{
			Severity: SeverityError, Code: "L302", Resource: "webhook",
			Message: fmt.Sprintf("webhook.port %d collides with dashboard.port — both can't bind the same port", cfg.Webhook.Port),
		})
	}
	return out
}

func lintAuth(cfg *config.Config) []Finding {
	var out []Finding
	for _, a := range cfg.APIs {
		res := fmt.Sprintf("api[%s]", a.Name)
		if a.JWT != nil {
			if a.JWT.HMACSecret == "" && a.JWT.JWKSURL == "" {
				out = append(out, Finding{
					Severity: SeverityError, Code: "L401", Resource: res,
					Message: "jwt requires either hmac_secret (HS*) or jwks_url (RS*/ES*/EdDSA)",
				})
			}
		}
		if a.MTLS != nil && a.MTLS.CAFile == "" {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L402", Resource: res,
				Message: "mtls.ca_file is required",
			})
		}
		if a.Session && cfg.Security.Sessions == nil {
			out = append(out, Finding{
				Severity: SeverityError, Code: "L403", Resource: res,
				Message: "API.Session=true but Security.Sessions is not configured",
			})
		}
		if a.HMAC != nil {
			for i, k := range a.HMAC.Keys {
				if k.ID == "" || k.Secret == "" {
					out = append(out, Finding{
						Severity: SeverityError, Code: "L404", Resource: res,
						Message: fmt.Sprintf("hmac.keys[%d] missing id or secret", i),
					})
				}
			}
		}
		if a.APIKey != nil {
			for i, k := range a.APIKey.Keys {
				if k.ID == "" || k.Secret == "" {
					out = append(out, Finding{
						Severity: SeverityError, Code: "L405", Resource: res,
						Message: fmt.Sprintf("api_key.keys[%d] missing id or secret", i),
					})
				}
			}
		}
	}
	return out
}

func lintSecurity(cfg *config.Config) []Finding {
	var out []Finding
	if cfg.Security.RBACEnforce && len(cfg.Security.AdminTokens) == 0 {
		out = append(out, Finding{
			Severity: SeverityError, Code: "L501", Resource: "security",
			Message: "rbac_enforce=true but no admin_tokens — nobody can write to /api/admin/*",
		})
	}
	// Consumers referencing groups that aren't declared = typo guard.
	declared := map[string]bool{}
	for _, g := range cfg.Security.ConsumerGroups {
		declared[g.Name] = true
	}
	if len(declared) > 0 {
		for _, c := range cfg.Security.Consumers {
			for _, g := range c.Groups {
				if !declared[g] {
					out = append(out, Finding{
						Severity: SeverityWarning, Code: "L502",
						Resource: fmt.Sprintf("consumer[%s]", c.ID),
						Message:  fmt.Sprintf("group %q not declared in consumer_groups (typo?)", g),
					})
				}
			}
		}
	}
	return out
}

// sortFindings: errors first, then by code, then by resource. Stable
// so CI diff stays readable.
func sortFindings(f []Finding) {
	// Simple insertion sort — usually <30 findings.
	for i := 1; i < len(f); i++ {
		for j := i; j > 0 && less(f[j], f[j-1]); j-- {
			f[j], f[j-1] = f[j-1], f[j]
		}
	}
}

func less(a, b Finding) bool {
	if a.Severity != b.Severity {
		return a.Severity > b.Severity // SeverityError (1) before Warning (0)
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	return strings.Compare(a.Resource, b.Resource) < 0
}
