package clients

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/sshx"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// TestEndToEnd backs up a real client to a real Proxmox Backup Server. CI
// runs it with two containers that boot systemd; it's skipped unless these
// are set:
//
//	PBCM_E2E_CLIENT        client address (SSH on port 22)
//	PBCM_E2E_ROOT_PASSWORD the client's root password
//	PBCM_E2E_RUNNER        linux/amd64 pbcm-runner build
//	PBCM_E2E_PBS           PBS address
//	PBCM_E2E_FINGERPRINT   PBS certificate fingerprint
//	PBCM_E2E_TOKEN_SECRET  secret of the API token ci@pbs!ci (DatastoreBackup on /datastore/store)
func TestEndToEnd(t *testing.T) {
	clientAddr := os.Getenv("PBCM_E2E_CLIENT")
	if clientAddr == "" {
		t.Skip("set PBCM_E2E_CLIENT to run against real containers")
	}
	rootPW := os.Getenv("PBCM_E2E_ROOT_PASSWORD")
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	st, err := store.Open(filepath.Join(dir, "pbcm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := sshx.LoadOrCreateIdentity(filepath.Join(dir, "ssh", "id_ed25519"), "pbcm-server@e2e")
	m := New(st, id, os.Getenv("PBCM_E2E_RUNNER"))
	m.Sync.LogDir = filepath.Join(dir, "logs")
	ctx := context.Background()

	// root shell on the client, to check what setup and runs left behind.
	probe, err := m.Probe(ctx, clientAddr, 22)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, _ := sshx.ParseKey(probe.HostKey)
	asRoot := func(cmd string) (string, int) {
		t.Helper()
		conn, err := sshx.Dial(ctx, sshx.Target{Addr: net.JoinHostPort(clientAddr, "22"), HostKey: hostKey, User: "root", Auth: sshx.PasswordAuth(rootPW)})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		out, errOut, code, err := sshx.Output(conn, cmd, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out + errOut, code
	}

	c, task, err := m.Add(AddRequest{Name: "e2e", Address: clientAddr, Port: 22, HostKey: probe.HostKey, Login: Login{User: "root", Password: rootPW}})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitTask(t, task, 10*time.Minute); !v.OK {
		t.Fatalf("setup failed: %s\n%s", v.Error, strings.Join(v.Lines, "\n"))
	}
	c, _ = st.GetClient(c.ID)
	t.Logf("client ready: %s, proxmox-backup-client %s, systemd %s", c.OSPretty, c.ClientVersion, c.SystemdVersion)

	d := &store.Destination{ID: "pbs", Name: "CI PBS", Host: os.Getenv("PBCM_E2E_PBS"), Port: 8007, Datastore: "store",
		Username: "ci@pbs", TokenName: "ci", Secret: os.Getenv("PBCM_E2E_TOKEN_SECRET"), Fingerprint: os.Getenv("PBCM_E2E_FINGERPRINT")}
	if err := st.SaveDestination(d); err != nil {
		t.Fatal(err)
	}
	// The server's own client can reach PBS (connection test).
	pbs := &backups.PBS{}
	if u, err := pbs.Status(ctx, d); err != nil {
		t.Fatalf("server-side connection test: %v", err)
	} else if u.Total != nil {
		t.Logf("datastore: %d bytes free of %d", *u.Avail, *u.Total)
	}

	job := func(id, folder, rate string) *store.Job {
		j := &store.Job{ID: id, ClientID: c.ID, Name: id, BackupID: "e2e-" + id, Shares: []bundle.Share{{Path: folder, Archive: "data"}},
			Schedule: bundle.Schedule{Type: "hourly", Time: "00:30", IntervalHours: 6}, ChangeDetection: "metadata", Rate: rate,
			Enabled: true, Destinations: []string{"pbs"}}
		if err := st.SaveJob(j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	ok, missing, slow := job("ok", "/etc", ""), job("missing", "/not/mounted", ""), job("slow", "/usr", "512KiB")
	if err := m.Apply(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if out, code := asRoot("systemctl list-timers --all --no-legend 'pbcm-job-*'"); code != 0 || !strings.Contains(out, "pbcm-job-ok.timer") {
		t.Fatalf("timers on the client:\n%s", out)
	}
	if out, _ := asRoot("ls /etc/pbcm/client/credentials/"); !strings.Contains(out, "dest-pbs.cred") {
		t.Fatalf("credential should be encrypted with systemd-creds:\n%s", out)
	}
	if out, _ := asRoot("grep -rlF " + sshx.Quote(d.Secret) + " /etc/pbcm/client || true"); strings.TrimSpace(out) != "" {
		t.Fatalf("token secret readable in plain text on the client:\n%s", out)
	}

	runUntilDone := func(j *store.Job) *store.Run {
		t.Helper()
		if err := m.StartJob(ctx, j.ID); err != nil {
			t.Fatalf("start %s: %v", j.Name, err)
		}
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			if _, err := m.SyncClient(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
			runs, _ := st.ListRuns(store.RunFilter{JobID: j.ID, Limit: 1})
			if len(runs) == 1 && runs[0].Status != bundle.Running {
				return runs[0]
			}
		}
		t.Fatalf("%s didn't finish", j.Name)
		return nil
	}

	r := runUntilDone(ok)
	chunk, _ := m.RunLog(ctx, c.ID, r.ID, 0)
	t.Logf("backup log:\n%s", chunk.Text)
	if r.Status != bundle.Success || r.ExitCode == nil || *r.ExitCode != 0 || !r.LogSaved {
		t.Fatalf("real backup: %+v", r)
	}
	if snaps, err := pbs.Snapshots(ctx, d, ok.BackupID); err != nil || len(snaps) != 1 {
		t.Fatalf("snapshot on PBS: %+v %v", snaps, err)
	}

	if r := runUntilDone(missing); r.Status != bundle.Failed || !strings.Contains(r.Summary, "don't exist or aren't mounted") {
		t.Fatalf("missing folder: %+v", r)
	}

	if err := m.StartJob(ctx, slow.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second)
	if err := m.CancelJob(ctx, slow.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	var cancelled *store.Run
	for time.Now().Before(deadline) && cancelled == nil {
		time.Sleep(2 * time.Second)
		m.SyncClient(ctx, c.ID)
		if runs, _ := st.ListRuns(store.RunFilter{JobID: slow.ID, Limit: 1}); len(runs) == 1 && runs[0].Status != bundle.Running {
			cancelled = runs[0]
		}
	}
	if cancelled == nil || cancelled.Status != bundle.Cancelled {
		t.Fatalf("cancel: %+v", cancelled)
	}

	// Uninstall removes the timers, the credentials and (shortly after) the account.
	out, err := m.Remove(ctx, c.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("uninstall:\n%s", out)
	time.Sleep(8 * time.Second)
	if out, code := asRoot("id pbcm"); code == 0 {
		t.Fatalf("the pbcm account should be gone: %s", out)
	}
	if out, _ := asRoot("ls /etc/systemd/system/ | grep -c pbcm-job || true"); strings.TrimSpace(out) != "0" {
		t.Fatalf("units left behind: %s", out)
	}
	if out, _ := asRoot("test -e /etc/pbcm/client && echo left || echo gone"); !strings.Contains(out, "gone") {
		t.Fatal("client settings left behind")
	}
}

func waitTask(t *testing.T, task *Task, timeout time.Duration) TaskView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v := task.View(0); v.Done {
			return v
		}
		time.Sleep(time.Second)
	}
	t.Fatal("task didn't finish in " + strconv.Itoa(int(timeout.Seconds())) + "s")
	return TaskView{}
}
