package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bradyloveland/pbcmanager/internal/sshx"
)

func testEnv(t *testing.T) (*Env, *bytes.Buffer, *[]string) {
	root := t.TempDir()
	var out bytes.Buffer
	var calls []string
	env := &Env{Root: root, Stdout: &out, Stderr: &out, Exec: func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "uname":
			return "x86_64\n", nil
		case "systemctl":
			return "systemd 252 (252.33-1~deb12u1)\n+PAM +AUDIT\n", nil
		case "proxmox-backup-client":
			return "client version: 3.4.1\n", nil
		case "getent":
			return "pbcm:x:999:999::" + "/var/lib/pbcm" + ":/bin/sh\n", nil
		}
		return "", nil
	}}
	return env, &out, &calls
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSplitWordsRoundTripsWithServerQuoting(t *testing.T) {
	cases := [][]string{
		{"browse", "/srv/dev-disk-by-uuid-1234/Media"},
		{"browse", "/srv/My Files/it's here"},
		{"browse", `/odd/back\slash`},
		{"uninstall", "--keep-history"},
		{"browse", ""},
	}
	for _, words := range cases {
		got, err := SplitWords(sshx.Join(words...))
		if err != nil || !reflect.DeepEqual(got, words) {
			t.Errorf("%q: got %q (%v)", words, got, err)
		}
	}
	if _, err := SplitWords("browse 'unclosed"); err == nil {
		t.Error("unclosed quote should fail")
	}
	if got, _ := SplitWords("detect; rm -rf /"); !reflect.DeepEqual(got, []string{"detect;", "rm", "-rf", "/"}) {
		t.Errorf("shell syntax must not be interpreted: %q", got)
	}
}

func TestDispatchRejectsUnknownAndNested(t *testing.T) {
	env, _, _ := testEnv(t)
	if Main(env, []string{"rm", "-rf", "/"}) != 2 {
		t.Error("unknown command should be a usage error")
	}
	t.Setenv("SSH_ORIGINAL_COMMAND", "ssh detect")
	if Main(env, []string{"ssh"}) != 2 {
		t.Error("nested ssh should be refused")
	}
	t.Setenv("SSH_ORIGINAL_COMMAND", "version")
	if Main(env, []string{"ssh"}) != 0 {
		t.Error("version through ssh should work")
	}
}

func TestDetect(t *testing.T) {
	env, out, _ := testEnv(t)
	write(t, env.path("/etc/os-release"), `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION_CODENAME=bookworm
ID=debian
`)
	if Main(env, []string{"detect"}) != 0 {
		t.Fatal(out.String())
	}
	var info Info
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	want := Info{OSID: "debian", OSPretty: "Debian GNU/Linux 12 (bookworm)", OSCodename: "bookworm", OSVersion: "12",
		Arch: "x86_64", SystemdVersion: "252", ClientVersion: "3.4.1"}
	info.Hostname, info.RunnerVersion = "", ""
	if info != want {
		t.Fatalf("got %+v", info)
	}
}

func TestBrowse(t *testing.T) {
	env, _, _ := testEnv(t)
	for _, d := range []string{"/srv/b", "/srv/A", "/srv/c/deeper"} {
		os.MkdirAll(env.path(d), 0o755)
	}
	write(t, env.path("/srv/file.txt"), "x")
	os.Symlink(env.path("/srv/c"), env.path("/srv/link"))
	l, err := Browse(env, "/srv/")
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != "/srv" || l.Parent != "/" || !reflect.DeepEqual(l.Dirs, []string{"A", "b", "c", "link"}) {
		t.Fatalf("got %+v", l)
	}
	if l, _ := Browse(env, "/missing"); l.Path != "/" {
		t.Fatalf("missing folder should fall back to /: %+v", l)
	}
	if _, err := Browse(env, "relative"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestUninstallRemovesOnlyOurFiles(t *testing.T) {
	env, out, calls := testEnv(t)
	key := KeyMarker + " ssh-ed25519 AAAA pbcm@server"
	write(t, env.path("/var/lib/pbcm/.ssh/authorized_keys"), "ssh-ed25519 BBBB someone-else\n"+key+"\n")
	write(t, env.path(SudoersPath), "rule")
	write(t, env.path(Path), "binary")
	write(t, env.path(ConfigDir+"/jobs/a.json"), "{}")
	write(t, env.path(StateDir+"/runs/1/log"), "log")
	write(t, env.path("/etc/pbcm/other"), "not ours")
	if err := Uninstall(env, true); err != nil {
		t.Fatal(err)
	}
	keys, _ := os.ReadFile(env.path("/var/lib/pbcm/.ssh/authorized_keys"))
	if strings.Contains(string(keys), "AAAA") || !strings.Contains(string(keys), "BBBB") {
		t.Fatalf("authorized_keys: %q", keys)
	}
	for _, gone := range []string{SudoersPath, Path, ConfigDir} {
		if _, err := os.Stat(env.path(gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", gone)
		}
	}
	if _, err := os.Stat(env.path(StateDir + "/runs/1/log")); err != nil {
		t.Error("history should be kept with --keep-history")
	}
	if _, err := os.Stat(env.path("/etc/pbcm/other")); err != nil {
		t.Error("unrelated files must stay")
	}
	last := (*calls)[len(*calls)-1]
	if !strings.HasPrefix(last, "systemd-run") || !strings.Contains(last, "userdel pbcm") || strings.Contains(last, "rm -rf") {
		t.Fatalf("account removal: %s", last)
	}
	_ = out
}

func TestUninstallKeepsAccountWhenServerIsHere(t *testing.T) {
	env, out, calls := testEnv(t)
	write(t, env.path(ServerUnit), "[Unit]")
	write(t, env.path(StateDir+"/runs/1/log"), "log")
	if err := Uninstall(env, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if strings.Contains(c, "userdel") {
			t.Fatal("must not delete the server's own account")
		}
	}
	if _, err := os.Stat(env.path(StateDir)); !os.IsNotExist(err) {
		t.Error("history should be removed without --keep-history")
	}
	if !strings.Contains(out.String(), "account was kept") {
		t.Fatalf("output: %s", out)
	}
}
