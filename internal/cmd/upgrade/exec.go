package upgrade

import "os/exec"

// lookPath isolates the one os/exec call this package makes, keeping the
// main file focused on UI. Tests can override via build tags if we ever
// add a test suite that simulates cosign-missing environments.
func lookPath(name string) (string, error) { return exec.LookPath(name) }
