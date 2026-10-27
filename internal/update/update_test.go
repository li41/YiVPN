package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serve(t *testing.T, m Manifest, exe []byte) string {
	mux := http.NewServeMux()
	mux.HandleFunc("/"+ManifestName, func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(m) })
	mux.HandleFunc("/"+m.File, func(w http.ResponseWriter, r *http.Request) { w.Write(exe) })
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s.URL
}

func TestApply(t *testing.T) {
	newExe := []byte("new version")
	sum := sha256.Sum256(newExe)
	base := serve(t, Manifest{Version: "1.2.0", File: "App.exe", SHA256: hex.EncodeToString(sum[:])}, newExe)

	m, err := Fetch(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Newer("1.1.9") || m.Newer("1.2.0") || m.Newer("1.10.0") {
		t.Error("version comparison wrong")
	}

	exe := filepath.Join(t.TempDir(), "App.exe")
	os.WriteFile(exe, []byte("old version"), 0o644)
	if err := Apply(context.Background(), base, m, exe, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new version" {
		t.Errorf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old version" {
		t.Errorf("old = %q", b)
	}
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Error("CleanupOld left the old exe")
	}
}

func TestApplyRejectsBadHash(t *testing.T) {
	base := serve(t, Manifest{Version: "9.0.0", File: "App.exe", SHA256: strings.Repeat("0", 64)}, []byte("tampered"))
	m, err := Fetch(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "App.exe")
	os.WriteFile(exe, []byte("old version"), 0o644)
	if err := Apply(context.Background(), base, m, exe, nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("want SHA-256 error, got %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old version" {
		t.Error("exe must be untouched after a failed update")
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Error(".new should be removed")
	}
}
