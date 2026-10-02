package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

// packages are the names proxmox-backup-client is installed under.
var packages = []string{"proxmox-backup-client", "proxmox-backup-client-static"}

// PackageInfo reports the installed proxmox-backup-client and the newest one
// in the client's package lists. It only reads: it doesn't run apt update,
// and changes nothing.
func PackageInfo(env *Env) error {
	info := bundle.PackageInfo{Checked: env.now().Unix()}
	for _, pkg := range packages {
		out, err := env.Exec("dpkg-query", "-W", "-f=${db:Status-Abbrev}|${Version}", pkg)
		status, version, _ := strings.Cut(strings.TrimSpace(out), "|")
		if err == nil && strings.HasPrefix(status, "ii") && version != "" {
			info.Package, info.Installed, info.FromApt = pkg, version, true
			break
		}
	}
	if info.FromApt {
		if out, err := env.Exec("apt-cache", "policy", info.Package); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok {
					if v = strings.TrimSpace(v); v != "(none)" {
						info.Candidate = v
					}
				}
			}
		}
		if info.Candidate != "" && info.Candidate != info.Installed {
			_, err := env.Exec("dpkg", "--compare-versions", info.Candidate, "gt", info.Installed)
			info.Newer = err == nil
		}
	}
	info.ListsUpdated = listsUpdated(env.path("/var/lib/apt/lists"))
	return json.NewEncoder(env.Stdout).Encode(info)
}

// listsUpdated is when apt's package lists last changed (0 if unknown).
func listsUpdated(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var newest int64
	for _, e := range entries {
		if e.IsDir() || e.Name() == "lock" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.ModTime().Unix() > newest {
			newest = st.ModTime().Unix()
		}
	}
	return newest
}
