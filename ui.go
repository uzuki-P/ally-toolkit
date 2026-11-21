package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// Design: the window takes after the Ally itself. Surfaces are its white
// shell (or graphite in dark mode), type is Bahnschrift, Windows' DIN, as
// printed on hardware. The one loud thing is the ring, the Ally's joystick
// light: one segment for each step of a launch, lit violet to magenta as
// the steps finish.

type palette struct {
	shell, ink, muted, line, ringA, ringB, ok, bad ui.Color
}

func paletteFor(dark bool) palette {
	if dark {
		return palette{
			shell: ui.Hex("#16181c"), ink: ui.Hex("#e8ebf0"), muted: ui.Hex("#959cab"),
			line: ui.Hex("#2c3038"), ringA: ui.Hex("#9a80ff"), ringB: ui.Hex("#f062b4"),
			ok: ui.Hex("#3cc68f"), bad: ui.Hex("#ff7272"),
		}
	}
	return palette{
		shell: ui.Hex("#eceef1"), ink: ui.Hex("#1b1e24"), muted: ui.Hex("#636b7a"),
		line: ui.Hex("#d3d8df"), ringA: ui.Hex("#6c4cf5"), ringB: ui.Hex("#d6378f"),
		ok: ui.Hex("#17885a"), bad: ui.Hex("#c93c3c"),
	}
}

const fontFamily = "Bahnschrift, Segoe UI"

// segColor is the color of a lit ring segment: violet to magenta around.
func (pl palette) segColor(i int) ui.Color {
	return pl.ringA.MixOklab(pl.ringB, float32(i)/float32(stepCount-1))
}

func (a *app) view(c *ui.Context) {
	base := c.Theme()
	pl := paletteFor(base.Dark)
	t := *base.WithAccent(pl.ringA)
	t.Font = fontFamily
	t.Radius = 7
	t.Background = pl.shell
	t.Text = pl.ink
	t.TextMuted = pl.muted
	t.Border = pl.line
	c.SetTheme(&t)

	if a.busy {
		c.AnimationFrame() // the elapsed time in the ring ticks
	}

	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Padding(28, 28, 24, 28).Gap(28).MaxWidth(620).Children(func() {
			a.titleView(c, pl)
			a.optionsView(c, pl)
			if a.gyro != nil {
				ui.Divider(c)
				a.gyroView(c, pl)
			}
			// The Armoury Crate fix, which ASUS made needless, waits behind
			// its switch in case the hang comes back.
			if a.cfg.CrateFix {
				ui.Divider(c)
				a.header(c, pl)
				a.stepsView(c, pl)
				a.switchRow(c, pl, &a.cfg.AutoFix, "Fix it whenever Armoury Crate opens",
					"Covers the Armoury Crate button and the Start menu.",
					func(on bool) { a.setAutoFix(on) })
				ui.Divider(c)
				a.backupsView(c, pl)
			}
		})
	})

	if a.restoring != "" {
		name := strings.TrimSuffix(strings.TrimPrefix(fileBase(a.restoring), backupPrefix), ".zip")
		switch ui.AlertDialog(c, &a.confirm, "Restore the layout from "+name+"?",
			"Armoury Crate will be closed. Your current layout is zipped to the backup folder first, so you can go back to it.",
			"Cancel", "Restore layout") {
		case 1:
			a.restore(a.restoring)
			a.restoring = ""
		case 0, ui.AlertDismissed:
			a.restoring = ""
		}
	}
}

func (a *app) titleView(c *ui.Context, pl palette) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, appName).FontSize(26).FontWeight(600)
		ui.Text(c, "Extra tools for the ROG Ally.").FontSize(15).TextColor(pl.muted)
		// Errors show in the fix's headline; with the fix hidden, here.
		if a.errText != "" && !a.cfg.CrateFix {
			ui.Text(c, a.errText).FontSize(13).TextColor(pl.bad)
		}
	})
}

// header heads the Armoury Crate fix: its ring, what it is doing, and the
// button that opens Armoury Crate through it.
func (a *app) header(c *ui.Context, pl palette) {
	ui.Row(c).Gap(24).AlignItems(ui.Center).Children(func() {
		a.ring(c, pl)
		ui.Column(c).Gap(10).Grow(1).Children(func() {
			ui.Text(c, "Armoury Crate fix").FontSize(20).FontWeight(600)
			ui.Text(c, a.headline()).FontSize(15).TextColor(pl.muted)
			label := "Open Armoury Crate"
			if a.busy {
				label = "Opening Armoury Crate"
			}
			ui.Row(c).Children(func() {
				if ui.PrimaryButton(c, label).Padding(9, 18).FontSize(15).Disabled(a.busy).Clicked() {
					a.openClicked()
				}
			})
		})
	})
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
		heading := "How a launch goes"
		sub := "Armoury Crate hangs on its logo while your key combinations exist, so they step aside until it has loaded."
		if !a.runStart.IsZero() {
			heading = "Last launch"
			sub = a.runSource + ", " + friendlyTime(a.runStart)
		}
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, heading).FontSize(17).FontWeight(600)
			ui.Text(c, sub).FontSize(13).TextColor(pl.muted)
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

func (a *app) optionsView(c *ui.Context, pl palette) {
	ui.Column(c).Gap(16).Children(func() {
		a.switchRow(c, pl, &a.cfg.KeepAwake, "Keep Windows awake",
			"Keeps the PC and its screen on, like PowerToys Awake. The tray icon shows a green dot while it is on.",
			func(on bool) { a.setKeepAwake(on) })
		a.switchRow(c, pl, &a.startup, "Start with Windows",
			"Waits in the tray. Click the tray icon to come back here.",
			func(on bool) { a.setStartup(on) })
		a.switchRow(c, pl, &a.cfg.CrateFix, "Armoury Crate fix",
			"For Armoury Crate hanging on its logo. ASUS has fixed it; turn this on if it comes back. Adds its menu to the tray.",
			func(on bool) { a.setCrateFix(on) })
	})
}

func (a *app) switchRow(c *ui.Context, pl palette, v *bool, title, sub string, changed func(bool)) {
	ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(3).Grow(1).Children(func() {
			ui.Text(c, title).FontSize(15)
			ui.Text(c, sub).FontSize(13).TextColor(pl.muted)
		})
		on := *v
		if ui.Switch(c, &on).Label(title).Changed() {
			changed(on)
		}
	})
}

func (a *app) backupsView(c *ui.Context, pl palette) {
	ui.Column(c).Gap(14).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(4).Grow(1).Children(func() {
				ui.Text(c, "Layout backups").FontSize(17).FontWeight(600)
				ui.Text(c, "Zips of your Desktop layout and its key combinations.").FontSize(13).TextColor(pl.muted)
			})
			if ui.Button(c, "Back up now").Disabled(a.busy).Clicked() {
				a.backupNow()
			}
		})

		ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Saved in "+a.cfg.BackupDir).FontSize(13).TextColor(pl.muted).Grow(1).Shrink(1)
			a.textAction(c, pl, "Change folder", a.chooseFolder)
			a.textAction(c, pl, "Open folder", a.openFolder)
		})

		if a.backupMsg != "" {
			col := pl.ok
			if a.backupErr {
				col = pl.bad
			}
			ui.Text(c, a.backupMsg).FontSize(13).TextColor(col)
		}

		ui.Column(c).Gap(0).Children(func() {
			if len(a.backups) == 0 {
				ui.Text(c, "No backups in this folder yet. Back up now before you change your layout.").
					FontSize(14).TextColor(pl.muted).Padding(10, 0)
			}
			for i, b := range a.backups {
				if i >= 8 {
					break
				}
				a.backupRow(c, pl, b, i > 0)
			}
			if a.cfg.BackupDir != "" {
				ui.Row(c).Padding(10, 0, 0, 0).Children(func() {
					a.textAction(c, pl, "Restore from another zip…", a.restoreFromFile)
				})
			}
		})
	})
}

func (a *app) backupRow(c *ui.Context, pl palette, b backup, rule bool) {
	row := ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(10, 0).Key(b.Path)
	if rule {
		row.BorderWidth(1, 0, 0, 0).BorderColor(pl.line)
	}
	row.Children(func() {
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, b.Time.Format("Mon 2 Jan 2006, 15:04")).FontSize(15)
			sub := fmt.Sprintf("%.1f KB", float64(b.Size)/1024)
			if b.Before {
				sub = "Saved before a restore, " + sub
			}
			ui.Text(c, sub).FontSize(12).TextColor(pl.muted)
		})
		if ui.Button(c, "Restore").Disabled(a.busy).Clicked() {
			a.restoring = b.Path
			a.confirm = true
		}
	})
}

// textAction is a quiet, link-like action.
func (a *app) textAction(c *ui.Context, pl palette, label string, fn func()) {
	e := ui.Text(c, label).FontSize(13).TextColor(pl.ringA).Cursor(ui.CursorPointer).Shrink(0)
	if e.Hovered() {
		e.Underline()
	}
	e.OnClick(fn)
}

// arc is a circular arc from deg0 to deg1, clockwise from the top.
func arc(cx, cy, r float32, deg0, deg1 float64) *ui.Path {
	p := &ui.Path{}
	n := int(math.Max(2, math.Ceil((deg1-deg0)/3)))
	for k := 0; k <= n; k++ {
		d := (deg0 + (deg1-deg0)*float64(k)/float64(n)) * math.Pi / 180
		x := cx + r*float32(math.Sin(d))
		y := cy - r*float32(math.Cos(d))
		if k == 0 {
			p.MoveTo(x, y)
		} else {
			p.LineTo(x, y)
		}
	}
	return p
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func plural0(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func friendlyTime(t time.Time) string {
	now := time.Now()
	switch {
	case now.Sub(t) < time.Minute:
		return "just now"
	case t.YearDay() == now.YearDay() && t.Year() == now.Year():
		return "today at " + t.Format("15:04")
	default:
		return t.Format("2 Jan at 15:04")
	}
}
