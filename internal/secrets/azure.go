package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// AzureKVProvider resolves `azure-kv://<vault>/<secret-name>` URIs. Like
// AWS-SM, we punt on the full SDK (~10 MB) and rely on the sidecar
// pattern: the Azure Key Vault CSI driver or akv2k8s sidecar writes
// resolved secrets to CacheDir, and we read them as files.
//
// Direct REST + MSI flow is feasible but adds ~300 LoC for AAD token
// exchange + retry on token expiry. Punt to v0.6 unless demand justifies.
type AzureKVProvider struct {
	CacheDir string // /var/run/secrets-cache
	Cache    *genericCache
}

func NewAzureKVProvider(ttl time.Duration) *AzureKVProvider {
	return &AzureKVProvider{
		CacheDir: "/var/run/secrets-cache",
		Cache:    newCache(ttl),
	}
}

func (a *AzureKVProvider) Scheme() string { return "azure-kv" }

func (a *AzureKVProvider) Resolve(_ context.Context, ref string) (string, error) {
	if cached, ok := a.Cache.get(ref); ok {
		return cached, nil
	}
	rest := strings.TrimPrefix(ref, "azure-kv://")
	// Sidecar layout: cacheDir/<vault>/<secret-name>
	cachePath := a.CacheDir + "/" + rest
	b, err := os.ReadFile(cachePath)
	if err != nil {
		return "", fmt.Errorf("azure-kv: read %s: %w (mount AKV CSI driver or akv2k8s sidecar)", cachePath, err)
	}
	val := strings.TrimRight(string(b), "\n")
	a.Cache.set(ref, val)
	return val, nil
}
