//go:build windows

// Package task manages the scheduled task that runs the updater as SYSTEM.
// Standard users may start it on demand (so the GUI can trigger an update
// without a UAC prompt), but cannot change or delete it.
package task

import (
	"encoding/binary"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"tsgui/internal/winx"
)

// Name is the task's path in Task Scheduler.
func Name(appID string) string { return `\` + appID + ` Updater` }

// Register creates or replaces the task. With interval 0 the task only runs
// at boot and on demand.
func Register(appID, description, exe, args string, interval time.Duration) error {
	var triggers strings.Builder
	triggers.WriteString(`<BootTrigger><Enabled>true</Enabled><Delay>PT5M</Delay></BootTrigger>`)
	if interval > 0 {
		fmt.Fprintf(&triggers, `<TimeTrigger><StartBoundary>%s</StartBoundary><Enabled>true</Enabled>`+
			`<Repetition><Interval>PT%dM</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition>`+
			`<RandomDelay>PT15M</RandomDelay></TimeTrigger>`,
			time.Now().Add(10*time.Minute).Format("2006-01-02T15:04:05"), int(interval.Minutes()))
	}

	// SY/BA: full control; AU (Authenticated Users): read + execute, i.e. may run it.
	xml := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>` + html.EscapeString(description) + `</Description>
    <SecurityDescriptor>D:(A;;FA;;;SY)(A;;FA;;;BA)(A;;GRGX;;;AU)</SecurityDescriptor>
  </RegistrationInfo>
  <Triggers>` + triggers.String() + `</Triggers>
  <Principals>
    <Principal id="System"><UserId>S-1-5-18</UserId><RunLevel>HighestAvailable</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT1H</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="System">
    <Exec><Command>` + html.EscapeString(`"`+exe+`"`) + `</Command><Arguments>` + html.EscapeString(args) + `</Arguments></Exec>
  </Actions>
</Task>`

	f, err := os.CreateTemp("", "task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// schtasks wants UTF-16LE with a BOM when the XML says so.
	u := append([]uint16{0xFEFF}, utf16.Encode([]rune(xml))...)
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	f.Write(b)
	f.Close()

	return schtasks("/create", "/f", "/tn", Name(appID), "/xml", f.Name())
}

// Delete removes the task if it exists.
func Delete(appID string) error {
	if !Exists(appID) {
		return nil
	}
	return schtasks("/delete", "/f", "/tn", Name(appID))
}

// Exists reports whether the task is registered.
func Exists(appID string) bool { return schtasks("/query", "/tn", Name(appID)) == nil }

// Run starts the task now. Works for standard users thanks to the task's
// security descriptor.
func Run(appID string) error { return schtasks("/run", "/tn", Name(appID)) }

func schtasks(args ...string) error {
	exe := filepath.Join(os.Getenv("SystemRoot"), `System32\schtasks.exe`)
	out, err := winx.HiddenCommand(exe, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks %s: %v: %s", args[0], err, strings.TrimSpace(winx.DecodeOEM(out)))
	}
	return nil
}
