package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// The Layout backups page: zips of Armoury Crate's Desktop controller
// layout, made and restored here. It stands on its own, apart from the
// Armoury Crate fix, since a layout is worth keeping either way.

// layoutsLine is the rail's line for the page.
func (a *app) layoutsLine() string {
	switch {
	case a.pkgErr != "":
		return "Armoury Crate not found"
	case len(a.backups) == 0:
		return "No backups yet"
	default:
		return "Last one " + friendlyTime(a.backups[0].Time)
	}
}

func (a *app) layoutsPage(c *ui.Context, pl palette) {
	pageHeader(c, pl, "Layout backups",
		"Zips of the Desktop layout you set in Armoury Crate: its button mapping and key combinations. Back one up before you change it.",
		func() {
			if ui.PrimaryButton(c, "Back up now").Padding(10, 20).FontSize(15).Disabled(a.busy || a.pkgErr != "").Clicked() {
				a.backupNow()
			}
		})

	if a.pkgErr != "" {
		ui.Text(c, a.pkgErr).FontSize(13).TextColor(pl.bad)
	}
	if a.backupMsg != "" {
		col := pl.ok
		if a.backupErr {
			col = pl.bad
		}
		ui.Text(c, a.backupMsg).FontSize(13).TextColor(col)
	}

	ui.Column(c).Gap(12).Children(func() {
		list := ui.Column(c).Background(pl.raised).Radius(12).Border(1, pl.line).Padding(4, 18)
		list.Children(func() {
			if len(a.backups) == 0 {
				ui.Text(c, "No backups in this folder yet.").FontSize(14).TextColor(pl.muted).Padding(18, 0)
			}
			for i, b := range a.backups {
				if i >= 8 {
					break
				}
				a.backupRow(c, pl, b, i > 0)
			}
		})
		ui.Row(c).Gap(16).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, "Saved in "+a.cfg.BackupDir).FontSize(13).TextColor(pl.muted).Grow(1).Shrink(1).TruncateMiddle()
			a.textAction(c, pl, "Change folder", a.chooseFolder)
			a.textAction(c, pl, "Open folder", a.openFolder)
			if a.cfg.BackupDir != "" {
				a.textAction(c, pl, "Restore another zip…", a.restoreFromFile)
			}
		})
	})
}

func (a *app) backupRow(c *ui.Context, pl palette, b backup, rule bool) {
	row := ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(12, 0).Key(b.Path)
	if rule {
		row.BorderWidth(1, 0, 0, 0).BorderColor(pl.line)
	}
	row.Children(func() {
		ui.Column(c).Gap(3).Grow(1).Children(func() {
			ui.Text(c, b.Time.Format("Mon 2 Jan 2006, 15:04")).FontSize(15)
			sub := fmt.Sprintf("%.1f KB", float64(b.Size)/1024)
			if b.Before {
				sub = "Saved before a restore, " + sub
			}
			ui.Text(c, sub).FontSize(12).TextColor(pl.muted)
		})
		if ui.Button(c, "Restore").Disabled(a.busy || a.pkgErr != "").Clicked() {
			a.restoring = b.Path
			a.confirm = true
		}
	})
}
