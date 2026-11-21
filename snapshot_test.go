package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestSnapshots writes PNGs of the window for design review.
// Opt in with ACF_SNAP=<folder>.
func TestSnapshots(t *testing.T) {
	out := os.Getenv("ACF_SNAP")
	if out == "" {
		t.Skip("set ACF_SNAP to a folder to write snapshots")
	}
	day := time.Date(2026, 11, 17, 19, 50, 0, 0, time.Local)
	mk := func() *app {
		a := fakeApp(t)
		a.combos = []string{"close tab", "next tab", "reopen closed tab"}
		a.gyro = &gyroService{model: modelFor("RC71L")}
		a.cfg.BackupDir = `C:\Users\you\Documents\Ally backups`
		a.backups = []backup{
			{Path: "a", Time: day, Size: 9420},
			{Path: "b", Time: day.Add(-time.Hour), Size: 9411, Before: true},
			{Path: "c", Time: day.Add(-26 * time.Hour), Size: 9302},
		}
		return a
	}
	type state struct {
		w, h int
		set  func(a *app)
	}
	gyroOn := func(a *app) {
		a.cfg.GyroServer = true
		a.gyro.st = gyroStatus{running: true, live: true, clients: 1, rate: 98,
			pose: eulerQuat(0.5, 0.45, -0.25), held: eulerQuat(0.5, 1.2, -0.2),
			sample: dsuSample{accel: [3]float32{0.02, -0.97, 0.21}, gyro: [3]float32{-12, 31, 4}}}
		a.gyroCentered = true
	}
	states := map[string]state{
		"gyro-off": {960, 680, func(a *app) {}},
		"gyro":     {960, 760, func(a *app) { gyroOn(a); a.cfg.KeepAwake = true }},
		"narrow":   {460, 900, gyroOn},
		"layouts": {960, 680, func(a *app) {
			a.page = pageLayouts
			a.backupMsg = "Backed up to Armoury Crate layout 2026-11-17 195000.zip"
		}},
		"settings": {960, 680, func(a *app) { a.page = pageSettings; a.cfg.CrateFix = false }},
		"fix":      {960, 680, func(a *app) { a.page = pageCrateFix }},
		"fix-running": {960, 680, func(a *app) {
			a.page = pageCrateFix
			a.busy = true
			a.runStart = time.Now().Add(-3200 * time.Millisecond)
			a.runSource = "Opened by the Armoury Crate button"
			a.steps = [stepCount]stepState{stateDone, stateDone, stateActive, statePending}
			a.notes = [stepCount]string{"3 hidden", "Left to open", "", ""}
		}},
		"fix-done": {960, 760, func(a *app) {
			a.page = pageCrateFix
			a.runStart = time.Now().Add(-40 * time.Second)
			a.runEnd = time.Now()
			a.loadTook = 6800 * time.Millisecond
			a.runSource = "Opened by the Armoury Crate button"
			a.steps = [stepCount]stepState{stateDone, stateDone, stateDone, stateDone}
			a.notes = [stepCount]string{"3 hidden", "Left to open", "Loaded in 6.8s", "3 restored"}
		}},
	}
	for name, s := range states {
		for _, dark := range []bool{false, true} {
			a := mk()
			s.set(a)
			tt := ui.NewTester(a.view, s.w, s.h)
			tt.SetDark(dark)
			tt.Frame()
			file := name
			if dark {
				file += "-dark"
			}
			f, err := os.Create(filepath.Join(out, file+".png"))
			if err != nil {
				t.Fatal(err)
			}
			_ = png.Encode(f, tt.Image())
			f.Close()
		}
	}
}
