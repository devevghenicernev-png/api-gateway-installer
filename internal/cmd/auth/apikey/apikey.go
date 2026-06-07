// Package apikey owns `apigw auth apikey ...` — the operator-facing
// commands that mint, list, and revoke API keys for an API. Keys are
// stored under the corresponding config.API.APIKey block in the live
// config file; mutations route through cfg.Save() so the flock-guarded
// atomic write applies, and the dashboard's fsnotify watcher refreshes
// the in-memory view immediately.
package apikey

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdAPIKey(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apikey <command>",
		Short: "Manage API keys per API (add / list / revoke / rotate)",
	}
	cmd.AddCommand(newAdd(f))
	cmd.AddCommand(newList(f))
	cmd.AddCommand(newRevoke(f))
	cmd.AddCommand(newRotate(f))
	return cmd
}

func newAdd(f *cmdutil.Factory) *cobra.Command {
	var owner, ttl, header, consumerID string
	var rps int
	var scopes []string
	cmd := &cobra.Command{
		Use:   "add <api> <key-id>",
		Short: "Mint a new key for an API",
		Args:  cobra.ExactArgs(2),
		Example: `  $ sudo apigw auth apikey add billing ci-bot --owner ci --rps 100 --ttl 720h
  $ sudo apigw auth apikey add billing readonly --scopes read,list`,
		RunE: func(c *cobra.Command, args []string) error {
			apiName, keyID := args[0], args[1]
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(apiName)
			if api == nil {
				return fmt.Errorf("api %q not found", apiName)
			}
			if api.APIKey == nil {
				api.APIKey = &config.APIKeyAuth{Header: header}
			} else if header != "" {
				api.APIKey.Header = header
			}
			for _, k := range api.APIKey.Keys {
				if k.ID == keyID {
					return fmt.Errorf("key %q already exists; rotate it instead", keyID)
				}
			}
			secret, gerr := generateKey()
			if gerr != nil {
				return gerr
			}
			entry := config.APIKey{
				ID:         keyID,
				Secret:     secret,
				Owner:      owner,
				RPS:        rps,
				Scopes:     scopes,
				ConsumerID: consumerID,
			}
			if ttl != "" {
				d, perr := time.ParseDuration(ttl)
				if perr != nil {
					return fmt.Errorf("--ttl: %w", perr)
				}
				entry.ExpiresAt = time.Now().Add(d).UTC()
			}
			api.APIKey.Keys = append(api.APIKey.Keys, entry)
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			printKey(f, apiName, entry)
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "free-form owner label (team/email)")
	cmd.Flags().IntVar(&rps, "rps", 0, "per-key requests-per-second cap (0 = inherit API limit)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "time until expiry, e.g. 720h (30d). Empty = never expires")
	cmd.Flags().StringVar(&header, "header", "", "override the X-API-Key header name for this API")
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "comma-separated scopes the key carries")
	cmd.Flags().StringVar(&consumerID, "consumer-id", "",
		"map this key to a Consumer entry (default: use the key ID itself as the consumer)")
	return cmd
}

func newList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list <api>",
		Short: "List keys for an API (secrets redacted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			if api.APIKey == nil || len(api.APIKey.Keys) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no keys configured")
				return nil
			}
			out := f.IOStreams.Out
			fmt.Fprintf(out, "%-24s  %-16s  %-6s  %-12s  %s\n", "ID", "OWNER", "RPS", "EXPIRES", "SECRET")
			for _, k := range api.APIKey.Keys {
				exp := "never"
				if !k.ExpiresAt.IsZero() {
					exp = k.ExpiresAt.Format("2006-01-02")
				}
				owner := k.Owner
				if owner == "" {
					owner = "-"
				}
				rps := "—"
				if k.RPS > 0 {
					rps = fmt.Sprintf("%d", k.RPS)
				}
				disabled := ""
				if k.Disabled {
					disabled = " (DISABLED)"
				}
				fmt.Fprintf(out, "%-24s  %-16s  %-6s  %-12s  %s%s\n",
					k.ID, owner, rps, exp, redact(k.Secret), disabled)
			}
			return nil
		},
	}
}

func newRevoke(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <api> <key-id>",
		Short: "Remove a key (immediately invalid)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			apiName, keyID := args[0], args[1]
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(apiName)
			if api == nil {
				return fmt.Errorf("api %q not found", apiName)
			}
			if api.APIKey == nil {
				return fmt.Errorf("api %q has no keys", apiName)
			}
			idx := -1
			for i, k := range api.APIKey.Keys {
				if k.ID == keyID {
					idx = i
					break
				}
			}
			if idx < 0 {
				return fmt.Errorf("key %q not found", keyID)
			}
			api.APIKey.Keys = append(api.APIKey.Keys[:idx], api.APIKey.Keys[idx+1:]...)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "revoked %s/%s\n", apiName, keyID)
			return nil
		},
	}
}

func newRotate(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "rotate <api> <key-id>",
		Short: "Rotate a key in place (preserves owner/scopes/TTL)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			apiName, keyID := args[0], args[1]
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(apiName)
			if api == nil || api.APIKey == nil {
				return fmt.Errorf("api %q has no keys", apiName)
			}
			for i, k := range api.APIKey.Keys {
				if k.ID == keyID {
					secret, gerr := generateKey()
					if gerr != nil {
						return gerr
					}
					api.APIKey.Keys[i].Secret = secret
					if err := cfg.Save(); err != nil {
						return err
					}
					printKey(f, apiName, api.APIKey.Keys[i])
					return nil
				}
			}
			return fmt.Errorf("key %q not found", keyID)
		},
	}
}

// ---------- helpers ----------

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

func generateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return "ak_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func redact(s string) string {
	if len(s) < 8 {
		return "********"
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func printKey(f *cmdutil.Factory, api string, k config.APIKey) {
	out := f.IOStreams.Out
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ┌──────────────────────────────────────────────────────────────┐")
	fmt.Fprintln(out, "  │  apigw api key issued — copy NOW, it won't be shown again   │")
	fmt.Fprintln(out, "  └──────────────────────────────────────────────────────────────┘")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "    api:    %s\n", api)
	fmt.Fprintf(out, "    id:     %s\n", k.ID)
	if k.Owner != "" {
		fmt.Fprintf(out, "    owner:  %s\n", k.Owner)
	}
	if len(k.Scopes) > 0 {
		fmt.Fprintf(out, "    scopes: %s\n", strings.Join(k.Scopes, ", "))
	}
	if !k.ExpiresAt.IsZero() {
		fmt.Fprintf(out, "    expires: %s\n", k.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "    secret: %s\n", k.Secret)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Use it like:")
	fmt.Fprintf(out, "    curl -H 'X-API-Key: %s' https://api.example.com/api/%s\n", k.Secret, api)
	fmt.Fprintln(out)
}
