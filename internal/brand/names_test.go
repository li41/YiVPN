package brand

import (
	"regexp"
	"testing"
)

func TestNames(t *testing.T) {
	c := &Config{AppID: "MyVPN"}
	if got, want := c.DaemonPipe(), `\\.\pipe\ProtectedPrefix\Administrators\MyVPN\MyVPN-service`; got != want {
		t.Errorf("DaemonPipe = %q, want %q", got, want)
	}
	if got, want := c.DaemonExe(), "MyVPN-service.exe"; got != want {
		t.Errorf("DaemonExe = %q, want %q", got, want)
	}
	g := c.AdapterGUID()
	if !regexp.MustCompile(`^\{[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\}$`).MatchString(g) {
		t.Errorf("AdapterGUID = %q, not a version 5 GUID", g)
	}
	// Changing it would orphan the adapter of every existing install.
	if g != c.AdapterGUID() || g == (&Config{AppID: "Other"}).AdapterGUID() {
		t.Error("AdapterGUID must be stable and differ per appID")
	}
	if g == "{37217669-42da-4657-a55b-0d995d328250}" {
		t.Error("AdapterGUID must not be upstream's")
	}
}
