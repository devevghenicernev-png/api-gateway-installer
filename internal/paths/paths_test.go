package paths

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestEnvOverrideWins(t *testing.T) {
	t.Setenv("APIGW_CONFIG_DIR", "/tmp/test-apigw")
	if got := ConfigDir(); got != "/tmp/test-apigw" {
		t.Errorf("env override ignored; got %s", got)
	}
}

func TestDefaultsForLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("linux-only defaults check; running on %s", runtime.GOOS)
	}
	t.Setenv("APIGW_CONFIG_DIR", "")
	if got := ConfigDir(); got != LinuxConfigDir {
		t.Errorf("Linux ConfigDir mismatch: got %s want %s", got, LinuxConfigDir)
	}
}

func TestDefaultsForDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("darwin-only check; running on %s", runtime.GOOS)
	}
	_ = os.Unsetenv("APIGW_CONFIG_DIR")
	got := ConfigDir()
	if !strings.HasSuffix(got, "/etc/apigw") {
		t.Errorf("darwin ConfigDir should end with /etc/apigw; got %s", got)
	}
	if !strings.HasPrefix(got, DarwinPrefix()) {
		t.Errorf("darwin ConfigDir should start with Homebrew prefix %s; got %s", DarwinPrefix(), got)
	}
}

func TestDarwinSitesEnabledFallsBackToAvailable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only fallback check")
	}
	if NginxSitesAvailable() != NginxSitesEnabled() {
		t.Errorf("on darwin sites-enabled should equal sites-available; got %s vs %s",
			NginxSitesEnabled(), NginxSitesAvailable())
	}
}
