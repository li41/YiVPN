//go:build windows

package install

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The machine-wide PATH, like the official MSI uses for tailscale.exe.
const envKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`

// addToPath appends dir to the machine PATH so the CLI runs from any new
// console. It does nothing if dir is already there.
func addToPath(dir string) error {
	return editPath(func(parts []string) []string {
		if slices.ContainsFunc(parts, func(p string) bool { return samePathEntry(p, dir) }) {
			return parts
		}
		return append(parts, dir)
	})
}

// removeFromPath takes dir out of the machine PATH.
func removeFromPath(dir string) error {
	return editPath(func(parts []string) []string {
		return slices.DeleteFunc(slices.Clone(parts), func(p string) bool { return samePathEntry(p, dir) })
	})
}

func editPath(edit func([]string) []string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, envKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	cur, typ, err := k.GetStringValue("Path") // unexpanded for REG_EXPAND_SZ
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	var parts []string
	for _, p := range strings.Split(cur, ";") {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	next := edit(parts)
	if slices.Equal(next, parts) {
		return nil
	}
	v := strings.Join(next, ";")
	if typ == registry.SZ {
		err = k.SetStringValue("Path", v)
	} else {
		err = k.SetExpandStringValue("Path", v)
	}
	if err != nil {
		return err
	}
	broadcastEnvChange()
	return nil
}

// samePathEntry compares a PATH entry, which may use %VARIABLES%, with dir.
func samePathEntry(entry, dir string) bool {
	if e, err := registry.ExpandString(entry); err == nil {
		entry = e
	}
	clean := func(p string) string { return strings.TrimRight(filepath.Clean(strings.TrimSpace(p)), `\`) }
	return strings.EqualFold(clean(entry), clean(dir))
}

var procSendMessageTimeoutW = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// broadcastEnvChange tells Explorer to reload the environment, so consoles
// opened from now on see the new PATH without signing out.
func broadcastEnvChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	env, _ := windows.UTF16PtrFromString("Environment")
	var result uintptr
	procSendMessageTimeoutW.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}
