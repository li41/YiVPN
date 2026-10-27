//go:build windows

package tray

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
	"tailscale.com/util/syspolicy/pkey"
	"tailscale.com/util/syspolicy/setting"
	"tsgui/internal/brand"
)

func testApp() *App {
	return New(&brand.Config{ProductName: "MyVPN", Version: "1.0.0",
		Tailscale: brand.Tailscale{AdminConsoleURL: "https://example.com"},
		Update:    brand.Update{BaseURL: "https://example.com/releases"}}, nil)
}

// dump renders the menu model as indented text for assertions.
func dump(items []item) string {
	var b strings.Builder
	var walk func([]item, string)
	walk = func(items []item, indent string) {
		for _, it := range tidy(items) {
			switch {
			case it.sep:
				b.WriteString(indent + "---\n")
				continue
			case it.check && it.checked:
				b.WriteString(indent + "[x] ")
			case it.check:
				b.WriteString(indent + "[ ] ")
			default:
				b.WriteString(indent)
			}
			b.WriteString(it.title)
			if it.disabled {
				b.WriteString(" (disabled)")
			}
			b.WriteString("\n")
			walk(it.children, indent+"  ")
		}
	}
	walk(items, "")
	return b.String()
}

func peer(name string, user tailcfg.UserID, ip string, online bool) *ipnstate.PeerStatus {
	return &ipnstate.PeerStatus{
		ID: tailcfg.StableNodeID("n-" + name), HostName: name, DNSName: name + ".tail1234.ts.net.",
		UserID: user, Online: online, TailscaleIPs: []netip.Addr{netip.MustParseAddr(ip)},
	}
}

func TestModelServiceDown(t *testing.T) {
	a := testApp()
	a.err = errors.New("dial pipe: not found")
	got := dump(a.model())
	want := "MyVPN service is not running (disabled)\n---\nAbout MyVPN\nExit\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if icon, tip := a.iconAndTooltip(); icon != "disconnected" || tip != "MyVPN: Service not running" {
		t.Errorf("icon/tooltip = %q, %q", icon, tip)
	}
}

func TestModelLoggedOut(t *testing.T) {
	a := testApp()
	a.st = &ipnstate.Status{BackendState: ipn.NeedsLogin.String()}
	a.prefs = ipn.NewPrefs()
	got := dump(a.model())
	if !strings.HasPrefix(got, "Log in...\n") {
		t.Errorf("first item should be Log in:\n%s", got)
	}
	for _, hidden := range []string{"Disconnect", "Exit nodes", "Admin console", "Network devices"} {
		if strings.Contains(got, hidden) {
			t.Errorf("%q should be hidden when logged out:\n%s", hidden, got)
		}
	}
}

func TestModelRunning(t *testing.T) {
	a := testApp()
	self := peer("laptop", 1, "100.64.0.1", true)
	exit := peer("gateway", 1, "100.64.0.2", true)
	exit.ExitNodeOption = true
	srv := peer("server", 0, "100.64.0.3", false)
	tags := views.SliceOf([]string{"tag:server"})
	srv.Tags = &tags
	bob := peer("bob-pc", 2, "100.64.0.4", true)

	a.st = &ipnstate.Status{
		BackendState:   ipn.Running.String(),
		Self:           self,
		Peer:           map[key.NodePublic]*ipnstate.PeerStatus{key.NewNode().Public(): exit, key.NewNode().Public(): srv, key.NewNode().Public(): bob},
		User:           map[tailcfg.UserID]tailcfg.UserProfile{2: {DisplayName: "Bob"}},
		ExitNodeStatus: &ipnstate.ExitNodeStatus{ID: exit.ID, Online: true},
		ClientVersion:  &tailcfg.ClientVersion{LatestVersion: "1.200.0"},
	}
	a.prefs = ipn.NewPrefs()
	a.prefs.WantRunning = true
	a.cur = ipn.LoginProfile{ID: "p1", Name: "me@example.com"}
	a.profiles = []ipn.LoginProfile{a.cur, {ID: "p2", Name: "work@example.com"}}
	a.policy = setting.NewSnapshot(map[pkey.Key]setting.RawItem{
		pkey.EnableTailscaleDNS:    setting.RawItemOf("always"),
		pkey.RunExitNodeVisibility: setting.RawItemOf("hide"),
	})

	got := dump(a.model())
	t.Logf("menu:\n%s", got)
	for _, want := range []string{
		"Disconnect\n",
		"me@example.com\n  [x] me@example.com\n  [ ] work@example.com\n  ---\n  Add another account...\n  Log out\n",
		"This device: laptop (100.64.0.1)\n",
		"Network devices\n  My devices\n    gateway\n  Tagged devices\n    server (offline)\n  Bob\n    bob-pc\n",
		"Exit nodes\n  [ ] None\n  ---\n  Tailnet exit nodes (disabled)\n  [x] gateway\n  ---\n  [ ] Allow local network access\n",
		"  [x] Use MyVPN DNS settings (disabled)\n",        // forced by policy
		"  [x] Automatically install updates (disabled)\n", // forced by branding
		"Admin console...\n",
		"Check for updates\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("menu missing:\n%s", want)
		}
	}
	for _, unwanted := range []string{"Tailscale", "Update available"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("menu should not mention %q", unwanted)
		}
	}
	if strings.Contains(got, "Run exit node") {
		t.Error("Run exit node should be hidden by policy")
	}
	if icon, tip := a.iconAndTooltip(); icon != "exitnode" || tip != "MyVPN: Connected (exit node)" {
		t.Errorf("icon/tooltip = %q, %q", icon, tip)
	}
}

func TestSignatureStable(t *testing.T) {
	a := testApp()
	a.st = &ipnstate.Status{BackendState: ipn.Stopped.String()}
	a.prefs = ipn.NewPrefs()
	a.cur = ipn.LoginProfile{ID: "p1", Name: "me@example.com"}
	if signature(a.model()) != signature(a.model()) {
		t.Error("same state must give the same signature (no needless menu rebuilds)")
	}
}
