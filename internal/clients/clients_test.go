package clients

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/runner"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/secret"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/sshx"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/store"
)

// fakeHost is an SSH server that behaves like a Debian machine just enough
// for setup: root and a sudo user sign in with passwords, setup "installs"
// the pbcwm account, and pbcwm then signs in with the server's key and gets
// pbcwm-runner answers.
type fakeHost struct {
	t        *testing.T
	ln       net.Listener
	mu       sync.Mutex
	hostKey  ssh.Signer
	execs    []execRecord
	files    map[string][]byte
	pbcwmKey ssh.PublicKey // authorized for pbcwm once setup ran
	rootKey  ssh.PublicKey // authorized for root, if set
	failWith string        // make setup.sh print this ERROR line
	removed  bool
}

type execRecord struct {
	user, cmd string
	stdin     []byte
}

const (
	rootPW  = "root-secret-pw"
	alicePW = "alice-secret-pw"
	tmpDir  = "/tmp/pbcwm-setup.Ab12Cd34"
)

func newSigner(t *testing.T) ssh.Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newFakeHost(t *testing.T) *fakeHost {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{t: t, ln: ln, hostKey: newSigner(t), files: map[string][]byte{}}
	t.Cleanup(func() { ln.Close() })
	go h.serve()
	return h
}

func (h *fakeHost) port() int { return h.ln.Addr().(*net.TCPAddr).Port }

func (h *fakeHost) setHostKey(s ssh.Signer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hostKey = s
}

func (h *fakeHost) file(path string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.files[path]
}

func (h *fakeHost) wasRemoved() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.removed
}

func (h *fakeHost) records() []execRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]execRecord{}, h.execs...)
}

func (h *fakeHost) serve() {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return
		}
		go h.handle(conn)
	}
}

func (h *fakeHost) handle(nc net.Conn) {
	h.mu.Lock()
	hostKey := h.hostKey
	h.mu.Unlock()
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if (m.User() == "root" && string(pw) == rootPW) || (m.User() == "alice" && string(pw) == alicePW) {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
		PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if m.User() == runner.Account && h.pbcwmKey != nil && bytes.Equal(k.Marshal(), h.pbcwmKey.Marshal()) {
				return nil, nil
			}
			if m.User() == "root" && h.rootKey != nil && bytes.Equal(k.Marshal(), h.rootKey.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(hostKey)
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range creqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var p struct{ Cmd string }
				ssh.Unmarshal(req.Payload, &p)
				req.Reply(true, nil)
				stdin, _ := io.ReadAll(ch)
				out, errOut, code := h.exec(sc.User(), p.Cmd, stdin)
				io.WriteString(ch, out)
				io.WriteString(ch.Stderr(), errOut)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				return
			}
		}()
	}
}

func (h *fakeHost) exec(user, cmd string, stdin []byte) (string, string, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.execs = append(h.execs, execRecord{user, cmd, stdin})
	if user == runner.Account {
		// The forced command hands everything to pbcwm-runner.
		words, err := runner.SplitWords(cmd)
		if err != nil || len(words) == 0 {
			return "", "Error: bad command", 2
		}
		switch words[0] {
		case "detect":
			b, _ := json.Marshal(runner.Info{OSID: "debian", OSPretty: "Debian GNU/Linux 12 (bookworm)", OSCodename: "bookworm",
				Arch: "x86_64", Hostname: "nas", SystemdVersion: "252", ClientVersion: "3.4.1", RunnerVersion: "2.0.0-test"})
			return string(b) + "\n", "", 0
		case "browse":
			b, _ := json.Marshal(runner.Listing{Path: words[1], Parent: "/", Dirs: []string{"Media", "Documents"}})
			return string(b), "", 0
		case "uninstall":
			h.removed, h.pbcwmKey = true, nil
			return "Removed the sudo rule, settings and pbcwm-runner.\n", "", 0
		}
		return "", "Error: unknown command", 2
	}
	switch {
	case cmd == "id -u":
		if user == "root" {
			return "0\n", "", 0
		}
		return "1000\n", "", 0
	case cmd == "sudo -n true":
		return "", "sudo: a password is required\n", 1
	case cmd == "sudo -S -p '' -v":
		if string(stdin) == alicePW+"\n" {
			return "", "", 0
		}
		return "", "Sorry, try again.\n", 1
	case cmd == "umask 077 && mktemp -d /tmp/pbcwm-setup.XXXXXXXX":
		return tmpDir + "\n", "", 0
	case strings.HasPrefix(cmd, "cat > "):
		path := strings.Trim(strings.TrimPrefix(cmd, "cat > "), "'")
		h.files[path] = stdin
		return "", "", 0
	case strings.HasPrefix(cmd, "rm -rf "):
		return "", "", 0
	case cmd == "bash "+tmpDir+"/setup.sh" || cmd == "sudo -S -p '' bash "+tmpDir+"/setup.sh":
		if user != "root" && string(stdin) != alicePW+"\n" {
			return "sudo: no password\n", "", 1
		}
		for _, f := range []string{"pbcwm-runner", "key.pub", "setup.sh"} {
			if _, ok := h.files[tmpDir+"/"+f]; !ok {
				return "ERROR: " + f + " is missing\n", "", 1
			}
		}
		if h.failWith != "" {
			return "==> Setting up Debian\nERROR: " + h.failWith + "\n", "", 1
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey(h.files[tmpDir+"/key.pub"])
		if err != nil {
			return "ERROR: bad key\n", "", 1
		}
		h.pbcwmKey = k
		return "==> Setting up Debian GNU/Linux 12 (bookworm) (x86_64)\n==> Setup finished\n", "", 0
	}
	return "", "sh: unexpected command: " + cmd + "\n", 127
}

type harness struct {
	m    *Manager
	st   *store.Store
	host *fakeHost
	dir  string
}

func newHarness(t *testing.T) *harness {
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	st, err := store.Open(filepath.Join(dir, "pbcwm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := sshx.LoadOrCreateIdentity(filepath.Join(dir, "ssh", "id_ed25519"), "pbcwm-server@test")
	if err != nil {
		t.Fatal(err)
	}
	runnerPath := filepath.Join(dir, "pbcwm-runner")
	os.WriteFile(runnerPath, []byte("RUNNER-BINARY"), 0o755)
	m := New(st, id, runnerPath)
	m.timeout = 5 * time.Second
	return &harness{m: m, st: st, host: newFakeHost(t), dir: dir}
}

func (h *harness) probe(t *testing.T) *ProbeResult {
	res, err := h.m.Probe(context.Background(), "127.0.0.1", h.host.port())
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
	c, task, err := h.m.Add(AddRequest{Name: "nas", Address: "127.0.0.1", Port: h.host.port(), HostKey: p.HostKey, Login: login})
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
	if res.Fingerprint != ssh.FingerprintSHA256(h.host.hostKey.PublicKey()) || res.Type != "ssh-ed25519" {
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
	if c.OSPretty != "Debian GNU/Linux 12 (bookworm)" || c.ClientVersion != "3.4.1" || c.Hostname != "nas" || c.LastContact == 0 {
		t.Fatalf("details not saved: %+v", c)
	}
	if string(h.host.file(tmpDir+"/pbcwm-runner")) != "RUNNER-BINARY" || !bytes.Equal(h.host.file(tmpDir+"/setup.sh"), setupScript) ||
		strings.TrimSpace(string(h.host.file(tmpDir+"/key.pub"))) != h.m.identity.AuthorizedKey {
		t.Fatal("uploaded files differ from what was sent")
	}
	joined := strings.Join(v.Lines, "\n")
	for _, want := range []string{"Signed in as root", "Setup finished", "Ready: Debian"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}
	// The password is never written anywhere.
	raw, _ := os.ReadFile(filepath.Join(h.dir, "pbcwm.db"))
	wal, _ := os.ReadFile(filepath.Join(h.dir, "pbcwm.db-wal"))
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
	for _, r := range h.host.records() {
		hasPW := bytes.Contains(r.stdin, []byte(alicePW))
		isSudo := strings.HasPrefix(r.cmd, "sudo -S -p '' ")
		if hasPW && !isSudo {
			t.Errorf("password sent to %q", r.cmd)
		}
		if strings.Contains(r.cmd, alicePW) {
			t.Errorf("password on a command line: %q", r.cmd)
		}
		if r.cmd == "sudo -S -p '' bash "+tmpDir+"/setup.sh" {
			sawScript = true
		}
	}
	if !sawScript {
		t.Fatal("setup.sh should run through sudo")
	}
}

func TestWrongSudoPassword(t *testing.T) {
	h := newHarness(t)
	p := h.probe(t)
	c, task, err := h.m.Add(AddRequest{Name: "nas", Address: "127.0.0.1", Port: h.host.port(), HostKey: p.HostKey,
		Login: Login{User: "alice", Password: "wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, task)
	if v.OK || !strings.Contains(v.Error, "didn't accept the sign-in") {
		t.Fatalf("wrong password: %+v", v)
	}
	c, _ = h.st.GetClient(c.ID)
	if c.Status != store.ClientError {
		t.Fatalf("status %s", c.Status)
	}
}

func TestHostKeyMustMatchWhatWasChecked(t *testing.T) {
	h := newHarness(t)
	other := sshx.FormatKey(newSigner(t).PublicKey())
	c, task, err := h.m.Add(AddRequest{Name: "nas", Address: "127.0.0.1", Port: h.host.port(), HostKey: other,
		Login: Login{User: "root", Password: rootPW}})
	if err != nil {
		t.Fatal(err)
	}
	v := wait(t, task)
	if v.OK || !strings.Contains(v.Error, "isn't the one you checked") {
		t.Fatalf("expected host key refusal: %+v", v)
	}
	for _, r := range h.host.records() {
		t.Errorf("nothing should run on an unverified host, ran %q", r.cmd)
	}
	c, _ = h.st.GetClient(c.ID)
	if c.Status != store.ClientError {
		t.Fatalf("status %s", c.Status)
	}
}

func TestSetupErrorIsReported(t *testing.T) {
	h := newHarness(t)
	h.host.failWith = "proxmox-backup-client is only made for x86-64 machines, and this one is aarch64."
	c, v := h.add(t, Login{User: "root", Password: rootPW})
	if v.OK || c.Status != store.ClientError || !strings.Contains(c.StatusDetail, "only made for x86-64") {
		t.Fatalf("got %+v / %s", v, c.StatusDetail)
	}
}

func TestSignInWithServerKey(t *testing.T) {
	h := newHarness(t)
	h.host.rootKey = h.m.identity.Signer.PublicKey()
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
	_, _, err = h.m.Add(AddRequest{Name: "other", Address: "127.0.0.1", Port: h.host.port(), HostKey: p.HostKey, Login: Login{Password: "x"}})
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

	l, err := h.m.Browse(ctx, c.ID, "/srv/My Files")
	if err != nil || l.Path != "/srv/My Files" || len(l.Dirs) != 2 {
		t.Fatalf("browse: %+v %v", l, err)
	}
	last := h.host.records()[len(h.host.records())-1]
	if last.user != "pbcwm" || last.cmd != "browse '/srv/My Files'" {
		t.Fatalf("browse ran %q as %s", last.cmd, last.user)
	}

	// The client is reinstalled: it now has a new host key.
	newKey := newSigner(t)
	h.host.setHostKey(newKey)
	c, err = h.m.Check(ctx, c.ID)
	if err == nil || c.Status != store.ClientHostKeyChanged || c.OfferedKey != sshx.FormatKey(newKey.PublicKey()) {
		t.Fatalf("check after key change: %v %+v", err, c)
	}
	if _, err := h.m.Browse(ctx, c.ID, "/srv"); err == nil {
		t.Fatal("must not talk to a host whose key changed")
	}

	// Repair with the new key the user checked.
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
	if err != nil || !strings.Contains(out, "Removed") || !h.host.wasRemoved() {
		t.Fatalf("remove: %q %v", out, err)
	}
	if _, err := h.st.GetClient(c.ID); err != store.ErrNotFound {
		t.Fatal("client should be deleted")
	}
}

func TestRemoveUnreachableNeedsListOnly(t *testing.T) {
	h := newHarness(t)
	c, _ := h.add(t, Login{User: "root", Password: rootPW})
	h.host.ln.Close()
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
