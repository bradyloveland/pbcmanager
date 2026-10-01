// Package runner is pbcwm-runner, the small program installed on each client.
// It isn't a background service: the server starts it over SSH for one
// command (through a forced command and a sudo rule that allow nothing else),
// and later milestones have systemd start it for backups.
package runner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/version"
)

// Fixed locations on a client.
const (
	Path        = "/usr/local/lib/pbcwm/pbcwm-runner"
	SudoersPath = "/etc/sudoers.d/pbcwm"
	ConfigDir   = "/etc/pbcwm/client"
	StateDir    = "/var/lib/pbcwm/client"
	Account     = "pbcwm"
	// KeyMarker identifies authorized_keys lines this project added.
	KeyMarker = `command="sudo -n /usr/local/lib/pbcwm/pbcwm-runner ssh"`
	// ServerUnit exists when the server itself is installed on this machine.
	ServerUnit = "/etc/systemd/system/pbcwm.service"
)

// Env is what commands touch, so tests can point them elsewhere.
type Env struct {
	Root   string // prefix for every absolute path ("" on a real client)
	Stdout io.Writer
	Stderr io.Writer
	// Exec runs a program and returns its combined output.
	Exec func(name string, args ...string) (string, error)
}

func (e *Env) path(p string) string { return filepath.Join(e.Root, p) }

// DefaultEnv is the real machine.
func DefaultEnv() *Env {
	return &Env{Stdout: os.Stdout, Stderr: os.Stderr, Exec: func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return string(out), err
	}}
}

// UsageError means the command line was wrong.
type UsageError struct{ msg string }

func (e *UsageError) Error() string { return e.msg }

// Main runs a command and returns the exit code.
func Main(env *Env, args []string) int {
	if err := Dispatch(env, args); err != nil {
		fmt.Fprintln(env.Stderr, "Error:", err)
		var ue *UsageError
		if errors.As(err, &ue) {
			return 2
		}
		return 1
	}
	return 0
}

// Dispatch runs one command.
func Dispatch(env *Env, args []string) error {
	if len(args) == 0 {
		return &UsageError{"no command given"}
	}
	switch args[0] {
	case "ssh":
		// Started by the forced command in authorized_keys: the server's
		// actual request is in SSH_ORIGINAL_COMMAND, which sudo keeps.
		words, err := SplitWords(os.Getenv("SSH_ORIGINAL_COMMAND"))
		if err != nil {
			return &UsageError{err.Error()}
		}
		if len(words) > 0 && words[0] == "ssh" {
			return &UsageError{"nested ssh command"}
		}
		return Dispatch(env, words)
	case "version":
		fmt.Fprintln(env.Stdout, version.Version)
		return nil
	case "detect":
		info := Detect(env)
		return json.NewEncoder(env.Stdout).Encode(info)
	case "browse":
		path := "/srv"
		if len(args) > 1 {
			path = args[1]
		}
		res, err := Browse(env, path)
		if err != nil {
			return err
		}
		return json.NewEncoder(env.Stdout).Encode(res)
	case "uninstall":
		keep := len(args) > 1 && args[1] == "--keep-history"
		return Uninstall(env, keep)
	}
	return &UsageError{fmt.Sprintf("unknown command %q", args[0])}
}

// SplitWords splits a command line the way the server builds it: words
// separated by spaces, single quotes taken literally, and a backslash escaping
// the next character outside quotes. No shell is involved, so nothing else is
// special.
func SplitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord, inQuote := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inQuote, inWord = true, true
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errors.New("command ends with a backslash")
			}
			i++
			cur.WriteByte(s[i])
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inQuote {
		return nil, errors.New("unclosed quote in command")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// --------------------------------------------------------------- detect

// Info describes the client.
type Info struct {
	OSID           string `json:"os_id"`
	OSPretty       string `json:"os_pretty"`
	OSCodename     string `json:"os_codename"`
	OSVersion      string `json:"os_version"`
	Arch           string `json:"arch"`
	Hostname       string `json:"hostname"`
	SystemdVersion string `json:"systemd_version"`
	ClientVersion  string `json:"client_version"`
	RunnerVersion  string `json:"runner_version"`
	ServerHere     bool   `json:"server_here"`
}

var (
	systemdRE = regexp.MustCompile(`^systemd (\d+)`)
	clientRE  = regexp.MustCompile(`(\d+\.\d+(\.\d+)?)`)
)

// ParseOSRelease reads KEY=value lines, removing quotes.
func ParseOSRelease(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}

// Detect gathers facts about the client.
func Detect(env *Env) Info {
	info := Info{RunnerVersion: version.Version}
	if f, err := os.Open(env.path("/etc/os-release")); err == nil {
		rel := ParseOSRelease(f)
		f.Close()
		info.OSID, info.OSPretty, info.OSCodename, info.OSVersion = rel["ID"], rel["PRETTY_NAME"], rel["VERSION_CODENAME"], rel["VERSION_ID"]
	}
	if out, err := env.Exec("uname", "-m"); err == nil {
		info.Arch = strings.TrimSpace(out)
	}
	info.Hostname, _ = os.Hostname()
	if out, err := env.Exec("systemctl", "--version"); err == nil {
		if m := systemdRE.FindStringSubmatch(out); m != nil {
			info.SystemdVersion = m[1]
		}
	}
	if out, err := env.Exec("proxmox-backup-client", "version"); err == nil {
		if m := clientRE.FindStringSubmatch(out); m != nil {
			info.ClientVersion = m[1]
		}
	}
	if _, err := os.Stat(env.path(ServerUnit)); err == nil {
		info.ServerHere = true
	}
	return info
}

// --------------------------------------------------------------- browse

// Listing is one folder's subfolders.
type Listing struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent"`
	Dirs   []string `json:"dirs"`
	More   bool     `json:"more"`
}

// Browse lists the folders inside path (following symlinks, as backups do
// for the folders you pick). A missing folder falls back to "/".
func Browse(env *Env, path string) (*Listing, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, &UsageError{"the folder path must be absolute"}
	}
	if strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return nil, &UsageError{"the folder path contains control characters"}
	}
	path = filepath.Clean(path)
	if st, err := os.Stat(env.path(path)); err != nil || !st.IsDir() {
		path = "/"
	}
	entries, err := os.ReadDir(env.path(path))
	if err != nil {
		return nil, fmt.Errorf("can't read %s: %w", path, err)
	}
	dirs := []string{}
	for _, e := range entries {
		full := env.path(filepath.Join(path, e.Name()))
		if st, err := os.Stat(full); err == nil && st.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
	res := &Listing{Path: path, Dirs: dirs}
	if len(dirs) > 1000 {
		res.Dirs, res.More = dirs[:1000], true
	}
	if path != "/" {
		res.Parent = filepath.Dir(path)
	}
	return res, nil
}

// ------------------------------------------------------------ uninstall

// Uninstall removes everything this project put on the client: the sudo
// rule, the server's key, the runner, client settings and (unless keepHistory)
// run history. The pbcwm account is removed a few seconds later, once this
// SSH session has ended, unless the server itself runs on this machine and
// uses the same account.
func Uninstall(env *Env, keepHistory bool) error {
	say := func(format string, a ...any) { fmt.Fprintf(env.Stdout, format+"\n", a...) }
	serverHere := false
	if _, err := os.Stat(env.path(ServerUnit)); err == nil {
		serverHere = true
	}
	home := ""
	if out, err := env.Exec("getent", "passwd", Account); err == nil {
		if f := strings.Split(strings.TrimSpace(out), ":"); len(f) >= 6 {
			home = f[5]
		}
	}
	if home != "" {
		if err := removeKeyLines(env.path(filepath.Join(home, ".ssh", "authorized_keys"))); err != nil {
			say("Couldn't tidy authorized_keys: %s", err)
		} else {
			say("Removed the server's SSH key.")
		}
	}
	for _, p := range []string{SudoersPath, ConfigDir, filepath.Dir(Path)} {
		if err := os.RemoveAll(env.path(p)); err != nil {
			return err
		}
	}
	say("Removed the sudo rule, settings and pbcwm-runner.")
	if !keepHistory {
		if err := os.RemoveAll(env.path(StateDir)); err != nil {
			return err
		}
		say("Removed run history.")
	}
	if serverHere {
		say("The server runs on this machine and uses the %s account, so the account was kept.", Account)
		return nil
	}
	script := "sleep 3; pkill -KILL -u " + Account + " 2>/dev/null; userdel " + Account
	if !keepHistory && home != "" {
		script += "; rm -rf " + home
	}
	script += "; rmdir /etc/pbcwm 2>/dev/null; true"
	if _, err := env.Exec("systemd-run", "--quiet", "--collect", "--unit=pbcwm-remove-account", "--on-active=2",
		"/bin/sh", "-c", script); err != nil {
		say("Remove the %s account by hand when convenient: userdel %s", Account, Account)
		return nil
	}
	say("The %s account will be removed in a few seconds.", Account)
	return nil
}

func removeKeyLines(path string) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, KeyMarker) {
			continue
		}
		kept = append(kept, line)
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o600)
}
