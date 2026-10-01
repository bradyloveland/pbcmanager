package runner

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/version"
)

// Locations for jobs on a client.
const (
	BundleFile  = ConfigDir + "/bundle.json"
	CredDir     = ConfigDir + "/credentials"
	RunsDir     = StateDir + "/runs"
	RuntimeDir  = "/run/pbcm"
	UnitDir     = "/etc/systemd/system"
	ServiceName = "pbcm-job@.service"
)

var runIDRE = regexp.MustCompile(`^\d{8}T\d{6}-[0-9a-f]{6}$`)

func newRunID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// stored is bundle.json: the bundle without secrets, plus its hash.
type stored struct {
	Hash   string        `json:"hash"`
	Bundle bundle.Bundle `json:"bundle"`
	// Encrypted says the credentials were stored with systemd-creds.
	Encrypted bool `json:"encrypted"`
}

func (e *Env) loadStored() (*stored, error) {
	raw, err := os.ReadFile(e.path(BundleFile))
	if errors.Is(err, os.ErrNotExist) {
		return &stored{Bundle: bundle.Bundle{Version: bundle.Version}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s stored
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s is damaged; send the settings again from the server", BundleFile)
	}
	return &s, nil
}

func (e *Env) findJob(id string) (*bundle.Job, *stored, error) {
	if !bundle.IDRE.MatchString(id) {
		return nil, nil, &UsageError{"invalid job ID"}
	}
	s, err := e.loadStored()
	if err != nil {
		return nil, nil, err
	}
	for i := range s.Bundle.Jobs {
		if s.Bundle.Jobs[i].ID == id {
			return &s.Bundle.Jobs[i], s, nil
		}
	}
	return nil, nil, fmt.Errorf("this client doesn't have a job with ID %s; send the settings again from the server", id)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ------------------------------------------------------------------ apply

func credName(kind, id string) string { return kind + "-" + id }

// Apply installs a bundle read from r: credentials, jobs and timers.
func Apply(env *Env, r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return err
	}
	var b bundle.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return &UsageError{"the settings sent by the server aren't valid JSON"}
	}
	if err := b.Check(); err != nil {
		return err
	}
	if out, err := env.Exec("systemctl", "is-system-running"); err != nil && !strings.Contains(out, "degraded") &&
		!strings.Contains(out, "running") && !strings.Contains(out, "starting") {
		return errors.New("systemd isn't running on this machine, so scheduled backups can't be set up")
	}

	// Credentials: encrypted with systemd-creds where it works, otherwise
	// files only root can read.
	encrypted := env.credsWork()
	want := map[string]string{}
	for _, d := range b.Destinations {
		want[credName("dest", d.ID)] = d.Secret
	}
	for _, j := range b.Jobs {
		if j.KeyfilePassword != "" {
			want[credName("key", j.ID)] = j.KeyfilePassword
		}
	}
	if err := os.MkdirAll(env.path(CredDir), 0o700); err != nil {
		return err
	}
	for name, secret := range want {
		if err := env.storeCred(name, secret, encrypted); err != nil {
			return err
		}
	}
	entries, _ := os.ReadDir(env.path(CredDir))
	for _, e := range entries {
		base := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".cred"), ".secret")
		if _, ok := want[base]; !ok {
			os.Remove(env.path(filepath.Join(CredDir, e.Name())))
		}
	}

	// Keep everything but the secrets.
	hash := b.Hash()
	clean := b
	clean.Destinations = append([]bundle.Destination{}, b.Destinations...)
	for i := range clean.Destinations {
		clean.Destinations[i].Secret = ""
	}
	clean.Jobs = append([]bundle.Job{}, b.Jobs...)
	for i := range clean.Jobs {
		clean.Jobs[i].KeyfilePassword = ""
	}
	data, _ := json.MarshalIndent(stored{Hash: hash, Bundle: clean, Encrypted: encrypted}, "", "  ")
	if err := writeAtomic(env.path(BundleFile), data, 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(env.path(RunsDir), 0o700); err != nil {
		return err
	}

	// systemd units: one service template, one timer per scheduled job.
	if err := writeAtomic(env.path(filepath.Join(UnitDir, ServiceName)), []byte(serviceUnit), 0o644); err != nil {
		return err
	}
	timers := map[string]string{}
	for _, j := range b.Jobs {
		spec, _ := bundle.OnCalendar(j.Schedule)
		if j.Enabled && spec != "" {
			timers["pbcm-job-"+j.ID+".timer"] = timerUnit(j, spec)
		}
	}
	existing, _ := filepath.Glob(env.path(filepath.Join(UnitDir, "pbcm-job-*.timer")))
	for _, path := range existing {
		name := filepath.Base(path)
		if _, keep := timers[name]; !keep {
			env.Exec("systemctl", "disable", "--now", name)
			os.Remove(path)
		}
	}
	for name, body := range timers {
		if err := writeAtomic(env.path(filepath.Join(UnitDir, name)), []byte(body), 0o644); err != nil {
			return err
		}
	}
	if out, err := env.Exec("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %s", strings.TrimSpace(out))
	}
	names := make([]string, 0, len(timers))
	for name := range timers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if out, err := env.Exec("systemctl", "enable", name); err != nil {
			return fmt.Errorf("couldn't turn on the schedule %s: %s", name, strings.TrimSpace(out))
		}
		// Restart so a changed schedule takes effect now.
		if out, err := env.Exec("systemctl", "restart", name); err != nil {
			return fmt.Errorf("couldn't start the schedule %s: %s", name, strings.TrimSpace(out))
		}
	}
	return json.NewEncoder(env.Stdout).Encode(map[string]any{"applied": hash, "timers": len(timers), "encrypted": encrypted})
}

const serviceUnit = `# Managed by PBC Manager. Runs one backup job; %i is the job ID.
[Unit]
Description=PBC Manager backup job %i
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
ExecStart=/usr/local/lib/pbcm/pbcm-runner run %i
# Stopping sends SIGTERM to pbcm-runner only; it asks proxmox-backup-client
# to stop cleanly and records the run as cancelled.
KillMode=mixed
TimeoutStopSec=60
Nice=10
IOSchedulingClass=best-effort
IOSchedulingPriority=7
`

func timerUnit(j bundle.Job, spec string) string {
	name := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, j.Name)
	return fmt.Sprintf(`# Managed by PBC Manager.
[Unit]
Description=PBC Manager schedule for %s

[Timer]
OnCalendar=%s
Persistent=false
AccuracySec=1s
Unit=pbcm-job@%s.service

[Install]
WantedBy=timers.target
`, name, spec, j.ID)
}

// credsWork reports whether systemd-creds can encrypt here.
func (e *Env) credsWork() bool {
	if e.Run == nil {
		return false
	}
	_, err := e.Run("probe", "systemd-creds", "encrypt", "--name=pbcm-probe", "-", "-")
	return err == nil
}

func (e *Env) storeCred(name, secret string, encrypted bool) error {
	plain := e.path(filepath.Join(CredDir, name+".secret"))
	enc := e.path(filepath.Join(CredDir, name+".cred"))
	if encrypted {
		out, err := e.Run(secret, "systemd-creds", "encrypt", "--name="+name, "-", "-")
		if err != nil {
			return fmt.Errorf("couldn't encrypt a credential: %w", err)
		}
		os.Remove(plain)
		return writeAtomic(enc, []byte(out), 0o600)
	}
	os.Remove(enc)
	return writeAtomic(plain, []byte(secret), 0o600)
}

// credFile returns a file holding the secret in plain text for the client to
// read, and a cleanup function. Encrypted credentials are decrypted into
// RuntimeDir (memory only) for the length of the run.
func (e *Env) credFile(name, runID string) (string, func(), error) {
	plain := e.path(filepath.Join(CredDir, name+".secret"))
	if _, err := os.Stat(plain); err == nil {
		return plain, func() {}, nil
	}
	enc := e.path(filepath.Join(CredDir, name+".cred"))
	if _, err := os.Stat(enc); err != nil {
		return "", nil, fmt.Errorf("the credential %s is missing; send the settings again from the server", name)
	}
	secret, err := e.Run("", "systemd-creds", "decrypt", "--name="+name, enc, "-")
	if err != nil {
		return "", nil, fmt.Errorf("couldn't decrypt the credential %s: %w", name, err)
	}
	dir := e.path(filepath.Join(RuntimeDir, runID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		return "", nil, err
	}
	return path, func() { os.Remove(path) }, nil
}

// -------------------------------------------------------------------- runs

func (e *Env) runDir(id string) string { return e.path(filepath.Join(RunsDir, id)) }

func (e *Env) saveRun(r *bundle.Run) error {
	r.Updated = e.now().Unix()
	if st, err := os.Stat(filepath.Join(e.runDir(r.ID), "log")); err == nil {
		r.LogSize = st.Size()
	}
	data, _ := json.Marshal(r)
	return writeAtomic(filepath.Join(e.runDir(r.ID), "result.json"), data, 0o600)
}

func (e *Env) loadRun(id string) (*bundle.Run, error) {
	raw, err := os.ReadFile(filepath.Join(e.runDir(id), "result.json"))
	if err != nil {
		return nil, err
	}
	var r bundle.Run
	return &r, json.Unmarshal(raw, &r)
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// RunJob backs a job up to each of its destinations in turn. Closing cancel
// asks the running backup to stop; remaining destinations are skipped.
func RunJob(env *Env, jobID string, cancel <-chan struct{}) error {
	job, st, err := env.findJob(jobID)
	if err != nil {
		return err
	}
	trigger := "schedule"
	marker := env.path(filepath.Join(RuntimeDir, "trigger-"+jobID))
	if _, err := os.Stat(marker); err == nil {
		trigger = "manual"
		os.Remove(marker)
	}
	dests := map[string]bundle.Destination{}
	for _, d := range st.Bundle.Destinations {
		dests[d.ID] = d
	}
	group := newRunID(env.now())
	failed := false
	for _, destID := range job.Destinations {
		select {
		case <-cancel:
			return errors.New("cancelled")
		default:
		}
		d := dests[destID]
		r := &bundle.Run{ID: newRunID(env.now()), Group: group, JobID: job.ID, JobName: job.Name,
			DestinationID: d.ID, DestinationName: d.Name, Trigger: trigger, Status: bundle.Running, Started: env.now().Unix()}
		if err := os.MkdirAll(env.runDir(r.ID), 0o700); err != nil {
			return err
		}
		if err := env.saveRun(r); err != nil {
			return err
		}
		env.backupOne(job, d, r, cancel)
		if r.Status != bundle.Success {
			failed = true
		}
		if r.Status == bundle.Cancelled {
			break
		}
	}
	env.prune(st.Bundle)
	if failed {
		return errors.New("one or more backups didn't succeed")
	}
	return nil
}

// BackupCommand is the proxmox-backup-client command line for a job.
func BackupCommand(client string, j *bundle.Job) []string {
	cmd := []string{client, "backup"}
	for _, s := range j.Shares {
		cmd = append(cmd, s.Archive+".pxar:"+s.Path)
	}
	cmd = append(cmd, "--backup-id", j.BackupID)
	if j.ChangeDetection == "data" || j.ChangeDetection == "metadata" {
		cmd = append(cmd, "--change-detection-mode", j.ChangeDetection)
	}
	if j.Rate != "" {
		cmd = append(cmd, "--rate", strings.ReplaceAll(j.Rate, " ", ""))
	}
	for _, x := range j.Excludes {
		cmd = append(cmd, "--exclude", x)
	}
	if j.Keyfile != "" {
		cmd = append(cmd, "--keyfile", j.Keyfile)
	}
	return cmd
}

func (e *Env) backupOne(job *bundle.Job, d bundle.Destination, r *bundle.Run, cancel <-chan struct{}) {
	logf, err := os.OpenFile(filepath.Join(e.runDir(r.ID), "log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		e.finish(r, bundle.Failed, nil, "Couldn't create the log file: "+err.Error())
		return
	}
	defer logf.Close()
	note := func(format string, a ...any) {
		fmt.Fprintf(logf, "[%s] %s\n", e.now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
	}
	fail := func(msg string) {
		note("%s", msg)
		e.finish(r, bundle.Failed, nil, msg)
	}
	var missing []string
	for _, s := range job.Shares {
		if st, err := os.Stat(e.path(s.Path)); err != nil || !st.IsDir() {
			missing = append(missing, s.Path)
		}
	}
	if len(missing) > 0 {
		fail("These folders don't exist or aren't mounted: " + strings.Join(missing, ", "))
		return
	}
	if job.Keyfile != "" {
		if _, err := os.Stat(e.path(job.Keyfile)); err != nil {
			fail("The encryption key file " + job.Keyfile + " doesn't exist.")
			return
		}
	}
	client := e.ClientBin
	if client == "" {
		client = "proxmox-backup-client"
	}
	if _, err := exec.LookPath(client); err != nil {
		fail("proxmox-backup-client isn't installed on this machine. Use Repair to install it.")
		return
	}
	secretPath, cleanup, err := e.credFile(credName("dest", d.ID), r.ID)
	if err != nil {
		fail(capital(err.Error()) + ".")
		return
	}
	defer cleanup()
	defer os.RemoveAll(e.path(filepath.Join(RuntimeDir, r.ID)))
	env := append(os.Environ(), "PBS_REPOSITORY="+d.Repository, "PBS_PASSWORD_FILE="+secretPath)
	if d.Fingerprint != "" {
		env = append(env, "PBS_FINGERPRINT="+d.Fingerprint)
	}
	if d.Namespace != "" {
		env = append(env, "PBS_NAMESPACE="+d.Namespace)
	}
	if job.KeyfilePassword != "" || fileExists(e.path(filepath.Join(CredDir, credName("key", job.ID)+".secret"))) ||
		fileExists(e.path(filepath.Join(CredDir, credName("key", job.ID)+".cred"))) {
		keyPath, keyCleanup, err := e.credFile(credName("key", job.ID), r.ID)
		if err != nil {
			fail(capital(err.Error()) + ".")
			return
		}
		defer keyCleanup()
		env = append(env, "PBS_ENCRYPTION_PASSWORD_FILE="+keyPath)
	}

	args := BackupCommand(client, job)
	note("Backing up %d folder(s) to %s (%s)", len(job.Shares), d.Name, d.Repository)
	if d.Namespace != "" {
		note("Namespace: %s", d.Namespace)
	}
	note("Command: %s", strings.Join(args, " "))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fail("Couldn't start proxmox-backup-client: " + err.Error())
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	cancelled := false
	var waitErr error
	select {
	case waitErr = <-done:
	case <-cancel:
		cancelled = true
		note("Stopping: the backup was cancelled.")
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		select {
		case waitErr = <-done:
		case <-time.After(20 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			waitErr = <-done
		}
	}
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	note("Finished with exit code %d.", code)
	logf.Sync()
	switch {
	case cancelled:
		e.finish(r, bundle.Cancelled, &code, "Cancelled while running.")
	case waitErr == nil && code == 0:
		e.finish(r, bundle.Success, &code, "Backup finished.")
	default:
		e.finish(r, bundle.Failed, &code, bundle.SummarizeError(tail(filepath.Join(e.runDir(r.ID), "log"), 8000)))
	}
}

func (e *Env) finish(r *bundle.Run, status string, code *int, summary string) {
	r.Status, r.ExitCode, r.Summary, r.Ended = status, code, summary, e.now().Unix()
	_ = e.saveRun(r)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func tail(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() > n {
		f.Seek(st.Size()-n, io.SeekStart)
	}
	raw, _ := io.ReadAll(f)
	return string(raw)
}

// prune keeps run history within the bundle's limits.
func (e *Env) prune(b bundle.Bundle) {
	keepRuns, keepDays := b.KeepRuns, b.KeepDays
	if keepRuns <= 0 {
		keepRuns = 500
	}
	if keepDays <= 0 {
		keepDays = 90
	}
	ids := e.runIDs()
	cutoff := e.now().AddDate(0, 0, -keepDays).UTC().Format("20060102T150405")
	for i, id := range ids { // newest first
		if i < keepRuns && id >= cutoff {
			continue
		}
		if r, err := e.loadRun(id); err == nil && r.Status == bundle.Running {
			continue
		}
		os.RemoveAll(e.runDir(id))
	}
}

// runIDs lists run IDs, newest first.
func (e *Env) runIDs() []string {
	entries, _ := os.ReadDir(e.path(RunsDir))
	var ids []string
	for _, en := range entries {
		if en.IsDir() && runIDRE.MatchString(en.Name()) {
			ids = append(ids, en.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids
}

// ------------------------------------------------- start, cancel, status, log

func (e *Env) unitActive(jobID string) bool {
	out, err := e.Exec("systemctl", "is-active", "pbcm-job@"+jobID+".service")
	s := strings.TrimSpace(out)
	return err == nil && (s == "active" || s == "activating" || s == "deactivating")
}

// Start runs a job now, in the background.
func Start(env *Env, jobID string) error {
	if _, _, err := env.findJob(jobID); err != nil {
		return err
	}
	if env.unitActive(jobID) {
		return errors.New("that job is already running")
	}
	if err := os.MkdirAll(env.path(RuntimeDir), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(env.path(filepath.Join(RuntimeDir, "trigger-"+jobID)), nil, 0o600); err != nil {
		return err
	}
	if out, err := env.Exec("systemctl", "start", "--no-block", "pbcm-job@"+jobID+".service"); err != nil {
		return fmt.Errorf("couldn't start the job: %s", strings.TrimSpace(out))
	}
	fmt.Fprintln(env.Stdout, `{"started":true}`)
	return nil
}

// Cancel stops a running job.
func Cancel(env *Env, jobID string) error {
	if !bundle.IDRE.MatchString(jobID) {
		return &UsageError{"invalid job ID"}
	}
	if !env.unitActive(jobID) {
		return errors.New("that job isn't running")
	}
	if out, err := env.Exec("systemctl", "stop", "--no-block", "pbcm-job@"+jobID+".service"); err != nil {
		return fmt.Errorf("couldn't stop the job: %s", strings.TrimSpace(out))
	}
	fmt.Fprintln(env.Stdout, `{"cancelling":true}`)
	return nil
}

// StatusSince reports runs changed at or after since (unix seconds). A run
// still marked running whose job isn't running any more (the machine
// restarted, or the runner was killed) is recorded as failed.
func StatusSince(env *Env, since int64) error {
	st, err := env.loadStored()
	if err != nil {
		return err
	}
	out := bundle.Status{Runs: []bundle.Run{}, Applied: st.Hash, Now: env.now().Unix(), Sizes: env.sizes(),
		Runner: env.selfHash(), RunnerVersion: version.Version}
	active := map[string]bool{}
	for _, id := range env.runIDs() {
		r, err := env.loadRun(id)
		if err != nil {
			continue
		}
		if r.Status == bundle.Running {
			on, seen := active[r.JobID]
			if !seen {
				on = env.unitActive(r.JobID)
				active[r.JobID] = on
			}
			if !on {
				r.Status, r.Ended = bundle.Failed, env.now().Unix()
				r.Summary = "Interrupted: the machine restarted or the backup was stopped before it finished."
				_ = env.saveRun(r)
			}
		}
		if r.Updated >= since {
			out.Runs = append(out.Runs, *r)
		}
	}
	return json.NewEncoder(env.Stdout).Encode(out)
}

// Log returns part of a run's log from offset.
func Log(env *Env, runID string, offset int64) error {
	if !runIDRE.MatchString(runID) {
		return &UsageError{"invalid run ID"}
	}
	r, err := env.loadRun(runID)
	if err != nil {
		return fmt.Errorf("that run isn't on this client any more")
	}
	f, err := os.Open(filepath.Join(env.runDir(runID), "log"))
	chunk := bundle.LogChunk{Offset: offset, Done: r.Status != bundle.Running}
	if err == nil {
		defer f.Close()
		st, _ := f.Stat()
		chunk.Size = st.Size()
		if offset < 0 || offset > st.Size() {
			offset = 0
		}
		f.Seek(offset, io.SeekStart)
		buf, _ := io.ReadAll(io.LimitReader(bufio.NewReader(f), 512<<10))
		chunk.Text = strings.ToValidUTF8(string(buf), "�")
		chunk.Offset = offset + int64(len(buf))
	}
	return json.NewEncoder(env.Stdout).Encode(chunk)
}

func parseInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
