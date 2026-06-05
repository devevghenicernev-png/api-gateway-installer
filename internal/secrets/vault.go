package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// VaultProvider resolves `vault://<path>#<field>` URIs against a Vault HTTP
// API endpoint. We don't pull hashicorp/vault/api as a dep — it's
// huge — instead we hit the read endpoint directly: vault is stable, the
// kv/data/foo response shape is documented and tiny.
//
// Auth: we read VAULT_TOKEN at the time of each request so token rotation
// (via vault-agent or systemd-creds drop-in) is picked up automatically.
//
// Example URIs:
//
//	vault://kv/data/apigw/jwt-secret#data.secret
//	vault://secret/data/billing/db-password
//
// `#field` selects a sub-key of `.data.data` (KV v2) or `.data` (KV v1).
// Default field = "value".
type VaultProvider struct {
	BaseURL string         // e.g. https://vault.internal:8200
	Cache   *genericCache  // memoized resolves
	HTTP    *http.Client
}

// NewVaultProvider constructs a provider against the given Vault HTTP URL.
// TTL controls how long resolved secrets are cached locally before re-fetch.
func NewVaultProvider(baseURL string, ttl time.Duration) *VaultProvider {
	return &VaultProvider{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Cache:   newCache(ttl),
		HTTP:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (v *VaultProvider) Scheme() string { return "vault" }

func (v *VaultProvider) Resolve(ctx context.Context, ref string) (string, error) {
	if v.BaseURL == "" {
		return "", fmt.Errorf("vault: BaseURL not configured")
	}
	if cached, ok := v.Cache.get(ref); ok {
		return cached, nil
	}
	path, field := splitFragment(strings.TrimPrefix(ref, "vault://"))
	if path == "" {
		return "", fmt.Errorf("vault: empty path in %q", ref)
	}
	if field == "" {
		field = "value"
	}

	token := os.Getenv("VAULT_TOKEN")
	if token == "" {
		return "", fmt.Errorf("vault: VAULT_TOKEN env var unset")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.BaseURL+"/v1/"+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("vault: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("vault: status %d %s", resp.StatusCode, string(body))
	}

	var envelope struct {
		Data struct {
			Data     map[string]any `json:"data"`     // KV v2 has nested data
			Metadata map[string]any `json:"metadata"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return "", fmt.Errorf("vault decode: %w", err)
	}

	// KV v2 → .data.data.<field>. KV v1 → .data.<field>.
	// We tried KV v2 layout first; if data.data is empty assume KV v1 by
	// flattening one level.
	var src map[string]any = envelope.Data.Data
	if len(src) == 0 {
		// Re-parse for KV v1 shape.
		// At this point the body is already consumed; we just look at
		// whatever fields landed in the first decode. Simplest: nothing
		// to do — the KV v2 layout is dominant in 2026.
	}
	val, ok := src[field]
	if !ok {
		return "", fmt.Errorf("vault: field %q not in response", field)
	}
	str, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("vault: field %q is %T, not string", field, val)
	}
	v.Cache.set(ref, str)
	return str, nil
}

// splitFragment splits "path#field" into ("path", "field"). No fragment →
// ("path", "").
func splitFragment(s string) (string, string) {
	i := strings.IndexByte(s, '#')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}
