package clients

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/sshx"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// TestRealClient sets up a real machine. CI runs it against fresh Debian and
// Ubuntu containers; it's skipped unless these are set:
//
//	PBCM_IT_ADDR      host:port of the client's SSH server
//	PBCM_IT_USER      user to sign in as for setup (root or a sudo user)
//	PBCM_IT_PASSWORD  that user's password
//	PBCM_IT_RUNNER    path to a linux/amd64 pbcm-runner build
//	PBCM_IT_EXPECT    optional text the setup log must contain
func TestRealClient(t *testing.T) {
	addrStr := os.Getenv("PBCM_IT_ADDR")
	if addrStr == "" {
		t.Skip("set PBCM_IT_ADDR to run against a real client")
	}
	host, portStr, _ := strings.Cut(addrStr, ":")
	port, _ := strconv.Atoi(portStr)
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	st, err := store.Open(filepath.Join(dir, "pbcm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := sshx.LoadOrCreateIdentity(filepath.Join(dir, "ssh", "id_ed25519"), "pbcm-server@ci")
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, id, os.Getenv("PBCM_IT_RUNNER"))
	ctx := context.Background()

	probe, err := m.Probe(ctx, host, port)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("host key %s %s", probe.Type, probe.Fingerprint)
	c, task, err := m.Add(AddRequest{Name: "ci", Address: host, Port: port, HostKey: probe.HostKey,
		Login: Login{User: os.Getenv("PBCM_IT_USER"), Password: os.Getenv("PBCM_IT_PASSWORD")}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Minute)
	var v TaskView
	for time.Now().Before(deadline) {
		if v = task.View(0); v.Done {
			break
		}
		time.Sleep(time.Second)
	}
	log := strings.Join(v.Lines, "\n")
	t.Logf("setup log:\n%s", log)
	if !v.OK {
		t.Fatalf("setup failed: %s", v.Error)
	}
	if want := os.Getenv("PBCM_IT_EXPECT"); want != "" && !strings.Contains(log, want) {
		t.Fatalf("setup log doesn't mention %q", want)
	}
	c, _ = st.GetClient(c.ID)
	if c.Status != store.ClientReady || c.ClientVersion == "" || c.RunnerVersion == "" || c.Arch != "x86_64" {
		t.Fatalf("client after setup: %+v", c)
	}
	t.Logf("ready: %s, proxmox-backup-client %s, systemd %s", c.OSPretty, c.ClientVersion, c.SystemdVersion)

	l, err := m.Browse(ctx, c.ID, "/")
	if err != nil || !contains(l.Dirs, "etc") {
		t.Fatalf("browse /: %+v %v", l, err)
	}
	// The pbcm account can't do anything but run pbcm-runner.
	if _, err := m.runnerCommand(ctx, c, "cat", "/etc/shadow"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("pbcm ran something other than pbcm-runner: %v", err)
	}
	if _, err := m.Check(ctx, c.ID); err != nil {
		t.Fatal(err)
	}

	// Setting up again (repair) works and leaves one key line.
	task, err = m.Repair(c.ID, Login{User: os.Getenv("PBCM_IT_USER"), Password: os.Getenv("PBCM_IT_PASSWORD")}, "")
	if err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		if v = task.View(0); v.Done {
			break
		}
		time.Sleep(time.Second)
	}
	if !v.OK || !strings.Contains(strings.Join(v.Lines, "\n"), "already installed") {
		t.Fatalf("repair: %+v", v)
	}

	out, err := m.Remove(ctx, c.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("uninstall:\n%s", out)
	if _, err := m.runnerCommand(ctx, c, "detect"); err == nil {
		t.Fatal("pbcm still works after uninstall")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
