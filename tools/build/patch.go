package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"tsgui/internal/brand"
)

// patch replaces old with new in file, which must contain old exactly n
// times. A different count means upstream changed that code: the build
// stops so the patch can be checked instead of silently doing nothing.
type patch struct {
	file     string
	old, new string
	n        int
}

// patches rebrand everything tailscaled registers with Windows. Paths are
// relative to the tailscale.com module root. New values are written as Go
// string literals with strconv.Quote.
func patches(cfg *brand.Config) []patch {
	id := cfg.ServiceName()
	q := strconv.Quote
	return []patch{
		// LocalAPI pipe, state file and the legacy per-user state file.
		{"paths/paths.go", "`Tailscale\\tailscaled`", q(cfg.DaemonPipeName()), 1},
		{"paths/paths.go", `"Tailscale"`, q(id), 2},
		// The state folder holds the node keys; it is only locked down to
		// SYSTEM and Administrators when it has the expected name.
		{"paths/paths_windows.go", `!= "tailscale"`, `!= ` + q(strings.ToLower(id)), 1},

		// Service name (also the event log source), adapter name, the
		// service description set by install-system-daemon.
		{"cmd/tailscaled/tailscaled_windows.go", `const serviceName = "Tailscale"`, `const serviceName = ` + q(id), 1},
		{"cmd/tailscaled/tailscaled.go", `return "Tailscale"`, `return ` + q(id), 1},
		{"cmd/tailscaled/install_windows.go", `"Connects this computer to others on the Tailscale network."`, q(cfg.Install.ServiceDescription), 1},

		// Everything else under %ProgramData%\Tailscale and %LocalAppData%\Tailscale.
		{"cmd/tailscaled/syspolicy.go", `"Tailscale"`, q(id), 1},
		{"envknob/envknob.go", `"Tailscale"`, q(id), 1},
		{"ipn/auditlog/store.go", `"Tailscale"`, q(id), 1},
		{"ipn/ipnlocal/profiles_windows.go", `"Tailscale"`, q(id), 1},
		{"logpolicy/logpolicy.go", `"Tailscale"`, q(id), 4},
		// The service only writes its Logs\ folder when it is tailscaled.exe.
		{"logpolicy/logpolicy.go", `"tailscaled.exe"`, q(cfg.DaemonExe()), 1},

		// Network adapter: recognize our own, its type ("<id> Tunnel" in
		// Device Manager) and its fixed GUID.
		{"net/netmon/state.go", `name == "Tailscale"`, `name == ` + q(id), 1},
		{"net/tstun/tun_windows.go", `"Tailscale"`, q(id), 1},
		{"net/tstun/tun_windows.go", `"{37217669-42da-4657-a55b-0d995d328250}"`, q(cfg.AdapterGUID()), 1},
		{"net/tshttpproxy/tshttpproxy_windows.go", `"Tailscale"`, q(id), 1},

		// System policy (HKLM/HKCU\SOFTWARE\Policies\<id>) and the legacy key.
		{"util/syspolicy/source/policy_store_windows.go", "`Policies\\Tailscale`", q(`Policies\` + id), 1},
		{"util/syspolicy/source/policy_store_windows.go", "`Tailscale IPN`", q(id + " IPN"), 1},
		{"util/winutil/winutil_windows.go", "`SOFTWARE\\Tailscale IPN`", q(`SOFTWARE\` + id + " IPN"), 1},
		{"util/winutil/winutil_windows.go", "`SOFTWARE\\Policies\\Tailscale`", q(cfg.PolicyKey()), 1},

		// Windows Defender Firewall rules and the WFP session names.
		{"wgengine/router/osrouter/router_windows.go", "Tailscale-In", id + "-In", 7},
		{"wgengine/router/osrouter/router_windows.go", "Tailscale-Process", id + "-Process", 8},
		{"wf/firewall.go", `"Tailscale `, `"` + id + ` `, 3},

		// The MagicDNS section in the hosts file.
		{"net/dns/manager_windows.go", "# Tailscale", "# " + id, 2},
		{"net/dns/manager_windows.go", "entries for Tailscale.", "entries for " + id + ".", 1},

		// Explorer "Send with ..." menu and a hidden window class.
		{"util/osshare/filesharingstatus_windows.go", "`*\\shell\\tailscale`", q(`*\shell\` + strings.ToLower(id)), 1},
		{"util/osshare/filesharingstatus_windows.go", `"Send with Tailscale..."`, q("Send with " + cfg.ProductName + "..."), 1},
		{"ipn/desktop/sessions_windows.go", `"Tailscale-SessionManager"`, q(id + "-SessionManager"), 1},
	}
}

// applyPatches edits the copy of the tailscale.com source in root.
func applyPatches(root string, ps []patch) error {
	for _, p := range ps {
		path := filepath.Join(root, filepath.FromSlash(p.file))
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		if n := strings.Count(s, p.old); n != p.n {
			return fmt.Errorf("修補 %s：預期找到 %d 處 %s，實際 %d 處（Tailscale 原始碼可能改了，請檢查 tools/build/patch.go）", p.file, p.n, p.old, n)
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(s, p.old, p.new)), 0o644); err != nil {
			return err
		}
	}
	return nil
}
