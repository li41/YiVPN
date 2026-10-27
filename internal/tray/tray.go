//go:build windows

// Package tray is the branded system-tray GUI. It talks to tailscaled over
// LocalAPI, exactly like the official tailscale-ipn.exe, and lays out its menu
// the same way. Based on tailscale.com/client/systray (BSD-3-Clause).
package tray

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/atotto/clipboard"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tailcfg/nodecap"
	"tailscale.com/util/syspolicy/pkey"
	"tailscale.com/util/syspolicy/setting"
	"tsgui/internal/brand"
	"tsgui/internal/brand/res"
	"tsgui/internal/winx"
)

// App is the tray application.
type App struct {
	cfg *brand.Config
	lc  *local.Client

	// TriggerUpdate starts the updater (the "Update available" menu item).
	TriggerUpdate func() error

	mu       sync.Mutex // guards the fields below
	st       *ipnstate.Status
	prefs    *ipn.Prefs
	cur      ipn.LoginProfile
	profiles []ipn.LoginProfile
	policy   *setting.Snapshot
	sugg     *apitype.ExitNodeSuggestionResponse
	err      error // LocalAPI unreachable
	denied   bool  // tailscaled is owned by another Windows user

	ctx       context.Context
	cancel    context.CancelFunc
	refreshCh chan struct{}

	// owned by the loop goroutine
	menuSig    string
	menuCancel context.CancelFunc
	icon       string
	tooltip    string
}

// New returns an App that uses lc to talk to tailscaled.
func New(cfg *brand.Config, lc *local.Client) *App {
	ctx, cancel := context.WithCancel(context.Background())
	return &App{
		cfg:       cfg,
		lc:        lc,
		ctx:       ctx,
		cancel:    cancel,
		refreshCh: make(chan struct{}, 1),
	}
}

// Run shows the tray icon and blocks until Exit is chosen.
func (a *App) Run() {
	systray.Run(a.onReady, a.cancel)
}

// Context is canceled when the app exits.
func (a *App) Context() context.Context { return a.ctx }

// Quit closes the tray app.
func (a *App) Quit() { systray.Quit() }

func (a *App) onReady() {
	a.setIcon("disconnected")
	a.setTooltip(fmt.Sprintf(txtTipStopped, a.cfg.ProductName))
	go a.watchIPNBus()
	go a.loop()
	a.kick()
}

// kick schedules a state refresh and menu rebuild.
func (a *App) kick() {
	select {
	case a.refreshCh <- struct{}{}:
	default:
	}
}

func (a *App) loop() {
	// Peers' online state isn't pushed when we ask for no netmap, so poll.
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.refreshCh:
		case <-t.C:
		}
		time.Sleep(250 * time.Millisecond) // coalesce bursts of notifications
		select {
		case <-a.refreshCh:
		default:
		}
		a.refresh()
		a.render()
	}
}

func (a *App) watchIPNBus() {
	for a.ctx.Err() == nil {
		if err := a.watchOnce(); err != nil && a.ctx.Err() == nil {
			log.Printf("ipn bus: %v", err)
		}
		a.kick() // show "service not running" or pick up the restarted daemon
		select {
		case <-a.ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *App) watchOnce() error {
	// Keeping this watch open is also what tells tailscaled a GUI is attached
	// for this Windows user, as with the official client.
	w, err := a.lc.WatchIPNBus(a.ctx, ipn.NotifyInitialState|ipn.NotifyInitialPrefs|ipn.NotifyNoNetMap)
	if err != nil {
		return err
	}
	defer w.Close()
	for {
		n, err := w.Next()
		if err != nil {
			return err
		}
		if n.BrowseToURL != nil && *n.BrowseToURL != "" {
			if err := winx.OpenURL(*n.BrowseToURL); err != nil {
				log.Printf("open login URL: %v", err)
			}
		}
		if n.ErrMessage != nil {
			log.Printf("tailscaled: %s", *n.ErrMessage)
		}
		if n.State != nil || n.Prefs != nil || n.LoginFinished != nil {
			a.kick()
		}
	}
}

func (a *App) refresh() {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()

	st, err := a.lc.Status(ctx)
	var (
		prefs    *ipn.Prefs
		cur      ipn.LoginProfile
		profiles []ipn.LoginProfile
		pol      *setting.Snapshot
		sugg     *apitype.ExitNodeSuggestionResponse
	)
	if err == nil {
		prefs, _ = a.lc.GetPrefs(ctx)
		cur, profiles, err = a.lc.ProfileStatus(ctx)
		pol, _ = a.lc.GetEffectivePolicy(ctx, setting.DefaultScope())
		if st.BackendState == ipn.Running.String() && st.Self != nil && st.Self.CapMap.Contains(nodecap.SuggestExitNodeUI) {
			if s, err := a.lc.SuggestExitNode(ctx); err == nil {
				sugg = &s
			}
		}
	}

	a.mu.Lock()
	a.err, a.denied = err, err != nil && local.IsAccessDeniedError(err)
	a.st, a.prefs, a.cur, a.profiles, a.policy, a.sugg = st, prefs, cur, profiles, pol, sugg
	a.mu.Unlock()
}

// ---- menu model ----

type item struct {
	title    string
	check    bool // render as checkbox
	checked  bool
	disabled bool
	sep      bool
	onClick  func(ctx context.Context)
	children []item
}

func sep() item               { return item{sep: true} }
func label(title string) item { return item{title: title, disabled: true} }
func action(title string, f func(context.Context)) item {
	return item{title: title, onClick: f}
}

func toggle(title string, on, locked bool, f func(context.Context, bool)) item {
	return item{title: title, check: true, checked: on, disabled: locked, onClick: func(ctx context.Context) { f(ctx, !on) }}
}

func signature(items []item) string {
	var b strings.Builder
	var walk func([]item, int)
	walk = func(items []item, depth int) {
		for _, it := range items {
			fmt.Fprintf(&b, "%d|%s|%v|%v|%v|%v\n", depth, it.title, it.check, it.checked, it.disabled, it.sep)
			walk(it.children, depth+1)
		}
	}
	walk(items, 0)
	return b.String()
}

func (a *App) render() {
	a.mu.Lock()
	model := a.model()
	icon, tip := a.iconAndTooltip()
	a.mu.Unlock()

	a.setIcon(icon)
	a.setTooltip(tip)

	// Only rebuild when something visible changed, so an open menu isn't
	// yanked away by the periodic refresh.
	sig := signature(model)
	if sig == a.menuSig {
		return
	}
	a.menuSig = sig
	if a.menuCancel != nil {
		a.menuCancel()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.menuCancel = cancel

	systray.ResetMenu()
	for _, it := range tidy(model) {
		if it.sep {
			systray.AddSeparator()
			continue
		}
		var mi *systray.MenuItem
		if it.check {
			mi = systray.AddMenuItemCheckbox(it.title, "", it.checked)
		} else {
			mi = systray.AddMenuItem(it.title, "")
		}
		a.bind(ctx, mi, it)
	}
}

func (a *App) bind(ctx context.Context, mi *systray.MenuItem, it item) {
	if it.disabled {
		mi.Disable()
	}
	for _, c := range tidy(it.children) {
		if c.sep {
			mi.AddSeparator()
			continue
		}
		var sub *systray.MenuItem
		if c.check {
			sub = mi.AddSubMenuItemCheckbox(c.title, "", c.checked)
		} else {
			sub = mi.AddSubMenuItem(c.title, "")
		}
		a.bind(ctx, sub, c)
	}
	if it.onClick == nil || it.disabled {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-mi.ClickedCh:
				actx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
				it.onClick(actx)
				cancel()
				a.kick()
			}
		}
	}()
}

// tidy drops leading, trailing and repeated separators.
func tidy(items []item) []item {
	var out []item
	for _, it := range items {
		if it.sep && (len(out) == 0 || out[len(out)-1].sep) {
			continue
		}
		out = append(out, it)
	}
	if len(out) > 0 && out[len(out)-1].sep {
		out = out[:len(out)-1]
	}
	return out
}

// model builds the menu from the current state. a.mu must be held.
func (a *App) model() []item {
	name := a.cfg.ProductName
	tail := []item{
		sep(),
		action(fmt.Sprintf(txtAbout, name), a.about),
		action(txtExit, func(context.Context) { systray.Quit() }),
	}
	if a.err != nil || a.st == nil {
		msg := txtServiceDown
		if a.denied {
			msg = txtOtherUser
		}
		return append([]item{label(fmt.Sprintf(msg, name))}, tail...)
	}

	st := a.st
	state := st.BackendState
	loggedIn := state != ipn.NeedsLogin.String() && state != ipn.NoState.String() && a.cur.ID != ""
	running := state == ipn.Running.String()

	var m []item
	switch {
	case !loggedIn:
		m = append(m, action(txtLogIn, a.login))
	case state == ipn.NeedsMachineAuth.String():
		m = append(m, label(txtWaitingApproval))
	}
	if loggedIn {
		if a.prefs != nil && a.prefs.WantRunning {
			m = append(m, action(txtDisconnect, func(ctx context.Context) { a.setWantRunning(ctx, false) }))
		} else {
			m = append(m, action(txtConnect, func(ctx context.Context) { a.setWantRunning(ctx, true) }))
		}
	}
	m = append(m, sep())

	if a.cur.ID != "" {
		m = append(m, a.accountMenu())
	}
	if loggedIn {
		if running && st.Self != nil && len(st.Self.TailscaleIPs) > 0 {
			self := st.Self
			m = append(m, action(fmt.Sprintf(txtThisDevice, self.HostName, self.TailscaleIPs[0]), func(context.Context) { copyIP(self) }))
		} else {
			m = append(m, label(txtThisDeviceNone))
		}
		if running && a.visible(pkey.NetworkDevicesVisibility) {
			m = append(m, a.networkDevicesMenu())
		}
	}
	m = append(m, sep())

	if loggedIn && a.visible(pkey.ExitNodeMenuVisibility) {
		m = append(m, a.exitNodeMenu())
	}
	if a.visible(pkey.PreferencesMenuVisibility) && a.prefs != nil {
		m = append(m, a.preferencesMenu())
	}
	if loggedIn && a.visible(pkey.AdminConsoleVisibility) {
		m = append(m, action(txtAdminConsole, func(context.Context) { winx.OpenURL(a.cfg.Tailscale.AdminConsoleURL) }))
	}
	if cv := st.ClientVersion; cv != nil && !cv.RunningLatest && cv.LatestVersion != "" && a.visible(pkey.UpdateMenuVisibility) {
		m = append(m, sep(), action(fmt.Sprintf(txtUpdateAvailable, cv.LatestVersion), a.installUpdate))
	}
	return append(m, tail...)
}

func (a *App) iconAndTooltip() (icon, tip string) {
	name := a.cfg.ProductName
	if a.err != nil || a.st == nil {
		return "disconnected", fmt.Sprintf(txtTipDown, name)
	}
	switch a.st.BackendState {
	case ipn.Running.String():
		if es := a.st.ExitNodeStatus; es != nil && !es.ID.IsZero() {
			if !es.Online {
				return "connecting", fmt.Sprintf(txtTipExitDown, name)
			}
			return "exitnode", fmt.Sprintf(txtTipExitNode, name)
		}
		return "connected", fmt.Sprintf(txtTipConnected, name)
	case ipn.Starting.String():
		return "connecting", fmt.Sprintf(txtTipStarting, name)
	case ipn.NeedsLogin.String(), ipn.NoState.String():
		return "disconnected", fmt.Sprintf(txtTipLoggedOut, name)
	case ipn.NeedsMachineAuth.String():
		return "connecting", fmt.Sprintf(txtTipApproval, name)
	}
	return "disconnected", fmt.Sprintf(txtTipStopped, name)
}

func (a *App) setIcon(name string) {
	if name != a.icon {
		a.icon = name
		systray.SetIcon(res.Icon(name))
	}
}

func (a *App) setTooltip(tip string) {
	if tip != a.tooltip {
		a.tooltip = tip
		systray.SetTooltip(tip)
	}
}

// ---- submenus ----

func (a *App) accountMenu() item {
	acct := item{title: profileTitle(a.cur)}
	for _, p := range a.profiles {
		id := p.ID
		acct.children = append(acct.children, item{
			title: profileTitle(p), check: true, checked: p.ID == a.cur.ID,
			onClick: func(ctx context.Context) {
				if err := a.lc.SwitchProfile(ctx, id); err != nil {
					a.showErr(err)
				}
			},
		})
	}
	acct.children = append(acct.children, sep(),
		action(txtAddAccount, func(ctx context.Context) {
			if err := a.lc.SwitchToEmptyProfile(ctx); err != nil {
				a.showErr(err)
				return
			}
			a.login(ctx)
		}),
		action(txtLogOut, func(ctx context.Context) {
			if err := a.lc.Logout(ctx); err != nil {
				a.showErr(err)
			}
		}),
	)
	return acct
}

func profileTitle(p ipn.LoginProfile) string {
	tailnet := ""
	if p.NetworkProfile.DomainName != "" {
		tailnet = p.NetworkProfile.DisplayNameOrDefault()
	}
	if tailnet == "" || strings.EqualFold(p.Name, tailnet) {
		return p.Name
	}
	return p.Name + " (" + tailnet + ")"
}

func peerName(ps *ipnstate.PeerStatus) string {
	if n, _, _ := strings.Cut(ps.DNSName, "."); n != "" {
		return n
	}
	return ps.HostName
}

func (a *App) networkDevicesMenu() item {
	st := a.st
	var selfUser tailcfg.UserID
	if st.Self != nil {
		selfUser = st.Self.UserID
	}
	mine, tagged := []*ipnstate.PeerStatus{}, []*ipnstate.PeerStatus{}
	others := map[tailcfg.UserID][]*ipnstate.PeerStatus{}
	for _, ps := range st.Peer {
		if ps.Location != nil { // Mullvad exit nodes are listed under Exit nodes
			continue
		}
		switch {
		case ps.Tags != nil && ps.Tags.Len() > 0:
			tagged = append(tagged, ps)
		case ps.UserID == selfUser:
			mine = append(mine, ps)
		default:
			others[ps.UserID] = append(others[ps.UserID], ps)
		}
	}

	group := func(title string, peers []*ipnstate.PeerStatus) item {
		slices.SortFunc(peers, func(x, y *ipnstate.PeerStatus) int {
			return cmp.Compare(strings.ToLower(peerName(x)), strings.ToLower(peerName(y)))
		})
		g := item{title: title}
		for _, ps := range peers {
			t := peerName(ps)
			if !ps.Online {
				t = fmt.Sprintf(txtOffline, t)
			}
			g.children = append(g.children, action(t, func(context.Context) { copyIP(ps) }))
		}
		if len(g.children) == 0 {
			g.children = []item{label(txtNoDevices)}
		}
		return g
	}

	nd := item{title: txtNetworkDevices}
	nd.children = append(nd.children, group(txtMyDevices, mine))
	if len(tagged) > 0 {
		nd.children = append(nd.children, group(txtTaggedDevices, tagged))
	}
	users := make([]tailcfg.UserID, 0, len(others))
	for u := range others {
		users = append(users, u)
	}
	userName := func(u tailcfg.UserID) string {
		if p, ok := st.User[u]; ok {
			return cmp.Or(p.DisplayName, p.LoginName)
		}
		return fmt.Sprint(u)
	}
	slices.SortFunc(users, func(x, y tailcfg.UserID) int { return cmp.Compare(userName(x), userName(y)) })
	for _, u := range users {
		nd.children = append(nd.children, group(userName(u), others[u]))
	}
	return nd
}

func (a *App) exitNodeMenu() item {
	st, p := a.st, a.prefs
	_, forced := a.policyString(pkey.ExitNodeID)
	var active tailcfg.StableNodeID
	if st.ExitNodeStatus != nil {
		active = st.ExitNodeStatus.ID
	}
	choose := func(title string, id tailcfg.StableNodeID, checked, offline bool) item {
		return item{title: title, check: true, checked: checked, disabled: forced || offline,
			onClick: func(ctx context.Context) { a.setExitNode(ctx, id) }}
	}

	en := item{title: txtExitNodes}
	en.children = append(en.children, choose(txtExitNone, "", active.IsZero(), false))
	if s := a.sugg; s != nil && !s.ID.IsZero() {
		t := peerNameFromFQDN(s.Name)
		if loc := s.Location; loc.Valid() && loc.Country() != "" {
			t = loc.Country() + ": " + loc.City()
		}
		en.children = append(en.children, choose(fmt.Sprintf(txtRecommended, t), s.ID, s.ID == active, false))
	}

	var tailnet, mullvad []*ipnstate.PeerStatus
	for _, ps := range st.Peer {
		switch {
		case !ps.ExitNodeOption:
		case ps.Location == nil:
			tailnet = append(tailnet, ps)
		default:
			mullvad = append(mullvad, ps)
		}
	}
	if len(tailnet) > 0 {
		slices.SortFunc(tailnet, func(x, y *ipnstate.PeerStatus) int { return cmp.Compare(peerName(x), peerName(y)) })
		en.children = append(en.children, sep(), label(txtTailnetExitNodes))
		for _, ps := range tailnet {
			t := peerName(ps)
			if !ps.Online {
				t = fmt.Sprintf(txtOffline, t)
			}
			en.children = append(en.children, choose(t, ps.ID, ps.ID == active, !ps.Online))
		}
	}
	if len(mullvad) > 0 {
		// One entry per country, using its highest-priority node.
		best := map[string]*ipnstate.PeerStatus{}
		usingCountry := ""
		for _, ps := range mullvad {
			cc := ps.Location.CountryCode
			if b := best[cc]; b == nil || ps.Location.Priority > b.Location.Priority {
				best[cc] = ps
			}
			if ps.ID == active {
				usingCountry = cc
			}
		}
		mv := item{title: txtMullvad, check: true, checked: usingCountry != ""}
		countries := make([]*ipnstate.PeerStatus, 0, len(best))
		for _, ps := range best {
			countries = append(countries, ps)
		}
		slices.SortFunc(countries, func(x, y *ipnstate.PeerStatus) int { return cmp.Compare(x.Location.Country, y.Location.Country) })
		for _, ps := range countries {
			mv.children = append(mv.children, choose(ps.Location.Country, ps.ID, ps.Location.CountryCode == usingCountry, false))
		}
		en.children = append(en.children, sep(), mv)
	}

	en.children = append(en.children, sep())
	lan, lanLocked := a.forcedBool(pkey.ExitNodeAllowLANAccess, p.ExitNodeAllowLANAccess)
	en.children = append(en.children, toggle(txtAllowLAN, lan, lanLocked, func(ctx context.Context, on bool) {
		a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{ExitNodeAllowLANAccess: on}, ExitNodeAllowLANAccessSet: true})
	}))
	if a.visible(pkey.RunExitNodeVisibility) {
		run, runLocked := a.forcedBool(pkey.EnableRunExitNode, p.AdvertisesExitNode())
		cur := p.Clone()
		en.children = append(en.children, toggle(txtRunExitNode, run, runLocked, func(ctx context.Context, on bool) {
			cur.SetAdvertiseExitNode(on)
			a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{AdvertiseRoutes: cur.AdvertiseRoutes}, AdvertiseRoutesSet: true})
		}))
	}
	return en
}

func peerNameFromFQDN(fqdn string) string {
	n, _, _ := strings.Cut(fqdn, ".")
	return n
}

func (a *App) preferencesMenu() item {
	p := a.prefs
	pm := item{title: txtPreferences}

	dns, dnsLocked := a.forcedBool(pkey.EnableTailscaleDNS, p.CorpDNS)
	pm.children = append(pm.children, toggle(txtUseDNS, dns, dnsLocked, func(ctx context.Context, on bool) {
		a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{CorpDNS: on}, CorpDNSSet: true})
	}))
	sub, subLocked := a.forcedBool(pkey.EnableTailscaleSubnets, p.RouteAll)
	pm.children = append(pm.children, toggle(txtUseSubnets, sub, subLocked, func(ctx context.Context, on bool) {
		a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{RouteAll: on}, RouteAllSet: true})
	}))
	in, inLocked := a.forcedBool(pkey.EnableIncomingConnections, !p.ShieldsUp)
	pm.children = append(pm.children, toggle(txtAllowIncoming, in, inLocked, func(ctx context.Context, on bool) {
		a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{ShieldsUp: !on}, ShieldsUpSet: true})
	}))
	ua, uaLocked := a.forcedBool(pkey.EnableServerMode, p.ForceDaemon)
	pm.children = append(pm.children, toggle(txtRunUnattended, ua, uaLocked, func(ctx context.Context, on bool) {
		a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{ForceDaemon: on}, ForceDaemonSet: true})
	}))
	if a.visible(pkey.AutoUpdateVisibility) {
		// Updates are done by our scheduled task (tailscaled's own updater
		// would install the MSI), so this only shows the configured state.
		pm.children = append(pm.children, item{title: txtAutoUpdate, check: true, checked: a.cfg.Tailscale.AutoUpdate, disabled: true})
	}
	return pm
}

// ---- system policy helpers (HKLM\SOFTWARE\Policies\Tailscale) ----

func (a *App) policyString(k pkey.Key) (string, bool) {
	if a.policy == nil {
		return "", false
	}
	it, ok := a.policy.GetSetting(k)
	if !ok || it.Error() != nil {
		return "", false
	}
	s, ok := it.Value().(string)
	return strings.ToLower(s), ok && s != ""
}

// visible reports whether a "show"/"hide" visibility policy allows an item.
func (a *App) visible(k pkey.Key) bool {
	v, _ := a.policyString(k)
	return v != "hide"
}

// forcedBool applies an "always"/"never"/"user-decides" policy to cur.
func (a *App) forcedBool(k pkey.Key, cur bool) (val, locked bool) {
	switch v, _ := a.policyString(k); v {
	case "always":
		return true, true
	case "never":
		return false, true
	}
	return cur, false
}

// ---- actions ----

func (a *App) editPrefs(ctx context.Context, mp *ipn.MaskedPrefs) {
	if _, err := a.lc.EditPrefs(ctx, mp); err != nil {
		a.showErr(err)
	}
}

func (a *App) setWantRunning(ctx context.Context, on bool) {
	a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{WantRunning: on}, WantRunningSet: true})
}

func (a *App) setExitNode(ctx context.Context, id tailcfg.StableNodeID) {
	if id.IsZero() {
		if err := a.lc.SetUseExitNode(ctx, false); err != nil {
			a.showErr(err)
		}
		return
	}
	a.editPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{ExitNodeID: id}, ExitNodeIDSet: true})
}

func (a *App) login(ctx context.Context) {
	// tailscaled replies with BrowseToURL on the IPN bus; watchOnce opens it.
	if err := a.lc.StartLoginInteractive(ctx); err != nil {
		a.showErr(err)
	}
}

func (a *App) installUpdate(context.Context) {
	if a.TriggerUpdate == nil {
		return
	}
	if err := a.TriggerUpdate(); err != nil {
		a.showErr(err)
		return
	}
	go winx.Info(a.cfg.ProductName, txtUpdating)
}

func (a *App) about(context.Context) {
	a.mu.Lock()
	tsVer := "-"
	if a.st != nil {
		tsVer = a.st.Version
	}
	a.mu.Unlock()
	text := fmt.Sprintf("%s %s\n\nTailscale %s\n\n%s", a.cfg.ProductName, a.cfg.Version, tsVer, a.cfg.Copyright)
	go winx.Info(fmt.Sprintf(txtAbout, a.cfg.ProductName), text)
}

func (a *App) showErr(err error) {
	log.Print(err)
	go winx.Error(a.cfg.ProductName, err.Error())
}

func copyIP(ps *ipnstate.PeerStatus) {
	if ps == nil || len(ps.TailscaleIPs) == 0 {
		return
	}
	if err := clipboard.WriteAll(ps.TailscaleIPs[0].String()); err != nil {
		log.Printf("clipboard: %v", err)
	}
}
