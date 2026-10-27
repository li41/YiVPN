//go:build windows

// Command tray is the branded Tailscale GUI. The same EXE is also the
// installer, the uninstaller and (run as SYSTEM by a scheduled task) the
// updater for itself and tailscaled. Build it with tools/build so the names
// and icons from branding.jsonc are applied.
package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"tailscale.com/client/local"
	"tsgui/internal/brand"
	"tsgui/internal/brand/res"
	"tsgui/internal/install"
	"tsgui/internal/task"
	"tsgui/internal/tray"
	"tsgui/internal/winx"
)

const (
	flagRunHere = "--run-here" // development: run the GUI without installing
	flagWaitPID = "--wait-pid" // set when the GUI restarts itself after an update
)

func main() {
	cfg := res.Current()
	args := os.Args[1:]
	has := func(f string) bool { return slices.Contains(args, f) }
	quiet := has(install.FlagQuiet)

	switch {
	case has(install.FlagUpdate):
		setupLog(install.MachineLogDir(cfg), "updater.log", cfg)
		exitOn(install.RunUpdate(cfg))
		return
	case has(install.FlagRegister):
		setupLog(install.MachineLogDir(cfg), "updater.log", cfg)
		exitOn(install.Register(cfg, install.Exe(cfg)))
		return
	case has(install.FlagInstallElevated):
		setupLog(install.MachineLogDir(cfg), "install.log", cfg)
		if err := install.InstallElevated(cfg, quiet); err != nil {
			log.Print(err)
			if !quiet {
				winx.Error(cfg.Install.DisplayName, err.Error())
			}
			os.Exit(1)
		}
		return
	case has(install.FlagUninstallElevated):
		setupLog(install.MachineLogDir(cfg), "install.log", cfg)
		exitOn(install.UninstallElevated(cfg))
		return
	case has(install.FlagUninstall):
		setupLog(os.TempDir(), cfg.AppID+"-uninstall.log", cfg)
		install.Uninstall(cfg, quiet)
		return
	}

	if !install.IsInstalledCopy(cfg) && !has(flagRunHere) {
		setupLog(os.TempDir(), cfg.AppID+"-setup.log", cfg)
		runSetup(cfg, quiet)
		return
	}
	runGUI(cfg, args)
}

// runSetup is what happens when the user double-clicks the downloaded EXE.
// With --quiet (for scripted deployment) it installs without asking.
func runSetup(cfg *brand.Config, quiet bool) {
	verb := "安裝"
	if _, err := os.Stat(install.Exe(cfg)); err == nil {
		verb = "更新"
	}
	if !quiet && !winx.Confirm(cfg.Install.DisplayName, verb+" "+cfg.ProductName+" "+cfg.Version+"？\n\n需要系統管理員權限。") {
		return
	}
	if err := install.Setup(cfg, quiet); err != nil {
		log.Printf("setup: %v", err)
		if !quiet && !install.IsSilent(err) {
			winx.Error(cfg.Install.DisplayName, err.Error())
		}
		os.Exit(1)
	}
	// Start the GUI as the current (non-elevated) user.
	if err := exec.Command(install.Exe(cfg)).Start(); err != nil {
		log.Printf("start GUI: %v", err)
	}
}

func runGUI(cfg *brand.Config, args []string) {
	setupLog(install.UserLogDir(cfg), cfg.AppID+".log", cfg)
	if i := slices.Index(args, flagWaitPID); i >= 0 && i+1 < len(args) {
		if pid, err := strconv.ParseUint(args[i+1], 10, 32); err == nil {
			winx.WaitExit(uint32(pid), 30_000)
		}
	}
	if !winx.SingleInstance(cfg.AppID + "-tray") {
		return
	}

	app := tray.New(cfg, &local.Client{})
	app.TriggerUpdate = func() error { return task.Run(cfg.AppID) }
	if install.IsInstalledCopy(cfg) {
		go restartWhenReplaced(app)
	}
	app.Run()
}

// restartWhenReplaced relaunches the GUI after the SYSTEM updater swapped
// the EXE, so every logged-in user ends up on the new version.
func restartWhenReplaced(app *tray.App) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return
	}
	started := fi.ModTime()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-app.Context().Done():
			return
		case <-t.C:
		}
		fi, err := os.Stat(exe)
		if err != nil || fi.ModTime().Equal(started) {
			continue
		}
		time.Sleep(5 * time.Second) // let the updater finish
		log.Printf("EXE was updated; restarting")
		if err := exec.Command(exe, flagWaitPID, strconv.Itoa(os.Getpid())).Start(); err != nil {
			log.Printf("restart: %v", err)
			started = fi.ModTime()
			continue
		}
		app.Quit()
		return
	}
}

func exitOn(err error) {
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func setupLog(dir, name string, cfg *brand.Config) {
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 1<<20 {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.Printf("---- %s %s pid=%d args=%q", cfg.ProductName, cfg.Version, os.Getpid(), os.Args[1:])
}
