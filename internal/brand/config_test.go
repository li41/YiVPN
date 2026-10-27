package brand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadShippedConfig(t *testing.T) {
	c, err := Load("../../branding.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	if c.ProductName == "" || c.Install.DisplayName == "" || c.Tailscale.AdminConsoleURL == "" {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.jsonc")
	os.WriteFile(path, []byte(`{
		// comment and trailing comma are fine
		"appID": "bad id!",
		"productName": "X",
		"exeName": "x.dll",
		"version": "1.0",
		"icons": {},
	}`), 0o644)
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"appID", "exeName", "version", "icons"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s: %v", want, err)
		}
	}
}

func TestExtraFiles(t *testing.T) {
	c := &Config{AppID: "MyVPN", ProductName: "MyVPN", ExeName: "MyVPN.exe", Version: "1.0.0",
		Icons: Icons{App: "a", Connected: "a", Disconnected: "a", Connecting: "a", ExitNode: "a"}}
	c.applyDefaults()
	c.Install.ExtraFiles = []string{"tools/Yi.exe", `other\readme.txt`}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid extra files rejected: %v", err)
	}
	for _, bad := range [][]string{
		{"x/MyVPN.exe"},          // the GUI itself
		{"MyVPN-service.exe"},    // tailscaled
		{"MyVPN-cli.exe"},        // the CLI
		{"WINTUN.DLL"},           // case-insensitive
		{"a/Yi.exe", "b/yi.exe"}, // same name twice
		{""},
	} {
		c.Install.ExtraFiles = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "extraFiles") {
			t.Errorf("extraFiles %q: err = %v, want an extraFiles error", bad, err)
		}
	}
}
