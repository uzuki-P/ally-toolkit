package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func fakeApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	return &app{
		watch:  &watcher{},
		cfg:    settings{AutoFix: true, BackupDir: dir},
		pkg:    paths{family: "test"},
		combos: []string{"close tab", "next tab"},
	}
}

// The view runs without a window in tests, which click as a user would.
func TestOpenButtonRunsFix(t *testing.T) {
	a := fakeApp(t)
	done := make(chan launchOptions, 1)
	a.fix = func(_ context.Context, _ paths, o launchOptions, on report) error {
		on(stepHide, stateDone, "2 hidden")
		done <- o
		return nil
	}
	tt := ui.NewTester(a.view, 600, 720)
	if !tt.HasText("Watching for Armoury Crate. Its 2 key combinations will be hidden while it loads.") {
		t.Errorf("texts %q", tt.Texts())
	}
	a.launch(false)
	select {
	case o := <-done:
		if o.byButton {
			t.Error("a launch from the window is not the button's")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fix not run")
	}
}

func TestMoveFilesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "CombKeys"), filepath.Join(dir, "hidden")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "a.json"), []byte(`{"Name":"close tab"}`), 0o644)

	if n, err := moveFiles(src, dst); err != nil || n != 1 {
		t.Fatalf("hide: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("CombKeys should be gone while hidden")
	}
	_ = os.MkdirAll(src, 0o755) // Armoury Crate recreates it empty
	if n, err := moveFiles(dst, src); err != nil || n != 1 {
		t.Fatalf("restore: n=%d err=%v", n, err)
	}
	if got := comboNames(src); len(got) != 1 || got[0] != "close tab" {
		t.Fatalf("names %q", got)
	}
}

func TestLoggedSince(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ac.log")
	_ = os.WriteFile(f, []byte("old; ROGConsoleLandingPage; Function out Init\n"), 0o644)
	off := fileSize(f)
	if loggedSince(f, off) {
		t.Fatal("old line must not count")
	}
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = fh.WriteString("2026; Information; ROGConsoleLandingPage; 1; 2] (5411)Function out Init\n")
	fh.Close()
	if !loggedSince(f, off) {
		t.Fatal("new marker not seen")
	}
}

func TestZipBackupRestore(t *testing.T) {
	dir := t.TempDir()
	backups := filepath.Join(dir, "home")
	ls := filepath.Join(dir, "LocalState")
	p := paths{localState: ls, combKeys: filepath.Join(ls, "GamepadCustomize", "CombKeys"), hidden: filepath.Join(ls, "GamepadCustomize.CombKeys.hidden")}
	layout := filepath.Join(ls, "GamepadCustomize", "Profiles", "_sys_desktop.json")
	_ = os.MkdirAll(filepath.Dir(layout), 0o755)
	_ = os.MkdirAll(p.combKeys, 0o755)
	_ = os.WriteFile(layout, []byte("v1"), 0o644)
	_ = os.WriteFile(filepath.Join(p.combKeys, "a.json"), []byte(`{"Name":"close tab"}`), 0o644)

	b, err := createBackup(p, backups, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(b.Path) != ".zip" || filepath.Dir(b.Path) != backups {
		t.Fatalf("backup at %s", b.Path)
	}
	_ = os.WriteFile(layout, []byte("v2"), 0o644)
	_ = os.Remove(filepath.Join(p.combKeys, "a.json"))

	if err := restoreBackup(p, b.Path, backups); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(layout); string(got) != "v1" {
		t.Fatalf("layout %q after restore", got)
	}
	if names := comboNames(p.combKeys); len(names) != 1 {
		t.Fatalf("combos %q after restore", names)
	}
	got := listBackups(backups)
	if len(got) != 2 {
		t.Fatalf("want the backup plus a before-restore backup, got %d", len(got))
	}
	if !got[0].Before && !got[1].Before {
		t.Fatal("no before-restore backup")
	}
}

func TestRestoreRejectsOtherZips(t *testing.T) {
	dir := t.TempDir()
	ls := filepath.Join(dir, "LocalState")
	p := paths{localState: ls}
	junk := filepath.Join(dir, "junk")
	_ = os.MkdirAll(junk, 0o755)
	_ = os.WriteFile(filepath.Join(junk, "x.txt"), []byte("x"), 0o644)
	zipPath := filepath.Join(dir, "junk.zip")
	if err := zipDir(junk, zipPath); err != nil {
		t.Fatal(err)
	}
	if err := restoreBackup(p, zipPath, dir); err == nil {
		t.Fatal("a zip without a layout must be refused")
	}
}
