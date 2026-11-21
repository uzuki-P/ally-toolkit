package main

import (
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

// Keep awake, as PowerToys Awake does it: while it is on, Windows neither
// sleeps nor turns the screen off. SetThreadExecutionState lasts for as long as the
// thread that set it lives, so one goroutine owns an OS thread and holds it.

const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
)

var setExecState = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")

type awakeHolder struct {
	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
}

func (h *awakeHolder) set(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if on == (h.stop != nil) {
		return
	}
	if !on {
		close(h.stop)
		<-h.done
		h.stop, h.done = nil, nil
		return
	}
	h.stop, h.done = make(chan struct{}), make(chan struct{})
	ready := make(chan struct{})
	go func(stop, done chan struct{}) {
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		defer close(done)
		setExecState.Call(uintptr(esContinuous | esSystemRequired | esDisplayRequired))
		close(ready)
		<-stop
		setExecState.Call(uintptr(esContinuous))
	}(h.stop, h.done)
	<-ready
}

func (a *app) setKeepAwake(on bool) {
	a.cfg.KeepAwake = on
	_ = a.cfg.save()
	a.awake.set(on)
	a.updateTrayIcon()
}

func (a *app) updateTrayIcon() {
	if a.tray == nil {
		return
	}
	icon, tip := a.trayIconNow(), a.trayTip()
	_ = a.tray.SetIcon(icon, false)
	a.tray.SetToolTip(tip)
	a.updateTrayMenu()
}

// updateTrayMenu brings the tray menu's checks, and whether it has the
// Armoury Crate fix, in line with the settings.
func (a *app) updateTrayMenu() {
	if a.tray == nil || a.trayMenu == nil {
		return
	}
	a.awakeItem.Checked = a.cfg.KeepAwake
	a.autoItem.Checked = a.cfg.AutoFix
	a.gyroItem.Checked = a.cfg.GyroServer
	a.startItem.Checked = a.startup
	a.fixMenu.Hidden = !a.cfg.CrateFix
	a.tray.SetMenu(a.trayMenu)
}

func (a *app) trayIconNow() []byte {
	if a.cfg.KeepAwake {
		return trayIconAwake
	}
	return trayIcon
}

func (a *app) trayTip() string {
	if a.cfg.KeepAwake {
		return appName + " — keeping Windows awake"
	}
	return appName
}
