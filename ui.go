package main

import (
	_ "embed"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// Design: the window takes after the Ally itself, a landscape handheld
// held in two hands. Surfaces are its shell, white or graphite. Names and
// titles are set in Barlow Semi Condensed, narrow and squared off as type
// printed on hardware; the rest is Bahnschrift, Windows' own DIN. The one
// loud thing is light: the ring around the
// Ally's sticks. Each tool in the rail wears a small ring, lit violet to
// magenta while the tool is working and dark while it is not, so the rail
// reads at a glance like the device's own status lights. Everything else
// stays quiet.
//
// The rail is recessed, a shade deeper than the pane; the chosen tool's
// row takes the pane's color and runs into it, as a tab does.

// The accent is Material Design's pink 500, on every control that is on or
// chosen. Small text in it, the links, takes a shade that reads on the
// surface: pink 700 on light, pink 300 on dark. The rings run from violet
// to the same pink, as the Ally's lights do.
var accent = ui.Hex("#E91E63") // Material pink 500

type palette struct {
	recess, shell, raised, ink, muted, line ui.Color
	accentText                              ui.Color // pink for small text
	ringA, ringB                            ui.Color
	ok, bad                                 ui.Color
}

func paletteFor(dark bool) palette {
	if dark {
		return palette{
			recess: ui.Hex("#101216"), shell: ui.Hex("#191b20"), raised: ui.Hex("#20232a"),
			ink: ui.Hex("#e8ebf0"), muted: ui.Hex("#959cab"), line: ui.Hex("#2c3038"),
			accentText: ui.Hex("#F06292"), // pink 300
			ringA:      ui.Hex("#9a80ff"), ringB: accent,
			ok: ui.Hex("#3cc68f"), bad: ui.Hex("#ff7272"),
		}
	}
	return palette{
		recess: ui.Hex("#dde1e7"), shell: ui.Hex("#f0f2f5"), raised: ui.Hex("#ffffff"),
		ink: ui.Hex("#1b1e24"), muted: ui.Hex("#5f6776"), line: ui.Hex("#d0d5dd"),
		accentText: ui.Hex("#C2185B"), // pink 700
		ringA:      ui.Hex("#6c4cf5"), ringB: accent,
		ok: ui.Hex("#17885a"), bad: ui.Hex("#c93c3c"),
	}
}

// choice is a row of options, one chosen, filled with the accent so the
// choice shows at a glance. It reports whether the choice changed.
func choice(c *ui.Context, pl palette, sel *int, labels ...string) bool {
	changed := false
	ui.Row(c).Gap(2).Padding(3).Radius(10).Background(pl.recess).Role(ui.RoleTabList).Children(func() {
		for i, label := range labels {
			on := *sel == i
			b := ui.ButtonBase(c).Role(ui.RoleTab).Label(label).AccessSelected(on).Key(i).
				Padding(7, 16).Radius(8).Cursor(ui.CursorPointer)
			col := pl.muted
			switch {
			case on:
				b.Background(accent)
				col = ui.Hex("#ffffff")
			case b.Hovered():
				b.Background(pl.shell)
				col = pl.ink
			}
			b.Children(func() { ui.Text(c, label).FontSize(14).FontWeight(500).TextColor(col) })
			if b.Clicked() && !on {
				*sel, changed = i, true
			}
		}
	})
	return changed
}

const (
	fontFamily = "Bahnschrift, Segoe UI"
	// fontNames is for names and titles: Barlow Semi Condensed, bundled
	// (SIL Open Font License, fonts/OFL-Barlow.txt), since Windows offers
	// Bahnschrift's narrower widths to no app by name.
	fontNames = "Ally Names, Bahnschrift, Segoe UI"
)

//go:embed fonts/BarlowSemiCondensed-Medium.ttf
var fontNamesMedium []byte

//go:embed fonts/BarlowSemiCondensed-SemiBold.ttf
var fontNamesSemiBold []byte

func init() {
	for _, f := range [][]byte{fontNamesMedium, fontNamesSemiBold} {
		if err := ui.RegisterFont(f, "Ally Names"); err != nil {
			log.Println("font:", err)
		}
	}
}

// The pages of the window, one per tool.
const (
	pageGyro     = "gyro"
	pageCrateFix = "cratefix"
	pageLayouts  = "layouts"
	pageSettings = "settings"
)

// lightLevel is how lit a tool's ring is.
type lightLevel int

const (
	lightOff   lightLevel = iota
	lightDim              // on, but waiting: no game yet, or starting
	lightOn               // working
	lightFault            // failed
)

func (a *app) view(c *ui.Context) {
	base := c.Theme()
	pl := paletteFor(base.Dark)
	t := *base.WithAccent(accent)
	t.InactiveAccent = accent // (the Ally is often driven without focus: keep the pink)
	t.Font = fontFamily
	t.Radius = 8
	t.Background = pl.shell
	t.Text = pl.ink
	t.TextMuted = pl.muted
	t.Border = pl.line
	c.SetTheme(&t)

	if a.busy {
		c.AnimationFrame() // the elapsed time in the ring ticks
	}
	if a.cfg.GyroServer {
		c.After(time.Second) // the rail's gyro status
	}
	if a.page == pageCrateFix && !a.cfg.CrateFix || a.page == "" {
		a.page = pageGyro
	}

	// A narrow window stacks the rail above the page.
	w, _ := c.Size()
	narrow := w < 680
	outer := ui.Row(c)
	if narrow {
		outer = ui.Column(c)
	}
	outer.Fill().AlignItems(ui.Stretch).Background(pl.shell).Children(func() {
		a.rail(c, pl, narrow)
		ui.Scroll(c).Grow(1).Shrink(1).Fill().Children(func() {
			pane := ui.Column(c).Padding(32, 36, 32, 36).Gap(28).MaxWidth(640)
			if narrow {
				pane.Padding(24, 18, 24, 18)
			}
			pane.Children(func() {
				switch a.page {
				case pageCrateFix:
					a.crateFixPage(c, pl)
				case pageLayouts:
					a.layoutsPage(c, pl)
				case pageSettings:
					a.settingsPage(c, pl)
				default:
					a.gyroPage(c, pl)
				}
			})
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

// ---- the rail

func (a *app) rail(c *ui.Context, pl palette, narrow bool) {
	r := ui.Column(c).Background(pl.recess).Shrink(0).Role(ui.RoleNavigation).Label("Tools")
	if narrow {
		r.Padding(18, 14, 10, 14).Gap(12)
	} else {
		r.Width(264).Padding(28, 0, 18, 14).Gap(26)
	}
	r.Children(func() {
		ui.Column(c).Gap(3).Padding(0, 14, 0, 12).Children(func() {
			ui.Text(c, appName).Font(fontNames).FontSize(28).FontWeight(600).LetterSpacing(-0.2)
			if a.gyro != nil {
				ui.Text(c, "for the "+a.gyro.model.name).FontSize(13).TextColor(pl.muted)
			}
		})

		items := func() {
			level, sub := a.gyroLight()
			a.railItem(c, pl, narrow, pageGyro, "Gyro for games", sub, level, true)
			if a.cfg.CrateFix {
				level, sub := a.crateFixLight()
				a.railItem(c, pl, narrow, pageCrateFix, "Armoury Crate fix", sub, level, true)
			}
			a.railItem(c, pl, narrow, pageLayouts, "Layout backups", a.layoutsLine(), lightOff, false)
			if narrow {
				a.railItem(c, pl, narrow, pageSettings, "Settings", "", lightOff, false)
			}
		}
		if narrow {
			ui.Row(c).Gap(6).Wrap().Children(items)
			a.awakeRow(c, pl)
			return
		}
		ui.Column(c).Gap(4).Children(items)
		ui.Spacer(c)
		ui.Column(c).Gap(4).Children(func() {
			a.awakeRow(c, pl)
			a.railItem(c, pl, narrow, pageSettings, "Settings", "", lightOff, false)
			ui.Text(c, a.versionLine("Version ", "Development build")).FontSize(12).TextColor(pl.muted).Padding(10, 14, 0, 12)
		})
	})
}

// versionLine names the version running, the one in mygo.json that names
// the installer too, after prefix; or says dev when run unpackaged.
func (a *app) versionLine(prefix, dev string) string {
	if a.version == "" {
		return dev
	}
	return prefix + a.version
}

// hand is the cursor over a control: the pointer while it can be clicked,
// the arrow while it is disabled.
func hand(disabled bool) ui.Cursor {
	if disabled {
		return ui.CursorDefault
	}
	return ui.CursorPointer
}

// railItem is a tool in the rail: its ring, its name, and a line of what it
// is doing. ring false leaves the ring out, for pages that are not tools
// that run, but keeps the names in line.
func (a *app) railItem(c *ui.Context, pl palette, narrow bool, page, name, sub string, level lightLevel, ring bool) {
	chosen := a.page == page
	b := ui.ButtonBase(c).Role(ui.RoleTab).Label(name).AccessSelected(chosen).Key(page).
		Cursor(ui.CursorPointer)
	if narrow {
		b.Padding(8, 12).Radius(8)
	} else {
		b.Padding(11, 14, 11, 12).Radius(10, 0, 0, 10)
	}
	switch {
	case chosen:
		b.Background(pl.shell)
	case b.Hovered():
		b.Background(pl.shell.Alpha(0.5))
	}
	b.AlignItems(ui.Stretch).Children(func() {
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
			if ring {
				lightRing(c, pl, level, 18)
			} else if !narrow {
				ui.Box(c).Size(18, 18).Shrink(0)
			}
			ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
				n := ui.Text(c, name).Font(fontNames).FontSize(17).FontWeight(500)
				if !chosen {
					n.TextColor(pl.ink.Alpha(0.82))
				}
				if sub != "" && !narrow {
					col := pl.muted
					if level == lightFault {
						col = pl.bad
					}
					ui.Text(c, sub).FontSize(12).TextColor(col).MaxLines(1)
				}
			})
		})
	})
	if b.Clicked() {
		a.page = page
	}
}

// awakeRow is Keep awake, which is one switch, so it switches in the rail.
func (a *app) awakeRow(c *ui.Context, pl palette) {
	level, sub := lightOff, "Off"
	if a.cfg.KeepAwake {
		level, sub = lightOn, "Screen stays on"
	}
	ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(11, 14, 11, 12).Children(func() {
		lightRing(c, pl, level, 18)
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "Keep awake").Font(fontNames).FontSize(17).FontWeight(500).TextColor(pl.ink.Alpha(0.82))
			ui.Text(c, sub).FontSize(12).TextColor(pl.muted)
		})
		on := a.cfg.KeepAwake
		if ui.Switch(c, &on).Label("Keep Windows awake").Cursor(ui.CursorPointer).Changed() {
			a.setKeepAwake(on)
		}
	})
}

// lightRing is the Ally's stick light, small: a ring lit violet to magenta
// while a tool works, half lit while it waits, red when it failed.
func lightRing(c *ui.Context, pl palette, level lightLevel, size float32) {
	ring := ui.Box(c).Size(size, size).Radius(size / 2).Shrink(0)
	stops := []ui.GradientStop{{Color: pl.ringA, At: 0}, {Color: pl.ringB, At: 0.5}, {Color: pl.ringA, At: 1}}
	switch level {
	case lightOn:
		ring.Border(3.5, ui.Color{}).BorderConicGradient(ui.ConicGradient{Stops: stops})
	case lightDim:
		for i := range stops {
			stops[i].Color = stops[i].Color.Alpha(0.4)
		}
		ring.Border(3.5, ui.Color{}).BorderConicGradient(ui.ConicGradient{Stops: stops})
	case lightFault:
		ring.Border(3.5, pl.bad)
	default:
		ring.Border(3.5, pl.muted.Alpha(0.35))
	}
}

func (a *app) gyroLight() (lightLevel, string) {
	if !a.cfg.GyroServer || a.gyro == nil {
		return lightOff, "Off"
	}
	st := a.gyro.status()
	switch {
	case st.err != "":
		return lightFault, "Stopped by an error"
	case st.clients > 0:
		return lightOn, "Sending to " + plural(st.clients, "game")
	case st.live:
		return lightDim, "Ready, no game yet"
	default:
		return lightDim, "Starting…"
	}
}

func (a *app) crateFixLight() (lightLevel, string) {
	switch {
	case a.busy && !a.runStart.IsZero() && a.runEnd.IsZero():
		return lightOn, "Opening Armoury Crate…"
	case a.pkgErr != "":
		return lightFault, "Armoury Crate not found"
	case !a.cfg.AutoFix:
		return lightOff, "Not watching"
	case len(a.combos) == 0:
		return lightDim, "Nothing to hide"
	default:
		return lightOn, "Watching for Armoury Crate"
	}
}

// ---- pages

// pageHeader names the page and says what it is for. right, if any, sits
// beside the name: the page's main switch or action.
func pageHeader(c *ui.Context, pl palette, title, about string, right func()) {
	ui.Row(c).Gap(20).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Gap(8).Grow(1).Shrink(1).Children(func() {
			ui.Text(c, title).Font(fontNames).FontSize(30).FontWeight(600).LetterSpacing(-0.3).Role(ui.RoleHeading)
			ui.Text(c, about).FontSize(15).TextColor(pl.muted).LineHeight(1.4)
		})
		if right != nil {
			ui.Row(c).Shrink(0).Padding(6, 0, 0, 0).Children(right)
		}
	})
}

func (a *app) settingsPage(c *ui.Context, pl palette) {
	pageHeader(c, pl, "Settings", "How "+appName+" looks and starts, and which tools it shows.", nil)
	if a.settingsErr != "" {
		ui.Text(c, a.settingsErr).FontSize(13).TextColor(pl.bad)
	}
	ui.Column(c).Gap(22).Children(func() {
		ui.Row(c).Gap(20).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Column(c).Gap(4).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, "Theme").FontSize(16).FontWeight(500)
				ui.Text(c, "System follows Windows' light or dark mode.").FontSize(13).TextColor(pl.muted).LineHeight(1.4)
			})
			if choice(c, pl, &a.cfg.Theme, "System", "Light", "Dark") {
				a.setTheme(a.cfg.Theme)
			}
		})
		a.switchRow(c, pl, &a.startup, "Start with Windows",
			"Waits in the tray. Click the tray icon to come back here.",
			func(on bool) { a.setStartup(on) })
		a.switchRow(c, pl, &a.cfg.CrateFix, "Show the Armoury Crate fix",
			"For Armoury Crate hanging on its logo. ASUS has fixed it; turn this on if it comes back. It adds a page here and a menu to the tray.",
			func(on bool) {
				a.setCrateFix(on)
				if on {
					a.page = pageCrateFix
				}
			})
	})
	ui.Text(c, a.versionLine(appName+" ", appName+", development build")).FontSize(13).TextColor(pl.muted)
}

func (a *app) switchRow(c *ui.Context, pl palette, v *bool, title, sub string, changed func(bool)) {
	ui.Row(c).Gap(20).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(4).Grow(1).Shrink(1).Children(func() {
			ui.Text(c, title).FontSize(16).FontWeight(500)
			ui.Text(c, sub).FontSize(13).TextColor(pl.muted).LineHeight(1.4)
		})
		on := *v
		if ui.Switch(c, &on).Label(title).Cursor(ui.CursorPointer).Changed() {
			changed(on)
		}
	})
}

// textAction is a quiet, link-like action.
func (a *app) textAction(c *ui.Context, pl palette, label string, fn func()) {
	e := ui.Text(c, label).FontSize(13).TextColor(pl.accentText).Cursor(ui.CursorPointer).Shrink(0)
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
