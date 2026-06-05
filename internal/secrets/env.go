package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// envProvider handles `env://<NAME>` references. Use case: secrets
// injected via systemd LoadCredential or Docker `secrets:` block —
// the operator already pinned the source, apigw just reads the env var.
type envProvider struct{}

func (envProvider) Scheme() string { return "env" }

func (envProvider) Resolve(_ context.Context, ref string) (string, error) {
	name := strings.TrimPrefix(ref, "env://")
	if name == "" {
		return "", fmt.Errorf("env: empty var name in %q", ref)
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("env: %s is empty", name)
	}
	return v, nil
}
