package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/release"
)

// docFiles are in every release but aren't needed in the program folder.
var docFiles = map[string]bool{"README.md": true, "LICENSE": true, "CHANGELOG.md": true}

// legacyFiles are what versions before 2.2.1 swapped, for rolling back an
// update recorded without its list of files.
var legacyFiles = []string{"pbcm", "pbcm-runner", "pbcm-runner-arm64", "MANIFEST", "MANIFEST.sig", "install.sh", "uninstall.sh"}

// programFiles are the files an update installs: everything the release's
// signed manifest lists except the documentation, plus the manifest itself.
// Taking them from the release, not a list built into this version, means a
// file that's new in the release (as pbcm-runner-arm64 was in 2.2.0) is
// installed too.
func programFiles(m *release.Manifest) []string {
	names := []string{"MANIFEST", "MANIFEST.sig"}
	for name := range m.Files {
		if !docFiles[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func executable(name string) bool {
	return name == "pbcm" || strings.HasPrefix(name, "pbcm-") || strings.HasSuffix(name, ".sh")
}

// keepBackups is how many database copies taken before updates are kept.
const keepBackups = 5

// Files is where things are on the server.
type Files struct {
	AppDir  string // the program folder (/opt/pbcm)
	DataDir string // the data folder (/var/lib/pbcm), holding pbcm.db
}

func (f Files) db() string { return filepath.Join(f.DataDir, "pbcm.db") }

// swapIn installs the staged release's program files, keeping the current
// ones as .prev. It returns the files it installed and, of those, the ones
// that didn't exist before (a rollback removes them).
func (f Files) swapIn(staged string, m *release.Manifest) (installed, added []string, err error) {
	names := programFiles(m)
	for _, name := range names {
		src := filepath.Join(staged, name)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		mode := os.FileMode(0o644)
		if executable(name) {
			mode = 0o755
		}
		dst := filepath.Join(f.AppDir, name)
		if err := copyFile(src, dst+".new", mode); err != nil {
			return nil, nil, fmt.Errorf("couldn't write the new %s: %w", name, err)
		}
	}
	// Keep the current files as .prev (by hard link, so pbcm is never
	// missing), then move the new ones into place.
	for _, name := range names {
		dst := filepath.Join(f.AppDir, name)
		if _, err := os.Stat(dst + ".new"); err != nil {
			continue
		}
		os.Remove(dst + ".prev")
		if _, err := os.Stat(dst); err == nil {
			if err := os.Link(dst, dst+".prev"); err != nil {
				if err := copyFile(dst, dst+".prev", 0o755); err != nil {
					return nil, nil, fmt.Errorf("couldn't keep the current %s: %w", name, err)
				}
			}
		} else {
			added = append(added, name)
		}
	}
	for _, name := range names {
		dst := filepath.Join(f.AppDir, name)
		if _, err := os.Stat(dst + ".new"); err != nil {
			continue
		}
		if err := os.Rename(dst+".new", dst); err != nil {
			return nil, nil, err
		}
		installed = append(installed, name)
	}
	return installed, added, nil
}

// HasPrevious reports whether the previous version's files are kept.
func (f Files) HasPrevious() bool {
	_, err := os.Stat(filepath.Join(f.AppDir, "pbcm.prev"))
	return err == nil
}

// Rollback puts back the previous version's files and the database copy
// taken before the update. The database must be closed (the server isn't
// running, or has stopped).
func (f Files) Rollback(reason string, manual bool) error {
	s, err := ReadState(f.DataDir)
	if err != nil {
		return err
	}
	if s == nil || s.Backup == "" || s.Phase == RolledBack {
		return errors.New("there's no update to undo")
	}
	if !f.HasPrevious() {
		return errors.New("the previous version's files aren't there anymore, so it can't be put back")
	}
	if _, err := os.Stat(s.Backup); err != nil {
		return errors.New("the database copy from before the update is missing, so it can't be put back")
	}
	// The database first: if this fails, the new version keeps running.
	tmp := f.db() + ".restore"
	if err := copyFile(s.Backup, tmp, 0o600); err != nil {
		return err
	}
	for _, ext := range []string{"-wal", "-shm"} {
		os.Remove(f.db() + ext)
	}
	if err := os.Rename(tmp, f.db()); err != nil {
		return err
	}
	names := s.Files
	if len(names) == 0 {
		names = legacyFiles // recorded by a version before 2.2.1
	}
	for _, name := range names {
		dst := filepath.Join(f.AppDir, name)
		if _, err := os.Stat(dst + ".prev"); err == nil {
			if err := os.Rename(dst+".prev", dst); err != nil {
				return err
			}
		}
	}
	// Files the failed version brought that the previous one didn't have.
	for _, name := range s.Added {
		os.Remove(filepath.Join(f.AppDir, name))
	}
	failed := s.To
	s.Phase, s.Reason, s.Manual, s.Seen, s.At = RolledBack, reason, manual, false, now()
	s.From, s.To = failed, s.From
	return writeState(f.DataDir, s)
}

// pruneBackups keeps the newest few database copies.
func pruneBackups(dir string) {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "pbcm-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // the names sort by time
	for len(names) > keepBackups {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

func backupName(version string, t time.Time) string {
	return "pbcm-" + t.UTC().Format("20060102-150405") + "-" + strings.ReplaceAll(version, "/", "_") + ".db"
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// notesFor pulls a version's section out of a CHANGELOG.md.
func notesFor(changelog []byte, version string) string {
	lines := strings.Split(string(changelog), "\n")
	var out []string
	in := false
	for _, l := range lines {
		if strings.HasPrefix(l, "## [") {
			if in {
				break
			}
			in = strings.HasPrefix(l, "## ["+version+"]")
			continue
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
