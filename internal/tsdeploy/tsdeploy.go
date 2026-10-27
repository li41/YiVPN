//go:build windows

// Package tsdeploy installs and updates the official, Tailscale-signed
// tailscaled without registering the Tailscale MSI with Windows Installer, so
// no "Tailscale" entry appears in Installed apps.
//
// The latest MSI is downloaded from pkgs.tailscale.com, its Authenticode
// signature is checked, and its files are unpacked with an administrative
// install (msiexec /a), which copies files without installing anything.
// tailscaled.exe then registers its own Windows service, the same way the MSI
// does. Everything here needs administrator rights.
package tsdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"tailscale.com/util/cmpver"
	"tailscale.com/util/winutil/authenticode"
	"tsgui/internal/winx"
)

// ServiceName is hard-coded in tailscaled; only the display name can change.
const ServiceName = "Tailscale"

// files are copied from the MSI, keyed by the expected Authenticode subject.
// tailscale-ipn.exe (the official GUI) is deliberately left out.
var files = map[string]string{
	"tailscaled.exe": "Tailscale Inc.",
	"tailscale.exe":  "Tailscale Inc.",
	"wintun.dll":     "WireGuard LLC",
}

// Service holds the branding applied to the Windows service.
type Service struct {
	DisplayName string
	Description string
}

// Latest is the newest Windows release on a track.
type Latest struct {
	Version string
	MSI     string // file name on pkgs.tailscale.com/<track>/
	Track   string
}

const pkgsBase = "https://pkgs.tailscale.com/"

// FetchLatest asks pkgs.tailscale.com for the newest MSI on track.
func FetchLatest(ctx context.Context, track string) (*Latest, error) {
	var j struct {
		MSIs        map[string]string
		MSIsVersion string
	}
	if err := getJSON(ctx, pkgsBase+track+"/?mode=json&os=windows", &j); err != nil {
		return nil, fmt.Errorf("查詢最新版 Tailscale：%w", err)
	}
	arch := runtime.GOARCH
	if arch == "386" {
		arch = "x86"
	}
	name := j.MSIs[arch]
	if name == "" || j.MSIsVersion == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("pkgs.tailscale.com 沒有 %s 的 Windows 版本", arch)
	}
	return &Latest{Version: j.MSIsVersion, MSI: name, Track: track}, nil
}

// InstalledVersion returns the version of dir\tailscaled.exe, or "" if absent.
func InstalledVersion(dir string) string {
	return fileVersion(filepath.Join(dir, "tailscaled.exe"))
}

// NeedsUpdate reports whether dir lacks tailscaled or has an older one.
func (l *Latest) NeedsUpdate(dir string) bool {
	cur := InstalledVersion(dir)
	return cur == "" || cmpver.Compare(l.Version, cur) > 0
}

// Deploy downloads l, verifies it and swaps the binaries in dir, then makes
// sure the service exists, is branded and is running. If the new version
// fails to start, the previous files are restored.
func Deploy(ctx context.Context, l *Latest, dir string, s Service, logf func(string, ...any)) error {
	tmp, err := secureTempDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	msi := filepath.Join(tmp, l.MSI)
	logf("downloading %s", l.MSI)
	if err := download(ctx, pkgsBase+l.Track+"/"+l.MSI, msi); err != nil {
		return fmt.Errorf("下載 %s：%w", l.MSI, err)
	}
	if err := authenticode.Verify(msi, "Tailscale Inc."); err != nil {
		return fmt.Errorf("MSI 簽章驗證失敗：%w", err)
	}

	logf("unpacking")
	unpacked := filepath.Join(tmp, "x")
	if out, err := winx.HiddenCommand(msiexec(), "/a", msi, "/qn", "TARGETDIR="+unpacked).CombinedOutput(); err != nil {
		return fmt.Errorf("解開 MSI：%v %s", err, out)
	}
	found := map[string]string{}
	filepath.WalkDir(unpacked, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if _, ok := files[strings.ToLower(d.Name())]; ok {
				found[strings.ToLower(d.Name())] = p
			}
		}
		return nil
	})
	for name, subject := range files {
		p, ok := found[name]
		if !ok {
			return fmt.Errorf("MSI 裡找不到 %s（官方安裝包格式可能改了）", name)
		}
		if err := authenticode.Verify(p, subject); err != nil {
			return fmt.Errorf("%s 簽章驗證失敗：%w", name, err)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	logf("stopping service")
	if err := stopService(); err != nil {
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
	for name := range files {
		dst := filepath.Join(dir, name)
		os.Remove(dst + ".old")
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, dst+".old"); err != nil {
				rollback()
				return err
			}
		}
		swapped = append(swapped, name)
		if err := copyFile(found[name], dst); err != nil {
			rollback()
			return err
		}
	}

	if err := EnsureService(dir, s); err != nil {
		logf("new version failed to start (%v); rolling back", err)
		stopService()
		rollback()
		if err2 := EnsureService(dir, s); err2 != nil {
			return fmt.Errorf("%v；還原舊版也失敗：%v", err, err2)
		}
		return err
	}
	for _, name := range swapped {
		os.Remove(filepath.Join(dir, name+".old"))
	}
	logf("tailscaled %s is running", l.Version)
	return nil
}

// EnsureService registers the service if needed (via tailscaled.exe
// install-system-daemon), applies the branding and starts it.
func EnsureService(dir string, s Service) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	exe := filepath.Join(dir, "tailscaled.exe")
	svcH, err := m.OpenService(ServiceName)
	if err != nil {
		if out, err := winx.HiddenCommand(exe, "install-system-daemon").CombinedOutput(); err != nil {
			return fmt.Errorf("建立服務：%v %s", err, out)
		}
		if svcH, err = m.OpenService(ServiceName); err != nil {
			return err
		}
	}
	defer svcH.Close()

	c, err := svcH.Config()
	if err != nil {
		return err
	}
	if !samePath(c.BinaryPathName, exe) {
		return fmt.Errorf("「%s」服務屬於別的安裝（%s），請先移除它", ServiceName, c.BinaryPathName)
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

// ServiceBinary returns the executable path of the Tailscale service, or "".
func ServiceBinary() string {
	m, err := mgr.Connect()
	if err != nil {
		return ""
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
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
// also runs its cleanup hooks. Login state in %ProgramData%\Tailscale is kept.
func Remove(dir string) error {
	exe := filepath.Join(dir, "tailscaled.exe")
	if _, err := os.Stat(exe); err != nil {
		return nil
	}
	if !samePath(ServiceBinary(), exe) {
		return nil // not ours
	}
	if out, err := winx.HiddenCommand(exe, "uninstall-system-daemon").CombinedOutput(); err != nil {
		return fmt.Errorf("移除服務：%v %s", err, out)
	}
	return nil
}

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
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

// secureTempDir creates a directory under %SystemRoot%\Temp. Files that an
// elevated process creates there can't be modified by standard users, so the
// verified MSI can't be swapped before it is unpacked.
func secureTempDir() (string, error) {
	return os.MkdirTemp(filepath.Join(os.Getenv("SystemRoot"), "Temp"), "tsdeploy-")
}

func msiexec() string { return filepath.Join(os.Getenv("SystemRoot"), `System32\msiexec.exe`) }

// fileVersion reads the fixed version resource as "major.minor.patch".
func fileVersion(path string) string {
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

var httpClient = &http.Client{Timeout: 15 * time.Minute}

func getJSON(ctx context.Context, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

func download(ctx context.Context, url, dst string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
