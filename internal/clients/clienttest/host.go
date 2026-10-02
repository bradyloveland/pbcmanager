// Package clienttest is a fake client machine for tests: an SSH server that
// behaves like a Debian box enough for setup (root and a sudo user sign in
// with passwords), and that answers the pbcm account with the real
// pbcm-runner code, running in a temporary folder with fake systemctl,
// systemd-creds and proxmox-backup-client.
package clienttest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/bradyloveland/pbcmanager/internal/runner"
)

// Passwords the fake accepts.
const (
	RootPassword  = "root-secret-pw"
	AlicePassword = "alice-secret-pw"
	TmpDir        = "/tmp/pbcm-setup.Ab12Cd34"
)

// FakeClientScript stands in for proxmox-backup-client. A folder path
// containing "fail" fails, one containing "slow" runs until interrupted.
const FakeClientScript = `#!/bin/sh
if [ "$1" = version ]; then echo "client version: 3.4.1"; exit 0; fi
echo "args: $*"
echo "repo=$PBS_REPOSITORY ns=$PBS_NAMESPACE"
[ -f "$PBS_PASSWORD_FILE" ] || { echo "Error: no password file"; exit 1; }
case "$*" in *fail*) echo "Error: connection refused"; exit 255;; esac
case "$*" in *slow*) trap 'echo "stopping on SIGINT"; exit 130' INT; i=0; while [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done;; esac
for a in "$@"; do case "$a" in *.pxar:*) n="${a%%.pxar:*}"
  echo "$n.mpxar: had to backup 12.5 KiB of 12.5 KiB (compressed 2.1 KiB) in 0.01 s (average 1.2 MiB/s)"
  echo "$n.ppxar: had to backup 48.75 MiB of 1.25 GiB (compressed 31.2 MiB) in 0.4 s (average 121.9 MiB/s)"
  echo "$n.ppxar: backup was done incrementally, reused 1.202 GiB (96.2%)";; esac; done
echo "Duration: 0.42s"
`

// TB is the part of TB the host needs, so it can also run outside
// tests (a throwaway dev client).
type TB interface {
	Helper()
	Fatal(args ...any)
	TempDir() string
	Cleanup(func())
}

// Exec records one command the host ran.
type Exec struct {
	User, Cmd string
	Stdin     []byte
}

// Host is the fake machine.
type Host struct {
	T    TB
	Root string // the machine's filesystem

	ln       net.Listener
	mu       sync.Mutex
	hostKey  ssh.Signer
	execs    []Exec
	files    map[string][]byte
	rootKey  ssh.PublicKey
	failWith string
	// candidate is the newest proxmox-backup-client in the fake package lists.
	candidate string
	// uname is what "uname -m" says (x86_64 unless set).
	uname string
	// refusePbcm makes sshd turn the pbcm account away, as OpenMediaVault's
	// "AllowGroups root _ssh" does for an account outside those groups.
	refusePbcm bool
	active     map[string]chan struct{}
	runs       sync.WaitGroup
	client     string
}

// NewSigner returns a fresh ed25519 key.
func NewSigner(t TB) ssh.Signer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// New starts a fake host on a random local port.
func New(t TB) *Host { return NewAt(t, "127.0.0.1:0") }

// NewAt starts a fake host on addr.
func NewAt(t TB, addr string) *Host {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{T: t, Root: t.TempDir(), ln: ln, hostKey: NewSigner(t), files: map[string][]byte{}, active: map[string]chan struct{}{}}
	h.client = filepath.Join(t.TempDir(), "proxmox-backup-client")
	if err := os.WriteFile(h.client, []byte(FakeClientScript), 0o755); err != nil {
		t.Fatal(err)
	}
	h.WriteFile("/etc/timezone", "America/Denver\n")
	h.WriteFile("/etc/os-release", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\nVERSION_CODENAME=bookworm\n")
	t.Cleanup(func() {
		ln.Close()
		h.mu.Lock()
		for _, c := range h.active {
			close(c)
		}
		h.active = map[string]chan struct{}{}
		h.mu.Unlock()
		h.runs.Wait()
	})
	go h.serve()
	return h
}

// Port is where the host listens.
func (h *Host) Port() int { return h.ln.Addr().(*net.TCPAddr).Port }

// Close stops answering (the client "goes offline").
func (h *Host) Close() { h.ln.Close() }

// HostKey returns the current host key.
func (h *Host) HostKey() ssh.PublicKey {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hostKey.PublicKey()
}

// SetHostKey replaces the host key (the machine was "reinstalled").
func (h *Host) SetHostKey(s ssh.Signer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hostKey = s
}

// AuthorizeRootKey lets k sign in as root.
func (h *Host) AuthorizeRootKey(k ssh.PublicKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rootKey = k
}

// SetMachine makes the fake report this CPU type ("uname -m"), such as aarch64.
func (h *Host) SetMachine(uname string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.uname = uname
}

func (h *Host) machine() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.uname == "" {
		return "x86_64"
	}
	return h.uname
}

// OfferPackage makes the fake package lists offer this proxmox-backup-client
// version ("" for the installed one, 3.4.1-1).
func (h *Host) OfferPackage(version string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.candidate = version
}

// FailSetupWith makes setup.sh stop with this error.
func (h *Host) FailSetupWith(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failWith = msg
}

// RefusePbcm makes the SSH server refuse every sign-in as pbcm.
func (h *Host) RefusePbcm(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refusePbcm = on
}

// Path maps a client path into the fake's filesystem.
func (h *Host) Path(p string) string { return filepath.Join(h.Root, p) }

// WriteFile creates a file on the fake machine.
func (h *Host) WriteFile(path, body string) {
	if err := os.MkdirAll(filepath.Dir(h.Path(path)), 0o755); err != nil {
		h.T.Fatal(err)
	}
	if err := os.WriteFile(h.Path(path), []byte(body), 0o644); err != nil {
		h.T.Fatal(err)
	}
}

// Mkdir creates a folder on the fake machine.
func (h *Host) Mkdir(path string) {
	if err := os.MkdirAll(h.Path(path), 0o755); err != nil {
		h.T.Fatal(err)
	}
}

// Uploaded returns a file setup copied to the machine.
func (h *Host) Uploaded(path string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.files[path]
}

// Execs returns every command run so far.
func (h *Host) Execs() []Exec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Exec{}, h.execs...)
}

// Running reports whether a job is running.
func (h *Host) Running(jobID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.active[jobID]
	return ok
}

// WaitIdle waits for every started job to finish.
func (h *Host) WaitIdle() { h.runs.Wait() }

func (h *Host) authorizedKeysPath() string { return h.Path("/var/lib/pbcm/.ssh/authorized_keys") }

func (h *Host) pbcmKeyOK(k ssh.PublicKey) bool {
	h.mu.Lock()
	refuse := h.refusePbcm
	h.mu.Unlock()
	if refuse {
		return false
	}
	raw, err := os.ReadFile(h.authorizedKeysPath())
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, runner.KeyMarker) {
			continue
		}
		fields := strings.Fields(line[strings.Index(line, runner.KeyMarker)+len(runner.KeyMarker):])
		if len(fields) >= 2 {
			if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(fields[0] + " " + fields[1])); err == nil && bytes.Equal(pk.Marshal(), k.Marshal()) {
				return true
			}
		}
	}
	return false
}

func (h *Host) serve() {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return
		}
		go h.handle(conn)
	}
}

func (h *Host) handle(nc net.Conn) {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if (m.User() == "root" && string(pw) == RootPassword) || (m.User() == "alice" && string(pw) == AlicePassword) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if m.User() == runner.Account && h.pbcmKeyOK(k) {
				return nil, nil
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if m.User() == "root" && h.rootKey != nil && bytes.Equal(k.Marshal(), h.rootKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	h.mu.Lock()
	cfg.AddHostKey(h.hostKey)
	h.mu.Unlock()
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

func (h *Host) exec(user, cmd string, stdin []byte) (string, string, int) {
	h.mu.Lock()
	h.execs = append(h.execs, Exec{user, cmd, stdin})
	h.mu.Unlock()
	if user == runner.Account {
		words, err := runner.SplitWords(cmd)
		if err != nil || len(words) == 0 || words[0] == "ssh" {
			return "", "Error: bad command", 2
		}
		var out, errOut bytes.Buffer
		env := h.env(bytes.NewReader(stdin), &out, &errOut)
		code := runner.Main(env, words)
		return out.String(), errOut.String(), code
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case cmd == "uname -m": // h.mu is held here
		if h.uname == "" {
			return "x86_64\n", "", 0
		}
		return h.uname + "\n", "", 0
	case cmd == "id -u":
		if user == "root" {
			return "0\n", "", 0
		}
		return "1000\n", "", 0
	case cmd == "sudo -n true":
		return "", "sudo: a password is required\n", 1
	case cmd == "sudo -S -p '' -v":
		if string(stdin) == AlicePassword+"\n" {
			return "", "", 0
		}
		return "", "Sorry, try again.\n", 1
	case cmd == "umask 077 && mktemp -d /tmp/pbcm-setup.XXXXXXXX":
		return TmpDir + "\n", "", 0
	case strings.HasPrefix(cmd, "cat > "):
		h.files[strings.Trim(strings.TrimPrefix(cmd, "cat > "), "'")] = stdin
		return "", "", 0
	case strings.HasPrefix(cmd, "rm -rf "):
		return "", "", 0
	case cmd == "bash "+TmpDir+"/setup.sh" || cmd == "sudo -S -p '' bash "+TmpDir+"/setup.sh":
		if user != "root" && string(stdin) != AlicePassword+"\n" {
			return "sudo: no password\n", "", 1
		}
		for _, f := range []string{"pbcm-runner", "key.pub", "setup.sh"} {
			if _, ok := h.files[TmpDir+"/"+f]; !ok {
				return "ERROR: " + f + " is missing\n", "", 1
			}
		}
		if h.failWith != "" {
			return "==> Setting up Debian\nERROR: " + h.failWith + "\n", "", 1
		}
		// What setup.sh does: install pbcm-runner, and add the key with the
		// forced command for the account.
		os.MkdirAll(filepath.Dir(h.Path(runner.Path)), 0o755)
		os.WriteFile(h.Path(runner.Path), h.files[TmpDir+"/pbcm-runner"], 0o755)
		line := "restrict," + runner.KeyMarker + " " + strings.TrimSpace(string(h.files[TmpDir+"/key.pub"])) + "\n"
		os.MkdirAll(filepath.Dir(h.authorizedKeysPath()), 0o700)
		if err := os.WriteFile(h.authorizedKeysPath(), []byte(line), 0o600); err != nil {
			return "ERROR: " + err.Error() + "\n", "", 1
		}
		return "==> Setting up Debian GNU/Linux 12 (bookworm) (x86_64)\n==> Setup finished\n", "", 0
	}
	return "", "sh: unexpected command: " + cmd + "\n", 127
}

// env is the runner's view of the fake machine.
func (h *Host) env(stdin io.Reader, stdout, stderr io.Writer) *runner.Env {
	env := &runner.Env{Root: h.Root, Stdin: stdin, Stdout: stdout, Stderr: stderr, ClientBin: h.client, Arch: "amd64"}
	if h.machine() == "aarch64" {
		env.Arch = "arm64"
	}
	env.Run = func(input, name string, args ...string) (string, error) {
		if name != "systemd-creds" {
			return "", errors.New("not available")
		}
		if args[0] == "encrypt" {
			return "ENC:" + input, nil
		}
		raw, err := os.ReadFile(args[2])
		return strings.TrimPrefix(string(raw), "ENC:"), err
	}
	env.Exec = func(name string, args ...string) (string, error) {
		switch name {
		case "uname":
			return h.machine() + "\n", nil
		case "proxmox-backup-client":
			return "client version: 3.4.1\n", nil
		case "systemctl":
			return h.systemctl(args)
		case "dpkg-query":
			if args[len(args)-1] == "proxmox-backup-client" {
				return "ii |3.4.1-1", nil
			}
			return "", errors.New("no packages found")
		case "apt-cache":
			h.mu.Lock()
			cand := h.candidate
			h.mu.Unlock()
			if cand == "" {
				cand = "3.4.1-1"
			}
			return "proxmox-backup-client:\n  Installed: 3.4.1-1\n  Candidate: " + cand + "\n", nil
		case "dpkg":
			// --compare-versions A gt B; the fake only offers newer versions.
			if len(args) == 4 && args[1] != args[3] {
				return "", nil
			}
			return "", errors.New("exit 1")
		case "getent":
			return "pbcm:x:999:999::/var/lib/pbcm:/bin/sh\n", nil
		case "systemd-run":
			// Measuring runs in the background on a real client; here it's
			// done before systemd-run returns.
			if n := len(args); n >= 2 && args[n-2] == "measure-run" {
				return "", runner.MeasureRun(h.env(nil, io.Discard, io.Discard), args[n-1])
			}
		}
		return "", nil
	}
	return env
}

func (h *Host) systemctl(args []string) (string, error) {
	unitJob := func(unit string) string {
		return strings.TrimSuffix(strings.TrimPrefix(unit, "pbcm-job@"), ".service")
	}
	switch args[0] {
	case "is-system-running":
		return "running\n", nil
	case "--version":
		return "systemd 252 (252.33-1~deb12u1)\n", nil
	case "is-active":
		if h.Running(unitJob(args[1])) {
			return "active\n", nil
		}
		return "inactive\n", fmt.Errorf("exit status 3")
	case "start":
		id := unitJob(args[len(args)-1])
		cancel := make(chan struct{})
		h.mu.Lock()
		h.active[id] = cancel
		h.runs.Add(1)
		h.mu.Unlock()
		go func() {
			defer h.runs.Done()
			env := h.env(nil, io.Discard, io.Discard)
			_ = runner.RunJob(env, id, cancel)
			h.mu.Lock()
			if h.active[id] == cancel {
				delete(h.active, id)
			}
			h.mu.Unlock()
		}()
		return "", nil
	case "stop":
		id := unitJob(args[len(args)-1])
		h.mu.Lock()
		if c, ok := h.active[id]; ok {
			close(c)
			delete(h.active, id)
		}
		h.mu.Unlock()
		return "", nil
	}
	return "", nil
}
