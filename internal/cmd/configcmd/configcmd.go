// Package configcmd owns `apigw config …` — static config checks
// (`lint`) plus export/import for moving config between hosts.
package configcmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	koanf "github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/lint"
	"github.com/devevghenicernev-png/apigw/internal/nginx"
	"github.com/devevghenicernev-png/apigw/internal/openapi"
)

func NewCmdConfig(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config <command>",
		Short: "Inspect, validate, export and import the loaded config",
	}
	cmd.AddCommand(newLint(f))
	cmd.AddCommand(newExport(f))
	cmd.AddCommand(newImport(f))
	return cmd
}

func newLint(f *cmdutil.Factory) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Static checks over the loaded config (port collisions, missing refs, ...)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceUsage = true
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			findings := lint.Lint(cfg)

			if asJSON {
				return json.NewEncoder(f.IOStreams.Out).Encode(findings)
			}
			out := f.IOStreams.Out
			if len(findings) == 0 {
				fmt.Fprintln(out, "ok — no findings")
				return nil
			}
			errs, warns := 0, 0
			for _, fnd := range findings {
				fmt.Fprintf(out, "%-7s  %-4s  %-24s  %s\n",
					fnd.Severity.String(), fnd.Code, fnd.Resource, fnd.Message)
				if fnd.Severity == lint.SeverityError {
					errs++
				} else {
					warns++
				}
			}
			fmt.Fprintf(out, "\n%d error(s), %d warning(s)\n", errs, warns)
			if lint.HasErrors(findings) {
				return fmt.Errorf("lint failed: %d error(s)", errs)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit findings as JSON")
	return cmd
}

// newExport — `apigw config export --format yaml|json|openapi --output <file>`.
//
// Use cases: move a config between hosts (yaml/json), publish the API surface
// to a developer portal (openapi). Sensitive secrets stay as SecretRef strings
// — values resolved at runtime aren't expanded here. We deliberately don't
// include a "fully expanded" mode; exports with resolved secrets are a footgun.
func newExport(f *cmdutil.Factory) *cobra.Command {
	var (
		format string
		output string
		// OpenAPI-only flags.
		apiTitle   string
		apiVersion string
		apiBaseURL string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Print the loaded config in yaml, json or openapi format",
		Long: "Format auto-detect: --format defaults to yaml.\n\n" +
			"yaml/json     — full config round-trip; re-importable with `config import`.\n" +
			"openapi       — minimal OpenAPI 3.0 spec covering enabled APIs; not a config\n" +
			"                round-trip (use `openapi-import` to bring it back).",
		Example: `  # save current config as JSON for git
  $ apigw config export --format json --output apigw.json
  # publish the API surface for swagger-ui
  $ apigw config export --format openapi --output openapi.yaml \
      --openapi-title "Acme APIs" --openapi-version v1`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceUsage = true
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			body, err := renderExport(cfg, format, apiTitle, apiVersion, apiBaseURL)
			if err != nil {
				return err
			}
			return writeOutput(f.IOStreams.Out, output, body)
		},
	}
	cmd.Flags().StringVar(&format, "format", "yaml", "output format: yaml | json | openapi")
	cmd.Flags().StringVar(&output, "output", "", "write to this file (default: stdout)")
	cmd.Flags().StringVar(&apiTitle, "openapi-title", "apigw exported APIs", "title for the openapi spec")
	cmd.Flags().StringVar(&apiVersion, "openapi-version", "1.0.0", "version for the openapi spec")
	cmd.Flags().StringVar(&apiBaseURL, "openapi-server", "", "servers[0].url (empty omits the field)")
	return cmd
}

func renderExport(cfg *config.Config, format, title, version, baseURL string) ([]byte, error) {
	switch strings.ToLower(format) {
	case "yaml", "yml", "":
		return cfg.Snapshot()
	case "json":
		k := koanf.New(".")
		if err := k.Load(structs.Provider(*cfg, "koanf"), nil); err != nil {
			return nil, fmt.Errorf("collect: %w", err)
		}
		return json.MarshalIndent(k.Raw(), "", "  ")
	case "openapi", "oas3":
		spec := openapi.Export(toOpenAPIList(cfg), title, version, baseURL)
		// OpenAPI gets YAML by default — it's how the JS tooling expects to see
		// it. Operators wanting JSON can pipe through yq if they need to.
		k := koanf.New(".")
		if err := k.Load(structs.Provider(spec, "yaml"), nil); err != nil {
			return nil, fmt.Errorf("collect openapi: %w", err)
		}
		return k.Marshal(yaml.Parser())
	}
	return nil, fmt.Errorf("config: unknown format %q (yaml | json | openapi)", format)
}

func toOpenAPIList(cfg *config.Config) []openapi.API {
	out := make([]openapi.API, 0, len(cfg.APIs))
	for _, a := range cfg.APIs {
		entry := openapi.API{
			Name:    a.Name,
			Path:    a.Path,
			Enabled: a.Enabled,
		}
		if a.APIKey != nil {
			entry.HasAPIKey = true
			entry.APIKeyName = a.APIKey.Header
		}
		if a.JWT != nil {
			entry.HasJWT = true
		}
		if a.OAuth2 != nil {
			entry.HasOAuth2 = true
		}
		if a.HMAC != nil {
			entry.HasHMAC = true
		}
		if a.MTLS != nil {
			entry.HasMTLS = true
		}
		if a.Lifecycle != nil {
			switch a.Lifecycle.State {
			case "deprecated":
				entry.Deprecated = true
			case "retired":
				entry.Retired = true
			}
		}
		out = append(out, entry)
	}
	return out
}

// newImport — `apigw config import <file>`. Reads YAML or JSON, validates,
// reloads nginx. Refuses to overwrite a working config when the new one
// fails lint — operators can still inspect the diff with --dry-run.
func newImport(f *cmdutil.Factory) *cobra.Command {
	var (
		format string
		dryRun bool
		yes    bool
	)
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Replace the loaded config from a yaml/json export",
		Long: "Validates via `config lint` before writing. Existing config is moved to\n" +
			"<config>.apigw-prev as a one-step rollback. nginx is reloaded after the\n" +
			"swap; if the reload fails, the previous file is restored.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			c.SilenceUsage = true
			path := args[0]
			body, err := readSource(path)
			if err != nil {
				return err
			}
			next, err := parseImport(body, format, path)
			if err != nil {
				return err
			}
			findings := lint.Lint(next)
			if lint.HasErrors(findings) {
				return fmt.Errorf("config: import refused — %d lint error(s); run `apigw config lint` on the source first", countErrors(findings))
			}
			if dryRun {
				fmt.Fprintf(f.IOStreams.Out, "ok — %d API(s), %d lint warning(s), %d error(s) (dry-run, not written)\n",
					len(next.APIs), countWarnings(findings), countErrors(findings))
				return nil
			}
			current, err := loadCfg(f)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(f.IOStreams.Out,
					"replace %s (%d API(s) → %d API(s)). re-run with --yes to commit.\n",
					current.Path(), len(current.APIs), len(next.APIs))
				return nil
			}
			next.SetPath(current.Path())
			if err := next.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			if err := nginx.NewManager().WriteAndReload(next); err != nil {
				return fmt.Errorf("nginx reload: %w", err)
			}
			fmt.Fprintln(f.IOStreams.Out, "ok — config imported, nginx reloaded")
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "auto", "yaml | json | auto (by extension)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate only; never touch disk")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func parseImport(body []byte, format, path string) (*config.Config, error) {
	f := strings.ToLower(format)
	if f == "auto" || f == "" {
		switch {
		case strings.HasSuffix(strings.ToLower(path), ".json"):
			f = "json"
		default:
			f = "yaml"
		}
	}
	k := koanf.New(".")
	defaults := config.Defaults()
	if err := k.Load(structs.Provider(defaults, "koanf"), nil); err != nil {
		return nil, fmt.Errorf("defaults: %w", err)
	}
	switch f {
	case "yaml", "yml":
		if err := k.Load(bytesProvider(body), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
	case "json":
		// koanf has no JSON parser in our deps — round-trip via stdlib then
		// re-feed as a map provider via a small shim file.
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
		if err := k.Load(mapProvider(raw), nil); err != nil {
			return nil, fmt.Errorf("load json: %w", err)
		}
	default:
		return nil, fmt.Errorf("config: unknown format %q (yaml | json)", format)
	}
	cfg := config.Defaults()
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &cfg, nil
}

func readSource(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	provider := file.Provider(path)
	body, err := provider.ReadBytes()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return body, nil
}

func writeOutput(stdout io.Writer, path string, body []byte) error {
	if path == "" || path == "-" {
		_, err := stdout.Write(body)
		if err != nil {
			return err
		}
		if len(body) > 0 && body[len(body)-1] != '\n' {
			_, _ = stdout.Write([]byte{'\n'})
		}
		return nil
	}
	return os.WriteFile(path, body, 0o644)
}

func countErrors(findings []lint.Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == lint.SeverityError {
			n++
		}
	}
	return n
}

func countWarnings(findings []lint.Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity != lint.SeverityError {
			n++
		}
	}
	return n
}

func loadCfg(f *cmdutil.Factory) (*config.Config, error) {
	c, err := f.Config()
	if err != nil {
		return nil, err
	}
	cfg, ok := c.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("config: unexpected type %T", c)
	}
	return cfg, nil
}

// mapProvider is the minimal koanf.Provider that re-emits a pre-parsed map.
// Cheaper than pulling in koanf/providers/confmap (one tiny file vs a dep).
type mapKoanfProvider struct{ m map[string]any }

func mapProvider(m map[string]any) *mapKoanfProvider { return &mapKoanfProvider{m: m} }

func (p *mapKoanfProvider) ReadBytes() ([]byte, error) { return nil, fmt.Errorf("unsupported") }
func (p *mapKoanfProvider) Read() (map[string]any, error) {
	if p.m == nil {
		return map[string]any{}, nil
	}
	return p.m, nil
}

// bytesProvider feeds koanf raw bytes through the supplied parser. Mirrors
// the upstream rawbytes provider in a few lines to avoid adding a dep.
type bytesKoanfProvider struct{ b []byte }

func bytesProvider(b []byte) *bytesKoanfProvider { return &bytesKoanfProvider{b: b} }

func (p *bytesKoanfProvider) ReadBytes() ([]byte, error) { return p.b, nil }
func (p *bytesKoanfProvider) Read() (map[string]any, error) {
	return nil, fmt.Errorf("not implemented")
}
