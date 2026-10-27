// Package brand holds the white-label settings that are baked into the EXE
// at build time (see branding.jsonc and tools/build).
package brand

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/tailscale/hujson"
)

// Config mirrors branding.jsonc.
type Config struct {
	AppID           string `json:"appID"`
	ProductName     string `json:"productName"`
	ExeName         string `json:"exeName"`
	FileDescription string `json:"fileDescription"`
	CompanyName     string `json:"companyName"`
	Copyright       string `json:"copyright"`
	Version         string `json:"version"`

	Icons     Icons     `json:"icons"`
	Install   Install   `json:"install"`
	Tailscale Tailscale `json:"tailscale"`
	Update    Update    `json:"update"`
}

// Icons are paths to .ico files, relative to the config file.
type Icons struct {
	App          string `json:"app"`
	Connected    string `json:"connected"`
	Disconnected string `json:"disconnected"`
	Connecting   string `json:"connecting"`
	ExitNode     string `json:"exitNode"`
}

type Install struct {
	DisplayName   string `json:"displayName"`
	Publisher     string `json:"publisher"`
	DirName       string `json:"dirName"`
	StartMenuName string `json:"startMenuName"`
	RunAtLogin    bool   `json:"runAtLogin"`

	ServiceDisplayName string `json:"serviceDisplayName"`
	ServiceDescription string `json:"serviceDescription"`
}

type Tailscale struct {
	AdminConsoleURL string `json:"adminConsoleURL"`
}

type Update struct {
	BaseURL       string `json:"baseURL"`
	CheckInterval string `json:"checkInterval"`
	SignerSubject string `json:"signerSubject"`
}

// IconFiles maps the file name used inside the EXE to the configured path.
func (c *Config) IconFiles() map[string]string {
	return map[string]string{
		"app.ico":          c.Icons.App,
		"connected.ico":    c.Icons.Connected,
		"disconnected.ico": c.Icons.Disconnected,
		"connecting.ico":   c.Icons.Connecting,
		"exitnode.ico":     c.Icons.ExitNode,
	}
}

// CheckInterval returns the parsed GUI update interval.
func (c *Config) CheckInterval() time.Duration {
	d, err := time.ParseDuration(c.Update.CheckInterval)
	if err != nil || d < time.Minute {
		return 6 * time.Hour
	}
	return d
}

// Load reads a HuJSON (JSON with comments) config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(std)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.FileDescription == "" {
		c.FileDescription = c.ProductName
	}
	if c.Install.DisplayName == "" {
		c.Install.DisplayName = c.ProductName
	}
	if c.Install.Publisher == "" {
		c.Install.Publisher = c.CompanyName
	}
	if c.Install.DirName == "" {
		c.Install.DirName = c.AppID
	}
	if c.Install.StartMenuName == "" {
		c.Install.StartMenuName = c.ProductName
	}
	if c.Install.ServiceDisplayName == "" {
		c.Install.ServiceDisplayName = c.ProductName + " Service"
	}
	if c.Tailscale.AdminConsoleURL == "" {
		c.Tailscale.AdminConsoleURL = "https://login.tailscale.com/admin/machines"
	}
	c.Update.BaseURL = strings.TrimRight(c.Update.BaseURL, "/")
}

var (
	appIDRe   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(\.\d+)?$`)
	badPathRe = regexp.MustCompile(`[<>:"/\\|?*]`)
)

// Validate reports the first problem found in c.
func (c *Config) Validate() error {
	var errs []error
	if !appIDRe.MatchString(c.AppID) {
		errs = append(errs, errors.New(`appID 只能用英數字、底線、連字號`))
	}
	if c.ProductName == "" {
		errs = append(errs, errors.New("productName 不能是空的"))
	}
	if !strings.EqualFold(".exe", lastExt(c.ExeName)) || badPathRe.MatchString(c.ExeName) {
		errs = append(errs, errors.New("exeName 必須是合法檔名且以 .exe 結尾"))
	}
	if !versionRe.MatchString(c.Version) {
		errs = append(errs, errors.New("version 必須是 x.y.z 格式"))
	}
	if badPathRe.MatchString(c.Install.DirName) || badPathRe.MatchString(c.Install.StartMenuName) {
		errs = append(errs, errors.New("install.dirName / install.startMenuName 含有不能用在檔名的字元"))
	}
	for name, p := range c.IconFiles() {
		if p == "" {
			errs = append(errs, fmt.Errorf("icons 缺少 %s", strings.TrimSuffix(name, ".ico")))
		}
	}
	if u := c.Update.BaseURL; u != "" && !strings.HasPrefix(u, "https://") && !isLoopbackHTTP(u) {
		errs = append(errs, errors.New("update.baseURL 必須是 https:// 開頭（測試時可用 http://127.0.0.1）"))
	}
	return errors.Join(errs...)
}

func lastExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// isLoopbackHTTP allows plain http only to this machine, for testing updates.
func isLoopbackHTTP(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1:") || strings.HasPrefix(u, "http://127.0.0.1/") ||
		strings.HasPrefix(u, "http://localhost:") || strings.HasPrefix(u, "http://localhost/")
}
