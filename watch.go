package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const exeName = "armourycratese.exe"

// armouryPIDs lists the running Armoury Crate SE window processes.
func armouryPIDs() map[uint32]bool {
	pids := map[uint32]bool{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return pids
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exeName) {
			pids[e.ProcessID] = true
		}
	}
	return pids
}

func isRunning() bool { return len(armouryPIDs()) > 0 }

// watcher notices Armoury Crate SE being opened by something other than
// this app, such as the Ally's Armoury Crate button, and calls onOpen.
type watcher struct {
	enabled atomic.Bool // fix launches automatically
	paused  atomic.Bool // this app is launching it itself
	onOpen  func()
}

func (w *watcher) run() {
	known := armouryPIDs()
	// Poll often: the key combinations must be hidden before Armoury Crate
	// reads them, a few seconds into its startup.
	for range time.Tick(100 * time.Millisecond) {
		now := armouryPIDs()
		fresh := false
		for pid := range now {
			if !known[pid] {
				fresh = true
			}
		}
		known = now
		if fresh && !w.paused.Load() && w.enabled.Load() && hasCombos() {
			w.onOpen()
		}
	}
}

// hasCombos reports whether the layout has key combinations, the cause of
// the hang. Without them Armoury Crate opens fine and is left alone.
func hasCombos() bool {
	p, err := findPackage()
	if err != nil {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(p.combKeys, "*.json"))
	return len(files) > 0
}

// Start with Windows, in the tray.

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runName = "ArmouryCrateFix"

func startupEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runName)
	return err == nil
}

func setStartup(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err = k.DeleteValue(runName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runName, `"`+exe+`" --tray`)
}
