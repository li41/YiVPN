package brand

import (
	"crypto/sha256"
	"fmt"
)

// The names below replace the "Tailscale" names hard-coded in tailscaled
// (tools/build patches its source with them) and are used by the installer
// and the GUI to find it. They all derive from appID, which must not change
// after the first release.

// ServiceName is the Windows service's internal name. tailscaled also uses
// it for the network adapter, %ProgramData%\<name> (login state and logs),
// the system policy key and the firewall rules.
func (c *Config) ServiceName() string { return c.AppID }

// DaemonExe is the file name of the self-built tailscaled.
func (c *Config) DaemonExe() string { return c.AppID + "-service.exe" }

// DaemonPipeName is the LocalAPI pipe below \\.\pipe\ProtectedPrefix\Administrators\.
func (c *Config) DaemonPipeName() string { return c.AppID + `\` + c.AppID + "-service" }

// DaemonPipe is the full LocalAPI pipe path. Only administrators can create
// pipes under ProtectedPrefix\Administrators, so the GUI can trust it.
func (c *Config) DaemonPipe() string {
	return `\\.\pipe\ProtectedPrefix\Administrators\` + c.DaemonPipeName()
}

// PolicyKey is the system policy key tailscaled reads (HKLM and HKCU).
func (c *Config) PolicyKey() string { return `SOFTWARE\Policies\` + c.AppID }

// AdapterGUID is the fixed GUID of the network adapter. Upstream uses one
// GUID for every Tailscale install; ours differs so the two never collide.
func (c *Config) AdapterGUID() string {
	h := sha256.Sum256([]byte("tsgui adapter\x00" + c.AppID))
	h[6] = h[6]&0x0f | 0x50 // version 5 (name-based)
	h[8] = h[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("{%x-%x-%x-%x-%x}", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}
