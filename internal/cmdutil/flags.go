package cmdutil

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
)

// StringEnumFlag enforces a fixed set of values for a string flag.
//
// Use over a plain StringVar when the flag must be one of a known set —
// e.g. --runtime auto|node|python|go|docker, --tls-strategy letsencrypt|duckdns|self-signed.
type StringEnumFlag struct {
	value   *string
	options []string
}

// NewStringEnumFlag returns a flag.Value that accepts only `options`.
// The pointed-to string receives the validated value on Set.
func NewStringEnumFlag(value *string, options []string, defaultValue string) *StringEnumFlag {
	*value = defaultValue
	return &StringEnumFlag{value: value, options: options}
}

func (s *StringEnumFlag) String() string { return *s.value }

func (s *StringEnumFlag) Set(v string) error {
	for _, o := range s.options {
		if v == o {
			*s.value = v
			return nil
		}
	}
	return FlagErrorf("invalid value %q for flag (allowed: %s)", v, strings.Join(s.options, ", "))
}

func (s *StringEnumFlag) Type() string { return "string" }

// Options exposes the allowed values for help / completion.
func (s *StringEnumFlag) Options() []string { return s.options }

// VarStringEnum is sugar that adds a StringEnumFlag to a pflag.FlagSet.
func VarStringEnum(fs *pflag.FlagSet, value *string, name, short, def, usage string, options []string) {
	f := NewStringEnumFlag(value, options, def)
	fs.VarP(f, name, short, fmt.Sprintf("%s (one of: %s)", usage, strings.Join(options, ", ")))
}
