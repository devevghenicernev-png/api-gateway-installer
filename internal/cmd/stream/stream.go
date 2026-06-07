// Package stream owns `apigw stream …` — TCP/UDP proxy entries served
// via nginx stream{} include. Use for Redis, Postgres, MQTT brokers,
// game-server UDP, anything that isn't HTTP. The HTTP gateway and
// stream gateway share the same nginx installation but never each
// other's config blocks.
package stream

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

func NewCmdStream(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stream <command>",
		Short: "TCP/UDP proxy entries (Redis, Postgres, MQTT, game servers, ...)",
	}
	cmd.AddCommand(newAdd(f))
	cmd.AddCommand(newList(f))
	cmd.AddCommand(newRemove(f))
	return cmd
}

func newAdd(f *cmdutil.Factory) *cobra.Command {
	var protocol, target, timeout string
	var port int
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a TCP or UDP proxy",
		Args:  cobra.ExactArgs(1),
		Example: `  $ sudo apigw stream add redis --port 6379 --target 127.0.0.1:6380
  $ sudo apigw stream add mqtt --protocol tcp --port 1883 --target broker:1883
  $ sudo apigw stream add coap --protocol udp --port 5683 --target coap.local:5683`,
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			if protocol != "tcp" && protocol != "udp" {
				return fmt.Errorf("--protocol must be tcp or udp")
			}
			if port <= 0 || port > 65535 {
				return fmt.Errorf("--port out of range")
			}
			if target == "" {
				return fmt.Errorf("--target host:port required")
			}
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			for _, s := range cfg.Streams {
				if s.Name == name {
					return fmt.Errorf("stream %q already exists", name)
				}
			}
			cfg.Streams = append(cfg.Streams, config.Stream{
				Name:         name,
				Protocol:     protocol,
				ListenPort:   port,
				Upstreams:    []config.Upstream{{Address: target}},
				ProxyTimeout: timeout,
				Enabled:      true,
			})
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "%s added %s stream %s on :%d → %s\n",
				tui.Styles.Success.Render(tui.GlyphCheck), protocol, name, port, target)
			return nil
		},
	}
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "tcp | udp")
	cmd.Flags().IntVar(&port, "port", 0, "listen port on the gateway host")
	cmd.Flags().StringVar(&target, "target", "", "upstream host:port")
	cmd.Flags().StringVar(&timeout, "timeout", "10m", "proxy_timeout for idle connections")
	return cmd
}

func newList(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured streams",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			if len(cfg.Streams) == 0 {
				fmt.Fprintln(f.IOStreams.Out, "no streams configured")
				return nil
			}
			out := f.IOStreams.Out
			fmt.Fprintf(out, "%-16s %-5s %-7s %-20s %s\n", "NAME", "PROTO", "PORT", "UPSTREAM", "STATUS")
			for _, s := range cfg.Streams {
				up := "—"
				if len(s.Upstreams) > 0 {
					up = s.Upstreams[0].Address
				}
				st := "enabled"
				if !s.Enabled {
					st = "disabled"
				}
				fmt.Fprintf(out, "%-16s %-5s %-7d %-20s %s\n", s.Name, s.Protocol, s.ListenPort, up, st)
			}
			return nil
		},
	}
}

func newRemove(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a stream by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			idx := -1
			for i, s := range cfg.Streams {
				if s.Name == args[0] {
					idx = i
					break
				}
			}
			if idx < 0 {
				return fmt.Errorf("stream %q not found", args[0])
			}
			cfg.Streams = append(cfg.Streams[:idx], cfg.Streams[idx+1:]...)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(f.IOStreams.Out, "removed stream %s\n", args[0])
			return nil
		},
	}
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
