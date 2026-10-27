//go:build windows

// Package winx wraps the handful of Win32 calls the app needs.
package winx

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	mbOK          = 0x0
	mbYesNo       = 0x4
	mbIconError   = 0x10
	mbIconQuest   = 0x20
	mbIconInfo    = 0x40
	mbSetFg       = 0x10000
	mbTopMost     = 0x40000
	idYes         = 6
	swShowNormal  = 1
	swHide        = 0
	createNoWindw = 0x08000000
)

func msgBox(title, text string, flags uint32) int32 {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	r, _ := windows.MessageBox(0, t, c, flags|mbSetFg|mbTopMost)
	return r
}

// Info shows an information dialog.
func Info(title, text string) { msgBox(title, text, mbOK|mbIconInfo) }

// Error shows an error dialog.
func Error(title, text string) { msgBox(title, text, mbOK|mbIconError) }

// Confirm shows a Yes/No dialog and reports whether Yes was chosen.
func Confirm(title, text string) bool { return msgBox(title, text, mbYesNo|mbIconQuest) == idYes }

// OpenURL opens url (or a file/folder) with the default handler.
func OpenURL(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	u, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, u, nil, nil, swShowNormal)
}

// HiddenCommand is exec.Command without a console window flashing up.
func HiddenCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindw}
	return cmd
}

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// ErrCancelled is returned when the user declines the UAC prompt.
var ErrCancelled = errors.New("使用者取消了系統管理員權限要求")

// RunElevated starts exe with a UAC prompt, waits for it and returns its exit code.
func RunElevated(exe string, args []string) (uint32, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(JoinArgs(args))
	if err != nil {
		return 0, err
	}
	sei := shellExecuteInfo{
		fMask:        0x40 | 0x100, // SEE_MASK_NOCLOSEPROCESS | SEE_MASK_NOASYNC
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        swHide,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	if r, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, ErrCancelled
		}
		return 0, err
	}
	defer windows.CloseHandle(sei.hProcess)
	if _, err := windows.WaitForSingleObject(sei.hProcess, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	err = windows.GetExitCodeProcess(sei.hProcess, &code)
	return code, err
}

// JoinArgs quotes args for a Windows command line.
func JoinArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = windows.EscapeArg(a)
	}
	return strings.Join(q, " ")
}

// IsElevated reports whether the current process runs as administrator.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// SingleInstance takes a named mutex, per session unless name starts with
// `Global\`. It returns false if another process already holds it. The handle
// is intentionally kept open until exit.
func SingleInstance(name string) bool {
	if !strings.Contains(name, `\`) {
		name = `Local\` + name
	}
	n, _ := windows.UTF16PtrFromString(name)
	_, err := windows.CreateMutex(nil, false, n)
	return !errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}

// Process is a running process seen by Processes.
type Process struct {
	PID  uint32
	Name string // e.g. "tailscale-ipn.exe"
}

// Processes lists other processes named exeName (case-insensitive). With
// allSessions false only the current Windows logon session is searched.
func Processes(exeName string, allSessions bool) ([]Process, error) {
	var mySession uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &mySession); err != nil {
		return nil, err
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	parent := ParentPID()
	var out []Process
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		if !strings.EqualFold(name, exeName) || e.ProcessID == windows.GetCurrentProcessId() || e.ProcessID == parent {
			continue
		}
		var s uint32
		if allSessions || (windows.ProcessIdToSessionId(e.ProcessID, &s) == nil && s == mySession) {
			out = append(out, Process{PID: e.ProcessID, Name: name})
		}
	}
	return out, nil
}

// ImagePath returns the full path of a running process' executable.
func ImagePath(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// Kill terminates pid and waits briefly for it to go away.
func Kill(pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 0); err != nil {
		return err
	}
	windows.WaitForSingleObject(h, 5000)
	return nil
}

// WaitExit waits up to timeout for process pid to exit.
func WaitExit(pid uint32, timeoutMs uint32) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	windows.WaitForSingleObject(h, timeoutMs)
}

// KillOthersAt terminates every other process whose image is exePath, in this
// session or (needs admin for other users) in all sessions.
func KillOthersAt(exePath string, allSessions bool) {
	procs, _ := Processes(filepath.Base(exePath), allSessions)
	for _, p := range procs {
		if img, err := ImagePath(p.PID); err == nil && strings.EqualFold(filepath.Clean(img), filepath.Clean(exePath)) {
			Kill(p.PID)
		}
	}
}

// DecodeOEM converts console output in the OEM code page (e.g. CP950 on
// Traditional Chinese Windows) to UTF-8.
func DecodeOEM(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	const cpOEM = 1
	n, err := windows.MultiByteToWideChar(cpOEM, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return string(b)
	}
	u := make([]uint16, n)
	windows.MultiByteToWideChar(cpOEM, 0, &b[0], int32(len(b)), &u[0], n)
	return windows.UTF16ToString(u)
}

// ParentPID returns the PID of the process that started this one, or 0.
// Process helpers skip it so an elevated child never kills its own launcher.
func ParentPID() uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	self := windows.GetCurrentProcessId()
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == self {
			return e.ParentProcessID
		}
	}
	return 0
}
