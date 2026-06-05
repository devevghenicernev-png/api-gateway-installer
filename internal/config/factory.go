package config

import (
	"fmt"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
)

// FromFactory loads the config via Factory.Config() and asserts the returned
// cmdutil.Config back to the concrete *Config so commands can call typed
// helpers (FindAPI, AddAPI, etc.) without re-exposing them on the interface.
//
// The interface in cmdutil exists only to break the import cycle; in practice
// every command uses this helper.
func FromFactory(f *cmdutil.Factory) (*Config, error) {
	c, err := f.Config()
	if err != nil {
		return nil, err
	}
	cfg, ok := c.(*Config)
	if !ok {
		return nil, fmt.Errorf("config: unexpected type %T", c)
	}
	return cfg, nil
}
