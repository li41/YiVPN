//go:build windows

// Package install installs the GUI and the tailscaled bundled in it
// machine-wide into %ProgramFiles%\<dirName>, registers the SYSTEM updater
// task, and removes everything again on uninstall. The official Tailscale MSI
// is never installed, so Installed apps only lists our product.
package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"tailscale.com/util/winutil/authenticode"
	"tsgui/internal/brand"
	"tsgui/internal/task"
	"tsgui/internal/tsdeploy"
	"tsgui/internal/update"
	"tsgui/internal/winx"
)

// Command-line flags understood by main.
const (
	FlagQuiet             = "--quiet"
	FlagUninstall         = "--uninstall"
	FlagUpdate            = "--update"             // run by the scheduled task as SYSTEM
	FlagRegister          = "--register"           // internal: deploy tailscaled and refresh registry after a GUI update
	FlagInstallElevated   = "--install-elevated"   // internal
	FlagUninstallElevated = "--uninstall-elevated" // internal
)

const (
	uninstallRoot = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`
	runKey        = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	// Earlier versions turned off the official updater here.
	legacyPolicyKey = `SOFTWARE\Policies\Tailscale`
)

// Dir is the install directory, %ProgramFiles%\<dirName>.
func Dir(c *brand.Config) string {
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		base = os.Getenv("ProgramFiles")
	}
	return filepath.Join(base, c.Install.DirName)
}

// Exe is the installed GUI/updater EXE.
func Exe(c *brand.Config) string { return filepath.Join(Dir(c), c.ExeName) }

// MachineLogDir holds installer and updater logs (%ProgramData%\<dirName>).
func MachineLogDir(c *brand.Config) string {
	return filepath.Join(os.Getenv("ProgramData"), c.Install.DirName)
}

// StateDir is where tailscaled keeps the login state and its logs
// (%ProgramData%\<appID>, hard-coded into it by tools/build).
func StateDir(c *brand.Config) string {
	return filepath.Join(os.Getenv("ProgramData"), c.ServiceName())
}

// UserLogDir holds the GUI log of the current user.
func UserLogDir(c *brand.Config) string {
	base, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		base = os.Getenv("LOCALAPPDATA")
	}
	return filepath.Join(base, c.Install.DirName)
}

// IsInstalledCopy reports whether the running EXE is the installed one.
func IsInstalledCopy(c *brand.Config) bool {
	self, err := os.Executable()
	return err == nil && strings.EqualFold(filepath.Clean(self), filepath.Clean(Exe(c)))
}

func serviceSettings(c *brand.Config) tsdeploy.Service {
	return tsdeploy.Service{
		Name:        c.ServiceName(),
		Exe:         c.DaemonExe(),
		DisplayName: c.Install.ServiceDisplayName,
		Description: c.Install.ServiceDescription,
	}
}

// deployBundle replaces the official tailscaled that earlier versions
// deployed, then installs or updates the one bundled in this EXE and the
// install.extraFiles.
func deployBundle(c *brand.Config) error {
	if err := tsdeploy.RemoveLegacy(Dir(c), StateDir(c), log.Printf); err != nil {
		return err
	}
	unblockOfficialUpdater()
	return errors.Join(
		tsdeploy.Deploy(Dir(c), serviceSettings(c), log.Printf),
		tsdeploy.DeployExtras(Dir(c), log.Printf),
	)
}

// Setup runs when the user starts the downloaded EXE. It elevates once and
// does the whole installation in the elevated child.
func Setup(c *brand.Config, quiet bool) error {
	if winx.IsElevated() {
		return InstallElevated(c, quiet)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{FlagInstallElevated}
	if quiet {
		args = append(args, FlagQuiet)
	}
	code, err := winx.RunElevated(self, args)
	if err != nil {
		return err
	}
	if code != 0 {
		return errSilent // the elevated child already reported it
	}
	return nil
}

var errSilent = errors.New("installation failed")

// IsSilent reports whether err was already shown to the user.
func IsSilent(err error) bool { return errors.Is(err, errSilent) }

// InstallElevated performs the installation. Needs administrator rights.
func InstallElevated(c *brand.Config, quiet bool) error {
	if err := removeOfficialMSI(c, quiet); err != nil {
		return err
	}

	dir, dst := Dir(c), Exe(c)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	winx.KillOthersAt(dst, true) // an older version may be running for any user
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(dst)) {
		log.Printf("copying %s -> %s", self, dst)
		if err := copyFile(self, dst+".new"); err != nil {
			return err
		}
		os.Remove(dst + ".old")
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, dst+".old"); err != nil {
				return err
			}
		}
		if err := os.Rename(dst+".new", dst); err != nil {
			return err
		}
		os.Remove(dst + ".old")
	}

	if err := deployBundle(c); err != nil {
		return err
	}
	if err := Register(c, dst); err != nil {
		return err
	}
	// tailscaled is updated together with the GUI, so without baseURL
	// there is nothing to check for.
	interval := c.CheckInterval()
	if c.Update.BaseURL == "" {
		interval = 0
	}
	if err := task.Register(c.AppID, "Keeps "+c.ProductName+" up to date.", dst, FlagUpdate, interval); err != nil {
		return err
	}
	log.Printf("installed %s %s with %s %s", c.ProductName, c.Version, c.DaemonExe(), tsdeploy.FileVersion(filepath.Join(dir, c.DaemonExe())))
	return nil
}

// unblockOfficialUpdater removes the InstallUpdates=never policy that earlier
// versions set for the official tailscaled, so it doesn't affect an official
// Tailscale installed later. Ours is built without that updater.
func unblockOfficialUpdater() {
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, legacyPolicyKey, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
		if v, _, _ := k.GetStringValue("InstallUpdates"); v == "never" {
			k.DeleteValue("InstallUpdates")
		}
		k.Close()
	}
}

// Refresh runs in the new EXE after a GUI self-update (as SYSTEM): it
// deploys the tailscaled bundled in it and refreshes Installed apps.
func Refresh(c *brand.Config) error {
	return errors.Join(deployBundle(c), Register(c, Exe(c)))
}

// Register writes the Installed apps entry, the Run-at-login entry, the
// Start menu shortcut for all users and the install folder on PATH. Needs
// administrator rights.
func Register(c *brand.Config, exe string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallRoot+c.AppID, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()

	quoted := `"` + exe + `"`
	strs := map[string]string{
		"DisplayName":          c.Install.DisplayName,
		"DisplayIcon":          exe + ",0",
		"DisplayVersion":       c.Version,
		"Publisher":            c.Install.Publisher,
		"InstallLocation":      Dir(c),
		"UninstallString":      quoted + " " + FlagUninstall,
		"QuietUninstallString": quoted + " " + FlagUninstall + " " + FlagQuiet,
	}
	if _, _, err := k.GetStringValue("InstallDate"); err != nil {
		strs["InstallDate"] = time.Now().Format("20060102")
	}
	for name, v := range strs {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	k.SetDWordValue("NoModify", 1)
	k.SetDWordValue("NoRepair", 1)
	k.SetDWordValue("EstimatedSize", uint32(dirSize(Dir(c))/1024))

	lnk := shortcutPath(c)
	if old, _, err := k.GetStringValue("ShortcutPath"); err == nil && !strings.EqualFold(old, lnk) {
		os.Remove(old)
	}
	if _, err := os.Stat(lnk); err != nil {
		if err := createShortcut(lnk, exe, c.FileDescription); err != nil {
			log.Printf("shortcut: %v", err)
		}
	}
	k.SetStringValue("ShortcutPath", lnk)

	if err := addToPath(Dir(c)); err != nil {
		log.Printf("PATH: %v", err) // only the CLI needs it
	}

	run, _, err := registry.CreateKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer run.Close()
	if c.Install.RunAtLogin {
		return run.SetStringValue(c.AppID, quoted)
	}
	run.DeleteValue(c.AppID)
	return nil
}

func shortcutPath(c *brand.Config) string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_CommonPrograms, 0)
	if err != nil {
		dir = filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs`)
	}
	return filepath.Join(dir, c.Install.StartMenuName+".lnk")
}

func createShortcut(lnk, target, desc string) error {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut(%s);$s.TargetPath=%s;$s.WorkingDirectory=%s;$s.IconLocation=%s;$s.Description=%s;$s.Save()`,
		q(lnk), q(target), q(filepath.Dir(target)), q(target+",0"), q(desc))
	out, err := winx.HiddenCommand("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, winx.DecodeOEM(out))
	}
	return nil
}

// removeOfficialMSI uninstalls an existing official Tailscale MSI, whose
// service would otherwise clash with ours. Login state is kept.
func removeOfficialMSI(c *brand.Config, quiet bool) error {
	code := officialProductCode()
	if code == "" {
		return nil
	}
	if !quiet && !winx.Confirm(c.Install.DisplayName,
		"這台電腦已經安裝官方 Tailscale。\n\n要改由 "+c.ProductName+" 管理嗎？會先移除官方版（登入狀態會保留）。") {
		return errors.New("已取消安裝")
	}
	log.Printf("removing official Tailscale MSI %s", code)
	cmd := winx.HiddenCommand(filepath.Join(os.Getenv("SystemRoot"), `System32\msiexec.exe`), "/x", code, "/qn", "/norestart")
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !(errors.As(err, &ee) && ee.ExitCode() == 3010) {
			return fmt.Errorf("移除官方 Tailscale：%w", err)
		}
	}
	return nil
}

// officialProductCode finds the official Tailscale MSI in Installed apps.
func officialProductCode() string {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallRoot, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer root.Close()
	names, _ := root.ReadSubKeyNames(0)
	for _, n := range names {
		k, err := registry.OpenKey(root, n, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		name, _, _ := k.GetStringValue("DisplayName")
		pub, _, _ := k.GetStringValue("Publisher")
		msi, _, _ := k.GetIntegerValue("WindowsInstaller")
		k.Close()
		if name == "Tailscale" && strings.Contains(pub, "Tailscale") && msi == 1 && strings.HasPrefix(n, "{") {
			return n
		}
	}
	return ""
}

// Uninstall runs from Installed apps. It elevates and removes everything.
func Uninstall(c *brand.Config, quiet bool) {
	title := c.Install.DisplayName
	if !quiet && !winx.Confirm(title, "要解除安裝 "+c.ProductName+" 嗎？\n\n網路連線會中斷。") {
		return
	}
	var err error
	if winx.IsElevated() {
		err = UninstallElevated(c)
	} else {
		self, _ := os.Executable()
		var code uint32
		code, err = winx.RunElevated(self, []string{FlagUninstallElevated})
		if err == nil && code != 0 {
			err = fmt.Errorf("解除安裝失敗（代碼 %d），詳見 %s", code, filepath.Join(MachineLogDir(c), "install.log"))
		}
	}
	if quiet {
		return
	}
	if err != nil {
		winx.Error(title, err.Error())
		return
	}
	winx.Info(title, c.ProductName+" 已解除安裝。")
}

// UninstallElevated removes the task, the service, the files and the
// registry entries. Needs administrator rights.
func UninstallElevated(c *brand.Config) error {
	dir, exe := Dir(c), Exe(c)
	winx.KillOthersAt(exe, true)
	// A running extra file would keep the folder from being deleted.
	for _, name := range tsdeploy.ExtraFiles() {
		if strings.EqualFold(filepath.Ext(name), ".exe") {
			winx.KillOthersAt(filepath.Join(dir, name), true)
		}
	}

	var errs []error
	if err := task.Delete(c.AppID); err != nil {
		errs = append(errs, err)
	}
	if err := tsdeploy.Remove(dir, serviceSettings(c)); err != nil {
		errs = append(errs, err)
	}
	if err := removeFromPath(dir); err != nil {
		log.Printf("PATH: %v", err)
	}
	unblockOfficialUpdater()
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallRoot+c.AppID, registry.QUERY_VALUE); err == nil {
		if lnk, _, err := k.GetStringValue("ShortcutPath"); err == nil {
			os.Remove(lnk)
		}
		k.Close()
	}
	os.Remove(shortcutPath(c))
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE); err == nil {
		k.DeleteValue(c.AppID)
		k.Close()
	}
	registry.DeleteKey(registry.LOCAL_MACHINE, uninstallRoot+c.AppID)

	// The running EXE can't delete itself: remove everything else now and
	// let cmd.exe remove the folder once we have exited. Only our own logs
	// go from MachineLogDir: it may also be StateDir, whose login state is
	// kept like the official client does.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !IsInstalledCopy(c) || !strings.EqualFold(p, exe) {
			os.RemoveAll(p)
		}
	}
	logs := MachineLogDir(c)
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), `System32\cmd.exe`))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		CmdLine: fmt.Sprintf(`/c ping -n 4 127.0.0.1 >nul & rmdir /s /q "%s" & del /q "%s\install.log*" "%s\updater.log*" & rmdir "%s"`,
			dir, logs, logs, logs),
	}
	cmd.Start()
	log.Printf("uninstalled (errors: %v)", errors.Join(errs...))
	return errors.Join(errs...)
}

// RunUpdate is the scheduled task's job (as SYSTEM): update the GUI from
// update.baseURL. The new GUI brings its tailscaled along (see Refresh).
func RunUpdate(c *brand.Config) error {
	if !winx.SingleInstance(`Global\` + c.AppID + "-update") {
		log.Print("another update is running")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
	defer cancel()
	// Left by the previous GUI update; deletable once no old GUI runs it.
	update.CleanupOld(Exe(c))

	var errs []error
	// Normally done by Refresh already; this repairs a failed or damaged deployment.
	if need, err := tsdeploy.NeedsDeploy(Dir(c)); err != nil || need {
		if err := deployBundle(c); err != nil {
			errs = append(errs, err)
		}
	}

	if base := c.Update.BaseURL; base != "" {
		if err := updateGUI(ctx, c, base); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func updateGUI(ctx context.Context, c *brand.Config, base string) error {
	m, err := update.Fetch(ctx, base)
	if err != nil {
		return err
	}
	if !m.Newer(c.Version) {
		log.Printf("GUI %s is up to date", c.Version)
		return nil
	}
	var verify update.Verifier
	if s := c.Update.SignerSubject; s != "" {
		verify = func(p string) error { return authenticode.Verify(p, s) }
	}
	log.Printf("GUI %s -> %s", c.Version, m.Version)
	exe := Exe(c)
	if err := update.Apply(ctx, base, m, exe, verify); err != nil {
		return err
	}
	// Let the new version deploy its tailscaled and refresh Installed apps;
	// running GUIs notice the changed EXE and restart themselves.
	return exec.Command(exe, FlagRegister).Start()
}

func dirSize(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
