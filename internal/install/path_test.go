//go:build windows

package install

import "testing"

func TestSamePathEntry(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	dir := `C:\Program Files\MyVPN`
	for _, tc := range []struct {
		entry string
		want  bool
	}{
		{`C:\Program Files\MyVPN`, true},
		{`c:\program files\myvpn\`, true},
		{` C:\Program Files\MyVPN `, true},
		{`%ProgramFiles%\MyVPN`, true},
		{`C:\Program Files\MyVPN2`, false},
		{`C:\Windows\System32`, false},
	} {
		if got := samePathEntry(tc.entry, dir); got != tc.want {
			t.Errorf("samePathEntry(%q) = %v, want %v", tc.entry, got, tc.want)
		}
	}
}
