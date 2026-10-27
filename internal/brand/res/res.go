// Package res embeds the branding chosen at build time. brand.json and the
// .ico files in this directory are rewritten by tools/build from
// branding.jsonc; the checked-in copies hold the defaults so a plain
// `go build` still works.
package res

import (
	"embed"
	"encoding/json"
	"sync"

	"tsgui/internal/brand"
)

//go:embed brand.json *.ico
var files embed.FS

var current = sync.OnceValue(func() *brand.Config {
	raw, err := files.ReadFile("brand.json")
	if err != nil {
		panic(err)
	}
	var c brand.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		panic(err)
	}
	return &c
})

// Current returns the branding compiled into this binary.
func Current() *brand.Config { return current() }

// Icon returns one of the embedded .ico files ("app", "connected",
// "disconnected", "connecting" or "exitnode").
func Icon(name string) []byte {
	b, err := files.ReadFile(name + ".ico")
	if err != nil {
		panic(err)
	}
	return b
}
