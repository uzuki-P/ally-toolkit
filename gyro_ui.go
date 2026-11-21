package main

import (
	"fmt"
	"math"
	"sort"

	"github.com/egoist/mygo/ui"
)

// The gyro section: the server's switch, what it is doing, and a preview of
// the Ally as games get it, turning with it. The drawing is the pose a game's
// fusion makes of the reports (gyroService's fusion): if the Ally tilts one
// way and the drawing the other, or it creeps back after a turn, the axes are
// wrong for games too.

func (a *app) setGyro(on bool) {
	a.cfg.GyroServer = on
	_ = a.cfg.save()
	if on {
		a.gyroCentered = false
		a.gyro.start()
	} else {
		a.gyro.stop()
	}
	a.updateTrayMenu()
}

func (a *app) setGyroInvert(ud, lr bool) {
	a.cfg.GyroInvertUD, a.cfg.GyroInvertLR = ud, lr
	a.gyro.invertUD.Store(ud)
	a.gyro.invertLR.Store(lr)
	_ = a.cfg.save()
}

func (a *app) gyroView(c *ui.Context, pl palette) {
	st := a.gyro.status()
	ui.Column(c).Gap(14).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "Gyro for games").FontSize(17).FontWeight(600)
			ui.Text(c, "Savage Wind, Cemu, Yuzu, Dolphin and other games that read a DSU motion server get the Ally's gyro, "+
				"its axes set for the "+a.gyro.model.name+".").FontSize(13).TextColor(pl.muted)
		})
		a.switchRow(c, pl, &a.cfg.GyroServer, "Send the gyro to games",
			"A DSU server at 127.0.0.1:26760, on this PC only.", func(on bool) { a.setGyro(on) })
		if !a.cfg.GyroServer {
			return
		}
		if st.running {
			c.AnimationFrame() // (the preview follows the Ally)
		}
		line, col := gyroLine(st), pl.muted
		if st.err != "" {
			col = pl.bad
		} else if st.clients > 0 {
			col = pl.ok
		}
		ui.Text(c, line).FontSize(13).TextColor(col)
		if st.err != "" {
			return
		}
		ui.Row(c).Children(func() {
			if ui.Segmented(c, &a.cfg.GyroPreview, "As you hold it", "As games see it").Changed() {
				a.gyroCentered = false
				_ = a.cfg.save()
			}
		})
		a.gyroPreview(c, pl, st)
		caption := "The Ally as you hold it. Tilt and turn it: the drawing should follow, and stay put when you stop."
		if a.cfg.GyroPreview == 1 {
			caption = "What games get. Held as you play, the Ally reads as a controller lying flat, screen up. If the " +
				"drawing tilts the wrong way or creeps back after a tilt, the axes are wrong for games too."
		}
		ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
			ui.Text(c, caption).FontSize(13).TextColor(pl.muted).Grow(1).Shrink(1)
			if ui.Button(c, "Center").Clicked() {
				a.gyroCentered = false
			}
		})
		ud, lr := a.cfg.GyroInvertUD, a.cfg.GyroInvertLR
		a.switchRow(c, pl, &ud, "Invert up and down", "Tilting the Ally's top away aims the other way.",
			func(on bool) { a.setGyroInvert(on, lr) })
		a.switchRow(c, pl, &lr, "Invert left and right", "Turning the Ally aims the other way.",
			func(on bool) { a.setGyroInvert(ud, on) })
	})
}

func gyroLine(st gyroStatus) string {
	switch {
	case st.err != "":
		return st.err
	case !st.running:
		return "Starting…"
	case st.clients > 0:
		return fmt.Sprintf("Sending to %s, %.0f readings a second.", plural(st.clients, "game"), st.rate)
	case st.live:
		return "Ready. In Savage Wind, pick DSU server (or Automatic) under Options > Controls > Motion source."
	default:
		return "Waiting for the first reading from the Ally's sensor…"
	}
}

// ---- the preview

type vec3 [3]float64

func dot3(a, b vec3) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

type camera struct {
	m         [3][3]float64 // the device's axes in the world, its heading at Center taken off
	fwd, up   vec3
	dist, k   float64
	cx, cy    float64
	faceDepth float64 // the body's face: what lies on it is drawn just above it
}

func (v *camera) world(p vec3) vec3 {
	return vec3{v.m[0][0]*p[0] + v.m[0][1]*p[1] + v.m[0][2]*p[2],
		v.m[1][0]*p[0] + v.m[1][1]*p[1] + v.m[1][2]*p[2],
		v.m[2][0]*p[0] + v.m[2][1]*p[1] + v.m[2][2]*p[2]}
}

func (v *camera) screen(w vec3) (x, y, depth float64) {
	depth = dot3(w, v.fwd)
	s := v.k * v.dist / (v.dist + depth)
	return v.cx + w[0]*s, v.cy - dot3(w, v.up)*s, depth
}

type facet struct {
	pts   [][2]float32
	col   ui.Color
	depth float64
}

// flat adds a flat shape of the device (its own axes) when its side faces the camera.
func (v *camera) flat(out *[]facet, pts []vec3, normal vec3, col ui.Color, lift float64) {
	nw, eye := v.world(normal), vec3{-v.fwd[0] * v.dist, -v.fwd[1] * v.dist, -v.fwd[2] * v.dist}
	var centre vec3
	f := facet{}
	for _, p := range pts {
		w := v.world(p)
		for i := range centre {
			centre[i] += w[i] / float64(len(pts))
		}
		x, y, d := v.screen(w)
		f.pts = append(f.pts, [2]float32{float32(x), float32(y)})
		f.depth += d / float64(len(pts))
	}
	if dot3(nw, vec3{centre[0] - eye[0], centre[1] - eye[1], centre[2] - eye[2]}) >= 0 {
		return // (facing away)
	}
	light := vec3{-0.3, -0.6, 0.74} // (from the front, above, a little left)
	k := 0.5 + 0.5*math.Max(0, dot3(nw, light))
	f.col = col.MixOklab(ui.Hex("#000000"), float32(1-k))
	if lift > 0 {
		f.depth = v.faceDepth - lift // (its own middle may be farther than the face's)
	}
	*out = append(*out, f)
}

func (v *camera) box(out *[]facet, a, b vec3, col ui.Color) {
	faces := []struct {
		n   vec3
		pts []vec3
	}{
		{vec3{0, 0, 1}, []vec3{{a[0], a[1], b[2]}, {b[0], a[1], b[2]}, {b[0], b[1], b[2]}, {a[0], b[1], b[2]}}},
		{vec3{0, 0, -1}, []vec3{{a[0], b[1], a[2]}, {b[0], b[1], a[2]}, {b[0], a[1], a[2]}, {a[0], a[1], a[2]}}},
		{vec3{0, 1, 0}, []vec3{{a[0], b[1], a[2]}, {a[0], b[1], b[2]}, {b[0], b[1], b[2]}, {b[0], b[1], a[2]}}},
		{vec3{0, -1, 0}, []vec3{{a[0], a[1], a[2]}, {b[0], a[1], a[2]}, {b[0], a[1], b[2]}, {a[0], a[1], b[2]}}},
		{vec3{1, 0, 0}, []vec3{{b[0], a[1], a[2]}, {b[0], b[1], a[2]}, {b[0], b[1], b[2]}, {b[0], a[1], b[2]}}},
		{vec3{-1, 0, 0}, []vec3{{a[0], a[1], a[2]}, {a[0], a[1], b[2]}, {a[0], b[1], b[2]}, {a[0], b[1], a[2]}}},
	}
	for _, f := range faces {
		v.flat(out, f.pts, f.n, col, 0)
	}
}

func (v *camera) disc(out *[]facet, x, y, z, r float64, col ui.Color, lift float64) {
	pts := make([]vec3, 16)
	for i := range pts {
		t := float64(i) * 2 * math.Pi / 16
		pts[i] = vec3{x + r*math.Cos(t), y + r*math.Sin(t), z}
	}
	v.flat(out, pts, vec3{0, 0, 1}, col, lift)
}

func (a *app) gyroPreview(c *ui.Context, pl palette, st gyroStatus) {
	q := st.held
	if a.cfg.GyroPreview == 1 {
		q = st.pose
	}
	if !st.live {
		q = quat{w: 1} // (lying flat, until the first reading)
	}
	m := q.mat()
	// its heading: the level part of its Y (lying flat) minus its Z (upright), as Savage Wind's aiming takes it
	fx, fy := m[0][1]-m[0][2], m[1][1]-m[1][2]
	yaw := 0.0
	if fx*fx+fy*fy > 1e-6 {
		yaw = math.Atan2(-fx, fy)
	}
	if st.live && !a.gyroCentered {
		a.gyroYaw0, a.gyroCentered = yaw, true
	}
	cs, sn := math.Cos(-a.gyroYaw0), math.Sin(-a.gyroYaw0)
	var v camera
	for col := 0; col < 3; col++ {
		v.m[0][col] = cs*m[0][col] - sn*m[1][col]
		v.m[1][col] = sn*m[0][col] + cs*m[1][col]
		v.m[2][col] = m[2][col]
	}
	e := 32 * math.Pi / 180 // (the camera in front, a little above)
	v.fwd, v.up, v.dist = vec3{0, math.Cos(e), -math.Sin(e)}, vec3{0, math.Sin(e), math.Cos(e)}, 7

	dark := c.Theme().Dark
	body, stage, screen, key := ui.Hex("#f6f7f9"), ui.Hex("#dfe3e9"), ui.Hex("#141821"), ui.Hex("#3b404a")
	if dark {
		body, stage, screen, key = ui.Hex("#454a54"), ui.Hex("#1e2127"), ui.Hex("#0b0d12"), ui.Hex("#22252b")
	}
	pitch := math.Asin(math.Max(-1, math.Min(1, m[2][1]))) * 180 / math.Pi
	roll := math.Atan2(-m[2][0], m[2][2]) * 180 / math.Pi
	turn := math.Remainder((yaw-a.gyroYaw0)*180/math.Pi, 360)

	ui.Column(c).Height(250).Radius(10).Background(stage).Clip().Draw(func(p *ui.Painter, r ui.Rect) {
		v.cx, v.cy, v.k = float64(r.X+r.W/2), float64(r.Y+r.H*0.54), float64(min(r.W, r.H*1.9))*0.3/1.5
		line := func(a, b vec3) { // (the floor, a little below it; kept inside the stage)
			x0, y0, _ := v.screen(a)
			x1, y1, _ := v.screen(b)
			if x0, y0, x1, y1, ok := clipSegment(x0, y0, x1, y1, r); ok {
				p.Line(float32(x0), float32(y0), float32(x1), float32(y1), 1, pl.line)
			}
		}
		for i := -3; i <= 3; i++ {
			g := float64(i) * 0.75
			line(vec3{g, -2.25, -0.9}, vec3{g, 2.25, -0.9})
			line(vec3{-2.25, g, -0.9}, vec3{2.25, g, -0.9})
		}
		var grips, main, onFace []facet
		v.box(&grips, vec3{-1.4, -0.62, -0.34}, vec3{-0.86, 0.26, -0.12}, body) // its grips, behind
		v.box(&grips, vec3{0.86, -0.62, -0.34}, vec3{1.4, 0.26, -0.12}, body)
		v.box(&main, vec3{-1.4, -0.56, -0.12}, vec3{1.4, 0.56, 0.12}, body)  // the body
		v.box(&main, vec3{-1.34, 0.56, -0.09}, vec3{-0.82, 0.64, 0.06}, key) // shoulder buttons: its top
		v.box(&main, vec3{0.82, 0.56, -0.09}, vec3{1.34, 0.64, 0.06}, key)
		const z = 0.121 // (on its face)
		_, _, v.faceDepth = v.screen(v.world(vec3{0, 0, 0.12}))
		v.flat(&onFace, []vec3{{-0.8, -0.46, z}, {0.8, -0.46, z}, {0.8, 0.46, z}, {-0.8, 0.46, z}}, vec3{0, 0, 1}, screen, 0.001)
		v.flat(&onFace, []vec3{{0, 0.34, z}, {-0.11, 0.16, z}, {0.11, 0.16, z}}, vec3{0, 0, 1}, pl.ringA, 0.002) // (up)
		v.disc(&onFace, -1.1, 0.2, z, 0.17, pl.ringA, 0.001)                                                     // the sticks in their light rings
		v.disc(&onFace, -1.1, 0.2, z, 0.13, key, 0.002)
		v.disc(&onFace, 1.08, -0.26, z, 0.16, pl.ringB, 0.001)
		v.disc(&onFace, 1.08, -0.26, z, 0.12, key, 0.002)
		v.flat(&onFace, []vec3{{-1.2, -0.3, z}, {-1.0, -0.3, z}, {-1.0, -0.22, z}, {-1.2, -0.22, z}}, vec3{0, 0, 1}, key, 0.002)
		v.flat(&onFace, []vec3{{-1.14, -0.36, z}, {-1.06, -0.36, z}, {-1.06, -0.16, z}, {-1.14, -0.16, z}}, vec3{0, 0, 1}, key, 0.002)
		for i := 0; i < 4; i++ { // A B X Y
			t := float64(i) * math.Pi / 2
			v.disc(&onFace, 1.1+0.11*math.Cos(t), 0.22+0.11*math.Sin(t), z, 0.045, key, 0.002)
		}
		// (depth order fails between boxes that touch: the grips are behind the body while its face shows, in
		// front of it otherwise; what lies on the face comes last)
		order := [][]facet{grips, main, onFace}
		if len(onFace) == 0 {
			order = [][]facet{main, grips}
		}
		outline := ui.Hex("#000000").Alpha(0.18)
		for _, fs := range order {
			sort.Slice(fs, func(i, j int) bool { return fs[i].depth > fs[j].depth })
			for _, f := range fs {
				path := &ui.Path{}
				for i, pt := range f.pts {
					if i == 0 {
						path.MoveTo(pt[0], pt[1])
					} else {
						path.LineTo(pt[0], pt[1])
					}
				}
				path.Close()
				p.FillPath(path, f.col)
				p.StrokePath(path, 1, outline)
			}
		}
		if !st.live {
			p.Fill(r, stage.Alpha(0.6), 10)
		}
	})
	if st.live {
		s := st.sample
		ui.Row(c).Gap(18).Children(func() {
			ui.Text(c, fmt.Sprintf("Tilt %.0f°  Lean %.0f°  Turned %.0f°", pitch, roll, turn)).FontSize(13).Grow(1)
			ui.Text(c, fmt.Sprintf("Gyro %+.0f %+.0f %+.0f °/s  Accel %+.2f %+.2f %+.2f G",
				s.gyro[0], s.gyro[1], s.gyro[2], s.accel[0], s.accel[1], s.accel[2])).FontSize(12).TextColor(pl.muted)
		})
	}
}

// clipSegment keeps the part of a line inside r (Liang–Barsky).
func clipSegment(x0, y0, x1, y1 float64, r ui.Rect) (float64, float64, float64, float64, bool) {
	t0, t1, dx, dy := 0.0, 1.0, x1-x0, y1-y0
	for _, e := range [][2]float64{{-dx, x0 - float64(r.X)}, {dx, float64(r.X+r.W) - x0}, {-dy, y0 - float64(r.Y)}, {dy, float64(r.Y+r.H) - y0}} {
		p, q := e[0], e[1]
		if p == 0 {
			if q < 0 {
				return 0, 0, 0, 0, false
			}
			continue
		}
		t := q / p
		if p < 0 {
			t0 = math.Max(t0, t)
		} else {
			t1 = math.Min(t1, t)
		}
	}
	if t0 > t1 {
		return 0, 0, 0, 0, false
	}
	return x0 + t0*dx, y0 + t0*dy, x0 + t1*dx, y0 + t1*dy, true
}
