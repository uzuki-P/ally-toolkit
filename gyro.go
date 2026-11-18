package main

import (
	"context"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows/registry"
)

// The gyro server: the Ally's own motion sensor sent to games over DSU, and
// the pose the window previews. It runs on a thread of its own (the sensor's
// Windows Runtime objects live in its apartment) until stopped.

const (
	gyroAddr = "127.0.0.1:26760"
	// A still sensor reports nothing (the Ally's: about 64 readings a second while it moves, none while it lies
	// still): after this long without one, the last reading again, so games keep the server (Savage Wind drops one
	// silent for 0.5 s). Longer than the sensor's ~15.6 ms between readings, so never in among them.
	gyroKeepAlive = 40 * time.Millisecond
)

type gyroService struct {
	open  func() (motionSource, func(), error) // the sensor, on the calling (locked) thread; tests replace it
	addr  string
	model handheldModel

	invertUD, invertLR atomic.Bool

	mu     sync.Mutex
	st     gyroStatus
	cancel context.CancelFunc
	done   chan struct{}
}

// gyroStatus is what the window shows.
type gyroStatus struct {
	running bool
	err     string
	clients int
	rate    float64 // the sensor's readings a second
	sample  dsuSample
	pose    quat // as a game's fusion has it (Savage Wind's axes)
	held    quat // the Ally as it is held: X right, Y to the top of its screen, Z out of it
	live    bool
}

func newGyroService() *gyroService {
	return &gyroService{open: openAllySensor, addr: gyroAddr, model: modelFor(firmwareProduct())}
}

func openAllySensor() (motionSource, func(), error) {
	uninit, err := roInitialize()
	if err != nil {
		return nil, nil, err
	}
	s, err := openWinSensors()
	if err != nil {
		uninit()
		return nil, nil, err
	}
	return s, func() { s.close(); uninit() }, nil
}

// firmwareProduct is this PC's model as its firmware names it.
func firmwareProduct() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	p, _, _ := k.GetStringValue("SystemProductName")
	b, _, _ := k.GetStringValue("BaseBoardProduct")
	return p + " " + b
}

func (g *gyroService) status() gyroStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.st
}

// start runs the server until stop. A sensor or port it can't have ends it,
// with the reason in status().err.
func (g *gyroService) start() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel, g.done = cancel, make(chan struct{})
	g.st = gyroStatus{running: true}
	go g.run(ctx, g.done)
}

func (g *gyroService) stop() {
	g.mu.Lock()
	cancel, done := g.cancel, g.done
	g.cancel = nil
	g.mu.Unlock()
	if cancel == nil {
		g.mu.Lock()
		g.st = gyroStatus{} // (a failed run: its reason goes)
		g.mu.Unlock()
		return
	}
	cancel()
	<-done
	g.mu.Lock()
	g.st = gyroStatus{}
	g.mu.Unlock()
}

func (g *gyroService) fail(msg string) {
	g.mu.Lock()
	g.st = gyroStatus{err: msg}
	g.cancel = nil
	g.mu.Unlock()
}

func (g *gyroService) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	src, closeSrc, err := g.open()
	if err != nil {
		g.fail(err.Error())
		return
	}
	defer closeSrc()
	srv, err := listenDSU(g.addr)
	if err != nil {
		g.fail("Another program is using port 26760, the DSU port: Handheld Companion, DS4Windows or another motion " +
			"server. Close it, then turn this on again.")
		return
	}
	defer srv.close()

	start := time.Now()
	var (
		fuse    fusion
		body    fusion    // (the Ally as held)
		last    uint64    // the last report's time (µs since the first reading)
		seen    int64     // the last reading's time (the sensor's, 100 ns ticks)
		real    uint64    // ... its report's time (µs)
		sent    time.Time // when the last report went (this clock)
		events  int
		counted = start
	)
	// A report's time is its reading's, as the sensor took it: a game turns the aim by the reading times the time
	// since the last report, and when the reading reached this app jitters. A repeat while the sensor is still goes
	// on from the last report by the time since.
	send := func() {
		gy, ac, ok := src.read()
		if !ok {
			return
		}
		a, w := toDSU(g.model, gy, ac, g.invertUD.Load(), g.invertLR.Load())
		now := time.Now()
		var micros uint64
		switch {
		case sent.IsZero(): // (the first report: time 0)
		case gy.ticks > seen: // (a new reading: after the last one by the sensor's own gap)
			micros = real + uint64(gy.ticks-seen)/10
		default:
			micros = last + uint64(now.Sub(sent).Microseconds()) // (the last again)
		}
		if micros <= last && !sent.IsZero() {
			micros = last + 1
		}
		if gy.ticks != seen || sent.IsZero() {
			real = micros
		}
		seen = gy.ticks
		dt := float64(micros-last) / 1e6
		if sent.IsZero() {
			dt = 0
		}
		last, sent = micros, now
		m := dsuSample{accel: a, gyro: w, micros: micros}
		srv.publish(m)
		fuse.update(a, w, dt)
		ha, hg := held(g.model, gy, ac)
		body.step(ha, hg, dt)
		g.mu.Lock()
		g.st.sample, g.st.pose, g.st.held, g.st.live = m, fuse.q, body.q, fuse.started && body.started
		g.mu.Unlock()
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-src.updated():
			events++
			send()
		case now := <-tick.C:
			if now.Sub(sent) >= gyroKeepAlive {
				send()
			}
			if d := now.Sub(counted); d >= 500*time.Millisecond {
				g.mu.Lock()
				g.st.rate, g.st.clients = float64(events)/d.Seconds(), srv.clients()
				g.mu.Unlock()
				events, counted = 0, now
			}
		}
	}
}

// ---- the pose, as a game's fusion makes it from the reports
// Savage Wind's (runtime/hle/motion.c): the reports in its axes (X right, Y
// forward, Z up out of the controller's face; the world's Z up), the
// gyroscope turning the pose, gravity slowly pulling its tilt right, the
// gyroscope's bias learnt while it lies still.

type quat struct{ w, x, y, z float64 }

func (a quat) mul(b quat) quat {
	return quat{a.w*b.w - a.x*b.x - a.y*b.y - a.z*b.z, a.w*b.x + a.x*b.w + a.y*b.z - a.z*b.y,
		a.w*b.y - a.x*b.z + a.y*b.w + a.z*b.x, a.w*b.z + a.x*b.y - a.y*b.x + a.z*b.w}
}

func (a quat) norm() quat {
	n := math.Sqrt(a.w*a.w + a.x*a.x + a.y*a.y + a.z*a.z)
	if n == 0 {
		return quat{w: 1}
	}
	return quat{a.w / n, a.x / n, a.y / n, a.z / n}
}

// mat is the rotation as a matrix: its columns are the controller's axes in the world.
func (q quat) mat() [3][3]float64 {
	xx, yy, zz, xy, xz, yz := q.x*q.x, q.y*q.y, q.z*q.z, q.x*q.y, q.x*q.z, q.y*q.z
	wx, wy, wz := q.w*q.x, q.w*q.y, q.w*q.z
	return [3][3]float64{
		{1 - 2*(yy+zz), 2 * (xy - wz), 2 * (xz + wy)},
		{2 * (xy + wz), 1 - 2*(xx+zz), 2 * (yz - wx)},
		{2 * (xz - wy), 2 * (yz + wx), 1 - 2*(xx+yy)},
	}
}

// eulerQuat: yaw about the world's Z, then pitch about the controller's X, then roll about its Y.
func eulerQuat(yaw, pitch, roll float64) quat {
	z := quat{math.Cos(yaw / 2), 0, 0, math.Sin(yaw / 2)}
	x := quat{math.Cos(pitch / 2), math.Sin(pitch / 2), 0, 0}
	y := quat{math.Cos(roll / 2), 0, math.Sin(roll / 2), 0}
	return z.mul(x).mul(y)
}

type fusion struct {
	q                quat
	started          bool
	bias, mean, vari [3]float64
}

const fusionPull = 0.6 // gravity's pull on the tilt, per radian a second

// update takes a DSU report as Savage Wind does (its dsu.c: DSU -> its axes).
func (f *fusion) update(a, g [3]float32, dt float64) {
	const rad = math.Pi / 180
	f.step([3]float64{float64(a[0]), -float64(a[2]), float64(a[1])},
		[3]float64{float64(g[0]) * rad, float64(g[2]) * rad, -float64(g[1]) * rad}, dt)
}

// step takes gravity's direction (G) and the turn (radians a second) in its axes.
func (f *fusion) step(acc, raw [3]float64, dt float64) {
	n := math.Sqrt(acc[0]*acc[0] + acc[1]*acc[1] + acc[2]*acc[2])
	if !f.started {
		if n > 0.5 {
			f.q = eulerQuat(0, math.Asin(math.Max(-1, math.Min(1, -acc[1]/n))), math.Atan2(acc[0], -acc[2]))
			f.started = true
		}
		return
	}
	if dt <= 0 || dt > 0.1 {
		return
	}
	ka, sd := math.Min(dt/0.2, 1), 0.0
	for i := range raw {
		d := raw[i] - f.mean[i]
		f.mean[i] += d * ka
		f.vari[i] += (d*d - f.vari[i]) * ka
		sd = math.Max(sd, math.Sqrt(f.vari[i]))
	}
	if math.Abs(n-1) < 0.15 && sd < 0.006 { // (lying still: what it reads is its bias)
		k := math.Min(dt/0.25, 1)
		for i := range f.bias {
			f.bias[i] += (f.mean[i] - f.bias[i]) * k
		}
	}
	w := [3]float64{raw[0] - f.bias[0], raw[1] - f.bias[1], raw[2] - f.bias[2]}
	if n > 0.75 && n < 1.25 {
		m := f.q.mat()
		v := [3]float64{-m[2][0], -m[2][1], -m[2][2]} // (gravity in its axes, as the pose has it)
		u := [3]float64{acc[0] / n, acc[1] / n, acc[2] / n}
		w[0] += fusionPull * (u[1]*v[2] - u[2]*v[1])
		w[1] += fusionPull * (u[2]*v[0] - u[0]*v[2])
		w[2] += fusionPull * (u[0]*v[1] - u[1]*v[0])
	}
	wn := math.Sqrt(w[0]*w[0] + w[1]*w[1] + w[2]*w[2])
	h, k := wn*dt/2, dt/2
	if wn > 1e-12 {
		k = math.Sin(h) / wn
	}
	f.q = f.q.mul(quat{math.Cos(h), w[0] * k, w[1] * k, w[2] * k}).norm()
}

// held is a reading in the handheld's own axes (X right, Y to the top of its
// screen, Z out of it; gravity's direction in G, the turn in radians a second
// by the right hand), for the preview of the Ally as it is held. Windows'
// axes are those already, but for X on the ROG Ally and Ally X, which their
// sensor reports mirrored on both (found on an RC71L, #860). Lying still,
// Windows reads Y -1 G held upright and Z -1 G lying screen up, as it should.
func held(m handheldModel, gyro, accel reading) (acc, turn [3]float64) {
	const rad = math.Pi / 180
	sx := 1.0
	if m.mirrorX {
		sx = -1
	}
	return [3]float64{sx * accel.x, accel.y, accel.z}, [3]float64{sx * gyro.x * rad, gyro.y * rad, gyro.z * rad}
}
