package clients

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/clients/clienttest"
	"github.com/bradyloveland/pbcmanager/internal/runner"
	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/sshx"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

const (
	rootPW  = clienttest.RootPassword
	alicePW = clienttest.AlicePassword
	tmpDir  = clienttest.TmpDir
)

type harness struct {
	m    *Manager
	st   *store.Store
	host *clienttest.Host
	dir  string
}

func newHarness(t *testing.T) *harness {
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	st, err := store.Open(filepath.Join(dir, "pbcm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := sshx.LoadOrCreateIdentity(filepath.Join(dir, "ssh", "id_ed25519"), "pbcm-server@test")
	if err != nil {
		t.Fatal(err)
	}
	runnerPath := filepath.Join(dir, "pbcm-runner")
	os.WriteFile(runnerPath, []byte("RUNNER-BINARY"), 0o755)
	m := New(st, id, runnerPath)
	m.timeout = 5 * time.Second
	m.Sync.LogDir = filepath.Join(dir, "logs")
	return &harness{m: m, st: st, host: clienttest.New(t), dir: dir}
}

func (h *harness) probe(t *testing.T) *ProbeResult {
	res, err := h.m.Probe(context.Background(), "127.0.0.1", h.host.Port())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func wait(t *testing.T, task *Task) TaskView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if v := task.View(0); v.Done {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task didn't finish")
	return TaskView{}
}

func (h *harness) add(t *testing.T, login Login) (*store.Client, TaskView) {
	t.Helper()
	p := h.probe(t)
	c, task, err := h.m.Add(AddRequest{Name: "nas", Address: "127.0.0.1", Port: h.host.Port(), HostKey: p.HostKey, Login: login})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, task)
	c, _ = h.st.GetClient(c.ID)
	return c, v
}

func TestProbeShowsTheHostKey(t *testing.T) {
	h := newHarness(t)
	res := h.probe(t)
	if res.Fingerprint != ssh.FingerprintSHA256(h.host.HostKey()) || res.Type != "ssh-ed25519" {
		t.Fatalf("probe: %+v", res)
	}
	if _, err := h.m.Probe(context.Background(), "127.0.0.1", 1); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("closed port: %v", err)
	}
	if _, err := h.m.Probe(context.Background(), "bad host!", 22); err == nil {
		t.Fatal("bad address accepted")
	}
}

func TestAddAsRoot(t *testing.T) {
	h := newHarness(t)
	c, v := h.add(t, Login{User: "root", Password: rootPW})
	if !v.OK || c.Status != store.ClientReady {
		t.Fatalf("setup failed: %+v %v", v, c.StatusDetail)
	}
	if c.OSPretty != "Debian GNU/Linux 12 (bookworm)" || c.ClientVersion != "3.4.1" || c.Hostname == "" || c.LastContact == 0 || c.Arch != "x86_64" {
		t.Fatalf("details not saved: %+v", c)
	}
	if string(h.host.Uploaded(tmpDir+"/pbcm-runner")) != "RUNNER-BINARY" || !bytes.Equal(h.host.Uploaded(tmpDir+"/setup.sh"), setupScript) ||
		strings.TrimSpace(string(h.host.Uploaded(tmpDir+"/key.pub"))) != h.m.identity.AuthorizedKey {
		t.Fatal("uploaded files differ from what was sent")
	}
	joined := strings.Join(v.Lines, "\n")
	for _, want := range []string{"Signed in as root", "Setup finished", "Ready: Debian"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(h.dir, "pbcm.db"))
	wal, _ := os.ReadFile(filepath.Join(h.dir, "pbcm.db-wal"))
	if bytes.Contains(raw, []byte(rootPW)) || bytes.Contains(wal, []byte(rootPW)) || strings.Contains(joined, rootPW) {
		t.Fatal("the root password was stored or logged")
	}
}

func TestSudoPasswordOnlyGoesToSudo(t *testing.T) {
	h := newHarness(t)
	c, v := h.add(t, Login{User: "alice", Password: alicePW})
	if !v.OK || c.Status != store.ClientReady {
		t.Fatalf("setup failed: %+v", v)
	}
	sawScript := false
	for _, r := range h.host.Execs() {
		if bytes.Contains(r.Stdin, []byte(alicePW)) && !strings.HasPrefix(r.Cmd, "sudo -S -p '' ") {
			t.Errorf("password sent to %q", r.Cmd)
		}
		if strings.Contains(r.Cmd, alicePW) {
			t.Errorf("password on a command line: %q", r.Cmd)
		}
		if r.Cmd == "sudo -S -p '' bash "+tmpDir+"/setup.sh" {
			sawScript = true
		}
	}
	if !sawScript {
		t.Fatal("setup.sh should run through sudo")
	}
}

func TestWrongPassword(t *testing.T) {
	h := newHarness(t)
	p := h.probe(t)
	c, task, err := h.m.Add(AddRequest{Name: "nas", Address: "127.0.0.1", Port: h.host.Port(), HostKey: p.HostKey,
		Login: Login{User: "alice", Password: "wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, task)
	if v.OK || !strings.Contains(v.Error, "didn't accept the sign-in") {
		t.Fatalf("wrong password: %+v", v)
	}
	if c, _ = h.st.GetClient(c.ID); c.Status != store.ClientError {
		t.Fatalf("status %s", c.Status)
	}
}

func TestHostKeyMustMatchWhatWasChecked(t *testing.T) {
	h := newHarness(t)
	other := sshx.FormatKey(clienttest.NewSigner(t).PublicKey())
	c, task, err := h.m.Add(AddRequest{Name: "nas", Address: "127.0.0.1", Port: h.host.Port(), HostKey: other,
		Login: Login{User: "root", Password: rootPW}})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, task)
	if v.OK || !strings.Contains(v.Error, "isn't the one you checked") {
		t.Fatalf("expected host key refusal: %+v", v)
	}
	for _, r := range h.host.Execs() {
		t.Errorf("nothing should run on an unverified host, ran %q", r.Cmd)
	}
	if c, _ = h.st.GetClient(c.ID); c.Status != store.ClientError {
		t.Fatalf("status %s", c.Status)
	}
}

func TestSetupErrorIsReported(t *testing.T) {
	h := newHarness(t)
	h.host.FailSetupWith("Ubuntu 20.04 LTS isn't supported. Clients need Debian 12 or 13.")
	c, v := h.add(t, Login{User: "root", Password: rootPW})
	if v.OK || c.Status != store.ClientError || !strings.Contains(c.StatusDetail, "isn't supported") {
		t.Fatalf("got %+v / %s", v, c.StatusDetail)
	}
}

func TestSignInWithServerKey(t *testing.T) {
	h := newHarness(t)
	h.host.AuthorizeRootKey(h.m.identity.Signer.PublicKey())
	c, v := h.add(t, Login{User: "root", UseKey: true})
	if !v.OK || c.Status != store.ClientReady {
		t.Fatalf("key sign-in setup failed: %+v", v)
	}
}

func TestDuplicatesAndBadInput(t *testing.T) {
	h := newHarness(t)
	h.add(t, Login{User: "root", Password: rootPW})
	p := h.probe(t)
	_, _, err := h.m.Add(AddRequest{Name: "nas", Address: "127.0.0.2", Port: 22, HostKey: p.HostKey, Login: Login{Password: "x"}})
	if err == nil || !strings.Contains(err.Error(), "already a client called") {
		t.Fatalf("duplicate name: %v", err)
	}
	_, _, err = h.m.Add(AddRequest{Name: "other", Address: "127.0.0.1", Port: h.host.Port(), HostKey: p.HostKey, Login: Login{Password: "x"}})
	if err == nil || !strings.Contains(err.Error(), "already been added") {
		t.Fatalf("duplicate address: %v", err)
	}
	_, _, err = h.m.Add(AddRequest{Name: "x", Address: "127.0.0.3", HostKey: p.HostKey, Login: Login{User: "root"}})
	if err == nil || !strings.Contains(err.Error(), "Enter the password") {
		t.Fatalf("missing password: %v", err)
	}
}

func TestCheckBrowseHostKeyChangeRepairAndRemove(t *testing.T) {
	h := newHarness(t)
	c, _ := h.add(t, Login{User: "root", Password: rootPW})
	ctx := context.Background()
	h.host.Mkdir("/srv/My Files/Media")
	h.host.Mkdir("/srv/My Files/Documents")

	l, err := h.m.Browse(ctx, c.ID, "/srv/My Files")
	if err != nil || l.Path != "/srv/My Files" || strings.Join(l.Dirs, ",") != "Documents,Media" {
		t.Fatalf("browse: %+v %v", l, err)
	}
	execs := h.host.Execs()
	if last := execs[len(execs)-1]; last.User != "pbcm" || last.Cmd != "browse '/srv/My Files'" {
		t.Fatalf("browse ran %q as %s", last.Cmd, last.User)
	}
	// The pbcm key only reaches pbcm-runner.
	if _, err := h.m.runnerCommand(ctx, c, "cat", "/etc/shadow"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("other commands must be refused: %v", err)
	}

	// The client is reinstalled: it now has a new host key.
	newKey := clienttest.NewSigner(t)
	h.host.SetHostKey(newKey)
	c, err = h.m.Check(ctx, c.ID)
	if err == nil || c.Status != store.ClientHostKeyChanged || c.OfferedKey != sshx.FormatKey(newKey.PublicKey()) {
		t.Fatalf("check after key change: %v %+v", err, c)
	}
	if _, err := h.m.Browse(ctx, c.ID, "/srv"); err == nil {
		t.Fatal("must not talk to a host whose key changed")
	}
	task, err := h.m.Repair(c.ID, Login{User: "root", Password: rootPW}, c.OfferedKey)
	if err != nil {
		t.Fatal(err)
	}
	if v := wait(t, task); !v.OK {
		t.Fatalf("repair: %+v", v)
	}
	c, _ = h.st.GetClient(c.ID)
	if c.Status != store.ClientReady || c.HostKey != sshx.FormatKey(newKey.PublicKey()) || c.OfferedKey != "" {
		t.Fatalf("after repair: %+v", c)
	}

	out, err := h.m.Remove(ctx, c.ID, true, false)
	if err != nil || !strings.Contains(out, "Removed") {
		t.Fatalf("remove: %q %v", out, err)
	}
	if _, err := h.st.GetClient(c.ID); err != store.ErrNotFound {
		t.Fatal("client should be deleted")
	}
	if _, err := h.m.runnerCommand(ctx, c, "detect"); err == nil {
		t.Fatal("the server's key should no longer work after uninstall")
	}
}

func TestRemoveUnreachableNeedsListOnly(t *testing.T) {
	h := newHarness(t)
	c, _ := h.add(t, Login{User: "root", Password: rootPW})
	h.host.Close()
	if _, err := h.m.Remove(context.Background(), c.ID, true, false); err == nil || !strings.Contains(err.Error(), "remove it from this list only") {
		t.Fatalf("expected advice to remove from list only: %v", err)
	}
	if _, err := h.m.Remove(context.Background(), c.ID, false, false); err != nil {
		t.Fatal(err)
	}
}

func TestTaskLogSplitsLines(t *testing.T) {
	task := &Task{}
	task.Write([]byte("one\ntw"))
	task.Write([]byte("o\r\nthree"))
	task.finish(nil)
	v := task.View(0)
	if strings.Join(v.Lines, "|") != "one|two|three" || !v.OK || v.Offset != 3 {
		t.Fatalf("got %+v", v)
	}
	if got := task.View(2); len(got.Lines) != 1 || got.Lines[0] != "three" {
		t.Fatalf("offset view: %+v", got)
	}
}

// ---------------------------------------------------------------- backups

func (h *harness) jobSetup(t *testing.T, folder string) (*store.Client, *store.Job) {
	t.Helper()
	c, v := h.add(t, Login{User: "root", Password: rootPW})
	if !v.OK {
		t.Fatalf("setup: %+v", v)
	}
	d := &store.Destination{ID: "d1", Name: "Home PBS", Host: "192.0.2.10", Port: 8007, Datastore: "store",
		Username: "nas@pbs", TokenName: "nas", Secret: "token-secret"}
	if err := h.st.SaveDestination(d); err != nil {
		t.Fatal(err)
	}
	h.host.Mkdir(folder)
	j := &store.Job{ID: "j1", ClientID: c.ID, Name: "media", BackupID: "nas", Shares: []bundle.Share{{Path: folder, Archive: "media"}},
		Schedule: bundle.Schedule{Type: "daily", Time: "02:00", Days: []int{0, 1, 2, 3, 4, 5, 6}}, ChangeDetection: "metadata",
		Enabled: true, Destinations: []string{"d1"}}
	if err := h.st.SaveJob(j); err != nil {
		t.Fatal(err)
	}
	return c, j
}

func TestApplyRunCollectAndLog(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, j := h.jobSetup(t, "/srv/media")
	if !h.m.Pending(c) {
		t.Fatal("a new job should be pending")
	}
	if err := h.m.Apply(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	c, _ = h.st.GetClient(c.ID)
	if h.m.Pending(c) || c.ApplyError != "" {
		t.Fatalf("after apply: pending=%v err=%q", h.m.Pending(c), c.ApplyError)
	}
	if timer, err := os.ReadFile(h.host.Path("/etc/systemd/system/pbcm-job-j1.timer")); err != nil || !strings.Contains(string(timer), "OnCalendar=*-*-* 02:00:00") {
		t.Fatalf("timer on client: %s %v", timer, err)
	}
	if bundleJSON, _ := os.ReadFile(h.host.Path(runner.BundleFile)); bytes.Contains(bundleJSON, []byte("token-secret")) {
		t.Fatal("the client's bundle.json must not hold secrets")
	}

	if err := h.m.StartJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	h.host.WaitIdle()
	finished, err := h.m.SyncClient(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 1 || finished[0].Status != bundle.Success || finished[0].Trigger != "manual" || finished[0].DestinationName != "Home PBS" {
		t.Fatalf("finished runs: %+v", finished)
	}
	r, _ := h.st.GetRun(c.ID, finished[0].ID)
	if !r.LogSaved {
		t.Fatal("finished log should be saved on the server")
	}
	chunk, err := h.m.RunLog(ctx, c.ID, r.ID, 0)
	if err != nil || !chunk.Done || !strings.Contains(chunk.Text, "repo=nas@pbs!nas@192.0.2.10:8007:store") || !strings.Contains(chunk.Text, "media.pxar:/srv/media") {
		t.Fatalf("log: %+v %v", chunk, err)
	}
	// Collecting again reports nothing new.
	if again, err := h.m.SyncClient(ctx, c.ID); err != nil || len(again) != 0 {
		t.Fatalf("second sync: %v %v", again, err)
	}

	// A changed job is sent again on the next check.
	j.Schedule = bundle.Schedule{Type: "manual"}
	h.st.SaveJob(j)
	c, _ = h.st.GetClient(c.ID)
	if !h.m.Pending(c) {
		t.Fatal("changed job should be pending")
	}
	h.m.SyncClient(ctx, c.ID)
	c, _ = h.st.GetClient(c.ID)
	if h.m.Pending(c) {
		t.Fatal("sync should send pending settings")
	}
	if _, err := os.Stat(h.host.Path("/etc/systemd/system/pbcm-job-j1.timer")); err == nil {
		t.Fatal("manual job's timer should be gone")
	}
}

func TestFailedAndCancelledRuns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, j := h.jobSetup(t, "/srv/fail")
	if err := h.m.StartJob(ctx, j.ID); err != nil { // sends the pending settings first
		t.Fatal(err)
	}
	h.host.WaitIdle()
	finished, _ := h.m.SyncClient(ctx, c.ID)
	if len(finished) != 1 || finished[0].Status != bundle.Failed || finished[0].Summary != "Error: connection refused" {
		t.Fatalf("failed run: %+v", finished)
	}

	j.Shares = []bundle.Share{{Path: "/srv/slow", Archive: "slow"}}
	h.host.Mkdir("/srv/slow")
	h.st.SaveJob(j)
	if err := h.m.StartJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !h.host.Running(j.ID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := h.m.StartJob(ctx, j.ID); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := h.m.CancelJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	h.host.WaitIdle()
	finished, _ = h.m.SyncClient(ctx, c.ID)
	if len(finished) != 1 || finished[0].Status != bundle.Cancelled {
		t.Fatalf("cancelled run: %+v", finished)
	}
}

func TestOfflineClientIsMarkedAndBackupsStayPending(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, _ := h.jobSetup(t, "/srv/media")
	h.host.Close()
	if _, err := h.m.SyncClient(ctx, c.ID); err == nil {
		t.Fatal("sync with an offline client should fail")
	}
	c, _ = h.st.GetClient(c.ID)
	if c.Status != store.ClientUnreachable || !h.m.Pending(c) {
		t.Fatalf("offline client: %s pending=%v", c.Status, h.m.Pending(c))
	}
	if err := h.m.Apply(ctx, c.ID); err == nil {
		t.Fatal("apply to an offline client should fail")
	}
	if c, _ = h.st.GetClient(c.ID); c.ApplyError == "" {
		t.Fatal("the apply error should be recorded")
	}
}
