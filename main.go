package main

import (
	"context"
	_ "embed"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// appName is what the app is called. It began as a fix for Armoury Crate and
// grew into extra tools for the ROG Ally. Its files, settings, startup entry
// and install keep the old armoury-crate-fix name, so updates install over it.
const appName = "Ally Toolkit"

//go:embed resources/tray.png
var trayIcon []byte

//go:embed resources/tray-awake.png
var trayIconAwake []byte

// app is the state the window shows. Worker goroutines change it only
// through onMain, which runs on the main thread and redraws.
type app struct {
	win   *mygo.Window
	tray  *mygo.Tray
	watch *watcher
	cfg   settings
	gyro  *gyroService

	awake     awakeHolder
	awakeItem *mygo.MenuItem
	autoItem  *mygo.MenuItem
	gyroItem  *mygo.MenuItem
	startItem *mygo.MenuItem
	fixMenu   *mygo.MenuItem // the tray's Armoury Crate fix submenu
	trayMenu  *mygo.Menu

	gyroYaw0     float64 // the preview's straight ahead (Center)
	gyroCentered bool

	version     string // the installed version, from mygo.json; "" when run unpackaged
	page        string // the window's page: pageGyro, …
	settingsErr string

	pkg     paths
	pkgErr  string
	combos  []string
	startup bool

	// The launch in progress, or the last one.
	busy      bool
	steps     [stepCount]stepState
	notes     [stepCount]string
	runStart  time.Time
	runEnd    time.Time
	runSource string
	loadTook  time.Duration
	errText   string

	backups   []backup
	backupMsg string
	backupErr bool
	restoring string // the zip the confirm dialog asks about
	confirm   bool

	// fix runs a launch; tests replace it.
	fix func(ctx context.Context, p paths, o launchOptions, on report) error
}

func newApp() *app {
	a := &app{watch: &watcher{}, cfg: loadSettings(), fix: runFix, gyro: newGyroService(), version: mygo.App.Version()}
	a.syncWatch()
	a.gyro.invertUD.Store(a.cfg.GyroInvertUD)
	a.gyro.invertLR.Store(a.cfg.GyroInvertLR)
	a.startup = startupEnabled()
	a.refresh()
	return a
}

// refresh reads the package, its key combinations and the backups again.
func (a *app) refresh() {
	if p, err := findPackage(); err != nil {
		a.pkgErr = err.Error()
	} else {
		a.pkg, a.pkgErr = p, ""
		a.combos = comboNames(p.combKeys)
	}
	a.backups = listBackups(a.cfg.BackupDir)
}

// onMain runs fn on the main thread and redraws, or directly in tests.
func (a *app) onMain(fn func()) {
	if a.win != nil {
		a.win.Update(fn)
	} else {
		fn()
	}
}

// launch runs the fix off the main thread. byButton: Armoury Crate was
// opened by something else and is starting right now.
func (a *app) launch(byButton bool) {
	if a.busy || a.pkgErr != "" {
		return
	}
	a.busy = true
	a.errText = ""
	a.steps = [stepCount]stepState{}
	a.notes = [stepCount]string{}
	a.runStart, a.runEnd, a.loadTook = time.Now(), time.Time{}, 0
	a.runSource = "Opened from this app"
	if byButton {
		a.runSource = "Opened by the Armoury Crate button"
	}
	a.watch.paused.Store(true)
	p := a.pkg
	o := launchOptions{byButton: byButton, settle: 2 * time.Second}
	start := a.runStart
	go func() {
		err := a.fix(context.Background(), p, o, func(step int, st stepState, note string) {
			a.onMain(func() {
				a.steps[step], a.notes[step] = st, note
				if step == stepLoad && st == stateDone {
					a.loadTook = time.Since(start)
				}
			})
		})
		a.onMain(func() {
			a.busy = false
			a.runEnd = time.Now()
			if err != nil {
				a.errText = err.Error()
			}
			a.refresh()
			a.watch.paused.Store(false)
		})
	}()
}

// openClicked opens Armoury Crate from the window or the tray.
func (a *app) openClicked() {
	if a.busy {
		return
	}
	if isRunning() {
		// Already open: just bring it to the front.
		_ = openArmouryCrate(a.pkg)
		return
	}
	a.launch(false)
}

func (a *app) setAutoFix(on bool) {
	a.cfg.AutoFix = on
	a.syncWatch()
	_ = a.cfg.save()
	a.updateTrayMenu()
}

// setCrateFix shows or hides the Armoury Crate launch fix. Hidden, it also
// stops watching for Armoury Crate; Fix it whenever is kept for when it is
// shown again.
func (a *app) setCrateFix(on bool) {
	a.cfg.CrateFix = on
	a.syncWatch()
	_ = a.cfg.save()
	a.updateTrayMenu()
}

func (a *app) setTheme(theme int) {
	a.cfg.Theme = theme
	_ = a.cfg.save()
	if a.win != nil { // (tests have no app to theme)
		mygo.Theme.SetSource(themeSource(theme))
	}
}

func themeSource(theme int) mygo.ThemeSource {
	switch theme {
	case themeLight:
		return mygo.ThemeLight
	case themeDark:
		return mygo.ThemeDark
	}
	return mygo.ThemeSystem
}

func (a *app) syncWatch() { a.watch.enabled.Store(a.cfg.AutoFix && a.cfg.CrateFix) }

func (a *app) setStartup(on bool) {
	a.settingsErr = ""
	if err := setStartup(on); err != nil {
		a.settingsErr = "Couldn't change Start with Windows: " + err.Error()
	}
	a.startup = startupEnabled()
	a.updateTrayMenu()
}

func (a *app) backupNow() {
	b, err := createBackup(a.pkg, a.cfg.BackupDir, false)
	if err != nil {
		a.backupMsg, a.backupErr = err.Error(), true
	} else {
		a.backupMsg, a.backupErr = "Backed up to "+filepath.Base(b.Path), false
	}
	a.backups = listBackups(a.cfg.BackupDir)
}

func (a *app) restore(zipPath string) {
	if a.busy {
		return
	}
	a.busy = true
	a.watch.paused.Store(true)
	p, dir := a.pkg, a.cfg.BackupDir
	go func() {
		err := restoreBackup(p, zipPath, dir)
		a.onMain(func() {
			a.busy = false
			a.watch.paused.Store(false)
			if err != nil {
				a.backupMsg, a.backupErr = err.Error(), true
			} else {
				a.backupMsg, a.backupErr = "Layout restored from "+filepath.Base(zipPath)+". Open Armoury Crate to use it.", false
			}
			a.refresh()
		})
	}()
}

func (a *app) chooseFolder() {
	go func() {
		got, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "Choose where backups are saved", Directory: true,
			DefaultPath: a.cfg.BackupDir, CreateDirectories: true, Parent: a.win,
		})
		if err != nil || len(got) == 0 {
			return
		}
		a.onMain(func() {
			a.cfg.BackupDir = got[0]
			_ = a.cfg.save()
			a.backupMsg = ""
			a.backups = listBackups(a.cfg.BackupDir)
		})
	}()
}

func (a *app) openFolder() { _ = mygo.Shell.OpenPath(a.cfg.BackupDir) }

func (a *app) restoreFromFile() {
	go func() {
		got, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title: "Restore a layout backup", DefaultPath: a.cfg.BackupDir, Parent: a.win,
			Filters: []mygo.FileFilter{{Name: "Layout backup", Extensions: []string{"zip"}}},
		})
		if err != nil || len(got) == 0 {
			return
		}
		a.onMain(func() {
			a.restoring = got[0]
			a.confirm = true
		})
	}()
}

func (a *app) showWindow() {
	if a.win == nil {
		return
	}
	a.onMain(a.refresh)
	a.win.Show()
	a.win.Restore()
	a.win.Focus()
}

func (a *app) makeTray() {
	a.autoItem = &mygo.MenuItem{Label: "Fix it whenever Armoury Crate opens", Type: mygo.MenuItemCheckbox, Checked: a.cfg.AutoFix,
		Click: func(it *mygo.MenuItem, _ *mygo.Window) { a.onMain(func() { a.setAutoFix(it.Checked) }) }}
	a.gyroItem = &mygo.MenuItem{Label: "Send the gyro to games", Type: mygo.MenuItemCheckbox, Checked: a.cfg.GyroServer,
		Click: func(it *mygo.MenuItem, _ *mygo.Window) { a.onMain(func() { a.setGyro(it.Checked) }) }}
	a.awakeItem = &mygo.MenuItem{Label: "Keep Windows awake", Type: mygo.MenuItemCheckbox, Checked: a.cfg.KeepAwake,
		Click: func(it *mygo.MenuItem, _ *mygo.Window) { a.onMain(func() { a.setKeepAwake(it.Checked) }) }}
	a.startItem = &mygo.MenuItem{Label: "Start with Windows", Type: mygo.MenuItemCheckbox, Checked: a.startup,
		Click: func(it *mygo.MenuItem, _ *mygo.Window) { a.onMain(func() { a.setStartup(it.Checked) }) }}
	// The fix's items sit in one submenu, there only while the fix is shown.
	a.fixMenu = &mygo.MenuItem{Label: "Armoury Crate fix", Hidden: !a.cfg.CrateFix, Submenu: []*mygo.MenuItem{
		{Label: "Open Armoury Crate", Click: func(*mygo.MenuItem, *mygo.Window) { a.onMain(a.openClicked) }},
		mygo.Separator(),
		a.autoItem,
	}}
	a.trayMenu = mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Show " + appName, Click: func(*mygo.MenuItem, *mygo.Window) { a.showWindow() }},
		mygo.Separator(),
		a.gyroItem,
		a.awakeItem,
		{Label: "Back up layout", Click: func(*mygo.MenuItem, *mygo.Window) { a.onMain(a.backupNow) }},
		a.fixMenu,
		mygo.Separator(),
		a.startItem,
		mygo.Separator(),
		{Role: mygo.RoleQuit},
	})
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "main",
		Icon:    a.trayIconNow(),
		ToolTip: a.trayTip(),
		Menu:    a.trayMenu,
	})
	if err != nil {
		log.Println("tray:", err)
		return
	}
	// A left click opens the window; a right click shows the menu.
	tray.OnClick(a.showWindow)
	a.tray = tray
}

func fileBase(p string) string { return filepath.Base(p) }

func main() {
	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance shows its window
	}
	inTray := slices.Contains(os.Args[1:], "--tray")
	a := newApp()

	quitting := false
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { quitting = true })
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { a.gyro.stop() })
	mygo.App.OnQuitCanceled(func() { quitting = false })
	mygo.App.OnSecondInstance(func([]string, string) { a.showWindow() })

	mygo.Theme.SetSource(themeSource(a.cfg.Theme)) // (before Run: it starts in it)
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     appName,
			Width:     960,
			Height:    680,
			MinWidth:  420,
			MinHeight: 480,
			StateKey:  "main-v2", // (v2: the rail; the narrow window's size of before doesn't come back)
			Hidden:    inTray,
			Content:   ui.View(a.view),
		})
		// Closing the window keeps the app running in the tray.
		a.win.OnClose(func(e *mygo.CloseEvent) {
			if !quitting && a.tray != nil {
				e.PreventDefault()
				a.win.Hide()
			}
		})
		a.awake.set(a.cfg.KeepAwake)
		a.makeTray()
		if a.cfg.GyroServer {
			a.gyro.start()
		}

		// The Ally's Armoury Crate button opens it on its own: hide the key
		// combinations at once, before it reads them, and let it load.
		a.watch.onOpen = func() { a.onMain(func() { a.launch(true) }) }
		go a.watch.run()

		// Put back key combinations left hidden by an interrupted run.
		if a.pkgErr == "" {
			if n, err := recoverHidden(a.pkg); err != nil {
				a.errText = "Couldn't put back key combinations from an interrupted launch: " + err.Error()
			} else if n > 0 {
				a.refresh()
			}
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
