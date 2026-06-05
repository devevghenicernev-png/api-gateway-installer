package tui

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/devevghenicernev-png/apigw/internal/iostreams"
)

// HuhPrompter is the production cmdutil.Prompter, backed by charmbracelet/huh.
// It auto-falls-back to line-based prompts when stdin is not a TTY.
type HuhPrompter struct{ ios *iostreams.IOStreams }

// NewHuhPrompter constructs a prompter wired to the given IOStreams.
func NewHuhPrompter(ios *iostreams.IOStreams) cmdutil.Prompter {
	return &HuhPrompter{ios: ios}
}

func (p *HuhPrompter) interactive() bool {
	return p.ios.IsStdinTTY() && p.ios.IsStdoutTTY()
}

func (p *HuhPrompter) Input(label, help string, validate func(string) error) (string, error) {
	if !p.interactive() {
		return p.readLineFallback(label)
	}
	var v string
	form := huh.NewInput().Title(label).Description(help).Value(&v)
	if validate != nil {
		form = form.Validate(validate)
	}
	if err := huh.NewForm(huh.NewGroup(form)).WithTheme(huh.ThemeCharm()).Run(); err != nil {
		return "", err
	}
	return v, nil
}

func (p *HuhPrompter) Select(label, help string, options []string) (string, error) {
	if !p.interactive() {
		return p.readLineFallback(label + " (" + strings.Join(options, "|") + ")")
	}
	var v string
	opts := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		opts = append(opts, huh.NewOption(o, o))
	}
	form := huh.NewSelect[string]().
		Title(label).
		Description(help).
		Options(opts...).
		Value(&v)
	if err := huh.NewForm(huh.NewGroup(form)).WithTheme(huh.ThemeCharm()).Run(); err != nil {
		return "", err
	}
	return v, nil
}

func (p *HuhPrompter) Confirm(label, help string, def bool) (bool, error) {
	if !p.interactive() {
		ans, err := p.readLineFallback(label + " [y/N]")
		if err != nil {
			return def, err
		}
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans == "" {
			return def, nil
		}
		return ans == "y" || ans == "yes", nil
	}
	v := def
	form := huh.NewConfirm().Title(label).Description(help).Value(&v)
	if err := huh.NewForm(huh.NewGroup(form)).WithTheme(huh.ThemeCharm()).Run(); err != nil {
		return def, err
	}
	return v, nil
}

func (p *HuhPrompter) TypeToConfirm(label, expected string) error {
	got, err := p.Input(label, fmt.Sprintf("type %q to confirm", expected), nil)
	if err != nil {
		return err
	}
	if got != expected {
		return fmt.Errorf("confirmation mismatch: expected %q, got %q", expected, got)
	}
	return nil
}

// readLineFallback is the headless path. Used when stdin is not a TTY (CI,
// piped input). Reads one line and returns it verbatim.
func (p *HuhPrompter) readLineFallback(label string) (string, error) {
	fmt.Fprintf(p.ios.ErrOut, "? %s: ", label)
	r := bufio.NewReader(p.ios.In)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
