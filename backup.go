package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A backup is a zip of Armoury Crate SE's GamepadCustomize folder: the
// Desktop layout (Profiles, SystemTemplates) and its key combinations
// (CombKeys). Backups are saved in the backup folder, the home folder
// unless the user picks another.

const (
	backupPrefix = "Armoury Crate layout "
	stampFormat  = "2006-01-02 150405"
	beforeNote   = " (before restore)"
)

type backup struct {
	Path string
	Time time.Time
	Size int64
	// Before is a backup made automatically before a restore.
	Before bool
}

func (b backup) Name() string { return filepath.Base(b.Path) }

// Settings are kept in %APPDATA%\armoury-crate-fix\settings.json.
type settings struct {
	AutoFix bool `json:"autoFix"`
	// CrateFix shows the Armoury Crate launch fix: its sections in the
	// window, its tray submenu, and its watcher. Off by default: ASUS fixed
	// the hang, and this stays for if it comes back.
	CrateFix     bool   `json:"crateFix"`
	BackupDir    string `json:"backupDir"`
	GyroServer   bool   `json:"gyroServer"`
	KeepAwake    bool   `json:"keepAwake"`
	GyroInvertUD bool   `json:"gyroInvertUpDown"`
	GyroInvertLR bool   `json:"gyroInvertLeftRight"`
	GyroPreview  int    `json:"gyroPreview"` // 0: the Ally as held, 1: as games see it
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "armoury-crate-fix", "settings.json")
}

func defaultBackupDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

func loadSettings() settings {
	s := settings{AutoFix: true, BackupDir: defaultBackupDir()}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.BackupDir == "" {
		s.BackupDir = defaultBackupDir()
	}
	return s
}

func (s settings) save() error {
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(settingsPath(), b, 0o644)
}

// listBackups returns the backups in dir, newest first.
func listBackups(dir string) []backup {
	files, _ := filepath.Glob(filepath.Join(dir, backupPrefix+"*.zip"))
	var out []backup
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), backupPrefix), ".zip")
		before := strings.HasSuffix(name, beforeNote)
		name = strings.TrimSuffix(name, beforeNote)
		t, err := time.ParseInLocation(stampFormat, name, time.Local)
		if err != nil {
			continue
		}
		var size int64
		if st, err := os.Stat(f); err == nil {
			size = st.Size()
		}
		out = append(out, backup{Path: f, Time: t, Size: size, Before: before})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// createBackup zips the current layout into dir.
func createBackup(p paths, dir string, before bool) (backup, error) {
	// While a launch has the key combinations hidden, a zip would miss them.
	if n, _ := os.ReadDir(p.hidden); len(n) > 0 {
		return backup{}, errors.New("a launch has the key combinations hidden; back up once it has finished")
	}
	src := p.gamepad()
	if _, err := os.Stat(src); err != nil {
		return backup{}, errors.New("there is no Desktop layout to back up yet")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return backup{}, err
	}
	now := time.Now()
	name := backupPrefix + now.Format(stampFormat)
	if before {
		name += beforeNote
	}
	dst := filepath.Join(dir, name+".zip")
	if _, err := os.Stat(dst); err == nil {
		return backup{}, errors.New("a backup was made this second already")
	}
	if err := zipDir(src, dst); err != nil {
		_ = os.Remove(dst)
		return backup{}, err
	}
	st, _ := os.Stat(dst)
	return backup{Path: dst, Time: now.Truncate(time.Second), Size: st.Size(), Before: before}, nil
}

// restoreBackup closes Armoury Crate SE, zips the current layout into dir,
// then replaces the layout with the one in zipPath.
func restoreBackup(p paths, zipPath, dir string) error {
	staged := p.gamepad() + ".restoring"
	_ = os.RemoveAll(staged)
	defer os.RemoveAll(staged)
	if err := unzip(zipPath, staged); err != nil {
		return fmt.Errorf("%s isn't a layout backup: %w", filepath.Base(zipPath), err)
	}
	if _, err := os.Stat(filepath.Join(staged, "Profiles")); err != nil {
		return fmt.Errorf("%s has no Desktop layout in it", filepath.Base(zipPath))
	}

	if p.family != "" && isRunning() { // family is empty only in tests
		closeArmouryCrate()
	}
	if _, err := os.Stat(p.gamepad()); err == nil {
		if _, err := createBackup(p, dir, true); err != nil {
			return fmt.Errorf("couldn't back up the current layout first: %w", err)
		}
	}

	dst := p.gamepad()
	old := dst + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("couldn't replace the layout: %w", err)
	}
	if err := os.Rename(staged, dst); err != nil {
		_ = os.Rename(old, dst)
		return fmt.Errorf("couldn't replace the layout: %w", err)
	}
	_ = os.RemoveAll(old)
	return nil
}

func zipDir(src, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func unzip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		to := filepath.Join(dst, filepath.FromSlash(zf.Name))
		if !strings.HasPrefix(to, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path %q", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(to, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		in, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(to)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
