package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// The Armoury Crate fix's page. Its ring is the Ally's stick light, large:
// one segment for each step of a launch, lit violet to magenta as the steps
// finish.

func (a *app) crateFixPage(c *ui.Context, pl palette) {
	pageHeader(c, pl, "Armoury Crate fix",
		"Armoury Crate hangs on its logo while your key combinations exist, so they step aside until it has loaded.", nil)
	ui.Row(c).Gap(28).AlignItems(ui.Center).Children(func() {
		a.ring(c, pl)
		ui.Column(c).Gap(14).Grow(1).Shrink(1).Children(func() {
			ui.Text(c, a.headline()).FontSize(16).LineHeight(1.4)
			label := "Open Armoury Crate"
			if a.busy {
				label = "Opening Armoury Crate"
			}
			ui.Row(c).Children(func() {
				if ui.PrimaryButton(c, label).Padding(10, 20).FontSize(15).Disabled(a.busy).Clicked() {
					a.openClicked()
				}
			})
		})
	})
	a.stepsView(c, pl)
	a.switchRow(c, pl, &a.cfg.AutoFix, "Fix it whenever Armoury Crate opens",
		"Covers the Armoury Crate button and the Start menu.",
		func(on bool) { a.setAutoFix(on) })
}

// segColor is the color of a lit ring segment: violet to magenta around.
func (pl palette) segColor(i int) ui.Color {
	return pl.ringA.MixOklab(pl.ringB, float32(i)/float32(stepCount-1))
}

// headline says, in one sentence, what the app is doing.
func (a *app) headline() string {
	switch {
	case a.busy:
		return stepStatus[a.activeStep()]
	case a.errText != "":
		return a.errText
	case a.pkgErr != "":
		return a.pkgErr
	case len(a.combos) == 0:
		return "Your Desktop layout has no key combinations, so Armoury Crate opens without help."
	case a.cfg.AutoFix:
		return fmt.Sprintf("Watching for Armoury Crate. Its %s will be hidden while it loads.", plural(len(a.combos), "key combination"))
	default:
		return "Automatic fixing is off. Open Armoury Crate from here to skip the hang."
	}
}

// stepStatus says what the app is doing during each step.
var stepStatus = [stepCount]string{
	"Hiding your key combinations…",
	"Opening Armoury Crate…",
	"Waiting for Armoury Crate to load…",
	"Putting your key combinations back…",
}

func (a *app) activeStep() int {
	for i, s := range a.steps {
		if s == stateActive {
			return i
		}
	}
	for i := stepCount - 1; i >= 0; i-- {
		if a.steps[i] != statePending {
			return i
		}
	}
	return 0
}

// ring draws the Ally's joystick light: four segments, one per step.
func (a *app) ring(c *ui.Context, pl palette) {
	const size = 132
	reduce := c.Preferences().ReduceMotion
	steps := a.steps
	// While it watches, the ring glows faintly, as the Ally's does at rest.
	armed := !a.busy && a.runStart.IsZero() && a.cfg.AutoFix && len(a.combos) > 0
	ui.Column(c).Size(size, size).Center().Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		rad := r.W/2 - 6
		for i := 0; i < stepCount; i++ {
			from, to := float64(i*90+6), float64((i+1)*90-6)
			seg := arc(cx, cy, rad, from, to)
			p.StrokePath(seg, 7, pl.line)
			if armed {
				p.StrokePath(seg, 7, pl.segColor(i).Alpha(0.3))
			}
			switch steps[i] {
			case stateDone, stateSkipped:
				col := pl.segColor(i)
				if steps[i] == stateSkipped {
					col = col.Alpha(0.45)
				}
				p.StrokePath(seg, 7, col)
			case stateFailed:
				p.StrokePath(seg, 7, pl.bad)
			case stateActive:
				if reduce {
					p.StrokePath(seg, 7, pl.segColor(i).Alpha(0.6))
					break
				}
				// A light that runs along the segment, as the Ally's rings do.
				p.AnimationFrame()
				phase := float32(p.Now().UnixMilli()%1100) / 1100
				head := arc(cx, cy, rad, from, from+(to-from)*float64(phase))
				p.StrokePath(head, 7, pl.segColor(i))
			}
		}
	}).Children(func() {
		big, small := a.ringText()
		ui.Text(c, big).FontSize(30).FontWeight(600)
		ui.Text(c, small).FontSize(12).TextColor(pl.muted)
	})
}

// ringText is what the middle of the ring says.
func (a *app) ringText() (string, string) {
	switch {
	case a.busy:
		return fmt.Sprintf("%.1f", time.Since(a.runStart).Seconds()), "seconds"
	case !a.runEnd.IsZero() && a.loadTook > 0:
		return fmt.Sprintf("%.1f", a.loadTook.Seconds()), "seconds to load"
	default:
		return fmt.Sprint(len(a.combos)), plural0(len(a.combos), "key combo")
	}
}

func (a *app) stepsView(c *ui.Context, pl palette) {
	ui.Column(c).Gap(12).Children(func() {
		heading, sub := "How a launch goes", ""
		if !a.runStart.IsZero() {
			heading = "Last launch"
			sub = a.runSource + ", " + friendlyTime(a.runStart)
		}
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, heading).Font(fontNames).FontSize(19).FontWeight(600)
			if sub != "" {
				ui.Text(c, sub).FontSize(13).TextColor(pl.muted)
			}
		})
		ui.Column(c).Gap(2).Children(func() {
			for i := 0; i < stepCount; i++ {
				a.stepRow(c, pl, i)
			}
		})
	})
}

func (a *app) stepRow(c *ui.Context, pl palette, i int) {
	st := a.steps[i]
	ui.Row(c).Gap(14).AlignItems(ui.Center).Padding(7, 0).Key(i).Children(func() {
		// The step's number in a dot, filled as the ring's segment is.
		dot := ui.Column(c).Size(26, 26).Radius(13).Center().Shrink(0)
		num := ui.Color{}
		switch st {
		case stateDone:
			dot.Background(pl.segColor(i))
			num = ui.Hex("#ffffff")
		case stateSkipped:
			dot.Background(pl.segColor(i).Alpha(0.45))
			num = ui.Hex("#ffffff")
		case stateFailed:
			dot.Background(pl.bad)
			num = ui.Hex("#ffffff")
		case stateActive:
			dot.Border(2, pl.segColor(i))
			num = pl.segColor(i)
		default:
			dot.Border(1.5, pl.line)
			num = pl.muted
		}
		dot.Children(func() {
			ui.Text(c, fmt.Sprint(i+1)).FontSize(13).FontWeight(600).TextColor(num)
		})
		name := ui.Text(c, stepNames[i]).FontSize(15).Grow(1)
		if st == statePending && a.busy {
			name.TextColor(pl.muted)
		}
		note := a.notes[i]
		if note != "" {
			col := pl.muted
			if st == stateFailed {
				col = pl.bad
			}
			ui.Text(c, note).FontSize(13).TextColor(col)
		}
	})
}
