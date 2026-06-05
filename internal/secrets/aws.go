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

// AWSSecretsManagerProvider resolves `aws-sm://<secret-id>` URIs. We
// avoid the AWS SDK (heavy) and call the public API directly via
// SigV4-signed POST to `secretsmanager.<region>.amazonaws.com`.
//
// SigV4 is non-trivial — for the v0.5 implementation we use the
// AWS_SDK_LOAD_CONFIG=true side-channel: if /var/run/secrets-cache/<id>
// exists (operator's responsibility — e.g. via aws-vault sidecar), we
// read from there. Otherwise we fail with a clear error pointing to the
// known-good integration patterns.
//
// True direct API (SigV4 manually) is a v0.6 stretch when there's
// customer demand justifying the ~400 LoC SigV4 implementation.
type AWSSecretsManagerProvider struct {
	Region    string
	CacheDir  string // /var/run/secrets-cache by default
	Cache     *genericCache
	HTTP      *http.Client
}

func NewAWSSMProvider(region string, ttl time.Duration) *AWSSecretsManagerProvider {
	return &AWSSecretsManagerProvider{
		Region:   region,
		CacheDir: "/var/run/secrets-cache",
		Cache:    newCache(ttl),
		HTTP:     &http.Client{Timeout: 5 * time.Second},
	}
}

func (a *AWSSecretsManagerProvider) Scheme() string { return "aws-sm" }

func (a *AWSSecretsManagerProvider) Resolve(ctx context.Context, ref string) (string, error) {
	if cached, ok := a.Cache.get(ref); ok {
		return cached, nil
	}
	id := strings.TrimPrefix(ref, "aws-sm://")
	if id == "" {
		return "", fmt.Errorf("aws-sm: empty secret id")
	}

	// Path A: secrets-cache sidecar (recommended pattern). Operators run
	// e.g. https://github.com/aws/aws-secretsmanager-caching-go behind a
	// sidecar that drops resolved secrets in CacheDir. We just read them.
	cachePath := a.CacheDir + "/" + id
	if b, err := os.ReadFile(cachePath); err == nil {
		val := strings.TrimRight(string(b), "\n")
		a.Cache.set(ref, val)
		return val, nil
	}

	// Path B: direct API call via instance role + IMDS-supplied creds.
	// This branch needs SigV4 signing; for now we surface an actionable
	// error pointing operators at Path A.
	body, _ := json.Marshal(map[string]string{"SecretId": id})
	url := fmt.Sprintf("https://secretsmanager.%s.amazonaws.com/", a.Region)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, io.NopCloser(strings.NewReader(string(body))))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	// NOTE: missing SigV4 signing — the request will 403. Documented as
	// "use sidecar pattern" until v0.6.
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("aws-sm: %w (use secrets-cache sidecar at %s/<id>)", err, a.CacheDir)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("aws-sm: status %d — direct API not yet supported; use sidecar at %s/<id>",
			resp.StatusCode, a.CacheDir)
	}
	var envelope struct {
		SecretString string `json:"SecretString"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return "", err
	}
	a.Cache.set(ref, envelope.SecretString)
	return envelope.SecretString, nil
}
