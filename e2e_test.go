package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// These run against the installed Armoury Crate SE. Opt in with ACF_E2E=1,
// since they close and open the real app.

func e2e(t *testing.T) paths {
	if os.Getenv("ACF_E2E") == "" {
		t.Skip("set ACF_E2E=1 to use the real Armoury Crate SE")
	}
	p, err := findPackage()
	if err != nil {
		t.Fatal(err)
	}
	closeArmouryCrate()
	return p
}

func logSteps(t *testing.T, start time.Time) report {
	names := []string{"pending", "active", "done", "skipped", "failed"}
	return func(step int, st stepState, note string) {
		t.Logf("%5.1fs  %-26s %-7s %s", time.Since(start).Seconds(), stepNames[step], names[st], note)
	}
}

// The Armoury Crate button opens it; the fix must not close that launch.
func TestRealButtonLaunch(t *testing.T) {
	p := e2e(t)
	start := time.Now()
	if err := openArmouryCrate(p); err != nil {
		t.Fatal(err)
	}
	var first map[uint32]bool
	for len(first) == 0 {
		time.Sleep(100 * time.Millisecond)
		first = armouryPIDs()
	}
	t.Logf("%5.1fs  process seen", time.Since(start).Seconds())
	err := runFix(context.Background(), p, launchOptions{byButton: true, settle: 2 * time.Second}, logSteps(t, start))
	if err != nil {
		t.Fatal(err)
	}
	now := armouryPIDs()
	for pid := range first {
		if !now[pid] {
			t.Errorf("the first launch (pid %d) was closed; running now: %v", pid, now)
		}
	}
}

func TestRealAppLaunch(t *testing.T) {
	p := e2e(t)
	start := time.Now()
	if err := runFix(context.Background(), p, launchOptions{settle: 2 * time.Second}, logSteps(t, start)); err != nil {
		t.Fatal(err)
	}
}
