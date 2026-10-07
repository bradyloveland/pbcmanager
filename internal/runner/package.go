package runner

import (
	"encoding/json"
	"errors"
	"fmt"
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
	info := readPackage(env)
	return json.NewEncoder(env.Stdout).Encode(info)
}

func readPackage(env *Env) bundle.PackageInfo {
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
	return info
}

// PackageUpdate upgrades proxmox-backup-client to version, which must be the
// update package-info reports. It installs nothing else, except what that
// version depends on, and never removes packages. A new major version is
// refused: it usually comes with an OS upgrade, which is done by hand.
func PackageUpdate(env *Env, version string) error {
	info := readPackage(env)
	switch {
	case !info.FromApt:
		return errors.New("proxmox-backup-client wasn't installed as a package on this machine, so it can't be updated from here")
	case !info.UpdateAvailable():
		return fmt.Errorf("there's no update waiting: %s is installed and it's the newest in this machine's package lists", info.Installed)
	case version != info.Candidate:
		return fmt.Errorf("the update waiting is now %s, not %s. Use Check now, then try again", info.Candidate, version)
	case info.MajorUpdate():
		return fmt.Errorf("%s is a new major version, which usually comes with an upgrade of the operating system. Follow Proxmox's upgrade notes and update it on the machine itself", version)
	}
	if job := env.runningJob(); job != "" {
		return fmt.Errorf("the backup job %q is running. Update when it has finished", job)
	}
	target := info.Package + "=" + version
	out, err := aptInstall(env, target)
	if err != nil && strings.Contains(out, "Failed to fetch") {
		// The package lists are older than what the mirror has now. Fetch
		// them and try the same version again.
		if uout, uerr := env.Exec("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update", "-q"); uerr != nil {
			return fmt.Errorf("couldn't download %s, and apt-get update failed too: %s", target, lastLines(uout, 3))
		}
		out, err = aptInstall(env, target)
	}
	if err != nil {
		switch {
		case strings.Contains(out, "Could not get lock") || strings.Contains(out, "is held by"):
			return errors.New("another program is installing updates on this machine. Try again when it has finished")
		case strings.Contains(out, "was not found"):
			return fmt.Errorf("%s isn't offered any more. Use Check now, then try again", version)
		}
		return fmt.Errorf("apt-get couldn't install %s: %s", target, lastLines(out, 3))
	}
	after := readPackage(env)
	if after.Installed != version {
		return fmt.Errorf("apt-get finished, but %s is still installed", after.Installed)
	}
	return json.NewEncoder(env.Stdout).Encode(after)
}

// aptInstall upgrades one package without prompts: keep changed config files,
// no recommended extras, and fail rather than remove anything.
func aptInstall(env *Env, target string) (string, error) {
	return env.Exec("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "-q",
		"--only-upgrade", "--no-install-recommends", "--no-remove",
		"-o", "DPkg::Lock::Timeout=60",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		target)
}

// runningJob is the name of a backup job that's running ("" if none).
func (e *Env) runningJob() string {
	s, err := e.loadStored()
	if err != nil {
		return ""
	}
	for _, j := range s.Bundle.Jobs {
		if e.unitActive(j.ID) {
			return j.Name
		}
	}
	return ""
}

// lastLines is the last n non-empty lines of out, joined with spaces.
func lastLines(out string, n int) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return "no output"
	}
	return strings.Join(lines, " ")
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
