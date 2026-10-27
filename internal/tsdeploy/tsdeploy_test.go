//go:build windows

package tsdeploy

import "testing"

func TestSamePath(t *testing.T) {
	exe := `C:\Program Files\MyVPN\MyVPN-service.exe`
	for _, tc := range []struct {
		cmdline string
		want    bool
	}{
		{`"C:\Program Files\MyVPN\MyVPN-service.exe"`, true},
		{`"c:\program files\myvpn\MYVPN-SERVICE.EXE" --tun x`, true},
		{`C:\Program Files\MyVPN\MyVPN-service.exe`, true},
		{`"C:\Program Files\Tailscale\tailscaled.exe"`, false},
		{``, false},
	} {
		if got := samePath(tc.cmdline, exe); got != tc.want {
			t.Errorf("samePath(%q) = %v, want %v", tc.cmdline, got, tc.want)
		}
	}
}

func TestNeedsDeploy(t *testing.T) {
	if _, err := loadManifest(); err != nil {
		t.Skip("no bundle; run build.cmd first")
	}
	need, err := NeedsDeploy(t.TempDir())
	if err != nil || !need {
		t.Errorf("empty folder: NeedsDeploy = %v, %v; want true", need, err)
	}
}

func TestDeployExtras(t *testing.T) {
	if len(ExtraFiles()) == 0 {
		t.Skip("no extra files in the bundle; run build.cmd first")
	}
	dir := t.TempDir()
	if err := DeployExtras(dir, t.Logf); err != nil {
		t.Fatal(err)
	}
	m, _ := loadManifest()
	if s := stale(dir, m.Extras); len(s) != 0 {
		t.Errorf("still stale after DeployExtras: %v", s)
	}
	if err := DeployExtras(dir, t.Logf); err != nil { // nothing to do the second time
		t.Fatal(err)
	}
}
