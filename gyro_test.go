package main

import (
	"encoding/binary"
	"math"
	"net"
	"testing"
	"time"
)

// fakeSensor is a motion sensor that reads what the test sets.
type fakeSensor struct {
	gyro, accel reading
	signal      chan struct{}
}

func (f *fakeSensor) read() (reading, reading, bool) { return f.gyro, f.accel, true }
func (f *fakeSensor) updated() <-chan struct{}       { return f.signal }
func (f *fakeSensor) close()                         {}

// dsuClient asks a server for slot 0's motion, as Savage Wind's dsu.c does.
func dsuClient(t *testing.T, server *net.UDPAddr) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func dsuRequest(msg uint32, body ...byte) []byte {
	p := make([]byte, 20+len(body))
	copy(p, "DSUC")
	binary.LittleEndian.PutUint16(p[4:], dsuProtocol)
	binary.LittleEndian.PutUint16(p[6:], uint16(4+len(body)))
	binary.LittleEndian.PutUint32(p[12:], 7)
	binary.LittleEndian.PutUint32(p[16:], msg)
	copy(p[20:], body)
	binary.LittleEndian.PutUint32(p[8:], dsuCRC(p))
	return p
}

// recvDSU reads the next server packet of type msg, checking it as Savage Wind does.
func recvDSU(t *testing.T, c *net.UDPConn, msg uint32) []byte {
	t.Helper()
	buf := make([]byte, 2048)
	for deadline := time.Now().Add(2 * time.Second); ; {
		_ = c.SetReadDeadline(deadline)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("no 0x%x packet: %v", msg, err)
		}
		p := buf[:n]
		if string(p[:4]) != "DSUS" || binary.LittleEndian.Uint16(p[4:]) != dsuProtocol {
			t.Fatalf("bad header % x", p[:8])
		}
		if size := 16 + int(binary.LittleEndian.Uint16(p[6:])); size != n {
			t.Fatalf("length %d, packet %d", size, n)
		}
		if binary.LittleEndian.Uint32(p[8:]) != dsuCRC(p) {
			t.Fatal("bad CRC")
		}
		if binary.LittleEndian.Uint32(p[16:]) == msg {
			return append([]byte(nil), p...)
		}
	}
}

func f32(p []byte) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(p)) }

func TestDSUServer(t *testing.T) {
	src := &fakeSensor{signal: make(chan struct{}, 1)}
	g := &gyroService{addr: "127.0.0.1:0", model: modelFor("ROG Ally RC71L_RC71L RC71L"),
		open: func() (motionSource, func(), error) { return src, func() {}, nil }}
	// Upright, facing the player, turning left: Windows reads gravity down the screen, the turn about its Y
	src.gyro = reading{ticks: 1, x: 0, y: 30, z: 0}
	src.accel = reading{ticks: 1, x: 0, y: -1, z: 0}
	// (the server's port: listen first on a free one, then run on it)
	probe, err := listenDSU("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.addr = probe.addr().String()
	probe.close()
	g.start()
	defer g.stop()
	time.Sleep(50 * time.Millisecond)
	if st := g.status(); st.err != "" {
		t.Fatal(st.err)
	}
	server, _ := net.ResolveUDPAddr("udp4", g.addr)
	c := dsuClient(t, server)

	_, _ = c.Write(dsuRequest(dsuVersionMsg))
	if v := recvDSU(t, c, dsuVersionMsg); len(v) != 22 || binary.LittleEndian.Uint16(v[20:]) != dsuProtocol {
		t.Fatalf("version % x", v)
	}
	_, _ = c.Write(dsuRequest(dsuPortsMsg, 2, 0, 0, 0, 0, 1))
	ports := recvDSU(t, c, dsuPortsMsg)
	if len(ports) != 32 || ports[20] != 0 || ports[21] != 2 || ports[22] != 2 {
		t.Fatalf("slot 0: % x (want 32 bytes, connected, full gyro)", ports)
	}
	if other := recvDSU(t, c, dsuPortsMsg); other[20] != 1 || other[21] != 0 {
		t.Fatalf("slot 1 should be empty: % x", other)
	}
	_, _ = c.Write(dsuRequest(dsuDataMsg, 1, 0, 0, 0, 0, 0, 0, 0))
	first := recvDSU(t, c, dsuDataMsg) // (the keep-alive repeats it while the sensor is still)
	second := recvDSU(t, c, dsuDataMsg)
	for _, p := range [][]byte{first, second} {
		if len(p) != 100 || p[31] != 1 {
			t.Fatalf("data packet % x", p)
		}
	}
	if s1, s2 := binary.LittleEndian.Uint32(first[32:]), binary.LittleEndian.Uint32(second[32:]); s2 <= s1 {
		t.Fatalf("packet numbers %d then %d", s1, s2)
	}
	if t1, t2 := binary.LittleEndian.Uint64(first[68:]), binary.LittleEndian.Uint64(second[68:]); t2 <= t1 {
		t.Fatalf("motion times %d then %d µs", t1, t2)
	}
	// Handheld Companion's ROG Ally: upright reads as a controller lying flat (DSU flat: 0, -1, 0), turning left as yaw
	a := [3]float32{f32(second[76:]), f32(second[80:]), f32(second[84:])}
	w := [3]float32{f32(second[88:]), f32(second[92:]), f32(second[96:])}
	if a != [3]float32{0, -1, 0} || w != [3]float32{0, 30, 0} {
		t.Fatalf("accel %v gyro %v", a, w)
	}
	time.Sleep(600 * time.Millisecond)
	if st := g.status(); st.clients != 1 || !st.live {
		t.Fatalf("status %+v", st)
	}
}

func TestDSUPortTaken(t *testing.T) {
	busy, err := listenDSU("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.close()
	g := &gyroService{addr: busy.addr().String(), model: defaultModel,
		open: func() (motionSource, func(), error) { return &fakeSensor{signal: make(chan struct{})}, func() {}, nil }}
	g.start()
	time.Sleep(50 * time.Millisecond)
	if st := g.status(); st.err == "" || st.running {
		t.Fatalf("a taken port must be reported: %+v", st)
	}
	g.stop()
	if st := g.status(); st.err != "" {
		t.Fatal("turning it off clears the reason")
	}
}

// The Ally's axes against Handheld Companion's chain, worked by hand from
// ROGAlly.json, MotionManager.SwapYawRoll and DSUServer's signs.
func TestAllyAxes(t *testing.T) {
	ally := modelFor("ROG Ally RC71L_RC71L RC71L")
	if ally.name != "ROG Ally" || modelFor("ROG Ally X RC72LA").name != "ROG Ally X" || modelFor("Some PC").name != "this PC" {
		t.Fatal("models")
	}
	a, g := toDSU(ally, reading{x: 1, y: 2, z: 3}, reading{x: 0.1, y: 0.2, z: 0.3}, false, false)
	if g != [3]float32{-1, 2, -3} || a != [3]float32{0.1, 0.2, -0.3} {
		t.Fatalf("ROG Ally: accel %v gyro %v", a, g)
	}
	a, g = toDSU(ally, reading{x: 1, y: 2, z: 3}, reading{x: 0.1, y: 0.2, z: 0.3}, true, false)
	if g != [3]float32{1, -2, -3} || a != [3]float32{-0.1, -0.2, -0.3} {
		t.Fatalf("up and down inverted: accel %v gyro %v", a, g)
	}
	a, g = toDSU(ally, reading{x: 1, y: 2, z: 3}, reading{x: 0.1, y: 0.2, z: 0.3}, false, true)
	if g != [3]float32{-1, -2, 3} || a != [3]float32{0.1, -0.2, 0.3} {
		t.Fatalf("left and right inverted: accel %v gyro %v", a, g)
	}
}

// The fusion levels a still Ally and follows a turn, as a game's would.
func TestFusionFollows(t *testing.T) {
	var f fusion
	flat := [3]float32{0, -1, 0} // (DSU: lying flat)
	f.update(flat, [3]float32{}, 0)
	for i := 0; i < 100; i++ { // a second of a 90°/s yaw
		f.update(flat, [3]float32{0, 90 + 2*float32(1-2*(i&1)), 0}, 0.01) // (a real gyroscope jitters)
	}
	m := f.q.mat()
	yaw := math.Atan2(-(m[0][1]-m[0][2]), m[1][1]-m[1][2]) * 180 / math.Pi
	if math.Abs(math.Abs(yaw)-90) > 3 {
		t.Fatalf("turned %.1f°, want 90", yaw)
	}
	if tilt := math.Asin(m[2][1]) * 180 / math.Pi; math.Abs(tilt) > 1 {
		t.Fatalf("tilted %.1f° by a level turn", tilt)
	}
}
