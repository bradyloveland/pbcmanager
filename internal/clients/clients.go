// Package clients manages the machines the server backs up: adding them over
// SSH (as root, once), checking them, browsing their folders, repairing and
// removing them. After setup the server only signs in as the limited pbcm
// account, whose key can run nothing but pbcm-runner.
package clients

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"

	"github.com/bradyloveland/pbcmanager/internal/runner"
	"github.com/bradyloveland/pbcmanager/internal/sshx"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

//go:embed setup.sh
var setupScript []byte

// Manager does everything that touches clients.
type Manager struct {
	store      *store.Store
	identity   *sshx.Identity
	runnerPath string
	timeout    time.Duration
	tasks      tasks

	busyMu   sync.Mutex
	busy     map[string]bool
	syncMus  map[string]*sync.Mutex
	nextSync map[string]time.Time
	// measureAsked is when the server last asked for each folder ("client:path").
	measureAsked map[string]time.Time
	measuring    map[string]bool

	// Sync configures keeping clients in step (see sync.go).
	Sync SyncConfig
}

// New returns a manager. runnerPath is the pbcm-runner executable to send
// to clients (linux/amd64).
func New(st *store.Store, id *sshx.Identity, runnerPath string) *Manager {
	return &Manager{store: st, identity: id, runnerPath: runnerPath, timeout: 15 * time.Second,
		tasks: tasks{items: map[string]*Task{}}, busy: map[string]bool{},
		Sync: SyncConfig{ActiveEvery: 30 * time.Second, IdleEvery: 5 * time.Minute}}
}

// Identity returns the server's SSH key.
func (m *Manager) Identity() *sshx.Identity { return m.identity }

// Task returns a running or recent task.
func (m *Manager) Task(id string) *Task { return m.tasks.get(id) }

// LatestTask returns the newest setup or repair task for a client, if any.
func (m *Manager) LatestTask(clientID string) *Task { return m.tasks.latest(clientID) }

// InputError is a problem with what the user entered.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

func inputErr(format string, a ...any) error { return &InputError{fmt.Sprintf(format, a...)} }

var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-]*[A-Za-z0-9])?$`)

// CleanAddress checks a host name or IP address and port.
func CleanAddress(address string, port int) (string, int, error) {
	a := strings.Trim(strings.TrimSpace(address), "[]")
	if ip := net.ParseIP(a); ip != nil {
		a = ip.String()
	} else if !hostRE.MatchString(a) || len(a) > 253 {
		return "", 0, inputErr("Enter the client's host name or IP address, like nas.lan or 192.0.2.20.")
	}
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return "", 0, inputErr("The SSH port must be between 1 and 65535.")
	}
	return a, port, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", inputErr("Give the client a name of up to 64 characters.")
	}
	return name, nil
}

func addr(c *store.Client) string { return net.JoinHostPort(c.Address, strconv.Itoa(c.Port)) }

// ProbeResult is a client's SSH host key, for the user to compare.
type ProbeResult struct {
	HostKey     string `json:"host_key"`
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
}

// Probe fetches the host key at address:port without signing in.
func (m *Manager) Probe(ctx context.Context, address string, port int) (*ProbeResult, error) {
	a, p, err := CleanAddress(address, port)
	if err != nil {
		return nil, err
	}
	k, err := sshx.Probe(ctx, net.JoinHostPort(a, strconv.Itoa(p)), m.timeout)
	if err != nil {
		return nil, err
	}
	return &ProbeResult{HostKey: sshx.FormatKey(k), Fingerprint: sshx.Fingerprint(k), Type: k.Type()}, nil
}

// Login is how to sign in for setup or repair. It is never stored.
type Login struct {
	User     string `json:"user"`
	Password string `json:"password"`
	// UseKey means the server's own key was added for this user by hand.
	UseKey bool `json:"use_key"`
}

func (l Login) clean() (Login, error) {
	l.User = strings.TrimSpace(l.User)
	if l.User == "" {
		l.User = "root"
	}
	if strings.ContainsAny(l.User, " :\t\n'\"") {
		return l, inputErr("That username isn't valid.")
	}
	if !l.UseKey && l.Password == "" {
		return l, inputErr("Enter the password for %s, or choose to sign in with this server's key.", l.User)
	}
	return l, nil
}

// AddRequest is a new client.
type AddRequest struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	// HostKey is the key from Probe that the user confirmed.
	HostKey string `json:"host_key"`
	Login   Login  `json:"login"`
}

// Add saves the client and starts setting it up.
func (m *Manager) Add(in AddRequest) (*store.Client, *Task, error) {
	a, p, err := CleanAddress(in.Address, in.Port)
	if err != nil {
		return nil, nil, err
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		name = a
	}
	if name, err = cleanName(name); err != nil {
		return nil, nil, err
	}
	key, err := sshx.ParseKey(in.HostKey)
	if err != nil {
		return nil, nil, inputErr("Check the client's SSH host key first.")
	}
	login, err := in.Login.clean()
	if err != nil {
		return nil, nil, err
	}
	c := &store.Client{ID: newID()[:12], Name: name, Address: a, Port: p, HostKey: sshx.FormatKey(key),
		Status: store.ClientSettingUp, StatusDetail: "Setting up…"}
	if err := m.store.CreateClient(c); err != nil {
		var dup *store.ErrDuplicate
		if errors.As(err, &dup) {
			if dup.Field == "name" {
				return nil, nil, inputErr("There's already a client called “%s”.", name)
			}
			return nil, nil, inputErr("%s port %d has already been added.", a, p)
		}
		return nil, nil, err
	}
	t, err := m.startSetup(c, login, key, "setup")
	return c, t, err
}

// Repair runs setup again with root (or sudo) credentials, for example after
// the client was reinstalled. hostKey, if set, replaces the pinned key once
// setup succeeds; the user has compared its fingerprint.
func (m *Manager) Repair(id string, login Login, hostKey string) (*Task, error) {
	c, err := m.store.GetClient(id)
	if err != nil {
		return nil, err
	}
	login, err = login.clean()
	if err != nil {
		return nil, err
	}
	key, err := sshx.ParseKey(c.HostKey)
	if hostKey != "" {
		key, err = sshx.ParseKey(hostKey)
	}
	if err != nil {
		return nil, inputErr("The SSH host key isn't valid. Check it again.")
	}
	return m.startSetup(c, login, key, "repair")
}

func (m *Manager) claim(id string) bool {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if m.busy[id] {
		return false
	}
	m.busy[id] = true
	return true
}

func (m *Manager) release(id string) {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	delete(m.busy, id)
}

func (m *Manager) startSetup(c *store.Client, login Login, key ssh.PublicKey, kind string) (*Task, error) {
	runnerBin, err := os.ReadFile(m.runnerPath)
	if err != nil {
		return nil, fmt.Errorf("this server's copy of pbcm-runner is missing (%s); reinstall the server", m.runnerPath)
	}
	if !m.claim(c.ID) {
		return nil, inputErr("Something is already being done on %s. Wait for it to finish.", c.Name)
	}
	t := m.tasks.start(c.ID, kind)
	c.Status, c.StatusDetail = store.ClientSettingUp, "Setting up…"
	_ = m.store.SaveClient(c)
	go func() {
		defer m.release(c.ID)
		err := m.setup(t, c, login, key, runnerBin)
		fresh, gerr := m.store.GetClient(c.ID)
		if gerr != nil {
			t.finish(err)
			return
		}
		if err != nil {
			fresh.Status, fresh.StatusDetail = store.ClientError, err.Error()
			_ = m.store.SaveClient(fresh)
			t.Logf("ERROR: " + err.Error())
			slog.Warn("client setup failed", "client", c.Name, "err", err)
		} else {
			slog.Info("client set up", "client", c.Name)
		}
		t.finish(err)
	}()
	return t, nil
}

var tmpDirRE = regexp.MustCompile(`^/[A-Za-z0-9._/\-]+/pbcm-setup\.[A-Za-z0-9]+$`)

func (m *Manager) setup(t *Task, c *store.Client, login Login, key ssh.PublicKey, runnerBin []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	auth := sshx.PasswordAuth(login.Password)
	how := "password"
	if login.UseKey {
		auth, how = sshx.KeyAuth(m.identity), "this server's key"
	}
	t.Logf(fmt.Sprintf("Connecting to %s as %s with %s…", addr(c), login.User, how))
	conn, err := sshx.Dial(ctx, sshx.Target{Addr: addr(c), HostKey: key, User: login.User, Auth: auth, Timeout: m.timeout})
	if err != nil {
		var hk *sshx.HostKeyChangedError
		if errors.As(err, &hk) {
			return fmt.Errorf("the client's SSH host key isn't the one you checked (it now shows %s). Check the client before trusting it", sshx.Fingerprint(hk.Offered))
		}
		return err
	}
	defer conn.Close()

	uid, _, _, err := sshx.Output(conn, "id -u", nil)
	if err != nil {
		return fmt.Errorf("running a command on the client failed: %w", err)
	}
	prefix, sudoInput := "", ""
	if strings.TrimSpace(uid) != "0" {
		if code, _ := sshx.Run(conn, "sudo -n true", nil, nil, nil); code == 0 {
			prefix = "sudo -n "
		} else if login.Password != "" {
			if code, _ := sshx.Run(conn, "sudo -S -p '' -v", strings.NewReader(login.Password+"\n"), nil, nil); code != 0 {
				return fmt.Errorf("%s can't use sudo with that password. Sign in as root, or as a user allowed to use sudo", login.User)
			}
			prefix, sudoInput = "sudo -S -p '' ", login.Password+"\n"
		} else {
			return fmt.Errorf("%s needs a password for sudo. Enter it, or sign in as root", login.User)
		}
		t.Logf("Signed in as " + login.User + "; using sudo for setup.")
	} else {
		t.Logf("Signed in as root.")
	}

	out, _, code, err := sshx.Output(conn, "umask 077 && mktemp -d /tmp/pbcm-setup.XXXXXXXX", nil)
	dir := strings.TrimSpace(out)
	if err != nil || code != 0 || !tmpDirRE.MatchString(dir) {
		return errors.New("couldn't create a temporary folder on the client")
	}
	cleanup := func() { _, _ = sshx.Run(conn, "rm -rf "+sshx.Quote(dir), nil, nil, nil) }
	upload := func(name string, data []byte) error {
		code, err := sshx.Run(conn, "cat > "+sshx.Quote(dir+"/"+name), strings.NewReader(string(data)), nil, nil)
		if err != nil || code != 0 {
			return fmt.Errorf("couldn't copy %s to the client", name)
		}
		return nil
	}
	t.Logf("Copying pbcm-runner and the setup script…")
	for _, f := range []struct {
		name string
		data []byte
	}{{"pbcm-runner", runnerBin}, {"key.pub", []byte(m.identity.AuthorizedKey + "\n")}, {"setup.sh", setupScript}} {
		if err := upload(f.name, f.data); err != nil {
			cleanup()
			return err
		}
	}
	var stdin *strings.Reader
	if sudoInput != "" {
		stdin = strings.NewReader(sudoInput)
	}
	cmd := prefix + "bash " + sshx.Quote(dir+"/setup.sh")
	if stdin == nil {
		code, err = sshx.Run(conn, cmd, nil, t, t)
	} else {
		code, err = sshx.Run(conn, cmd, stdin, t, t)
	}
	if err != nil || code != 0 {
		cleanup()
		if msg := t.LastError(); msg != "" {
			return errors.New(strings.TrimSuffix(msg, "."))
		}
		if err != nil {
			return fmt.Errorf("setup stopped: %w", err)
		}
		return fmt.Errorf("setup stopped with exit code %d; see the log above", code)
	}
	conn.Close()

	t.Logf("Checking that the server can sign in as pbcm…")
	c.HostKey = sshx.FormatKey(key)
	c.OfferedKey = ""
	if err := m.refresh(ctx, c); err != nil {
		return fmt.Errorf("setup finished, but signing in as pbcm failed: %s. If the client's SSH settings limit who can sign in (AllowUsers or AllowGroups), add pbcm", err)
	}
	t.Logf(fmt.Sprintf("Ready: %s, proxmox-backup-client %s.", c.OSPretty, orNone(c.ClientVersion)))
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(not found)"
	}
	return s
}

// runnerCommand signs in as pbcm and runs a pbcm-runner command.
func (m *Manager) runnerCommand(ctx context.Context, c *store.Client, words ...string) (string, error) {
	return m.runnerCommandInput(ctx, c, nil, words...)
}

// runnerCommandInput is runnerCommand with data on the command's stdin.
func (m *Manager) runnerCommandInput(ctx context.Context, c *store.Client, stdin io.Reader, words ...string) (string, error) {
	key, err := sshx.ParseKey(c.HostKey)
	if err != nil {
		return "", err
	}
	conn, err := sshx.Dial(ctx, sshx.Target{Addr: addr(c), HostKey: key, User: runner.Account,
		Auth: sshx.KeyAuth(m.identity), Timeout: m.timeout})
	if err != nil {
		return "", err
	}
	defer conn.Close()
	stdout, stderr, code, err := sshx.Output(conn, sshx.Join(words...), stdin)
	if err != nil {
		return "", err
	}
	if code != 0 {
		msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stderr), "Error:"))
		if msg == "" {
			msg = fmt.Sprintf("pbcm-runner exited with code %d", code)
		}
		if strings.Contains(stderr, "sudo:") {
			msg = "the pbcm sudo rule is missing or broken on the client. Use Repair"
		}
		return "", errors.New(msg)
	}
	return stdout, nil
}

// refresh signs in as pbcm, reads the client's details and saves them with
// status ready, or records why it couldn't.
func (m *Manager) refresh(ctx context.Context, c *store.Client) error {
	out, err := m.runnerCommand(ctx, c, "detect")
	if err != nil {
		var hk *sshx.HostKeyChangedError
		if errors.As(err, &hk) {
			c.Status, c.OfferedKey = store.ClientHostKeyChanged, sshx.FormatKey(hk.Offered)
			c.StatusDetail = "The client's SSH host key has changed. If it was reinstalled, check the new key and use Repair; otherwise, investigate before trusting it."
		} else {
			c.Status, c.StatusDetail = store.ClientUnreachable, err.Error()
			if c.UnreachableSince == 0 {
				c.UnreachableSince = time.Now().Unix()
			}
		}
		_ = m.store.SaveClient(c)
		return err
	}
	var info runner.Info
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return fmt.Errorf("pbcm-runner sent an unexpected reply")
	}
	c.OSID, c.OSPretty, c.OSCodename, c.Arch = info.OSID, info.OSPretty, info.OSCodename, info.Arch
	c.Hostname, c.SystemdVersion, c.ClientVersion, c.RunnerVersion = info.Hostname, info.SystemdVersion, info.ClientVersion, info.RunnerVersion
	c.ServerHere, c.LastContact, c.Timezone = info.ServerHere, time.Now().Unix(), info.Timezone
	c.Status, c.StatusDetail, c.OfferedKey = store.ClientReady, "", ""
	m.backOnline(c)
	if info.ClientVersion == "" {
		c.Status, c.StatusDetail = store.ClientError, "proxmox-backup-client isn't installed on the client. Use Repair to install it."
	}
	return m.store.SaveClient(c)
}

// Check contacts a client and updates its details.
func (m *Manager) Check(ctx context.Context, id string) (*store.Client, error) {
	c, err := m.store.GetClient(id)
	if err != nil {
		return nil, err
	}
	if !m.claim(c.ID) {
		return c, nil
	}
	defer m.release(c.ID)
	err = m.refresh(ctx, c)
	return c, err
}

// Browse lists folders on a client.
func (m *Manager) Browse(ctx context.Context, id, path string) (*runner.Listing, error) {
	c, err := m.store.GetClient(id)
	if err != nil {
		return nil, err
	}
	out, err := m.runnerCommand(ctx, c, "browse", path)
	if err != nil {
		return nil, err
	}
	var l runner.Listing
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		return nil, fmt.Errorf("pbcm-runner sent an unexpected reply")
	}
	return &l, nil
}

// Remove deletes a client. With uninstall it first removes everything setup
// put on the client; that needs the client to be reachable.
func (m *Manager) Remove(ctx context.Context, id string, uninstall, keepHistory bool) (string, error) {
	c, err := m.store.GetClient(id)
	if err != nil {
		return "", err
	}
	if !m.claim(c.ID) {
		return "", inputErr("Something is being done on %s right now. Wait for it to finish.", c.Name)
	}
	defer m.release(c.ID)
	out := ""
	if uninstall {
		words := []string{"uninstall"}
		if keepHistory {
			words = append(words, "--keep-history")
		}
		if out, err = m.runnerCommand(ctx, c, words...); err != nil {
			return "", fmt.Errorf("couldn't clean up %s: %s. Fix the connection, or remove it from this list only", c.Name, err)
		}
	}
	if err := m.store.DeleteClient(id); err != nil { // its jobs go with it
		return "", err
	}
	_ = m.store.DeleteClientRuns(id)
	if m.Sync.LogDir != "" {
		os.RemoveAll(filepath.Join(m.Sync.LogDir, id))
	}
	slog.Info("client removed", "client", c.Name, "uninstalled", uninstall)
	return out, nil
}
