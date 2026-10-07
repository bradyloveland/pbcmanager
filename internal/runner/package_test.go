package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

// fakeApt answers dpkg-query, apt-cache and dpkg like a Debian client with
// the given installed package and candidate.
func fakeApt(pkg, installed, candidate string, newer bool) func(string, ...string) (string, error) {
	return func(name string, args ...string) (string, error) {
		switch name {
		case "dpkg-query":
			if args[len(args)-1] == pkg && installed != "" {
				return "ii |" + installed, nil
			}
			return "dpkg-query: no packages found matching " + args[len(args)-1], errors.New("exit 1")
		case "apt-cache":
			return pkg + ":\n  Installed: " + installed + "\n  Candidate: " + candidate + "\n  Version table:\n", nil
		case "dpkg":
			if newer {
				return "", nil
			}
			return "", errors.New("exit 1")
		}
		return "", nil
	}
}

func packageInfo(t *testing.T, exec func(string, ...string) (string, error), lists string) bundle.PackageInfo {
	t.Helper()
	root := t.TempDir()
	if lists != "" {
		os.MkdirAll(root+"/var/lib/apt/lists", 0o755)
		os.WriteFile(root+"/var/lib/apt/lists/"+lists, []byte("x"), 0o644)
	}
	var out bytes.Buffer
	env := &Env{Root: root, Stdout: &out, Exec: exec, Now: func() time.Time { return time.Unix(2000000000, 0) }}
	if err := PackageInfo(env); err != nil {
		t.Fatal(err)
	}
	var info bundle.PackageInfo
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestPackageInfo(t *testing.T) {
	// The static build, with a newer bug-fix release in the lists.
	info := packageInfo(t, fakeApt("proxmox-backup-client-static", "3.4.6-1", "3.4.7-1", true), "download.proxmox.com_debian_pbs-client_dists_bookworm_main_binary-amd64_Packages")
	if info.Package != "proxmox-backup-client-static" || info.Installed != "3.4.6-1" || info.Candidate != "3.4.7-1" || !info.UpdateAvailable() || info.MajorUpdate() ||
		info.ListsUpdated == 0 || info.Checked != 2000000000 {
		t.Fatalf("update available: %+v", info)
	}
	// Up to date.
	if info := packageInfo(t, fakeApt("proxmox-backup-client", "3.4.7-1", "3.4.7-1", false), ""); info.UpdateAvailable() || info.Package != "proxmox-backup-client" {
		t.Fatalf("up to date: %+v", info)
	}
	// A new major version.
	if info := packageInfo(t, fakeApt("proxmox-backup-client", "3.4.7-1", "4.2.7-1", true), ""); !info.MajorUpdate() {
		t.Fatalf("major update: %+v", info)
	}
	// Pinned to an older candidate: dpkg says it isn't newer.
	if info := packageInfo(t, fakeApt("proxmox-backup-client", "3.4.7-1", "3.4.5-1", false), ""); info.UpdateAvailable() {
		t.Fatalf("older candidate: %+v", info)
	}
	// Not installed from apt (copied by hand), and no package lists.
	none := packageInfo(t, fakeApt("proxmox-backup-client", "", "(none)", false), "")
	if none.FromApt || none.UpdateAvailable() || none.ListsUpdated != 0 {
		t.Fatalf("not from apt: %+v", none)
	}
}

func TestPackageInfoIsReadOnly(t *testing.T) {
	var calls []string
	apt := fakeApt("proxmox-backup-client", "3.4.6-1", "3.4.7-1", true)
	packageInfo(t, func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return apt(name, args...)
	}, "")
	for _, c := range calls {
		if strings.Contains(c, "install") || strings.Contains(c, "upgrade") || strings.Contains(c, "apt-get") || strings.HasPrefix(c, "apt-cache update") {
			t.Fatalf("package-info must only read, but ran %q", c)
		}
	}
}

// aptMachine is a fake Debian machine for package-update: apt-get install
// changes the installed version, and each step can be made to fail.
type aptMachine struct {
	pkg, installed, candidate string
	fetchFails, lockHeld      bool
	running                   bool // a pbcm job unit is active
	calls                     []string
}

func (m *aptMachine) exec(name string, args ...string) (string, error) {
	m.calls = append(m.calls, name+" "+strings.Join(args, " "))
	if name == "env" && len(args) > 2 && args[1] == "apt-get" {
		switch args[2] {
		case "update":
			m.fetchFails = false
			return "Reading package lists...\n", nil
		case "install":
			switch {
			case m.lockHeld:
				return "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 123 (apt-get)\n", errors.New("exit 100")
			case m.fetchFails:
				return "E: Failed to fetch http://download.proxmox.com/... 404 Not Found\n", errors.New("exit 100")
			}
			m.installed = strings.TrimPrefix(args[len(args)-1], m.pkg+"=")
			return "Setting up " + m.pkg + " (" + m.installed + ") ...\n", nil
		}
	}
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		if m.running {
			return "active\n", nil
		}
		return "inactive\n", errors.New("exit 3")
	}
	return fakeApt(m.pkg, m.installed, m.candidate, m.installed != m.candidate)(name, args...)
}

func packageUpdate(t *testing.T, m *aptMachine, version string) (bundle.PackageInfo, error) {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(root+ConfigDir, 0o755)
	os.WriteFile(root+BundleFile, []byte(`{"bundle":{"version":1,"jobs":[{"id":"abc123","name":"media"}]}}`), 0o600)
	var out bytes.Buffer
	env := &Env{Root: root, Stdout: &out, Exec: m.exec, Now: time.Now}
	var info bundle.PackageInfo
	if err := PackageUpdate(env, version); err != nil {
		return info, err
	}
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	return info, nil
}

func TestPackageUpdate(t *testing.T) {
	// A bug-fix release: only that package, to exactly that version, with
	// nothing removed and no prompts.
	m := &aptMachine{pkg: "proxmox-backup-client", installed: "4.2.6-1", candidate: "4.2.8-1"}
	info, err := packageUpdate(t, m, "4.2.8-1")
	if err != nil || info.Installed != "4.2.8-1" || info.UpdateAvailable() {
		t.Fatalf("update: %+v %v", info, err)
	}
	var install string
	for _, c := range m.calls {
		if strings.Contains(c, "apt-get install") {
			install = c
		}
	}
	for _, want := range []string{"DEBIAN_FRONTEND=noninteractive", "--only-upgrade", "--no-remove", "--force-confold", " proxmox-backup-client=4.2.8-1"} {
		if !strings.Contains(install, want) {
			t.Fatalf("install command %q lacks %q", install, want)
		}
	}

	// The static build is updated under its own name.
	m = &aptMachine{pkg: "proxmox-backup-client-static", installed: "4.2.4-1", candidate: "4.2.8-1"}
	if info, err := packageUpdate(t, m, "4.2.8-1"); err != nil || info.Package != "proxmox-backup-client-static" || info.Installed != "4.2.8-1" {
		t.Fatalf("static: %+v %v", info, err)
	}

	// Old package lists: the download fails, so they're refreshed and the
	// same version is tried again.
	m = &aptMachine{pkg: "proxmox-backup-client", installed: "4.2.6-1", candidate: "4.2.8-1", fetchFails: true}
	if info, err := packageUpdate(t, m, "4.2.8-1"); err != nil || info.Installed != "4.2.8-1" {
		t.Fatalf("after apt-get update: %+v %v", info, err)
	}
}

func TestPackageUpdateRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		m       aptMachine
		version string
		want    string
	}{
		{"up to date", aptMachine{installed: "4.2.8-1", candidate: "4.2.8-1"}, "4.2.8-1", "no update waiting"},
		{"not the version shown", aptMachine{installed: "4.2.6-1", candidate: "4.2.9-1"}, "4.2.8-1", "now 4.2.9-1"},
		{"major version", aptMachine{installed: "3.4.7-1", candidate: "4.2.8-1"}, "4.2.8-1", "major version"},
		{"not from apt", aptMachine{installed: "", candidate: "(none)"}, "4.2.8-1", "wasn't installed as a package"},
		{"backup running", aptMachine{installed: "4.2.6-1", candidate: "4.2.8-1", running: true}, "4.2.8-1", `"media" is running`},
		{"apt busy", aptMachine{installed: "4.2.6-1", candidate: "4.2.8-1", lockHeld: true}, "4.2.8-1", "another program is installing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.m
			m.pkg = "proxmox-backup-client"
			_, err := packageUpdate(t, &m, tc.version)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if tc.name != "apt busy" {
				for _, c := range m.calls {
					if strings.Contains(c, "apt-get") {
						t.Fatalf("ran %q", c)
					}
				}
			}
		})
	}
}
