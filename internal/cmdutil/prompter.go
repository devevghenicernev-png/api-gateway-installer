package cmdutil

// Prompter is the minimal interactive-prompt interface used by commands.
//
// The production implementation wraps charmbracelet/huh; tests inject a fake
// that returns canned answers. Defining the interface here (not in tui) keeps
// the cmdutil package free of bubbletea — important for `apigw version` and
// other commands that must work without a TTY.
type Prompter interface {
	// Input asks for a single string, returning what the user typed.
	// validate is called on submit; non-nil error keeps the form open.
	Input(label, help string, validate func(string) error) (string, error)

	// Select asks the user to pick one of options. Returns the chosen option.
	Select(label, help string, options []string) (string, error)

	// Confirm shows a [y/N] prompt; default determines the default answer.
	Confirm(label, help string, def bool) (bool, error)

	// TypeToConfirm makes the user retype `expected` exactly. Used for
	// destructive commands per the design system rule "type-the-name confirms".
	TypeToConfirm(label, expected string) error
}
