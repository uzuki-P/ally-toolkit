package main

import (
	"encoding/binary"
	"math"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeSensor is a motion sensor that reads what the test sets.
type fakeSensor struct {
	mu          sync.Mutex
	gyro, accel reading
	signal      chan struct{}
}

func (f *fakeSensor) read() (reading, reading, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gyro, f.accel, true
}

// report is a new gyroscope reading, taken at ticks.
func (f *fakeSensor) report(ticks int64, g reading) {
	f.mu.Lock()
	g.ticks = ticks
	f.gyro = g
	f.mu.Unlock()
	f.signal <- struct{}{}
}
func (f *fakeSensor) updated() <-chan struct{} { return f.signal }
func (f *fakeSensor) close()                   {}

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
	if a != [3]float32{0, -1, 0} || w != [3]float32{0, -30, 0} { // (DSU's yaw: negative is left)
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

// The Ally's axes, worked by hand: X un-mirrored, then (-X, Z, Y) in the
// game's axes for both sensors, then DSU's as Savage Wind's dsu.c reads them.
func TestAllyAxes(t *testing.T) {
	ally := modelFor("ROG Ally RC71L_RC71L RC71L")
	if ally.name != "ROG Ally" || modelFor("ROG Ally X RC72LA").name != "ROG Ally X" || modelFor("Some PC").name != "this PC" {
		t.Fatal("models")
	}
	a, g := toDSU(ally, reading{x: 1, y: 2, z: 3}, reading{x: 0.1, y: 0.2, z: 0.3}, false, false)
	if g != [3]float32{1, -2, 3} || a != [3]float32{0.1, 0.2, -0.3} {
		t.Fatalf("ROG Ally: accel %v gyro %v", a, g)
	}
	a, g = toDSU(ally, reading{x: 1, y: 2, z: 3}, reading{x: 0.1, y: 0.2, z: 0.3}, true, false)
	if g != [3]float32{-1, 2, 3} || a != [3]float32{-0.1, -0.2, -0.3} {
		t.Fatalf("up and down inverted: accel %v gyro %v", a, g)
	}
	a, g = toDSU(ally, reading{x: 1, y: 2, z: 3}, reading{x: 0.1, y: 0.2, z: 0.3}, false, true)
	if g != [3]float32{1, 2, -3} || a != [3]float32{0.1, -0.2, 0.3} {
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

// As held: the Ally's own readings at rest put the drawing as it lies (its
// readings: upright Y -1 G, lying screen up Z -1 G, as Windows reads them).
func TestHeldPose(t *testing.T) {
	ally := modelFor("RC71L")
	for _, c := range []struct {
		name  string
		accel reading
		axis  int // the device axis that should point up: 1 its top, 2 out of its screen
	}{
		{"upright", reading{x: -0.01, y: -1.006, z: -0.148}, 1},
		{"lying screen up", reading{x: 0, y: 0, z: -0.98}, 2},
	} {
		var f fusion
		a, w := held(ally, reading{}, c.accel)
		f.step(a, w, 0)
		m := f.q.mat()
		if up := m[2][c.axis]; up < 0.95 {
			t.Errorf("%s: axis %d points %.2f up, want about 1", c.name, c.axis, up)
		}
	}
}

// rotate is v turned by q.
func rotate(q quat, v vec3) vec3 {
	m := q.mat()
	return vec3{m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2], m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2]}
}

// allyReading is what an RC71L's sensor reads (Windows' axes, X mirrored on
// both) for a device whose pose is q (its axes in the world) turning at w
// (radians a second, its own axes).
func allyReading(q quat, w vec3) (gyro, accel reading) {
	inv := quat{q.w, -q.x, -q.y, -q.z}
	g := rotate(inv, vec3{0, 0, -1}) // (gravity's direction in its axes)
	const deg = 180 / math.Pi
	return reading{x: -w[0] * deg, y: w[1] * deg, z: w[2] * deg}, reading{x: -g[0], y: g[1], z: g[2]}
}

// Tilted and held there, the pose a game makes of the reports stays tilted:
// the gyroscope's turn and gravity agree. (They didn't: the tilt crept back to
// where it began, the aim with it.)
func TestTiltStays(t *testing.T) {
	ally := modelFor("RC71L")
	for _, c := range []struct {
		name string
		axis vec3 // the turn, in the Ally's own axes
	}{
		{"top away", vec3{-1, 0, 0}},
		{"top toward you", vec3{1, 0, 0}},
		{"lean right", vec3{0, 0, -1}},
		{"lean left", vec3{0, 0, 1}},
	} {
		q := quat{math.Cos(math.Pi / 4), math.Sin(math.Pi / 4), 0, 0} // upright, its screen toward you
		var f fusion
		const dt, rate = 0.01, 60 * math.Pi / 180
		step := func(w vec3) {
			gy, ac := allyReading(q, w)
			a, g := toDSU(ally, gy, ac, false, false)
			f.update(a, g, dt)
		}
		for i := 0; i < 50; i++ { // still
			step(vec3{})
		}
		start := f.q
		w := vec3{c.axis[0] * rate, c.axis[1] * rate, c.axis[2] * rate}
		for i := 0; i < 50; i++ { // half a second at 60°/s: 30°
			h := rate * dt / 2
			q = q.mul(quat{math.Cos(h), c.axis[0] * math.Sin(h), c.axis[1] * math.Sin(h), c.axis[2] * math.Sin(h)}).norm()
			step(w)
		}
		for i := 0; i < 300; i++ { // held there for 3 s
			step(vec3{})
		}
		d := start.mul(quat{f.q.w, -f.q.x, -f.q.y, -f.q.z}) // (from the start to now)
		angle := 2 * math.Acos(math.Min(1, math.Abs(d.w))) * 180 / math.Pi
		if math.Abs(angle-30) > 2 {
			t.Errorf("%s: the game's pose turned %.1f° and stayed, want 30°", c.name, angle)
		}
	}
}

// Each report carries its reading's own time (the sensor's, ~15.6 ms apart on
// the Ally), not when it reached the app; a still sensor's last reading is sent
// again after gyroKeepAlive, its time going on from there.
func TestReportTimes(t *testing.T) {
	src := &fakeSensor{signal: make(chan struct{}, 1), accel: reading{ticks: 1, y: -1}}
	probe, err := listenDSU("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.addr().String()
	probe.close()
	g := &gyroService{addr: addr, model: modelFor("RC71L"),
		open: func() (motionSource, func(), error) { return src, func() {}, nil }}
	src.report(5_000_000, reading{y: 10})
	g.start()
	defer g.stop()
	server, _ := net.ResolveUDPAddr("udp4", addr)
	c := dsuClient(t, server)
	_, _ = c.Write(dsuRequest(dsuDataMsg, 0, 0, 0, 0, 0, 0, 0, 0))
	time.Sleep(100 * time.Millisecond) // (subscribed; the first reading repeated meanwhile)
	for len(src.signal) > 0 {
	}
	drain := func() uint64 { // the newest report's time
		var at uint64
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
		buf := make([]byte, 2048)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return at
			}
			if n == 100 {
				at = binary.LittleEndian.Uint64(buf[68:])
			}
		}
	}
	drain()
	var times []uint64
	for k := 1; k <= 3; k++ {
		src.report(5_000_000+int64(k)*156_000, reading{y: 10}) // (15.6 ms apart, delivered whenever)
		times = append(times, drain())
	}
	if times[1]-times[0] != 15_600 || times[2]-times[1] != 15_600 {
		t.Fatalf("report times %v µs: want 15600 apart, as the sensor took them", times)
	}
}
