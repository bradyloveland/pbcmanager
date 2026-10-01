package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

// jobEnv is a client in a temp folder with fake systemctl, systemd-creds
// and proxmox-backup-client.
type jobEnv struct {
	*Env
	out      *bytes.Buffer
	mu       sync.Mutex
	calls    []string
	active   map[string]bool
	creds    bool // systemd-creds works
	noSystem bool // systemd isn't running
}

const fakeClient = `#!/bin/sh
echo "args: $*"
echo "repo=$PBS_REPOSITORY ns=$PBS_NAMESPACE fp=$PBS_FINGERPRINT"
echo "secret=$(cat "$PBS_PASSWORD_FILE")"
[ -n "$PBS_ENCRYPTION_PASSWORD_FILE" ] && echo "keypass=$(cat "$PBS_ENCRYPTION_PASSWORD_FILE")"
case "$*" in *fail*) echo "Error: connection refused"; exit 255;; esac
case "$*" in *slow*) trap 'echo "stopping on SIGINT"; exit 130' INT; i=0; while [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done;; esac
echo "Duration: 0.01s"
`

func newJobEnv(t *testing.T) *jobEnv {
	root := t.TempDir()
	client := filepath.Join(t.TempDir(), "proxmox-backup-client")
	if err := os.WriteFile(client, []byte(fakeClient), 0o755); err != nil {
		t.Fatal(err)
	}
	je := &jobEnv{out: &bytes.Buffer{}, active: map[string]bool{}, creds: true}
	je.Env = &Env{Root: root, Stdout: je.out, Stderr: je.out, ClientBin: client,
		Exec: func(name string, args ...string) (string, error) {
			je.mu.Lock()
			defer je.mu.Unlock()
			je.calls = append(je.calls, name+" "+strings.Join(args, " "))
			if name != "systemctl" {
				return "", nil
			}
			switch args[0] {
			case "is-system-running":
				if je.noSystem {
					return "offline\n", errors.New("exit 1")
				}
				return "degraded\n", errors.New("exit 1")
			case "is-active":
				if je.active[strings.TrimSuffix(strings.TrimPrefix(args[1], "pbcm-job@"), ".service")] {
					return "active\n", nil
				}
				return "inactive\n", errors.New("exit 3")
			}
			return "", nil
		},
		Run: func(input, name string, args ...string) (string, error) {
			if name != "systemd-creds" || !je.creds {
				return "", errors.New("not available")
			}
			if args[0] == "encrypt" {
				return "ENC:" + input, nil
			}
			raw, err := os.ReadFile(args[2])
			return strings.TrimPrefix(string(raw), "ENC:"), err
		}}
	return je
}

func (je *jobEnv) callLog() string {
	je.mu.Lock()
	defer je.mu.Unlock()
	return strings.Join(je.calls, "\n")
}

func testBundle(paths ...string) bundle.Bundle {
	shares := []bundle.Share{}
	for i, p := range paths {
		shares = append(shares, bundle.Share{Path: p, Archive: "a" + string(rune('0'+i))})
	}
	return bundle.Bundle{Version: 1,
		Destinations: []bundle.Destination{
			{ID: "d1", Name: "Home PBS", Repository: "nas@pbs!nas@192.0.2.10:8007:store", Fingerprint: "ab:cd", Secret: "secret-one"},
			{ID: "d2", Name: "Offsite", Repository: "nas@pbs!nas@198.51.100.7:8007:remote", Namespace: "nas", Secret: "secret-two"}},
		Jobs: []bundle.Job{
			{ID: "j1", Name: "media", BackupID: "nas", Shares: shares, Excludes: []string{"*.tmp"},
				Schedule: bundle.Schedule{Type: "daily", Time: "02:00", Days: []int{0, 1, 2, 3, 4, 5, 6}},
				Enabled:  true, Destinations: []string{"d1", "d2"}, KeyfilePassword: "key-pass", Keyfile: "/root/k.json"},
			{ID: "j2", Name: "manual", BackupID: "nas2", Shares: shares, Schedule: bundle.Schedule{Type: "manual"},
				Enabled: true, Destinations: []string{"d1"}}}}
}

func (je *jobEnv) apply(t *testing.T, b bundle.Bundle) error {
	t.Helper()
	raw, _ := json.Marshal(b)
	je.out.Reset()
	return Apply(je.Env, bytes.NewReader(raw))
}

func TestApplyWritesCredentialsJobsAndTimers(t *testing.T) {
	je := newJobEnv(t)
	b := testBundle("/srv/media")
	if err := je.apply(t, b); err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	json.Unmarshal(je.out.Bytes(), &res)
	if res["applied"] != b.Hash() || res["timers"].(float64) != 1 || res["encrypted"] != true {
		t.Fatalf("apply result %v", res)
	}
	raw, _ := os.ReadFile(je.path(BundleFile))
	for _, secret := range []string{"secret-one", "secret-two", "key-pass"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("bundle.json contains a secret: %s", secret)
		}
	}
	cred, _ := os.ReadFile(je.path(CredDir + "/dest-d1.cred"))
	if string(cred) != "ENC:secret-one" {
		t.Fatalf("credential not encrypted: %q", cred)
	}
	if st, _ := os.Stat(je.path(CredDir + "/dest-d1.cred")); st.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode %v", st.Mode())
	}
	timer, _ := os.ReadFile(je.path(UnitDir + "/pbcm-job-j1.timer"))
	if !strings.Contains(string(timer), "OnCalendar=*-*-* 02:00:00") || !strings.Contains(string(timer), "Unit=pbcm-job@j1.service") {
		t.Fatalf("timer:\n%s", timer)
	}
	if _, err := os.Stat(je.path(UnitDir + "/pbcm-job-j2.timer")); err == nil {
		t.Fatal("manual jobs get no timer")
	}
	svc, _ := os.ReadFile(je.path(UnitDir + "/" + ServiceName))
	if !strings.Contains(string(svc), "ExecStart=/usr/local/lib/pbcm/pbcm-runner run %i") {
		t.Fatalf("service:\n%s", svc)
	}
	calls := je.callLog()
	for _, want := range []string{"systemctl daemon-reload", "systemctl enable pbcm-job-j1.timer", "systemctl restart pbcm-job-j1.timer"} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in\n%s", want, calls)
		}
	}

	// Removing the schedule and a destination cleans up after itself.
	b.Jobs[0].Schedule = bundle.Schedule{Type: "manual"}
	b.Jobs[0].Destinations = []string{"d1"}
	b.Destinations = b.Destinations[:1]
	if err := je.apply(t, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(je.path(UnitDir + "/pbcm-job-j1.timer")); err == nil {
		t.Fatal("timer should be removed")
	}
	if _, err := os.Stat(je.path(CredDir + "/dest-d2.cred")); err == nil {
		t.Fatal("unused credential should be removed")
	}
	if !strings.Contains(je.callLog(), "systemctl disable --now pbcm-job-j1.timer") {
		t.Fatal("removed timer should be disabled")
	}
	info := Detect(je.Env)
	if info.Applied != b.Hash() {
		t.Fatal("detect should report the applied hash")
	}
}

func TestApplyRefusesBadInput(t *testing.T) {
	je := newJobEnv(t)
	b := testBundle("/srv/media")
	b.Jobs[0].ID = "../x"
	if err := je.apply(t, b); err == nil {
		t.Fatal("bad job ID accepted")
	}
	je.noSystem = true
	if err := je.apply(t, testBundle("/srv/media")); err == nil || !strings.Contains(err.Error(), "systemd isn't running") {
		t.Fatalf("no systemd: %v", err)
	}
	if _, err := os.Stat(je.path(BundleFile)); err == nil {
		t.Fatal("nothing should be written when refusing")
	}
}

func TestPlainCredentialsWithoutSystemdCreds(t *testing.T) {
	je := newJobEnv(t)
	je.creds = false
	if err := je.apply(t, testBundle("/srv/media")); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(je.path(CredDir + "/dest-d1.secret")); string(raw) != "secret-one" {
		t.Fatalf("plain credential %q", raw)
	}
}

func readRuns(t *testing.T, je *jobEnv) []bundle.Run {
	t.Helper()
	je.out.Reset()
	if err := StatusSince(je.Env, 0); err != nil {
		t.Fatal(err)
	}
	var st bundle.Status
	if err := json.Unmarshal(je.out.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st.Runs
}

func TestRunBacksUpToEachDestination(t *testing.T) {
	je := newJobEnv(t)
	os.MkdirAll(je.path("/srv/media"), 0o755)
	os.MkdirAll(je.path("/root"), 0o755)
	os.WriteFile(je.path("/root/k.json"), []byte("{}"), 0o600)
	b := testBundle("/srv/media")
	if err := je.apply(t, b); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(je.path(RuntimeDir), 0o700)
	os.WriteFile(je.path(RuntimeDir+"/trigger-j1"), nil, 0o600)
	if err := RunJob(je.Env, "j1", make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	runs := readRuns(t, je)
	if len(runs) != 2 {
		t.Fatalf("want a run per destination, got %d", len(runs))
	}
	seen := map[string]bundle.Run{}
	for _, r := range runs {
		seen[r.DestinationID] = r
		if r.Status != bundle.Success || r.Trigger != "manual" || *r.ExitCode != 0 || r.Group != runs[0].Group {
			t.Fatalf("run %+v", r)
		}
	}
	for dest, want := range map[string][]string{
		"d1": {"repo=nas@pbs!nas@192.0.2.10:8007:store", "fp=ab:cd", "secret=secret-one", "keypass=key-pass", "a0.pxar:/srv/media", "--exclude *.tmp", "--keyfile /root/k.json"},
		"d2": {"ns=nas", "secret=secret-two"},
	} {
		je.out.Reset()
		Log(je.Env, seen[dest].ID, 0)
		var chunk bundle.LogChunk
		json.Unmarshal(je.out.Bytes(), &chunk)
		for _, w := range want {
			if !strings.Contains(chunk.Text, w) {
				t.Errorf("%s log missing %q:\n%s", dest, w, chunk.Text)
			}
		}
		if !chunk.Done || chunk.Offset != chunk.Size {
			t.Errorf("chunk %+v", chunk)
		}
	}
	// Decrypted secrets don't outlive the run.
	if left, _ := filepath.Glob(je.path(RuntimeDir + "/*/dest-*")); len(left) != 0 {
		t.Fatalf("decrypted secrets left behind: %v", left)
	}
	if _, err := os.Stat(je.path(RuntimeDir + "/trigger-j1")); err == nil {
		t.Fatal("trigger marker should be used up")
	}
}

func TestRunFailures(t *testing.T) {
	je := newJobEnv(t)
	os.MkdirAll(je.path("/srv/fail"), 0o755)
	b := testBundle("/srv/fail")
	b.Jobs[0].Keyfile, b.Jobs[0].KeyfilePassword = "", ""
	je.apply(t, b)
	if err := RunJob(je.Env, "j2", make(chan struct{})); err == nil {
		t.Fatal("a failed backup should return an error (so systemd shows it)")
	}
	r := readRuns(t, je)[0]
	if r.Status != bundle.Failed || r.Summary != "Error: connection refused" || *r.ExitCode != 255 || r.Trigger != "schedule" {
		t.Fatalf("failed run %+v", r)
	}

	je2 := newJobEnv(t)
	je2.apply(t, testBundle("/srv/not-mounted"))
	RunJob(je2.Env, "j2", make(chan struct{}))
	r = readRuns(t, je2)[0]
	if r.Status != bundle.Failed || !strings.Contains(r.Summary, "don't exist or aren't mounted: /srv/not-mounted") {
		t.Fatalf("missing folder run %+v", r)
	}
}

func TestCancelStopsTheBackup(t *testing.T) {
	je := newJobEnv(t)
	os.MkdirAll(je.path("/srv/slow"), 0o755)
	b := testBundle("/srv/slow")
	b.Jobs[0].Keyfile, b.Jobs[0].KeyfilePassword = "", ""
	je.apply(t, b)
	cancel := make(chan struct{})
	done := make(chan error)
	go func() { done <- RunJob(je.Env, "j1", cancel) }()
	time.Sleep(300 * time.Millisecond)
	close(cancel)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancel didn't stop the run")
	}
	runs := readRuns(t, je)
	if len(runs) != 1 || runs[0].Status != bundle.Cancelled {
		t.Fatalf("want one cancelled run and the second destination skipped, got %+v", runs)
	}
	je.out.Reset()
	Log(je.Env, runs[0].ID, 0)
	if !strings.Contains(je.out.String(), "stopping on SIGINT") {
		t.Fatalf("the client should get SIGINT: %s", je.out.String())
	}
}

func TestStatusMarksInterruptedRunsAndFiltersBySince(t *testing.T) {
	je := newJobEnv(t)
	je.apply(t, testBundle("/srv/media"))
	clock := time.Unix(1_800_000_000, 0)
	je.Now = func() time.Time { return clock }
	for _, id := range []string{"20261001T010000-aaaaaa", "20261001T020000-bbbbbb"} {
		os.MkdirAll(je.runDir(id), 0o700)
		je.saveRun(&bundle.Run{ID: id, JobID: "j1", Status: bundle.Running})
	}
	je.active["j1"] = true
	if runs := readRuns(t, je); runs[0].Status != bundle.Running {
		t.Fatal("still-running job should stay running")
	}
	je.active["j1"] = false
	clock = clock.Add(time.Minute)
	runs := readRuns(t, je)
	for _, r := range runs {
		if r.Status != bundle.Failed || !strings.Contains(r.Summary, "Interrupted") {
			t.Fatalf("run %+v", r)
		}
	}
	je.out.Reset()
	StatusSince(je.Env, clock.Unix()+1)
	var st bundle.Status
	json.Unmarshal(je.out.Bytes(), &st)
	if len(st.Runs) != 0 || st.Applied == "" {
		t.Fatalf("since filter: %+v", st)
	}
}

func TestStartAndCancelUseSystemd(t *testing.T) {
	je := newJobEnv(t)
	je.apply(t, testBundle("/srv/media"))
	if err := Start(je.Env, "j1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(je.callLog(), "systemctl start --no-block pbcm-job@j1.service") {
		t.Fatal("start should go through systemd")
	}
	if _, err := os.Stat(je.path(RuntimeDir + "/trigger-j1")); err != nil {
		t.Fatal("start should mark the run as manual")
	}
	if err := Start(je.Env, "nope"); err == nil {
		t.Fatal("unknown job started")
	}
	je.active["j1"] = true
	if err := Start(je.Env, "j1"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("double start: %v", err)
	}
	if err := Cancel(je.Env, "j1"); err != nil || !strings.Contains(je.callLog(), "systemctl stop --no-block pbcm-job@j1.service") {
		t.Fatalf("cancel: %v", err)
	}
	je.active["j1"] = false
	if err := Cancel(je.Env, "j1"); err == nil {
		t.Fatal("cancelling an idle job should say so")
	}
}

func TestPruneKeepsRecentAndRunning(t *testing.T) {
	je := newJobEnv(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	je.Now = func() time.Time { return clock }
	ids := []string{"20260101T000000-000001", "20260920T000000-000002", "20260921T000000-000003", "20260922T000000-000004", "20250101T000000-000005"}
	for _, id := range ids {
		os.MkdirAll(je.runDir(id), 0o700)
		status := bundle.Success
		if id == "20250101T000000-000005" {
			status = bundle.Running
		}
		je.saveRun(&bundle.Run{ID: id, Status: status})
	}
	je.prune(bundle.Bundle{KeepRuns: 2, KeepDays: 90})
	got := je.runIDs()
	want := []string{"20260922T000000-000004", "20260921T000000-000003", "20250101T000000-000005"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("kept %v, want %v", got, want)
	}
}

func TestBackupCommand(t *testing.T) {
	j := &bundle.Job{BackupID: "nas", Shares: []bundle.Share{{Path: "/srv/a", Archive: "a"}, {Path: "/srv/b", Archive: "b"}},
		ChangeDetection: "metadata", Rate: "20 MiB", Excludes: []string{"lost+found"}, Keyfile: "/root/k"}
	got := strings.Join(BackupCommand("pbc", j), " ")
	want := "pbc backup a.pxar:/srv/a b.pxar:/srv/b --backup-id nas --change-detection-mode metadata --rate 20MiB --exclude lost+found --keyfile /root/k"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	j.ChangeDetection = "legacy"
	if strings.Contains(strings.Join(BackupCommand("pbc", j), " "), "change-detection") {
		t.Fatal("legacy mode passes no flag")
	}
}
