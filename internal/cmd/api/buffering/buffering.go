// Package buffering owns `apigw api buffering …` — per-route control
// of nginx's request + response buffering. Streaming uploads (large
// tarballs, log shipping) need request buffering off. SSE / chunked /
// long-poll responses need response buffering off.
package buffering

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
)

func NewCmdBuffering(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "buffering <command>",
		Short: "Per-route request/response buffering tuning",
		Example: `  $ apigw api buffering set uploads --request off          # streaming uploads
  $ apigw api buffering set sse --response off              # streaming responses
  $ apigw api buffering set api --client-body 16k --proxy-buffer 64k --proxy-buffers "8 16k"
  $ apigw api buffering show uploads
  $ apigw api buffering clear uploads`,
	}
	cmd.AddCommand(newSet(f))
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newClear(f))
	return cmd
}

func newSet(f *cmdutil.Factory) *cobra.Command {
	var req, resp string
	var cbs, pbs, pbufs string
	cmd := &cobra.Command{
		Use:   "set <api>",
		Short: "Set buffering overrides on an API",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			b := &config.Buffering{
				ClientBodyBufferSize: cbs,
				ProxyBufferSize:      pbs,
				ProxyBuffers:         pbufs,
			}
			reqBool, err := parseOnOff(req)
			if err != nil {
				return fmt.Errorf("--request: %w", err)
			}
			respBool, err := parseOnOff(resp)
			if err != nil {
				return fmt.Errorf("--response: %w", err)
			}
			b.Request = reqBool
			b.Response = respBool
			if b.Request == nil && b.Response == nil &&
				b.ClientBodyBufferSize == "" && b.ProxyBufferSize == "" && b.ProxyBuffers == "" {
				return fmt.Errorf("at least one flag must be set (use `clear` to remove)")
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			api := cfg.FindAPI(args[0])
			if api == nil {
				return fmt.Errorf("api %q not found", args[0])
			}
			api.Buffering = b
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save: %w", err)
			}
			fmt.Fprintf(f.IOStreams.Out, "buffering set on %s — run `apigw api reload` to apply.\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&req, "request", "", `proxy_request_buffering: "on" or "off" (empty = nginx default "on")`)
	cmd.Flags().StringVar(&resp, "response", "", `proxy_buffering: "on" or "off" (empty = apigw default "off")`)
	cmd.Flags().StringVar(&cbs, "client-body", "", "client_body_buffer_size (e.g. 16k, 1m)")
	cmd.Flags().StringVar(&pbs, "proxy-buffer", "", "proxy_buffer_size (first response chunk, e.g. 8k)")
	cmd.Flags().StringVar(&pbufs, "proxy-buffers", "", `proxy_buffers: "<count> <size>", e.g. "8 16k"`)
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show <api>",
		Short: "Show buffering overrides",
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
			out := f.IOStreams.Out
			if api.Buffering == nil {
				fmt.Fprintf(out, "%s: no overrides (nginx defaults; proxy_buffering off via template)\n", args[0])
				return nil
			}
			b := api.Buffering
			fmt.Fprintf(out, "%s buffering:\n", args[0])
			fmt.Fprintf(out, "  request:           %s\n", dashIfNil(b.Request))
			fmt.Fprintf(out, "  response:          %s\n", dashIfNil(b.Response))
			fmt.Fprintf(out, "  client_body size:  %s\n", dashIfEmpty(b.ClientBodyBufferSize))
			fmt.Fprintf(out, "  proxy_buffer size: %s\n", dashIfEmpty(b.ProxyBufferSize))
			fmt.Fprintf(out, "  proxy_buffers:     %s\n", dashIfEmpty(b.ProxyBuffers))
			return nil
		},
	}
}

func newClear(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "clear <api>",
		Short: "Remove buffering overrides",
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
			if api.Buffering == nil {
				fmt.Fprintln(f.IOStreams.Out, "no overrides to clear")
				return nil
			}
			api.Buffering = nil
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "buffering cleared on %s — run `apigw api reload` to apply.\n", args[0])
			return nil
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

func parseOnOff(s string) (*bool, error) {
	switch s {
	case "":
		return nil, nil
	case "on", "true", "yes":
		t := true
		return &t, nil
	case "off", "false", "no":
		f := false
		return &f, nil
	}
	return nil, fmt.Errorf("expected on/off (got %q)", s)
}

func dashIfNil(b *bool) string {
	if b == nil {
		return "-"
	}
	if *b {
		return "on"
	}
	return "off"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
