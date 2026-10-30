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
		// The state file's folder is only used as VarRoot (TLS certs for
		// cert/serve/funnel, Taildrop) when it has the expected name.
		{"cmd/tailscaled/tailscaled.go", `strings.EqualFold(filepath.Base(dir), "tailscale")`, `strings.EqualFold(filepath.Base(dir), ` + q(id) + `)`, 1},
		// The CLI looks for this process to explain why it can't connect.
		{"cmd/tailscale/cli/diag.go", `"tailscaled.exe"`, q(cfg.DaemonExe()), 1},

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
		// Captive portal checks skip our own adapter by its name.
		{"net/captivedetection/captivedetection.go", `[]string{"tailscale", `, `[]string{"tailscale", ` + q(strings.ToLower(id)) + `, `, 1},

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

		// Turn off NetBIOS over TCP/IP on the adapter before it gets its
		// addresses (see netbiosGo).
		{"wgengine/router/osrouter/ifconfig_windows.go", "err = syncAddresses(iface, addresses)", "disableNetBIOS(luid)\n\terr = syncAddresses(iface, addresses)", 1},
	}
}

// sources are files added to the copy of the tailscale.com source, keyed by
// path relative to the module root.
var sources = map[string]string{
	"wgengine/router/osrouter/netbios_windows.go": netbiosGo,
}

// netbiosGo replaces upstream's disableNetBIOS in net/dns, which never runs
// (it first looks up an AF_NETBIOS IP interface, which Windows always
// rejects) and would be too late anyway: NetBT only reads NetbiosOptions when
// an address is added, and Windows resets it to 0 (enabled) every time Wintun
// creates the adapter, i.e. on every service start.
const netbiosGo = `// Added by tsgui's build (tools/build/patch.go).

package osrouter

import (
	"log"

	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"tailscale.com/util/winutil"
)

// disableNetBIOS sets NetbiosOptions to 2 (NetBIOS over TCP/IP disabled) for
// the adapter. It must run before the adapter's addresses are added.
func disableNetBIOS(luid winipcfg.LUID) {
	guid, err := luid.GUID()
	if err != nil {
		log.Printf("disableNetBIOS: %v", err)
		return
	}
	path := winutil.NetBTInterfacePrefix.WithSuffix(guid.String())
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, string(path), registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Printf("disableNetBIOS: opening %s: %v", path, err)
		return
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("NetbiosOptions"); err == nil && v == 2 {
		return
	}
	if err := k.SetDWordValue("NetbiosOptions", 2); err != nil {
		log.Printf("disableNetBIOS: %v", err)
		return
	}
	log.Printf("disableNetBIOS: NetBIOS over TCP/IP disabled on %v", guid)
}
`

// addSources writes sources into root. Upstream adding a file with the same
// name stops the build, like a patch that no longer matches.
func addSources(root string) error {
	for name, src := range sources {
		path := filepath.Join(root, filepath.FromSlash(name))
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("加入 %s：Tailscale 原始碼已經有這個檔案，請檢查 tools/build/patch.go", name)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			return err
		}
	}
	return nil
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
