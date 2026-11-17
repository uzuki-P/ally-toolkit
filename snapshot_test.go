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
		a.cfg.BackupDir = `C:\Users\uzuki_p`
		a.backups = []backup{
			{Path: "a", Time: day, Size: 9420},
			{Path: "b", Time: day.Add(-time.Hour), Size: 9411, Before: true},
			{Path: "c", Time: day.Add(-26 * time.Hour), Size: 9302},
		}
		return a
	}
	states := map[string]func(a *app){
		"idle": func(a *app) {},
		"running": func(a *app) {
			a.busy = true
			a.runStart = time.Now().Add(-3200 * time.Millisecond)
			a.runSource = "Opened by the Armoury Crate button"
			a.steps = [stepCount]stepState{stateDone, stateDone, stateActive, statePending}
			a.notes = [stepCount]string{"3 hidden", "Left to open", "", ""}
		},
		"done": func(a *app) {
			a.runStart = time.Now().Add(-40 * time.Second)
			a.runEnd = time.Now()
			a.loadTook = 6800 * time.Millisecond
			a.runSource = "Opened by the Armoury Crate button"
			a.steps = [stepCount]stepState{stateDone, stateDone, stateDone, stateDone}
			a.notes = [stepCount]string{"3 hidden", "Left to open", "Loaded in 6.8s", "3 restored"}
			a.backupMsg = "Backed up to Armoury Crate layout 2026-11-17 195000.zip"
		},
	}
	for name, set := range states {
		for _, dark := range []bool{false, true} {
			a := mk()
			set(a)
			tt := ui.NewTester(a.view, 600, 900)
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
