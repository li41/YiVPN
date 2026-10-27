//go:build windows

package tsdeploy

import "testing"

func TestSamePath(t *testing.T) {
	exe := `C:\Program Files\MyVPN\tailscaled.exe`
	for _, tc := range []struct {
		cmdline string
		want    bool
	}{
		{`"C:\Program Files\MyVPN\tailscaled.exe"`, true},
		{`"c:\program files\myvpn\TAILSCALED.EXE" --tun x`, true},
		{`C:\Program Files\MyVPN\tailscaled.exe`, true},
		{`"C:\Program Files\Tailscale\tailscaled.exe"`, false},
		{``, false},
	} {
		if got := samePath(tc.cmdline, exe); got != tc.want {
			t.Errorf("samePath(%q) = %v, want %v", tc.cmdline, got, tc.want)
		}
	}
}

func TestNeedsUpdate(t *testing.T) {
	dir := t.TempDir() // no tailscaled.exe
	if !(&Latest{Version: "1.0.0"}).NeedsUpdate(dir) {
		t.Error("missing tailscaled must need an update")
	}
}
