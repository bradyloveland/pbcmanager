package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

// SizesDir holds folder size measurements on a client.
const SizesDir = StateDir + "/sizes"

func sizeKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

func (e *Env) sizeFile(path string) string {
	return e.path(filepath.Join(SizesDir, sizeKey(path)+".json"))
}

func cleanFolder(p string) (string, error) {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\n\x00") {
		return "", &UsageError{"folder paths must be absolute"}
	}
	return filepath.Clean(p), nil
}

// Measure starts measuring each folder in the background (as a transient
// systemd unit at idle priority), unless it's already being measured. Large
// folders take a while; the result is reported with status.
func Measure(env *Env, paths []string) error {
	started := 0
	for _, raw := range paths {
		p, err := cleanFolder(raw)
		if err != nil {
			return err
		}
		unit := "pbcm-measure-" + sizeKey(p)
		if out, err := env.Exec("systemctl", "is-active", unit+".service"); err == nil && strings.TrimSpace(out) == "active" {
			continue
		}
		prev := bundle.FolderSize{Path: p}
		if raw, err := os.ReadFile(env.sizeFile(p)); err == nil {
			json.Unmarshal(raw, &prev)
		}
		prev.Measuring = true
		if err := env.saveSize(prev); err != nil {
			return err
		}
		if out, err := env.Exec("systemd-run", "--quiet", "--collect", "--unit="+unit, "--property=Nice=19",
			"--property=IOSchedulingClass=idle", Path, "measure-run", p); err != nil {
			prev.Measuring = false
			prev.Error = "couldn't start measuring: " + strings.TrimSpace(out)
			env.saveSize(prev)
			continue
		}
		started++
	}
	return json.NewEncoder(env.Stdout).Encode(map[string]int{"started": started})
}

func (e *Env) saveSize(s bundle.FolderSize) error {
	data, _ := json.Marshal(s)
	return writeAtomic(e.sizeFile(s.Path), data, 0o600)
}

// MeasureRun measures one folder now and records the result. It's what the
// background unit runs.
func MeasureRun(env *Env, raw string) error {
	p, err := cleanFolder(raw)
	if err != nil {
		return err
	}
	start := env.now()
	res := bundle.FolderSize{Path: p}
	st, err := os.Stat(env.path(p))
	switch {
	case err != nil || !st.IsDir():
		res.Error = "Folder not found or not mounted."
	default:
		n, err := folderBytes(env.path(p))
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Bytes = &n
		}
	}
	res.Measured, res.Seconds = env.now().Unix(), env.now().Sub(start).Seconds()
	return env.saveSize(res)
}

// folderBytes is the apparent size of everything under path, staying on one
// filesystem like the backup does. It uses du when it can (fast, and it
// copes with huge trees) and walks the tree itself otherwise.
func folderBytes(path string) (int64, error) {
	if du, err := exec.LookPath("du"); err == nil {
		out, _ := exec.Command(du, "-sxb", "--", path).Output()
		// du exits 1 when some entries couldn't be read but still prints a total.
		if f := strings.Fields(string(out)); len(f) > 0 {
			if n, err := strconv.ParseInt(f[0], 10, 64); err == nil {
				return n, nil
			}
		}
	}
	return walkBytes(path)
}

func walkBytes(root string) (int64, error) {
	st, err := os.Lstat(root)
	if err != nil {
		return 0, err
	}
	dev := deviceOf(st)
	var total int64
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if e.IsDir() {
				if deviceOf(info) == dev {
					stack = append(stack, filepath.Join(dir, e.Name()))
				}
				continue
			}
			if info.Mode().IsRegular() {
				total += info.Size()
			}
		}
	}
	return total, nil
}

// Sizes returns every folder measurement on the client.
func (e *Env) sizes() []bundle.FolderSize {
	entries, _ := os.ReadDir(e.path(SizesDir))
	out := []bundle.FolderSize{}
	for _, en := range entries {
		raw, err := os.ReadFile(e.path(filepath.Join(SizesDir, en.Name())))
		if err != nil {
			continue
		}
		var s bundle.FolderSize
		if json.Unmarshal(raw, &s) == nil {
			// A measurement that's been "running" for a day was lost (reboot).
			if s.Measuring && s.Measured > 0 && e.now().Sub(time.Unix(s.Measured, 0)) > 24*time.Hour {
				s.Measuring = false
			}
			out = append(out, s)
		}
	}
	return out
}

func measureUsage(args []string) error {
	if len(args) < 2 {
		return &UsageError{fmt.Sprintf("%s needs a folder", args[0])}
	}
	return nil
}
