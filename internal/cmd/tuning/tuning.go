// Package tuning owns `apigw tuning …` — surface for nginx main-context
// directives (worker_processes, worker_cpu_affinity, worker_rlimit_nofile)
// that can't live inside conf.d.
package tuning

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	tuningpkg "github.com/devevghenicernev-png/apigw/internal/tuning"
)

func NewCmdTuning(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tuning <command>",
		Short: "nginx main-context tuning (worker_processes, cpu_affinity, rlimit_nofile)",
		Long: "These directives live above http{} in /etc/nginx/nginx.conf and can't be\n" +
			"included from conf.d/* (that include is inside http{}). `apigw tuning apply`\n" +
			"injects a marker-delimited block into the main config, idempotently;\n" +
			"`apigw tuning revert` strips it. Backup at nginx.conf.apigw-prev.",
	}
	cmd.AddCommand(newShow(f))
	cmd.AddCommand(newApply(f))
	cmd.AddCommand(newRevert(f))
	return cmd
}

func newShow(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the block that would be written + what's currently live",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceUsage = true
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			spec := specFromConfig(cfg)
			out := f.IOStreams.Out
			fmt.Fprintln(out, "configured (would write):")
			if spec.Empty() {
				fmt.Fprintln(out, "  (none — Tuning section is empty)")
			} else {
				fmt.Fprintln(out, indent(spec.Render(), "  "))
			}
			fmt.Fprintln(out, "live (in nginx.conf):")
			live, err := tuningpkg.CurrentBlock()
			if err != nil {
				fmt.Fprintf(out, "  (read failed: %v)\n", err)
				return nil
			}
			if live == "" {
				fmt.Fprintln(out, "  (no apigw tuning block present)")
			} else {
				fmt.Fprintln(out, indent(live, "  "))
			}
			return nil
		},
	}
}

func newApply(f *cmdutil.Factory) *cobra.Command {
	var reload bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Write the tuning block into nginx.conf and (optionally) reload",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceUsage = true
			cfg, err := loadCfg(f)
			if err != nil {
				return err
			}
			spec := specFromConfig(cfg)
			changed, err := tuningpkg.Apply(spec)
			if err != nil {
				return err
			}
			out := f.IOStreams.Out
			if !changed {
				fmt.Fprintln(out, "ok — nothing to change")
				return nil
			}
			// Validate the rendered file BEFORE we tell the operator "ok".
			// Without this, `apigw tuning apply` printed `ok` and then
			// `nginx -s reload` failed on a duplicate worker_processes —
			// leaving a broken config on disk that the next `systemctl
			// restart nginx` would refuse (L-2 in DOCKER_TEST_REPORT.md).
			if vErr := nginxValidate(); vErr != nil {
				if rErr := tuningpkg.RestoreBackup(); rErr != nil {
					return fmt.Errorf("nginx -t failed (%w); restore also failed: %v — fix nginx.conf by hand from %s.apigw-prev", vErr, rErr, "/etc/nginx/nginx.conf")
				}
				return fmt.Errorf("nginx -t rejected the rendered config — rolled back to nginx.conf.apigw-prev: %w", vErr)
			}
			fmt.Fprintln(out, "ok — nginx.conf updated")
			if reload {
				if err := nginxReload(); err != nil {
					// Reload failed AFTER a successful validate is rare
					// (PID file missing, permission). Restore so we don't
					// leave the operator with an unreviewed delta.
					_ = tuningpkg.RestoreBackup()
					return fmt.Errorf("nginx reload: %w (changes rolled back)", err)
				}
				fmt.Fprintln(out, "  nginx reloaded")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reload, "reload", false, "run `nginx -s reload` after the write")
	return cmd
}

func newRevert(f *cmdutil.Factory) *cobra.Command {
	var reload bool
	cmd := &cobra.Command{
		Use:   "revert",
		Short: "Strip the apigw tuning block from nginx.conf",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			c.SilenceUsage = true
			changed, err := tuningpkg.Revert()
			if err != nil {
				return err
			}
			out := f.IOStreams.Out
			if !changed {
				fmt.Fprintln(out, "ok — nothing to revert")
				return nil
			}
			fmt.Fprintln(out, "ok — block removed from nginx.conf")
			if reload {
				if err := nginxReload(); err != nil {
					return fmt.Errorf("nginx reload: %w", err)
				}
				fmt.Fprintln(out, "  nginx reloaded")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reload, "reload", false, "run `nginx -s reload` after the strip")
	return cmd
}

func specFromConfig(cfg *config.Config) tuningpkg.Spec {
	return tuningpkg.Spec{
		WorkerProcesses:    cfg.Tuning.WorkerProcesses,
		CPUAffinity:        cfg.Tuning.CPUAffinity,
		WorkerRLimitNofile: cfg.Tuning.WorkerRLimitNofile,
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

// indent re-indents each non-empty line of `s` with `prefix`.
func indent(s, prefix string) string {
	out := ""
	for i, line := range splitLines(s) {
		if line == "" && i == len(splitLines(s))-1 {
			out += "\n"
			continue
		}
		if line == "" {
			out += "\n"
			continue
		}
		out += prefix + line + "\n"
	}
	return out
}

func splitLines(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// nginxReload mirrors nginx.Manager's defaultReload but in this CLI we
// don't want to drag the whole package — we just need the reload call.
func nginxReload() error {
	if err := exec.Command("nginx", "-s", "reload").Run(); err == nil {
		return nil
	}
	return exec.Command("systemctl", "reload", "nginx").Run()
}

// nginxValidate runs `nginx -t` and returns the combined output as the
// error message when it exits non-zero (so the operator can see WHY
// validation failed without re-running by hand).
func nginxValidate() error {
	cmd := exec.Command("nginx", "-t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
