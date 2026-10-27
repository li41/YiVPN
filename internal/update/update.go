// Package update implements self-update for the GUI EXE.
//
// A release is two files uploaded next to each other at update.baseURL:
// the EXE and latest.json (both produced by tools/build).
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"tailscale.com/util/cmpver"
)

// ManifestName is the file name of the release manifest.
const ManifestName = "latest.json"

// Manifest describes the newest release.
type Manifest struct {
	Version string `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

// Fetch downloads the manifest from baseURL.
func Fetch(ctx context.Context, baseURL string) (*Manifest, error) {
	body, err := get(ctx, baseURL+"/"+ManifestName)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("解析 %s：%w", ManifestName, err)
	}
	if m.Version == "" || m.File == "" || len(m.SHA256) != 64 || strings.ContainsAny(m.File, `/\`) {
		return nil, errors.New("latest.json 內容不完整")
	}
	return &m, nil
}

// Newer reports whether m is newer than the running version.
func (m *Manifest) Newer(current string) bool { return cmpver.Compare(m.Version, current) > 0 }

// Verifier optionally checks the downloaded file (e.g. Authenticode).
type Verifier func(path string) error

// Apply downloads the release to exePath+".new", checks its SHA-256 (and
// verify, if non-nil), then swaps it into exePath. Windows allows renaming a
// running EXE, so the old one is moved to exePath+".old" and removed by
// CleanupOld on the next start.
func Apply(ctx context.Context, baseURL string, m *Manifest, exePath string, verify Verifier) error {
	body, err := get(ctx, baseURL+"/"+url.PathEscape(m.File))
	if err != nil {
		return err
	}
	defer body.Close()

	tmp := exePath + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, m.SHA256) {
		os.Remove(tmp)
		return fmt.Errorf("SHA-256 不符（預期 %s，實際 %s）", m.SHA256, got)
	}
	if verify != nil {
		if err := verify(tmp); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("簽章驗證失敗：%w", err)
		}
	}

	old := exePath + ".old"
	os.Remove(old)
	if err := os.Rename(exePath, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exePath); err != nil {
		os.Rename(old, exePath)
		return err
	}
	return nil
}

// CleanupOld removes the previous EXE left behind by Apply.
func CleanupOld(exePath string) { os.Remove(exePath + ".old") }

func get(ctx context.Context, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp.Body, nil
}
