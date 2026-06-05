// Package svcmgr abstracts the OS service supervisor so apigw can install
// long-running daemons + periodic timers on systemd (Linux), launchd (macOS),
// and OpenRC (Alpine/Void) without per-platform call-site branching.
//
// Detection happens once at startup via Active(). The Manager surface is
// intentionally high-level — each method installs a specific apigw unit —
// because the unit bodies differ too much across platforms (and even between
// the daemons themselves: webhook vs dashboard need different ReadWritePaths,
// different MemoryMax, different W+X policy for node vs go).
//
// Logical names used across impls:
//   - "apigw-webhook"   — webhook receiver + worker daemon
//   - "apigw-dashboard" — dashboard + webhook + worker (consolidated)
//   - "apigw-tls-renew" — periodic TLS renewal
//   - "apigw-deploy-<name>" — per-deploy app instance
package svcmgr

import "errors"

// Kind enumerates the supervisors apigw knows about.
type Kind string

const (
	Systemd Kind = "systemd"
	Launchd Kind = "launchd"
	OpenRC  Kind = "openrc"
	None    Kind = "none"
)

// DeploySpec describes a per-deploy unit the supervisor must install. Each
// concrete impl picks the right hardening defaults for the runtime — Go
// gets MemoryDenyWriteExecute, Node/Python don't.
type DeploySpec struct {
	Name     string
	Port     int
	User     string
	Group    string
	EnvFile  string
	WorkDir  string
	StartCmd string
	Runtime  string // "node" | "python" | "go" | "static"
}

// Status is what Status(name) returns. Detail is the raw status line from
// the supervisor — useful for surfacing in `apigw status`.
type Status struct {
	Active  bool
	Enabled bool
	Detail  string
}

// Manager is the cross-platform supervisor surface. Concrete impls are in
// systemd.go, launchd.go, openrc.go. Obtain via Active() — never construct
// an impl directly.
type Manager interface {
	Kind() Kind

	// High-level installs — one per apigw unit kind.
	InstallWebhookService(binPath, addr string) error
	UninstallWebhookService() error

	InstallDashboardService(binPath, addr, webhookAddr string) error
	UninstallDashboardService() error

	InstallTLSRenewTimer(binPath string) error
	UninstallTLSRenewTimer() error

	InstallDeployService(DeploySpec) error
	UninstallDeployService(name string) error
	WriteDeployOverride(name string, port int, envFile string) error

	// Lifecycle.
	Start(name string) error
	Stop(name string) error
	Restart(name string) error
	Reload(name string) error
	Enable(name string) error
	Disable(name string) error

	IsActive(name string) bool
	Status(name string) Status

	// RunTimer runs a timer's target immediately, regardless of schedule.
	RunTimer(name string) error

	// DaemonReload tells the supervisor to re-read unit files. No-op on
	// launchd (per-plist load handles this) and OpenRC.
	DaemonReload() error
}

// ErrUnsupported is returned by no-op managers when the host has no
// supervisor we can drive, or when a specific operation isn't implemented
// on this platform.
var ErrUnsupported = errors.New("svcmgr: operation unsupported on this platform")
