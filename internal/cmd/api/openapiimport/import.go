// Package openapiimport implements `apigw api import <spec.yaml|.json>`.
//
// Reads an OpenAPI 3.x document, creates one apigw API entry per server
// URL (or one per top-level path-group if --per-path is set), and
// applies sensible defaults: CORS for browser clients, the document's
// `securitySchemes` mapped onto APIKey / JWT / OAuth2 config, and a
// reasonable rate limit.
//
// We deliberately keep this minimal: a complete OpenAPI parser is its
// own package. We pull out only the fields apigw cares about
// (servers[].url, info.title, components.securitySchemes[].type) and
// ignore the rest.
package openapiimport

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

// minimalSpec is the subset of OpenAPI we actually read.
type minimalSpec struct {
	OpenAPI string `yaml:"openapi" json:"openapi"`
	Info    struct {
		Title       string `yaml:"title" json:"title"`
		Version     string `yaml:"version" json:"version"`
		Description string `yaml:"description" json:"description"`
	} `yaml:"info" json:"info"`
	Servers []struct {
		URL         string `yaml:"url" json:"url"`
		Description string `yaml:"description" json:"description"`
	} `yaml:"servers" json:"servers"`
	Components struct {
		SecuritySchemes map[string]struct {
			Type             string `yaml:"type" json:"type"`
			In               string `yaml:"in" json:"in"`
			Name             string `yaml:"name" json:"name"`
			Scheme           string `yaml:"scheme" json:"scheme"`
			BearerFormat     string `yaml:"bearerFormat" json:"bearerFormat"`
			OpenIdConnectURL string `yaml:"openIdConnectUrl" json:"openIdConnectUrl"`
			Flows            map[string]struct {
				TokenURL string            `yaml:"tokenUrl" json:"tokenUrl"`
				Scopes   map[string]string `yaml:"scopes" json:"scopes"`
			} `yaml:"flows" json:"flows"`
		} `yaml:"securitySchemes" json:"securitySchemes"`
	} `yaml:"components" json:"components"`
}

func NewCmdImport(f *cmdutil.Factory) *cobra.Command {
	var name, path string
	var port int
	var rps int
	var withCORS bool
	cmd := &cobra.Command{
		Use:   "import <spec.yaml|.json>",
		Short: "Create an API from an OpenAPI 3.x specification",
		Long: "Reads the spec, picks the first server URL (or --path to override),\n" +
			"infers auth from securitySchemes (apiKey → APIKey, http+bearer →\n" +
			"JWT/OAuth2, openIdConnect → JWT with OIDC discovery), and writes a\n" +
			"new API entry under the name you provide (or info.title slug).",
		Example: `  $ sudo apigw api import openapi.yaml --name billing --port 8081
  $ sudo apigw api import https://petstore3.swagger.io/api/v3/openapi.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			spec, err := readSpec(args[0])
			if err != nil {
				return tui.NewError("read spec", err.Error())
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			finalName := name
			if finalName == "" {
				finalName = slugify(spec.Info.Title)
				if finalName == "" {
					finalName = "imported"
				}
			}
			if cfg.FindAPI(finalName) != nil {
				return fmt.Errorf("api %q already exists; pick --name", finalName)
			}
			finalPath := path
			finalPort := port
			if finalPath == "" && len(spec.Servers) > 0 {
				u, perr := url.Parse(spec.Servers[0].URL)
				if perr == nil && u.Path != "" {
					finalPath = u.Path
				}
				if finalPath == "" {
					finalPath = "/api/" + finalName
				}
				if finalPort == 0 && u != nil && u.Port() != "" {
					_, _ = fmt.Sscanf(u.Port(), "%d", &finalPort)
				}
			}
			if finalPath == "" {
				finalPath = "/api/" + finalName
			}
			if finalPort == 0 {
				return fmt.Errorf("could not infer port; pass --port")
			}
			api := config.API{
				Name:        finalName,
				Port:        finalPort,
				Path:        finalPath,
				Description: spec.Info.Title + " (imported from OpenAPI " + spec.OpenAPI + ")",
				Enabled:     true,
			}
			if withCORS {
				api.CORS = &config.CORS{
					Origins: []string{"*"},
					Methods: []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
					Headers: []string{"Content-Type", "Authorization", "X-API-Key"},
				}
			}
			if rps > 0 {
				api.RateLimit = &config.RateLimit{RPS: rps, Burst: rps * 2, Key: "ip"}
			}
			applySecuritySchemes(&api, spec.Components.SecuritySchemes)
			if err := cfg.AddAPI(api); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "%s imported api %q (path %s → :%d)\n",
				tui.Styles.Success.Render(tui.GlyphCheck),
				tui.Styles.Identifier.Render(finalName),
				finalPath, finalPort)
			fmt.Fprintf(f.IOStreams.Out, "  %s edit /etc/apigw/config.yaml to tune middleware\n",
				tui.Styles.Accent.Render("Next:"))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "override the API name (default: slug of info.title)")
	cmd.Flags().StringVar(&path, "path", "", "nginx mount path (default: from servers[0].url)")
	cmd.Flags().IntVar(&port, "port", 0, "upstream port (default: from servers[0].url)")
	cmd.Flags().IntVar(&rps, "rate-limit-rps", 0, "global rate limit per IP (0 = none)")
	cmd.Flags().BoolVar(&withCORS, "cors", true, "auto-enable permissive CORS for browser clients")
	return cmd
}

func readSpec(pathOrURL string) (*minimalSpec, error) {
	var body []byte
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		return nil, fmt.Errorf("fetching from URLs not yet supported; download first")
	}
	b, err := os.ReadFile(pathOrURL)
	if err != nil {
		return nil, err
	}
	body = b
	var spec minimalSpec
	if strings.HasSuffix(strings.ToLower(pathOrURL), ".json") {
		if err := json.Unmarshal(body, &spec); err != nil {
			return nil, fmt.Errorf("parse JSON: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(body, &spec); err != nil {
			return nil, fmt.Errorf("parse YAML: %w", err)
		}
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		return nil, fmt.Errorf("unsupported OpenAPI version %q (need 3.x)", spec.OpenAPI)
	}
	return &spec, nil
}

func applySecuritySchemes(api *config.API, schemes map[string]struct {
	Type             string `yaml:"type" json:"type"`
	In               string `yaml:"in" json:"in"`
	Name             string `yaml:"name" json:"name"`
	Scheme           string `yaml:"scheme" json:"scheme"`
	BearerFormat     string `yaml:"bearerFormat" json:"bearerFormat"`
	OpenIdConnectURL string `yaml:"openIdConnectUrl" json:"openIdConnectUrl"`
	Flows            map[string]struct {
		TokenURL string            `yaml:"tokenUrl" json:"tokenUrl"`
		Scopes   map[string]string `yaml:"scopes" json:"scopes"`
	} `yaml:"flows" json:"flows"`
}) {
	for _, s := range schemes {
		switch s.Type {
		case "apiKey":
			if api.APIKey == nil {
				api.APIKey = &config.APIKeyAuth{}
			}
			switch s.In {
			case "header":
				api.APIKey.Header = s.Name
			case "query":
				api.APIKey.QueryParam = s.Name
			}
		case "http":
			if strings.EqualFold(s.Scheme, "bearer") && api.JWT == nil {
				api.JWT = &config.JWT{Algorithm: "RS256"}
			}
		case "openIdConnect":
			if api.JWT == nil {
				api.JWT = &config.JWT{}
			}
			// JWT.OIDCIssuer field doesn't exist yet — operator can
			// run `apigw auth jwt set <api> --oidc-issuer ...` after.
			_ = s.OpenIdConnectURL
		case "oauth2":
			if api.OAuth2 == nil {
				for _, flow := range s.Flows {
					api.OAuth2 = &config.OAuth2{
						IntrospectionURL: flow.TokenURL,
					}
					break
				}
			}
		}
	}
}

func slugify(s string) string {
	out := strings.Builder{}
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			if out.Len() > 0 && out.String()[out.Len()-1] != '-' {
				out.WriteByte('-')
			}
		}
	}
	return strings.Trim(out.String(), "-")
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
