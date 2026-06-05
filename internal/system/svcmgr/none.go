package svcmgr

// noneManager is the no-op fallback when Detect() finds neither systemd,
// launchd, nor OpenRC. Read-only methods report safe defaults; mutating
// methods return ErrUnsupported so commands like `apigw install` fail loudly
// with an actionable error.
type noneManager struct{}

func (noneManager) Kind() Kind { return None }

func (noneManager) InstallWebhookService(_, _ string) error    { return ErrUnsupported }
func (noneManager) UninstallWebhookService() error             { return ErrUnsupported }
func (noneManager) InstallDashboardService(_, _, _ string) error { return ErrUnsupported }
func (noneManager) UninstallDashboardService() error           { return ErrUnsupported }
func (noneManager) InstallTLSRenewTimer(_ string) error        { return ErrUnsupported }
func (noneManager) UninstallTLSRenewTimer() error              { return ErrUnsupported }
func (noneManager) InstallDeployService(DeploySpec) error      { return ErrUnsupported }
func (noneManager) UninstallDeployService(string) error        { return ErrUnsupported }
func (noneManager) WriteDeployOverride(string, int, string) error { return ErrUnsupported }

func (noneManager) Start(string) error   { return ErrUnsupported }
func (noneManager) Stop(string) error    { return ErrUnsupported }
func (noneManager) Restart(string) error { return ErrUnsupported }
func (noneManager) Reload(string) error  { return ErrUnsupported }
func (noneManager) Enable(string) error  { return ErrUnsupported }
func (noneManager) Disable(string) error { return ErrUnsupported }

func (noneManager) IsActive(string) bool { return false }
func (noneManager) Status(string) Status { return Status{Detail: "no service supervisor detected"} }

func (noneManager) RunTimer(string) error { return ErrUnsupported }
func (noneManager) DaemonReload() error   { return ErrUnsupported }
