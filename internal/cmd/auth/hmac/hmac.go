// Package hmac owns `apigw auth hmac …` — operator-facing commands
// that mint, list, rotate and revoke HMAC signing credentials. Keys
// land in config.API.HMAC.Keys; mutations route through cfg.Save() so
// the flock-guarded atomic write applies. nginx auth_request to
// /auth/hmac/<api> validates every signed request.
package hmac

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

func NewCmdHMAC(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hmac <command>",
		Short: "Manage HMAC signing keys per API (add / list / revoke / rotate)",
		Long: "HMAC request signing — clients send Authorization: HMAC " +
			"<key-id>:<sig> plus X-Apigw-Date / X-Apigw-Nonce / " +
			"X-Apigw-Content-SHA256. nginx auth_request /auth/hmac/<api> " +
			"validates every signed request.",
	}
	cmd.AddCommand(newAdd(f))
	cmd.AddCommand(newList(f))
	cmd.AddCommand(newRevoke(f))
	cmd.AddCommand(newRotate(f))
	return cmd
}

func newAdd(f *cmdutil.Factory) *cobra.Command {
	var owner, ttl, alg, consumerID string
	var scopes []string
	var requireBodyHash bool
	cmd := &cobra.Command{
		Use:   "add <api> <key-id>",
		Short: "Mint a new HMAC signing key for an API",
		Args:  cobra.ExactArgs(2),
		Example: `  $ sudo apigw auth hmac add billing ci-bot --owner ci --ttl 720h
  $ sudo apigw auth hmac add billing payments --algorithm hmac-sha512 --require-body-hash`,
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
			if api.HMAC == nil {
				api.HMAC = &config.HMACAuth{}
			}
			if requireBodyHash {
				api.HMAC.RequireBodyHash = true
			}
			for _, k := range api.HMAC.Keys {
				if k.ID == keyID {
					return fmt.Errorf("key %q already exists; rotate it instead", keyID)
				}
			}
			secret, gerr := generateSecret()
			if gerr != nil {
				return gerr
			}
			entry := config.HMACKey{
				ID:         keyID,
				Secret:     secret,
				Algorithm:  alg,
				Owner:      owner,
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
			api.HMAC.Keys = append(api.HMAC.Keys, entry)
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			printKey(f, apiName, entry)
			return nil
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "free-form owner label (team/email)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "time until expiry, e.g. 720h (30d). Empty = never expires")
	cmd.Flags().StringVar(&alg, "algorithm", "", "hash function: hmac-sha256 (default) or hmac-sha512")
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "comma-separated scopes the key carries")
	cmd.Flags().BoolVar(&requireBodyHash, "require-body-hash", false,
		"reject UNSIGNED-PAYLOAD on this API (forces clients to include X-Apigw-Content-SHA256)")
	cmd.Flags().StringVar(&consumerID, "consumer-id", "",
		"map this key to a Consumer entry (default: use the key ID itself as the consumer)")
	return cmd
}

func newList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list <api>",
		Short: "List HMAC keys for an API (secrets redacted)",
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
			if api.HMAC == nil || len(api.HMAC.Keys) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no HMAC keys configured")
				return nil
			}
			out := f.IOStreams.Out
			fmt.Fprintf(out, "%-24s  %-16s  %-12s  %-12s  %s\n", "ID", "OWNER", "ALG", "EXPIRES", "SECRET")
			for _, k := range api.HMAC.Keys {
				exp := "never"
				if !k.ExpiresAt.IsZero() {
					exp = k.ExpiresAt.Format("2006-01-02")
				}
				owner := k.Owner
				if owner == "" {
					owner = "-"
				}
				alg := k.Algorithm
				if alg == "" {
					alg = "-"
				}
				disabled := ""
				if k.Disabled {
					disabled = " (DISABLED)"
				}
				fmt.Fprintf(out, "%-24s  %-16s  %-12s  %-12s  %s%s\n",
					k.ID, owner, alg, exp, redact(k.Secret), disabled)
			}
			return nil
		},
	}
}

func newRevoke(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <api> <key-id>",
		Short: "Remove an HMAC key (immediately invalid)",
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
			if api.HMAC == nil {
				return fmt.Errorf("api %q has no HMAC keys", apiName)
			}
			idx := -1
			for i, k := range api.HMAC.Keys {
				if k.ID == keyID {
					idx = i
					break
				}
			}
			if idx < 0 {
				return fmt.Errorf("key %q not found", keyID)
			}
			api.HMAC.Keys = append(api.HMAC.Keys[:idx], api.HMAC.Keys[idx+1:]...)
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
		Short: "Rotate an HMAC key in place (preserves owner/scopes/TTL/algorithm)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			apiName, keyID := args[0], args[1]
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(apiName)
			if api == nil || api.HMAC == nil {
				return fmt.Errorf("api %q has no HMAC keys", apiName)
			}
			for i, k := range api.HMAC.Keys {
				if k.ID == keyID {
					secret, gerr := generateSecret()
					if gerr != nil {
						return gerr
					}
					api.HMAC.Keys[i].Secret = secret
					if err := cfg.Save(); err != nil {
						return err
					}
					printKey(f, apiName, api.HMAC.Keys[i])
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

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return "hk_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func redact(s string) string {
	if len(s) < 8 {
		return "********"
	}
	return s[:6] + "…" + s[len(s)-4:]
}

func printKey(f *cmdutil.Factory, api string, k config.HMACKey) {
	out := f.IOStreams.Out
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ┌──────────────────────────────────────────────────────────────┐")
	fmt.Fprintln(out, "  │  HMAC key issued — copy the secret NOW, it won't be shown   │")
	fmt.Fprintln(out, "  │  again. Sign requests like AWS Sig v4 (see example below).  │")
	fmt.Fprintln(out, "  └──────────────────────────────────────────────────────────────┘")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "    api:       %s\n", api)
	fmt.Fprintf(out, "    id:        %s\n", k.ID)
	if k.Algorithm != "" {
		fmt.Fprintf(out, "    algorithm: %s\n", k.Algorithm)
	}
	if k.Owner != "" {
		fmt.Fprintf(out, "    owner:     %s\n", k.Owner)
	}
	if len(k.Scopes) > 0 {
		fmt.Fprintf(out, "    scopes:    %s\n", strings.Join(k.Scopes, ", "))
	}
	if !k.ExpiresAt.IsZero() {
		fmt.Fprintf(out, "    expires:   %s\n", k.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "    secret:    %s\n", k.Secret)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Signing string (newline-delimited):")
	fmt.Fprintln(out, "    <METHOD>\\n<PATH>\\n<QUERY>\\n<DATE>\\n<NONCE>\\n<CONTENT-SHA256>")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Required headers on every request:")
	fmt.Fprintf(out, "    Authorization: HMAC %s:<base64(HMAC-SHA256(secret, signing-string))>\n", k.ID)
	fmt.Fprintln(out, "    X-Apigw-Date: <RFC3339 timestamp, within 5 min of server time>")
	fmt.Fprintln(out, "    X-Apigw-Nonce: <unique random hex/base64, never repeated>")
	fmt.Fprintln(out, "    X-Apigw-Content-SHA256: <hex(sha256(body))>  # or UNSIGNED-PAYLOAD")
	fmt.Fprintln(out)
}
