package config

import (
	"fmt"
	"time"
)

// FindDeploy returns a pointer to the named deploy entry, or nil if absent.
func (c *Config) FindDeploy(name string) *Deploy {
	for i := range c.Deploys {
		if c.Deploys[i].Name == name {
			return &c.Deploys[i]
		}
	}
	return nil
}

// AddDeploy registers a new deploy; returns an error if a duplicate name exists.
func (c *Config) AddDeploy(d Deploy) error {
	if c.FindDeploy(d.Name) != nil {
		return fmt.Errorf("deploy %q already exists", d.Name)
	}
	c.Deploys = append(c.Deploys, d)
	return nil
}

// RemoveDeploy deletes the named deploy.
func (c *Config) RemoveDeploy(name string) error {
	for i := range c.Deploys {
		if c.Deploys[i].Name == name {
			c.Deploys = append(c.Deploys[:i], c.Deploys[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("deploy %q not found", name)
}

// SetDeployStatus updates the runtime metadata (LastSHA, LastDeploy,
// LastStatus, LastError) for a deploy. Returns the modified deploy by value.
//
// Caller is responsible for calling Save() after — this just mutates in memory.
func (c *Config) SetDeployStatus(name, sha, status, errMsg string) error {
	d := c.FindDeploy(name)
	if d == nil {
		return fmt.Errorf("deploy %q not found", name)
	}
	if sha != "" {
		d.LastSHA = sha
	}
	d.LastStatus = status
	d.LastError = errMsg
	d.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	return nil
}
