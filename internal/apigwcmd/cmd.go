// Package apigwcmd is the binary's entrypoint. It wires up the Factory and
// maps errors from RunE to stable exit codes. Keep this file small — all
// real logic lives in subcommands under internal/cmd/.
package apigwcmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	charmlog "github.com/charmbracelet/log"
	"github.com/devevghenicernev-png/apigw/internal/build"
	"github.com/devevghenicernev-png/apigw/internal/cmd/root"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/iostreams"
	"github.com/devevghenicernev-png/apigw/internal/selfupdate"
	"github.com/devevghenicernev-png/apigw/internal/shim"
	"github.com/devevghenicernev-png/apigw/internal/tui"
	"github.com/spf13/afero"
)

// ExitCode is the integer apigw returns to the shell. Documented and stable.
type ExitCode int

const (
	ExitOK         ExitCode = 0
	ExitError      ExitCode = 1
	ExitUsage      ExitCode = 2
	ExitConfig     ExitCode = 3
	ExitNetwork    ExitCode = 4
	ExitPermission ExitCode = 5
	ExitSIGINT     ExitCode = 130
)

// Main is the real entrypoint. Returns the exit code to pass to os.Exit.
func Main() ExitCode {
	ios := iostreams.System()
	logger := newLogger(ios)

	// Legacy CLI compatibility: when invoked via the api-manage symlink,
	// rewrite os.Args into cobra's shape and emit a one-line deprecation
	// banner. The translation table lives in internal/shim.
	if len(os.Args) > 0 && shim.IsShim(os.Args[0]) {
		shim.PrintDeprecation(ios.ErrOut)
		os.Args = shim.Translate(os.Args)
	}

	// Daily update check — kicked off non-blocking; banner prints AFTER
	// the user's command output via the deferred Banner() call below, so
	// it never delays the user's command. APIGW_NO_UPDATE_CHECK=1 turns
	// it off; dev/unstamped builds skip implicitly.
	updateCheck := selfupdate.NewBackgroundCheck(selfupdate.DefaultRepo)
	defer func() {
		if ios.IsStderrTTY() {
			_ = updateCheck.Banner(ios.ErrOut)
		}
	}()

	// SIGINT/SIGTERM context — propagates cancellation through to RunE.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f := &cmdutil.Factory{
		AppVersion: build.Version,
		Commit:     build.Commit,
		BuildDate:  build.Date,
		IOStreams:  ios,
		Logger:     logger,
		Prompter:   tui.NewHuhPrompter(ios),
		FS:         afero.NewOsFs(),
	}
	f.RebuildLogger = func(level slog.Level) *slog.Logger { return newLoggerAt(ios, level) }
	f.Config = lazyConfigFor(f)

	cmd := root.NewCmdRoot(f)
	cmd.SetContext(ctx)
	cmd.SetIn(ios.In)
	cmd.SetOut(ios.Out)
	cmd.SetErr(ios.ErrOut)
	cmd.SilenceErrors = true // we render errors ourselves
	cmd.SilenceUsage = true

	err := cmd.ExecuteContext(ctx)
	return mapError(err, ios)
}

// mapError turns a RunE error into the right exit code, rendering it nicely
// on the way out. The three behavioural error types from cmdutil are
// recognized and short-circuit normal rendering.
func mapError(err error, ios *iostreams.IOStreams) ExitCode {
	if err == nil {
		return ExitOK
	}

	// Context cancelled (Ctrl-C)
	if errors.Is(err, context.Canceled) {
		return ExitSIGINT
	}
	if errors.Is(err, cmdutil.CancelError) {
		return ExitSIGINT
	}

	// Already-printed silent errors
	if errors.Is(err, cmdutil.SilentError) {
		return ExitError
	}

	// FlagError: print the message, exit 2. Cobra has already printed usage
	// if SilenceUsage was false; we keep it true to avoid noise, and append
	// a "Run apigw --help" hint instead.
	if cmdutil.IsFlagError(err) {
		fmt.Fprintf(ios.ErrOut, "%s %s\n",
			tui.Styles.Danger.Render(tui.GlyphCross),
			err.Error())
		fmt.Fprintln(ios.ErrOut, tui.Styles.Muted.Render("  run `apigw --help` for usage"))
		return ExitUsage
	}

	if cmdutil.IsConfigError(err) {
		renderPlain(ios, err)
		return ExitConfig
	}
	if cmdutil.IsNetworkError(err) {
		renderPlain(ios, err)
		return ExitNetwork
	}
	if cmdutil.IsPermissionError(err) {
		var pe *cmdutil.PermissionError
		errors.As(err, &pe)
		renderPlain(ios, err)
		if pe.Hint != "" {
			fmt.Fprintln(ios.ErrOut, "  "+tui.Styles.Muted.Render(pe.Hint))
		}
		return ExitPermission
	}

	// Rich errors render themselves.
	var rerr *tui.RichError
	if errors.As(err, &rerr) {
		rerr.Render(ios.ErrOut, ios)
		return ExitError
	}

	// Default: one-line lowercase summary.
	renderPlain(ios, err)
	return ExitError
}

func renderPlain(ios *iostreams.IOStreams, err error) {
	fmt.Fprintf(ios.ErrOut, "%s %s\n",
		tui.Styles.Danger.Render(tui.GlyphCross),
		err.Error())
}

// newLogger constructs the startup logger before flags are parsed. Defaults
// to Warn so early-life messages don't spam first-time users; root's
// PersistentPreRunE upgrades the level via Factory.RebuildLogger once it
// sees --verbose / --debug.
func newLogger(ios *iostreams.IOStreams) *slog.Logger {
	return newLoggerAt(ios, slog.LevelWarn)
}

// newLoggerAt picks a slog handler based on TTY-ness and the explicit level.
// Piped → JSON (so journald gets structured logs); TTY → charmbracelet/log
// with the same level.
func newLoggerAt(ios *iostreams.IOStreams, level slog.Level) *slog.Logger {
	if !ios.IsStderrTTY() {
		return slog.New(slog.NewJSONHandler(ios.ErrOut, &slog.HandlerOptions{Level: level}))
	}
	clevel := charmlog.InfoLevel
	switch {
	case level <= slog.LevelDebug:
		clevel = charmlog.DebugLevel
	case level <= slog.LevelInfo:
		clevel = charmlog.InfoLevel
	case level <= slog.LevelWarn:
		clevel = charmlog.WarnLevel
	default:
		clevel = charmlog.ErrorLevel
	}
	h := charmlog.NewWithOptions(ios.ErrOut, charmlog.Options{
		ReportTimestamp: false,
		Level:           clevel,
	})
	return slog.New(h)
}

// lazyConfigFor honours Factory.ConfigPath (set by --config). Memoised so
// repeated Config() calls don't double-read the file — but the memo is
// keyed on the path so changing --config between sub-invocations works
// (relevant only in tests that reuse a Factory).
func lazyConfigFor(f *cmdutil.Factory) func() (cmdutil.Config, error) {
	var cfg cmdutil.Config
	var err error
	loadedPath := ""
	return func() (cmdutil.Config, error) {
		if cfg != nil && loadedPath == f.ConfigPath {
			return cfg, err
		}
		loadedPath = f.ConfigPath
		cfg, err = config.LoadFrom(f.ConfigPath)
		return cfg, err
	}
}
