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
