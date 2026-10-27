//go:build windows

// Package tsdeploy installs and updates tailscaled from the copy bundled
// inside the GUI EXE. tools/build builds it from the Tailscale source with
// every Windows-visible "Tailscale" name replaced by appID, so updating the
// GUI also updates tailscaled and nothing is ever downloaded from Tailscale.
// Everything here needs administrator rights.
package tsdeploy

import (
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"tsgui/internal/winx"
)

// bundle holds manifest.json and the gzipped files listed in it, written by
// tools/build. The checked-in directory only has a README, so a plain
// `go build` compiles but can't install.
//
//go:embed bundle
var bundle embed.FS

// manifest must match the one written by tools/build.
type manifest struct {
	Version string       `json:"version"`
	Files   []bundleFile `json:"files"`  // tailscaled and wintun.dll: replaced with the service stopped
	Extras  []bundleFile `json:"extras"` // the CLI, notices and install.extraFiles
}

type bundleFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

var loadManifest = sync.OnceValues(func() (*manifest, error) {
	b, err := bundle.ReadFile("bundle/manifest.json")
	if err != nil {
		return nil, errors.New("這個 EXE 沒有內建服務程式，請用 build.cmd 打包")
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("bundle/manifest.json: %w", err)
	}
	return &m, nil
})

// BundledVersion returns the Tailscale version of the bundled tailscaled.
func BundledVersion() string {
	if m, err := loadManifest(); err == nil {
		return m.Version
	}
	return ""
}

// Service describes the tailscaled Windows service.
type Service struct {
	Name        string // internal name, hard-coded into our tailscaled
	Exe         string // tailscaled's file name in the install folder
	DisplayName string
	Description string
}

// NeedsDeploy reports whether dir lacks a bundled file (including the extra
// files) or has another version of it.
func NeedsDeploy(dir string) (bool, error) {
	m, err := loadManifest()
	if err != nil {
		return false, err
	}
	return len(stale(dir, m.Files))+len(stale(dir, m.Extras)) > 0, nil
}

// ExtraFiles returns the names of the bundled files that aren't part of the
// service (the CLI, notices and install.extraFiles).
func ExtraFiles() []string {
	m, err := loadManifest()
	if err != nil {
		return nil
	}
	var names []string
	for _, f := range m.Extras {
		names = append(names, f.Name)
	}
	return names
}

func stale(dir string, files []bundleFile) []bundleFile {
	var out []bundleFile
	for _, f := range files {
		if !strings.EqualFold(fileSHA256(filepath.Join(dir, f.Name)), f.SHA256) {
			out = append(out, f)
		}
	}
	return out
}

func staleFiles(dir string) ([]string, error) {
	m, err := loadManifest()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range stale(dir, m.Files) {
		names = append(names, f.Name)
	}
	return names, nil
}

// DeployExtras writes the bundled files that aren't part of the service and
// differ into dir, without stopping the service.
// A running copy is renamed out of the way (Windows allows that) and its
// .old file removed on a later run.
func DeployExtras(dir string, logf func(string, ...any)) error {
	m, err := loadManifest()
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range m.Extras {
		os.Remove(filepath.Join(dir, f.Name+".old"))
	}
	for _, f := range stale(dir, m.Extras) {
		dst := filepath.Join(dir, f.Name)
		if err := extract(f.Name, f.SHA256, dst+".new"); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, dst+".old"); err != nil {
				os.Remove(dst + ".new")
				errs = append(errs, err)
				continue
			}
		}
		if err := os.Rename(dst+".new", dst); err != nil {
			os.Rename(dst+".old", dst)
			errs = append(errs, err)
			continue
		}
		os.Remove(dst + ".old")
		logf("installed %s", f.Name)
	}
	return errors.Join(errs...)
}

// Deploy writes the bundled files that differ into dir, then makes sure the
// service exists, is branded and is running. If the new version fails to
// start, the previous files are restored.
func Deploy(dir string, s Service, logf func(string, ...any)) error {
	m, err := loadManifest()
	if err != nil {
		return err
	}
	stale, err := staleFiles(dir)
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		return EnsureService(dir, s)
	}
	want := map[string]string{}
	for _, f := range m.Files {
		want[f.Name] = f.SHA256
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range stale {
		if err := extract(name, want[name], filepath.Join(dir, name+".new")); err != nil {
			return err
		}
	}

	logf("stopping service")
	if err := stopService(s.Name); err != nil {
		for _, name := range stale {
			os.Remove(filepath.Join(dir, name+".new"))
		}
		return err
	}
	var swapped []string
	rollback := func() {
		for _, name := range swapped {
			dst := filepath.Join(dir, name)
			os.Remove(dst)
			os.Rename(dst+".old", dst)
		}
	}
	for _, name := range stale {
		dst := filepath.Join(dir, name)
		os.Remove(dst + ".old")
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, dst+".old"); err != nil {
				rollback()
				return err
			}
		}
		swapped = append(swapped, name)
		if err := os.Rename(dst+".new", dst); err != nil {
			rollback()
			return err
		}
	}

	if err := EnsureService(dir, s); err != nil {
		logf("new version failed to start (%v); rolling back", err)
		stopService(s.Name)
		rollback()
		if err2 := EnsureService(dir, s); err2 != nil {
			return fmt.Errorf("%v；還原舊版也失敗：%v", err, err2)
		}
		return err
	}
	for _, name := range swapped {
		os.Remove(filepath.Join(dir, name+".old"))
	}
	logf("%s %s is running", s.Exe, m.Version)
	return nil
}

// extract un-gzips a bundled file to dst and checks its SHA-256.
func extract(name, want, dst string) error {
	f, err := bundle.Open("bundle/" + name + ".gz")
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), zr)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		err = fmt.Errorf("內建的 %s 已損毀", name)
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}

// EnsureService registers the service if needed (via tailscaled
// install-system-daemon), applies the branding and starts it.
func EnsureService(dir string, s Service) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	exe := filepath.Join(dir, s.Exe)
	svcH, err := m.OpenService(s.Name)
	if err != nil {
		if out, err := winx.HiddenCommand(exe, "install-system-daemon").CombinedOutput(); err != nil {
			return fmt.Errorf("建立服務：%v %s", err, out)
		}
		if svcH, err = m.OpenService(s.Name); err != nil {
			return fmt.Errorf("找不到「%s」服務（%s 內建的服務名稱不同？）：%w", s.Name, s.Exe, err)
		}
	}
	defer svcH.Close()

	c, err := svcH.Config()
	if err != nil {
		return err
	}
	if !samePath(c.BinaryPathName, exe) {
		return fmt.Errorf("「%s」服務屬於別的安裝（%s），請先移除它", s.Name, c.BinaryPathName)
	}
	if c.DisplayName != s.DisplayName || c.Description != s.Description {
		c.DisplayName, c.Description = s.DisplayName, s.Description
		if err := svcH.UpdateConfig(c); err != nil {
			return err
		}
	}

	st, err := svcH.Query()
	if err != nil {
		return err
	}
	if st.State != svc.Running {
		if err := svcH.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return fmt.Errorf("啟動服務：%w", err)
		}
	}
	return waitState(svcH, svc.Running, 30*time.Second)
}

// ServiceBinary returns the executable path of the named service, or "".
func ServiceBinary(name string) string {
	m, err := mgr.Connect()
	if err != nil {
		return ""
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return ""
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return ""
	}
	return unquoteExe(c.BinaryPathName)
}

// Remove unregisters the service using tailscaled's own uninstaller, which
// also runs its cleanup hooks. Login state in %ProgramData%\<name> is kept.
func Remove(dir string, s Service) error {
	return uninstall(s.Name, filepath.Join(dir, s.Exe))
}

func uninstall(name, exe string) error {
	if _, err := os.Stat(exe); err != nil {
		return nil
	}
	if !samePath(ServiceBinary(name), exe) {
		return nil // not ours
	}
	if out, err := winx.HiddenCommand(exe, "uninstall-system-daemon").CombinedOutput(); err != nil {
		return fmt.Errorf("移除服務：%v %s", err, out)
	}
	return nil
}

// legacyService is the official tailscaled's service name, used by earlier
// versions of this program, which deployed the official binaries.
const legacyService = "Tailscale"

// RemoveLegacy removes the official tailscaled that an earlier version
// deployed into dir, then carries the official login state over to stateDir
// so the device doesn't have to log in again.
func RemoveLegacy(dir, stateDir string, logf func(string, ...any)) error {
	old := filepath.Join(dir, "tailscaled.exe")
	if samePath(ServiceBinary(legacyService), old) {
		logf("removing the official tailscaled service")
		if err := stopService(legacyService); err != nil {
			return err
		}
		if err := uninstall(legacyService, old); err != nil {
			return err
		}
	}
	for _, name := range []string{"tailscaled.exe", "tailscale.exe"} {
		os.Remove(filepath.Join(dir, name))
	}
	return migrateState(stateDir, logf)
}

func migrateState(stateDir string, logf func(string, ...any)) error {
	if ServiceBinary(legacyService) != "" {
		return nil // still used by an official install; two devices must not share a key
	}
	dst := filepath.Join(stateDir, "server-state.conf")
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	src := filepath.Join(os.Getenv("ProgramData"), legacyService, "server-state.conf")
	b, err := os.ReadFile(src)
	if err != nil {
		return nil
	}
	// The file holds the device's private keys. tailscaled locks the folder
	// down when it starts; do it now so it is never readable by users.
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := lockDown(stateDir); err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return err
	}
	logf("copied login state from %s", src)
	return nil
}

// lockDown gives only SYSTEM and Administrators access to dir and its contents.
func lockDown(dir string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func stopService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return nil // not installed yet
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil || st.State == svc.Stopped {
		return err
	}
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return fmt.Errorf("停止服務：%w", err)
	}
	return waitState(s, svc.Stopped, 30*time.Second)
}

func waitState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if want == svc.Running && st.State == svc.Stopped && time.Since(start) > 3*time.Second {
			return errors.New("服務啟動後又停止了")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服務狀態逾時（目前 %d）", st.State)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func unquoteExe(cmdline string) string {
	cmdline = strings.TrimSpace(cmdline)
	if strings.HasPrefix(cmdline, `"`) {
		if i := strings.Index(cmdline[1:], `"`); i >= 0 {
			return cmdline[1 : i+1]
		}
	}
	if i := strings.Index(strings.ToLower(cmdline), ".exe"); i >= 0 {
		return cmdline[:i+4]
	}
	return cmdline
}

func samePath(cmdline, exe string) bool {
	return strings.EqualFold(filepath.Clean(unquoteExe(cmdline)), filepath.Clean(exe))
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FileVersion reads the fixed version resource of an EXE as "major.minor.patch".
func FileVersion(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return ""
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &n); err != nil || fixed == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff, fixed.FileVersionLS>>16)
}
