// Package cmdutil holds shared command plumbing: the Factory DI container,
// error types with behaviour (mapped to exit codes by apigwcmd.Main), and
// flag helpers. Mirror of cli/cli/pkg/cmdutil.
package cmdutil

import (
	"errors"
	"fmt"
)

// FlagError is returned when the user typed something Cobra couldn't accept
// (bad flag, missing arg, unknown subcommand). Main() maps it to exit 2 and
// prints the command's usage.
type FlagError struct{ err error }

func (e *FlagError) Error() string { return e.err.Error() }
func (e *FlagError) Unwrap() error { return e.err }

// FlagErrorf is the constructor analogue of fmt.Errorf for FlagError.
func FlagErrorf(format string, a ...any) error {
	return &FlagError{fmt.Errorf(format, a...)}
}

// IsFlagError reports whether err (or any wrapped error) is a *FlagError.
func IsFlagError(err error) bool {
	var fe *FlagError
	return errors.As(err, &fe)
}

// SilentError means "the command failed but I already printed a nice error".
// Main() exits 1 without printing anything more.
var SilentError = errors.New("SilentError")

// CancelError means the user cancelled (Ctrl-C / Esc on a wizard).
// Main() exits 2 — same code as a flag error — without an error message.
var CancelError = errors.New("CancelError")

// ConfigError indicates a problem loading or parsing /etc/apigw/config.yaml
// or its XDG equivalent. Maps to exit 3.
type ConfigError struct{ err error }

func (e *ConfigError) Error() string { return fmt.Sprintf("config: %s", e.err) }
func (e *ConfigError) Unwrap() error { return e.err }

func NewConfigError(err error) error { return &ConfigError{err} }

// IsConfigError reports whether err is a *ConfigError.
func IsConfigError(err error) bool {
	var ce *ConfigError
	return errors.As(err, &ce)
}

// NetworkError indicates a failure talking to a remote (ACME server, GitHub
// webhook, package mirror, etc.). Maps to exit 4.
type NetworkError struct{ err error }

func (e *NetworkError) Error() string { return e.err.Error() }
func (e *NetworkError) Unwrap() error { return e.err }

func NewNetworkError(err error) error { return &NetworkError{err} }

func IsNetworkError(err error) bool {
	var ne *NetworkError
	return errors.As(err, &ne)
}

// PermissionError indicates the user needs sudo or group apigw membership.
// Maps to exit 5.
type PermissionError struct {
	err  error
	Hint string // e.g. "run with sudo" — printed on a second line
}

func (e *PermissionError) Error() string { return e.err.Error() }
func (e *PermissionError) Unwrap() error { return e.err }

func NewPermissionError(err error, hint string) error {
	return &PermissionError{err: err, Hint: hint}
}

func IsPermissionError(err error) bool {
	var pe *PermissionError
	return errors.As(err, &pe)
}
