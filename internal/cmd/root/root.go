// Package root builds the top-level `apigw` cobra command and assembles the
// subcommand tree. Subcommands live under internal/cmd/<noun>/ — this file is
// just the registry.
package root

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	aicmd "github.com/devevghenicernev-png/apigw/internal/cmd/ai"
	apicmd "github.com/devevghenicernev-png/apigw/internal/cmd/api"
	auditcmd "github.com/devevghenicernev-png/apigw/internal/cmd/audit"
	authcmd "github.com/devevghenicernev-png/apigw/internal/cmd/auth"
	backupcmd "github.com/devevghenicernev-png/apigw/internal/cmd/backup"
	completioncmd "github.com/devevghenicernev-png/apigw/internal/cmd/completion"
	dashboardcmd "github.com/devevghenicernev-png/apigw/internal/cmd/dashboard"
	deploycmd "github.com/devevghenicernev-png/apigw/internal/cmd/deploy"
	doctorcmd "github.com/devevghenicernev-png/apigw/internal/cmd/doctor"
	gendocscmd "github.com/devevghenicernev-png/apigw/internal/cmd/gendocs"
	installcmd "github.com/devevghenicernev-png/apigw/internal/cmd/install"
	logscmd "github.com/devevghenicernev-png/apigw/internal/cmd/logs"
	migratecmd "github.com/devevghenicernev-png/apigw/internal/cmd/migrate"
	restorecmd "github.com/devevghenicernev-png/apigw/internal/cmd/restore"
	statuscmd "github.com/devevghenicernev-png/apigw/internal/cmd/status"
	streamcmd "github.com/devevghenicernev-png/apigw/internal/cmd/stream"
	tlscmd "github.com/devevghenicernev-png/apigw/internal/cmd/tls"
	uninstallcmd "github.com/devevghenicernev-png/apigw/internal/cmd/uninstall"
	upgradecmd "github.com/devevghenicernev-png/apigw/internal/cmd/upgrade"
	versioncmd "github.com/devevghenicernev-png/apigw/internal/cmd/version"
	webhookcmd "github.com/devevghenicernev-png/apigw/internal/cmd/webhook"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/tui"
)

// NewCmdRoot returns the root command, ready to ExecuteContext.
//
// Mirror of cli/cli/pkg/cmd/root.NewCmdRoot — every binary-wide concern is
// configured here: groups, persistent flags, the help template, and the
// "did you mean?" distance.
func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apigw <command> <subcommand> [flags]",
		Short: "API Gateway installer and operator",
		Long: tui.Styles.Heading.Render("apigw") + " — install, manage, and observe an nginx-fronted API gateway.\n\n" +
			"Single binary. Interactive when run by a human, scriptable with --json when run by CI.\n" +
			"HTTPS, deployments, webhooks, and a live dashboard all in one tool.",
		Example: heredoc(`
			$ apigw install                              # interactive install wizard
			$ apigw api add hello --port 8080            # register an upstream
			$ apigw api list --json                      # machine-readable listing
			$ apigw tls enable letsencrypt --domain api.example.com --email me@example.com
			$ apigw deploy logs api.example.com --follow
		`),
		Version:       buildVersionString(f),
		SilenceErrors: true,
		SilenceUsage:  true,

		// Don't run anything when called bare — print help. Cobra does this
		// when RunE is nil; we explicitly nil it for clarity.
		RunE: nil,
	}

	// Persistent flags — every subcommand inherits these.
	cmd.PersistentFlags().BoolP("quiet", "q", false, "suppress non-essential output")
	cmd.PersistentFlags().CountP("verbose", "v", "increase verbosity (-v info, -vv debug, -vvv trace)")
	cmd.PersistentFlags().Bool("debug", false, "alias for -vv")
	cmd.PersistentFlags().Bool("no-color", false, "disable colored output (also honors NO_COLOR env var)")
	cmd.PersistentFlags().Bool("json", false, "emit JSON output")
	cmd.PersistentFlags().String("config", "", "path to config file (overrides search)")
	cmd.PersistentFlags().Bool("yes", false, "bypass confirmation prompts")
	cmd.PersistentFlags().Bool("dry-run", false, "show the plan and exit; touch nothing")

	// Honor --quiet / --verbose / --debug / --no-color / --config.
	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		q, _ := c.Flags().GetBool("quiet")
		v, _ := c.Flags().GetCount("verbose")
		debug, _ := c.Flags().GetBool("debug")
		noColor, _ := c.Flags().GetBool("no-color")
		cfgPath, _ := c.Flags().GetString("config")
		if debug && v < 2 {
			v = 2
		}
		f.IOStreams.SetQuiet(q)
		f.IOStreams.SetVerbose(v)
		if noColor {
			f.IOStreams.SetColorForTest(false)
		}
		// Push the decision into lipgloss too — lipgloss has its own profile
		// detection that ignores our --no-color flag otherwise.
		if !f.IOStreams.ColorEnabled() {
			lipgloss.SetColorProfile(termenv.Ascii)
		}

		// Map verbose count → slog level and REPLACE the logger handler
		// so every command sees the new level. Previously the handler was
		// frozen at LevelInfo regardless of -v / -vv.
		var level slog.Level
		switch v {
		case 0:
			level = slog.LevelWarn
		case 1:
			level = slog.LevelInfo
		default:
			level = slog.LevelDebug
		}
		f.LogLevel = level
		if f.RebuildLogger != nil {
			f.Logger = f.RebuildLogger(level)
		}

		// --config picks an explicit config path; rebuild the lazy loader.
		if cfgPath != "" && f.ConfigPath != cfgPath {
			f.ConfigPath = cfgPath
		}
		return nil
	}

	// Did-you-mean: distance 2 catches typos like `apigw aip add` → `apigw api add`.
	cmd.SuggestionsMinimumDistance = 2

	// Groups (visual; Cobra renders these in help output).
	cmd.AddGroup(
		&cobra.Group{ID: "core", Title: "Core:"},
		&cobra.Group{ID: "manage", Title: "Manage:"},
		&cobra.Group{ID: "diagnostics", Title: "Diagnostics:"},
		&cobra.Group{ID: "system", Title: "System:"},
	)

	// Phase 0 + Phase 1 subcommands. Phases 2+ register their own here later.
	installCmd := installcmd.NewCmdInstall(f)
	installCmd.GroupID = "core"
	cmd.AddCommand(installCmd)

	uninstallCmd := uninstallcmd.NewCmdUninstall(f)
	uninstallCmd.GroupID = "core"
	cmd.AddCommand(uninstallCmd)

	apiCmd := apicmd.NewCmdAPI(f)
	apiCmd.GroupID = "manage"
	cmd.AddCommand(apiCmd)

	streamCmd := streamcmd.NewCmdStream(f)
	streamCmd.GroupID = "manage"
	cmd.AddCommand(streamCmd)

	tlsCmd := tlscmd.NewCmdTLS(f)
	tlsCmd.GroupID = "manage"
	cmd.AddCommand(tlsCmd)

	deployCmd := deploycmd.NewCmdDeploy(f)
	deployCmd.GroupID = "manage"
	cmd.AddCommand(deployCmd)

	webhookCmd := webhookcmd.NewCmdWebhook(f)
	webhookCmd.GroupID = "manage"
	cmd.AddCommand(webhookCmd)

	dashboardCmd := dashboardcmd.NewCmdDashboard(f)
	dashboardCmd.GroupID = "manage"
	cmd.AddCommand(dashboardCmd)

	aiCmd := aicmd.NewCmdAI(f)
	aiCmd.GroupID = "manage"
	cmd.AddCommand(aiCmd)

	authCmd := authcmd.NewCmdAuth(f)
	authCmd.GroupID = "manage"
	cmd.AddCommand(authCmd)

	statusCmd := statuscmd.NewCmdStatus(f)
	statusCmd.GroupID = "diagnostics"
	cmd.AddCommand(statusCmd)

	doctorCmd := doctorcmd.NewCmdDoctor(f)
	doctorCmd.GroupID = "diagnostics"
	cmd.AddCommand(doctorCmd)

	auditCmd := auditcmd.NewCmdAudit(f)
	auditCmd.GroupID = "diagnostics"
	cmd.AddCommand(auditCmd)

	logsCmd := logscmd.NewCmdLogs(f)
	logsCmd.GroupID = "diagnostics"
	cmd.AddCommand(logsCmd)

	backupCmd := backupcmd.NewCmdBackup(f)
	backupCmd.GroupID = "system"
	cmd.AddCommand(backupCmd)

	restoreCmd := restorecmd.NewCmdRestore(f)
	restoreCmd.GroupID = "system"
	cmd.AddCommand(restoreCmd)

	migrateCmd := migratecmd.NewCmdMigrate(f)
	migrateCmd.GroupID = "system"
	cmd.AddCommand(migrateCmd)

	upgradeCmd := upgradecmd.NewCmdUpgrade(f)
	upgradeCmd.GroupID = "system"
	cmd.AddCommand(upgradeCmd)

	completionCmd := completioncmd.NewCmdCompletion(f)
	completionCmd.GroupID = "system"
	cmd.AddCommand(completionCmd)

	// gen-docs is hidden — author tooling, not for end users.
	cmd.AddCommand(gendocscmd.NewCmdGenDocs(f))

	versionCmd := versioncmd.NewCmdVersion(f)
	versionCmd.GroupID = "system"
	cmd.AddCommand(versionCmd)

	cmd.SetHelpTemplate(helpTemplate)
	cmd.SetUsageTemplate(usageTemplate)

	return cmd
}

func buildVersionString(f *cmdutil.Factory) string {
	if f.Commit == "none" || f.Commit == "" {
		return f.AppVersion
	}
	return fmt.Sprintf("%s (%s, %s)", f.AppVersion, f.Commit, f.BuildDate)
}

// heredoc trims leading whitespace from a multi-line example block so it can
// be written naturally in source. Mirrors gh's MarkdownDocumentation.
func heredoc(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	min := -1
	for _, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if t == "" {
			continue
		}
		n := len(l) - len(t)
		if min == -1 || n < min {
			min = n
		}
	}
	if min <= 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= min {
			out[i] = l[min:]
		} else {
			out[i] = l
		}
	}
	return strings.Join(out, "\n")
}

// helpTemplate puts Examples directly under the short description, before
// flags — the design system rule "help leads with examples". Copied from gh's
// template and minimally adjusted.
const helpTemplate = `{{if or .Long .Short}}{{.Long | trimTrailingWhitespaces}}{{if not .Long}}{{.Short}}{{end}}

{{end}}{{if .Example}}Examples:
{{.Example | trimTrailingWhitespaces}}

{{end}}{{if .HasAvailableSubCommands}}{{range $group := .Groups}}{{$group.Title}}
{{range $.Commands}}{{if (eq .GroupID $group.ID)}}  {{rpad .Name .NamePadding}} {{.Short}}
{{end}}{{end}}
{{end}}{{$cmds := .Commands}}{{$cmdGroups := .Groups}}{{$ungrouped := false}}{{range $cmds}}{{if (and (eq .GroupID "") .IsAvailableCommand)}}{{if not $ungrouped}}Additional Commands:
{{$ungrouped = true}}{{end}}  {{rpad .Name .NamePadding}} {{.Short}}
{{end}}{{end}}
{{end}}{{if .HasAvailableLocalFlags}}Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}

{{end}}{{if .HasAvailableInheritedFlags}}Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}

{{end}}{{if .HasHelpSubCommands}}Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}

{{end}}{{if .HasAvailableSubCommands}}Use "{{.CommandPath}} [command] --help" for more information about a command.
{{end}}`

const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}
`
