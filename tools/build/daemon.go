package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tailscale.com/util/winutil/authenticode"
	"tsgui/internal/brand"
)

const (
	workDir   = "_build" // scratch space; the leading _ keeps it out of ./...
	bundleDir = "internal/tsdeploy/bundle"

	// Official Wintun driver, as shipped in Tailscale's MSI. It must stay
	// signed by WireGuard LLC, so it is downloaded rather than built.
	wintunURL    = "https://www.wintun.net/builds/wintun-0.14.1.zip"
	wintunSHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
)

// bundleManifest must match the one read by internal/tsdeploy.
type bundleManifest struct {
	Version string       `json:"version"`
	Files   []bundleFile `json:"files"`  // replaced with the service stopped
	Extras  []bundleFile `json:"extras"` // CLI, notices, install.extraFiles
}

type bundleFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// buildDaemon builds the rebranded tailscaled and CLI from the tailscale.com
// version in go.mod and writes them, wintun.dll, the license notices and
// install.extraFiles into internal/tsdeploy/bundle, which the GUI EXE embeds.
func buildDaemon(cfg *brand.Config, cfgDir string) error {
	tsDir, tsVer, err := tailscaleModule()
	if err != nil {
		return err
	}
	src := filepath.Join(workDir, "tailscale")
	out := filepath.Join(workDir, "out")
	os.RemoveAll(src)
	os.RemoveAll(out)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	step("複製 Tailscale %s 原始碼到 %s", tsVer, src)
	if err := copyTree(tsDir, src); err != nil {
		return err
	}
	step("把 Tailscale 名稱改成 %s", cfg.ServiceName())
	if err := applyPatches(src, patches(cfg)); err != nil {
		return err
	}
	cmds := []struct {
		pkg, exe, description string
	}{
		{"tailscaled", cfg.DaemonExe(), cfg.Install.ServiceDisplayName},
		{"tailscale", cfg.CLIExe(), cfg.ProductName + " CLI"},
	}
	for _, c := range cmds {
		exe := filepath.Join(out, c.exe)
		step("編譯 %s", exe)
		pkg := filepath.Join(src, "cmd", c.pkg)
		if err := writeCmdSyso(pkg, resources{
			icon:        resolve(cfgDir, cfg.Icons.App),
			version:     tsVer,
			description: c.description,
			product:     cfg.ProductName,
			company:     cfg.CompanyName,
			copyright:   cfg.Copyright,
			filename:    c.exe,
			internal:    strings.TrimSuffix(c.exe, ".exe"),
		}); err != nil {
			return err
		}
		if err := goBuild(src, "./cmd/"+c.pkg, exe, tsVer); err != nil {
			return err
		}
	}
	daemon, cli := filepath.Join(out, cfg.DaemonExe()), filepath.Join(out, cfg.CLIExe())

	step("取得 wintun.dll")
	wintun, wintunLicense, err := fetchWintun(out)
	if err != nil {
		return err
	}

	tsLicense, err := os.ReadFile(filepath.Join(tsDir, "LICENSE"))
	if err != nil {
		return err
	}
	notices := filepath.Join(out, "THIRD-PARTY-NOTICES.txt")
	text := cfg.DaemonExe() + " and " + cfg.CLIExe() + " are built from Tailscale " + tsVer + " (https://github.com/tailscale/tailscale).\r\n\r\n" +
		crlf(tsLicense) + "\r\n\r\n----------------------------------------\r\n\r\n" +
		"wintun.dll is the unmodified Wintun driver (https://www.wintun.net).\r\n\r\n" + crlf(wintunLicense)
	if err := os.WriteFile(notices, []byte(text), 0o644); err != nil {
		return err
	}

	// Only the service's own files need it stopped to be replaced.
	extras := []string{cli, notices}
	for _, p := range cfg.Install.ExtraFiles {
		p = resolve(cfgDir, p)
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("install.extraFiles：找不到 %s", p)
		}
		step("加入額外檔案 %s", p)
		extras = append(extras, p)
	}

	step("寫入 %s", bundleDir)
	return writeBundle(tsVer, []string{daemon, wintun}, extras)
}

// goBuild builds a command from the patched Tailscale source.
func goBuild(src, pkg, out, tsVer string) error {
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	stamp := "-X tailscale.com/version.shortStamp=" + tsVer + " -X tailscale.com/version.longStamp=" + tsVer
	cmd := exec.Command("go", "build", "-trimpath",
		// _build is inside our git repo: without this our commit would be
		// stamped in and every build would differ, restarting the service
		// on every GUI update.
		"-buildvcs=false",
		"-tags", "ts_omit_clientupdate", // its updater would install the official MSI
		"-ldflags", "-s -w "+stamp, "-o", abs, pkg)
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH="+*arch, "CGO_ENABLED=0", "GOWORK=off")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// tailscaleModule returns the source directory and version of the
// tailscale.com module the GUI is built against.
func tailscaleModule() (dir, ver string, err error) {
	b, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}|{{.Version}}", "tailscale.com").Output()
	if err != nil {
		return "", "", fmt.Errorf("go list tailscale.com: %w", err)
	}
	dir, ver, ok := strings.Cut(strings.TrimSpace(string(b)), "|")
	if !ok || dir == "" {
		return "", "", fmt.Errorf("找不到 tailscale.com 原始碼（先執行 go mod download）：%q", b)
	}
	return dir, strings.TrimPrefix(ver, "v"), nil
}

// writeCmdSyso replaces upstream's manifest-only resources in pkg with ours:
// the same manifest plus version info and the app icon, so Task Manager and
// the file's Properties show our names.
func writeCmdSyso(pkg string, r resources) error {
	old, _ := filepath.Glob(filepath.Join(pkg, "*.syso"))
	for _, o := range old {
		if err := os.Remove(o); err != nil {
			return err
		}
	}
	return writeResources(filepath.Join(pkg, "rsrc_windows_"+*arch+".syso"), r)
}

// fetchWintun downloads (once) the pinned Wintun release, checks it and
// extracts this architecture's wintun.dll into dir.
func fetchWintun(dir string) (dll string, license []byte, err error) {
	zipPath := filepath.Join(workDir, filepath.Base(wintunURL))
	b, err := os.ReadFile(zipPath)
	if err != nil || sha256Hex(b) != wintunSHA256 {
		resp, err := http.Get(wintunURL)
		if err != nil {
			return "", nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", nil, fmt.Errorf("下載 %s：%s", wintunURL, resp.Status)
		}
		if b, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20)); err != nil {
			return "", nil, err
		}
		if got := sha256Hex(b); got != wintunSHA256 {
			return "", nil, fmt.Errorf("%s 的 SHA-256 不符（預期 %s，實際 %s）", wintunURL, wintunSHA256, got)
		}
		if err := os.WriteFile(zipPath, b, 0o644); err != nil {
			return "", nil, err
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", nil, err
	}
	read := func(name string) ([]byte, error) {
		f, err := zr.Open(name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		defer f.Close()
		return io.ReadAll(f)
	}
	data, err := read("wintun/bin/" + *arch + "/wintun.dll")
	if err != nil {
		return "", nil, err
	}
	if license, err = read("wintun/LICENSE.txt"); err != nil {
		return "", nil, err
	}
	dll = filepath.Join(dir, "wintun.dll")
	if err := os.WriteFile(dll, data, 0o644); err != nil {
		return "", nil, err
	}
	if err := authenticode.Verify(dll, "WireGuard LLC"); err != nil {
		return "", nil, fmt.Errorf("wintun.dll 簽章驗證失敗：%w", err)
	}
	return dll, license, nil
}

// writeBundle gzips files and extras into bundleDir and lists them in
// manifest.json. README.md stays; everything else there is regenerated.
func writeBundle(tsVer string, files, extras []string) error {
	entries, _ := os.ReadDir(bundleDir)
	for _, e := range entries {
		if e.Name() != "README.md" {
			os.Remove(filepath.Join(bundleDir, e.Name()))
		}
	}
	add := func(f string) (bundleFile, error) {
		b, err := os.ReadFile(f)
		if err != nil {
			return bundleFile{}, err
		}
		name := filepath.Base(f)
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		zw.Write(b)
		if err := zw.Close(); err != nil {
			return bundleFile{}, err
		}
		if err := os.WriteFile(filepath.Join(bundleDir, name+".gz"), gz.Bytes(), 0o644); err != nil {
			return bundleFile{}, err
		}
		return bundleFile{Name: name, SHA256: sha256Hex(b)}, nil
	}
	m := bundleManifest{Version: tsVer, Files: []bundleFile{}, Extras: []bundleFile{}}
	for _, f := range files {
		bf, err := add(f)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, bf)
	}
	for _, f := range extras {
		bf, err := add(f)
		if err != nil {
			return err
		}
		m.Extras = append(m.Extras, bf)
	}
	j, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bundleDir, "manifest.json"), append(j, '\n'), 0o644)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644) // module cache files are read-only
	})
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func crlf(b []byte) string {
	return strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n")
}
