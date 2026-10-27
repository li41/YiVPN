// Command build reads branding.jsonc and produces the branded EXE in dist/.
//
//	go run ./tools/build [-config branding.jsonc] [-arch amd64|arm64]
//
// It:
//  1. copies the icons and a normalized brand.json into internal/brand/res
//     (embedded into the binary for the tray icon, menu text, install names);
//  2. builds tailscaled from the tailscale.com source in the module cache,
//     with every "Tailscale" name Windows shows replaced by appID (see
//     patch.go), and bundles it with wintun.dll into internal/tsdeploy/bundle;
//  3. writes cmd/tray/rsrc_windows_<arch>.syso with the EXE icon, version info
//     (FileDescription, ProductName, CompanyName...) and manifest;
//  4. runs go build with -o dist/<exeName> (and signs it if -sign is given);
//  5. writes dist/latest.json for the GUI self-updater.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
	"tsgui/internal/brand"
	"tsgui/internal/update"
)

var (
	configPath = flag.String("config", "branding.jsonc", "branding config file")
	arch       = flag.String("arch", "amd64", "target architecture: amd64 or arm64")
	outDir     = flag.String("out", "dist", "output directory")
	signCmd    = flag.String("sign", "", `code-signing command; the EXE path is appended, e.g. -sign "signtool sign /a /fd sha256 /tr http://timestamp.digicert.com /td sha256"`)
)

const (
	genDir  = "internal/brand/res"
	mainPkg = "cmd/tray"
)

func main() {
	log.SetFlags(0)
	flag.Parse()

	if *arch != "amd64" && *arch != "arm64" {
		log.Fatalf("-arch 只能是 amd64 或 arm64")
	}
	cfg, err := brand.Load(*configPath)
	if err != nil {
		log.Fatalf("設定檔錯誤：\n%v", err)
	}
	cfgDir := filepath.Dir(*configPath)

	step("寫入品牌資料 %s", genDir)
	must(writeGenerated(cfg, cfgDir))

	must(buildDaemon(cfg, cfgDir))

	syso := filepath.Join(mainPkg, "rsrc_windows_"+*arch+".syso")
	step("產生 Windows 資源 %s", syso)
	must(writeSyso(cfg, cfgDir, syso))

	exe := filepath.Join(*outDir, cfg.ExeName)
	step("編譯 %s", exe)
	must(os.MkdirAll(*outDir, 0o755))
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-H windowsgui -s -w", "-o", exe, "./"+mainPkg)
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH="+*arch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	must(cmd.Run())

	if *signCmd != "" {
		step("簽章 %s", exe)
		sign := exec.Command("cmd.exe")
		sign.SysProcAttr = &syscall.SysProcAttr{CmdLine: fmt.Sprintf(`/c %s "%s"`, *signCmd, exe)}
		sign.Stdout, sign.Stderr = os.Stdout, os.Stderr
		must(sign.Run())
	}

	step("寫入更新資訊 %s", filepath.Join(*outDir, update.ManifestName))
	must(writeManifest(cfg, exe))

	fmt.Printf("\n完成：%s（%s %s）\n", exe, cfg.ProductName, cfg.Version)
	if cfg.Update.BaseURL != "" {
		fmt.Printf("發布更新：把 %s 與 %s 上傳到 %s/\n", cfg.ExeName, update.ManifestName, cfg.Update.BaseURL)
	}
}

func writeGenerated(cfg *brand.Config, cfgDir string) error {
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return err
	}
	for name, src := range cfg.IconFiles() {
		b, err := os.ReadFile(resolve(cfgDir, src))
		if err != nil {
			return err
		}
		if _, err := winres.LoadICO(bytes.NewReader(b)); err != nil {
			return fmt.Errorf("%s 不是有效的 .ico 檔：%w", src, err)
		}
		if err := os.WriteFile(filepath.Join(genDir, name), b, 0o644); err != nil {
			return err
		}
	}
	j, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(genDir, "brand.json"), append(j, '\n'), 0o644)
}

func writeSyso(cfg *brand.Config, cfgDir, path string) error {
	// Only one architecture's .syso may be present, or the linker sees
	// duplicate resources when cross-compiling.
	old, _ := filepath.Glob(filepath.Join(mainPkg, "rsrc_windows_*.syso"))
	for _, o := range old {
		os.Remove(o)
	}
	return writeResources(path, resources{
		icon:        resolve(cfgDir, cfg.Icons.App),
		version:     cfg.Version,
		description: cfg.FileDescription,
		product:     cfg.ProductName,
		company:     cfg.CompanyName,
		copyright:   cfg.Copyright,
		filename:    cfg.ExeName,
		internal:    cfg.AppID,
		gui:         true,
	})
}

// resources is what an EXE shows in Explorer and Task Manager.
type resources struct {
	icon, version                            string
	description, product, company, copyright string
	filename, internal                       string
	gui                                      bool // common controls v6 and DPI awareness
}

func writeResources(path string, r resources) error {
	rs := &winres.ResourceSet{}

	f, err := os.Open(r.icon)
	if err != nil {
		return err
	}
	defer f.Close()
	icon, err := winres.LoadICO(f)
	if err != nil {
		return err
	}
	// Explorer, Task Manager and the Installed apps list use the first icon group.
	if err := rs.SetIcon(winres.Name("APP"), icon); err != nil {
		return err
	}

	vi := version.Info{}
	vi.SetFileVersion(r.version)
	vi.SetProductVersion(r.version)
	for k, v := range map[string]string{
		version.FileDescription:  r.description,
		version.ProductName:      r.product,
		version.CompanyName:      r.company,
		version.LegalCopyright:   r.copyright,
		version.OriginalFilename: r.filename,
		version.InternalName:     r.internal,
	} {
		if v == "" {
			continue
		}
		if err := vi.Set(version.LangDefault, k, v); err != nil {
			return err
		}
	}
	rs.SetVersionInfo(vi)

	m := winres.AppManifest{
		Description:    r.product,
		Compatibility:  winres.Win10AndAbove,
		ExecutionLevel: winres.AsInvoker,
	}
	if r.gui {
		m.DPIAwareness = winres.DPIPerMonitorV2
		m.UseCommonControlsV6 = true
	}
	rs.SetManifest(m)

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	return rs.WriteObject(out, winres.Arch(*arch))
}

func writeManifest(cfg *brand.Config, exe string) error {
	f, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	m := update.Manifest{
		Version: cfg.Version,
		File:    cfg.ExeName,
		SHA256:  hex.EncodeToString(h.Sum(nil)),
	}
	j, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*outDir, update.ManifestName), append(j, '\n'), 0o644)
}

func step(format string, args ...any) { log.Printf("==> "+format, args...) }

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// resolve makes p relative to the config file unless it is absolute.
func resolve(cfgDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cfgDir, p)
}
