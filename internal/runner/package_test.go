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
