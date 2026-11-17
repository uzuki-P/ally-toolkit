package main

import (
	"net"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestSensorProbe reads this PC's real motion sensor for three seconds: move
// it meanwhile to see readings stream (a still sensor reports little).
// Opt in with ACF_SENSOR=1.
func TestSensorProbe(t *testing.T) {
	if os.Getenv("ACF_SENSOR") == "" {
		t.Skip("set ACF_SENSOR=1 to read this PC's motion sensor")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	done, err := roInitialize()
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	s, err := openWinSensors()
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	n := 0
	timeout := time.After(3 * time.Second)
loop:
	for {
		select {
		case <-s.updated():
			n++
		case <-timeout:
			break loop
		}
	}
	g, a, ok := s.read()
	t.Logf("%d gyroscope readings in 3 s; both sensors read: %v; gyroscope %.2f %.2f %.2f deg/s, accelerometer %.3f %.3f %.3f G",
		n, ok, g.x, g.y, g.z, a.x, a.y, a.z)
	if !ok {
		t.Fatal("no reading from one of the sensors")
	}
}

// TestRealDSU runs the gyro server on this PC's sensor and port 26760, and
// reads it as a game would. Opt in with ACF_SENSOR=1.
func TestRealDSU(t *testing.T) {
	if os.Getenv("ACF_SENSOR") == "" {
		t.Skip("set ACF_SENSOR=1 to read this PC's motion sensor")
	}
	g := newGyroService()
	g.start()
	defer g.stop()
	time.Sleep(200 * time.Millisecond)
	if st := g.status(); st.err != "" {
		t.Fatal(st.err)
	}
	server, _ := net.ResolveUDPAddr("udp4", gyroAddr)
	c := dsuClient(t, server)
	_, _ = c.Write(dsuRequest(dsuDataMsg, 0, 0, 0, 0, 0, 0, 0, 0))
	n, start := 0, time.Now()
	var p []byte
	for time.Since(start) < time.Second {
		p = recvDSU(t, c, dsuDataMsg)
		n++
	}
	t.Logf("%s: %d reports in 1 s; accel %+.3f %+.3f %+.3f G, gyro %+.2f %+.2f %+.2f deg/s", g.model.name, n,
		f32(p[76:]), f32(p[80:]), f32(p[84:]), f32(p[88:]), f32(p[92:]), f32(p[96:]))
	if n < 50 {
		t.Fatalf("only %d reports", n)
	}
}
