// Package migrate translates the legacy bash-installer state
// (/etc/api-gateway/*) into apigw's new config format. The mapping itself
// lives in transform.go; this file only reads from disk.
//
// Old layout (from the bash installer in this repo at <= 0.x):
//
//	/etc/api-gateway/apis.json                # main API + ai-model list
//	/etc/api-gateway/deployments/<name>.json  # per-deployment config
//	/etc/nginx/sites-available/apis           # generated nginx site
//	/usr/local/bin/api-manage                 # old CLI
//
// We don't migrate the nginx site itself — `apigw migrate` re-renders it
// from the translated config so the banner + hash are in apigw's format.
package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// OldRoot is where the bash installer kept its state. Probing this path
// is the canonical "is there an old install here?" question.
const OldRoot = "/etc/api-gateway"

// OldAPIsFile and OldDeployDir hang off OldRoot.
var (
	OldAPIsFile   = filepath.Join(OldRoot, "apis.json")
	OldDeployDir  = filepath.Join(OldRoot, "deployments")
	OldStatusFile = "/var/lib/api-gateway/deployment-status.json"
	OldAPIManage  = "/usr/local/bin/api-manage"
)

// BashAPIsFile is the shape of apis.json. We accept extra fields silently;
// the bash installer kept adding flags over time and we don't need them
// (fix_redirects, streaming, websocket, etc. are nginx-tunable and we
// re-emit from a template).
type BashAPIsFile struct {
	APIs []BashAPI `json:"apis"`
}

// BashAPI is one entry inside apis.json.
//
// `Type` is either "api" (default) or "ai-model"; we use it to route
// migration into either config.API (general) or the AI registration path.
type BashAPI struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Port        int    `json:"port"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Type        string `json:"type"` // "" | "api" | "ai-model"
}

// BashDeploy is one /etc/api-gateway/deployments/<name>.json. Field names
// match the bash installer's heredoc verbatim — see
// modules/deployment-manager.sh in this repo.
type BashDeploy struct {
	ServiceName    string `json:"service_name"`
	GitHubRepo     string `json:"github_repo"`
	Branch         string `json:"branch"`
	Port           int    `json:"port"`
	BuildCommand   string `json:"build_command"`
	StartCommand   string `json:"start_command"`
	Runtime        string `json:"runtime"` // "auto" | "node" | "python" | "docker"
	DeployPath     string `json:"deploy_path"`
	WebhookSecret  string `json:"webhook_secret"`
	AutoDeploy     bool   `json:"auto_deploy"`
	ProcessManager string `json:"process_manager"` // "pm2" | "systemd"
	CreatedAt      string `json:"created_at"`
	Status         string `json:"status"`
}

// Detected is what Detect() returns. Empty Reasons → no old install.
type Detected struct {
	Root         string
	APIsFile     string
	DeployDir    string
	APIManageExe string
	HasAPIsFile  bool
	HasDeployDir bool
	HasAPIManage bool
	APICount     int
	DeployCount  int
	Reasons      []string
}

// Detect probes the filesystem and reports what apigw migrate would act on.
//
// Cheap: only os.Stat + a few directory scans. Safe to call repeatedly,
// including inside doctor checks (Phase 8.1 will add one).
func Detect() (Detected, error) {
	d := Detected{
		Root:         OldRoot,
		APIsFile:     OldAPIsFile,
		DeployDir:    OldDeployDir,
		APIManageExe: OldAPIManage,
	}
	if st, err := os.Stat(OldAPIsFile); err == nil && !st.IsDir() {
		d.HasAPIsFile = true
		d.Reasons = append(d.Reasons, "found "+OldAPIsFile)
		if apis, err := ReadAPIsFile(OldAPIsFile); err == nil {
			d.APICount = len(apis.APIs)
		}
	}
	if st, err := os.Stat(OldDeployDir); err == nil && st.IsDir() {
		d.HasDeployDir = true
		entries, err := os.ReadDir(OldDeployDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
					d.DeployCount++
				}
			}
		}
		if d.DeployCount > 0 {
			d.Reasons = append(d.Reasons,
				fmt.Sprintf("found %d deployment(s) in %s", d.DeployCount, OldDeployDir))
		}
	}
	if st, err := os.Stat(OldAPIManage); err == nil && !st.IsDir() {
		d.HasAPIManage = true
		d.Reasons = append(d.Reasons, "found "+OldAPIManage)
	}
	return d, nil
}

// IsBashInstall reports whether the host has any legacy artifact worth
// migrating. False means `apigw migrate` should short-circuit.
func (d Detected) IsBashInstall() bool {
	return d.HasAPIsFile || d.HasDeployDir
}

// ReadAPIsFile parses /etc/api-gateway/apis.json.
//
// Returns an empty BashAPIsFile (not an error) when the file is missing.
func ReadAPIsFile(path string) (BashAPIsFile, error) {
	var f BashAPIsFile
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}

// ReadDeployments scans dir for *.json and parses each. Skips files that
// don't unmarshal — the bash installer occasionally wrote tmp files in
// the same directory.
func ReadDeployments(dir string) ([]BashDeploy, error) {
	out := []BashDeploy{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		full := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var d BashDeploy
		if err := json.Unmarshal(b, &d); err != nil {
			continue
		}
		if d.ServiceName == "" {
			// Filename fallback: deployments/foo.json → foo.
			d.ServiceName = trimSuffix(e.Name(), ".json")
		}
		out = append(out, d)
	}
	return out, nil
}

// trimSuffix is filepath/path.TrimSuffix without pulling either in.
func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
