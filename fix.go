package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Armoury Crate SE is a Store app. Its Desktop layout can map buttons to
// key combinations ("CombKeys"). While those combination files exist,
// Armoury Crate SE hangs on its splash screen at launch. The fix hides the
// combination files during launch and puts them back once it has loaded.

const packagePrefix = "B9ECED6F.ArmouryCrateSE_"

// readyMarker is logged once the main page has finished initializing.
var readyMarker = []string{"ROGConsoleLandingPage", "Function out Init"}

type paths struct {
	family     string // package family name, e.g. B9ECED6F.ArmouryCrateSE_qmba6cd70vzyy
	localState string
	combKeys   string // GamepadCustomize\CombKeys
	hidden     string // where the combination files wait during launch
}

func (p paths) gamepad() string { return filepath.Join(p.localState, "GamepadCustomize") }

// findPackage locates the installed Armoury Crate SE package data.
func findPackage() (paths, error) {
	base := filepath.Join(os.Getenv("LOCALAPPDATA"), "Packages")
	m, _ := filepath.Glob(filepath.Join(base, packagePrefix+"*"))
	if len(m) == 0 {
		return paths{}, errors.New("Armoury Crate SE isn't installed for this Windows user")
	}
	ls := filepath.Join(m[0], "LocalState")
	return paths{
		family:     filepath.Base(m[0]),
		localState: ls,
		combKeys:   filepath.Join(ls, "GamepadCustomize", "CombKeys"),
		hidden:     filepath.Join(ls, "GamepadCustomize.CombKeys.hidden"),
	}, nil
}

// comboNames lists the names of the key combinations in dir.
func comboNames(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var names []string
	for _, f := range files {
		var v struct{ Name string }
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &v) == nil && v.Name != "" {
			names = append(names, v.Name)
		} else {
			names = append(names, strings.TrimSuffix(filepath.Base(f), ".json"))
		}
	}
	return names
}

// moveFiles moves every file from src into dst and removes src if empty.
func moveFiles(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		to := filepath.Join(dst, e.Name())
		_ = os.Remove(to) // a stale copy Armoury Crate may have written
		if err := os.Rename(filepath.Join(src, e.Name()), to); err != nil {
			return n, err
		}
		n++
	}
	_ = os.Remove(src) // only succeeds when empty
	return n, nil
}

// recoverHidden puts back combinations left hidden by an interrupted run.
func recoverHidden(p paths) (int, error) { return moveFiles(p.hidden, p.combKeys) }

func hiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

// openArmouryCrate starts Armoury Crate SE, or brings it to the front.
func openArmouryCrate(p paths) error {
	return hiddenCmd("explorer.exe", `shell:AppsFolder\`+p.family+"!App").Start()
}

// closeArmouryCrate closes the Armoury Crate SE window (not its services).
func closeArmouryCrate() {
	_ = hiddenCmd("taskkill", "/IM", "ArmouryCrateSE.exe", "/F").Run()
	for i := 0; i < 20 && isRunning(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

// logFile is today's Armoury Crate SE log.
func logFile(p paths) string {
	return filepath.Join(p.localState, "ArmouryCrate_"+time.Now().Format("20060102")+".log")
}

func fileSize(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}

// loggedSince reports whether the ready marker appears after offset.
func loggedSince(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() < offset {
		offset = 0 // the log was rotated
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	b, _ := io.ReadAll(f)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, readyMarker[0]) && strings.Contains(line, readyMarker[1]) {
			return true
		}
	}
	return false
}

// The steps of a launch, in order.
const (
	stepHide = iota
	stepOpen
	stepLoad
	stepRestore
	stepCount
)

var stepNames = [stepCount]string{
	"Hide key combinations",
	"Open Armoury Crate",
	"Wait for it to load",
	"Put key combinations back",
}

type stepState int

const (
	statePending stepState = iota
	stateActive
	stateDone
	stateSkipped
	stateFailed
)

// report tells the window how a step is going; note is a short detail.
type report func(step int, st stepState, note string)

// launchOptions say how a launch started.
type launchOptions struct {
	// byButton: Armoury Crate was already opened by something else, such as
	// the Ally's Armoury Crate button, and is starting up right now.
	byButton bool
	settle   time.Duration
	// loadTimeout is how long a launch may take before it counts as stuck.
	loadTimeout time.Duration
}

// runFix hides the key combinations, lets Armoury Crate SE open, waits until
// it has loaded, then puts the key combinations back. A launch the button
// started is left to finish; only if it is stuck anyway is it reopened.
func runFix(ctx context.Context, p paths, o launchOptions, on report) (err error) {
	if o.loadTimeout == 0 {
		o.loadTimeout = 45 * time.Second
	}
	lf := logFile(p)
	offset := fileSize(lf)

	// 1. Hide. First, so a launch in progress reads no combinations.
	on(stepHide, stateActive, "")
	n, err := moveFiles(p.combKeys, p.hidden)
	if err != nil {
		_, _ = moveFiles(p.hidden, p.combKeys)
		on(stepHide, stateFailed, err.Error())
		return fmt.Errorf("couldn't hide the key combinations: %w", err)
	}
	if n == 0 {
		on(stepHide, stateSkipped, "None in your layout")
	} else {
		on(stepHide, stateDone, fmt.Sprintf("%d hidden", n))
		defer func() {
			on(stepRestore, stateActive, "")
			if _, rerr := moveFiles(p.hidden, p.combKeys); rerr != nil {
				on(stepRestore, stateFailed, rerr.Error())
				err = errors.Join(err, fmt.Errorf("couldn't put the key combinations back; they are in %s: %w", p.hidden, rerr))
				return
			}
			on(stepRestore, stateDone, fmt.Sprintf("%d restored", n))
		}()
	}

	// 2. Open.
	on(stepOpen, stateActive, "")
	if o.byButton {
		on(stepOpen, stateDone, "Left to open")
	} else {
		if err := openArmouryCrate(p); err != nil {
			on(stepOpen, stateFailed, err.Error())
			return fmt.Errorf("couldn't open Armoury Crate: %w", err)
		}
		on(stepOpen, stateDone, "")
	}

	// 3. Wait for the main page.
	start := time.Now()
	on(stepLoad, stateActive, "")
	loaded, err := waitLoaded(ctx, lf, offset, o.loadTimeout)
	if err != nil {
		return err
	}
	if !loaded && n > 0 {
		// The launch read the key combinations before they were hidden and
		// is stuck. Reopen it once, with them hidden.
		on(stepLoad, stateActive, "Stuck on the logo, reopening it")
		closeArmouryCrate()
		offset = fileSize(lf)
		if err := openArmouryCrate(p); err != nil {
			on(stepLoad, stateFailed, err.Error())
			return fmt.Errorf("couldn't reopen Armoury Crate: %w", err)
		}
		if loaded, err = waitLoaded(ctx, lf, offset, o.loadTimeout); err != nil {
			return err
		}
	}
	took := time.Since(start).Round(100 * time.Millisecond)
	if !loaded {
		on(stepLoad, stateFailed, "Didn't finish loading in "+took.String())
	} else {
		on(stepLoad, stateDone, "Loaded in "+took.String())
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(o.settle):
	}
	return nil
}

// waitLoaded waits until the log says the main page is up.
func waitLoaded(ctx context.Context, lf string, offset int64, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if loggedSince(lf, offset) {
			return true, nil
		}
	}
	return false, nil
}
