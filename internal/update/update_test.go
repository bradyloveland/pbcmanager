package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/release"
	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

type env struct {
	u    *Updater
	priv ed25519.PrivateKey
	key  release.Key
}

func newEnv(t *testing.T, version string) *env {
	t.Helper()
	root := t.TempDir()
	app, data := filepath.Join(root, "opt"), filepath.Join(root, "data")
	os.MkdirAll(app, 0o755)
	os.MkdirAll(data, 0o700)
	os.WriteFile(filepath.Join(app, "pbcm"), []byte("pbcm "+version), 0o755)
	os.WriteFile(filepath.Join(app, "pbcm-runner"), []byte("runner "+version), 0o755)
	box, _ := secret.New(make([]byte, 32))
	st, err := store.Open(filepath.Join(data, "pbcm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	k := release.Key{ID: "test", Pub: pub}
	u := &Updater{Files: Files{AppDir: app, DataDir: data}, Store: st, Version: version, Keys: []release.Key{k},
		Arch: "amd64", Supervised: func() bool { return true }, Executable: filepath.Join(app, "pbcm")}
	return &env{u: u, priv: priv, key: k}
}

// archive builds a signed release file.
func (e *env) archive(t *testing.T, version, arch string, signed bool) []byte {
	t.Helper()
	files := map[string]string{"pbcm": "pbcm " + version, "pbcm-runner": "runner " + version, "install.sh": "#!/bin/sh\n",
		"CHANGELOG.md": "## [Unreleased]\n\n## [" + version + "] - 2026-10-01\n\n### Added\n- Something new.\n\n## [2.0.0] - 2026-09-01\n- Old.\n"}
	m := &release.Manifest{Version: version, Arch: arch, Files: map[string]string{}}
	for n, b := range files {
		m.Files[n] = release.Hash([]byte(b))
	}
	raw := m.Encode()
	files["MANIFEST"] = string(raw)
	if signed {
		files["MANIFEST.sig"] = string(release.Sign("test", e.priv, raw))
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := fmt.Sprintf("pbcm-%s-linux-%s/", version, arch)
	tw.WriteHeader(&tar.Header{Name: top, Typeflag: tar.TypeDir, Mode: 0o755})
	for n, b := range files {
		tw.WriteHeader(&tar.Header{Name: top + n, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b))})
		tw.Write([]byte(b))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestUploadInstallConfirm(t *testing.T) {
	e := newEnv(t, "2.0.0")
	e.u.Store.SetSetting("marker", "before")

	for name, c := range map[string]struct {
		file []byte
		want string
	}{
		"unsigned":   {e.archive(t, "2.1.0", "amd64", false), "isn't signed"},
		"wrong arch": {e.archive(t, "2.1.0", "arm64", true), "for arm64 servers"},
		"not a tar":  {[]byte("hello"), "isn't a PBC Manager release"},
	} {
		if _, err := e.u.Upload(bytes.NewReader(c.file)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if e.u.StagedRelease() != nil {
		t.Fatal("nothing should be staged")
	}

	old := e.archive(t, "1.9.9", "amd64", true)
	if _, err := e.u.Upload(bytes.NewReader(old)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.u.Install(); err == nil || !strings.Contains(err.Error(), "isn't newer") {
		t.Fatalf("downgrade: %v", err)
	}

	st, err := e.u.Upload(bytes.NewReader(e.archive(t, "2.1.0", "amd64", true)))
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "2.1.0" || !strings.Contains(st.Notes, "Something new") || strings.Contains(st.Notes, "Old.") {
		t.Fatalf("staged: %+v", st)
	}
	to, err := e.u.Install()
	if err != nil || to != "2.1.0" {
		t.Fatalf("install: %v %v", to, err)
	}
	app := e.u.AppDir
	if read(t, app+"/pbcm") != "pbcm 2.1.0" || read(t, app+"/pbcm.prev") != "pbcm 2.0.0" || read(t, app+"/pbcm-runner") != "runner 2.1.0" {
		t.Fatal("files should be swapped, keeping the old ones")
	}
	s, _ := ReadState(e.u.DataDir)
	if s.Phase != Installed || s.From != "2.0.0" || s.To != "2.1.0" {
		t.Fatalf("state: %+v", s)
	}
	if _, err := os.Stat(s.Backup); err != nil {
		t.Fatal("the database should be backed up")
	}

	// The new version starts and stays up.
	e.u.Store.SetSetting("marker", "after")
	nu := e.u.as("2.1.0")
	nu.Started(50 * time.Millisecond)
	if s, _ := ReadState(e.u.DataDir); s.Phase != Confirming {
		t.Fatalf("starting: %+v", s)
	}
	time.Sleep(200 * time.Millisecond)
	if s, _ := ReadState(e.u.DataDir); s.Phase != Done {
		t.Fatalf("after settling: %+v", s)
	}
	if nu.CanRollBack() != "2.0.0" {
		t.Fatal("going back should be offered")
	}

	// Going back by hand restores the files and the database.
	e.u.Store.Close()
	if err := nu.Rollback("Rolled back by hand.", true); err != nil {
		t.Fatal(err)
	}
	if read(t, app+"/pbcm") != "pbcm 2.0.0" || read(t, app+"/pbcm-runner") != "runner 2.0.0" {
		t.Fatal("old files should be back")
	}
	s, _ = ReadState(e.u.DataDir)
	if s.Phase != RolledBack || s.From != "2.1.0" || s.To != "2.0.0" || !s.Manual {
		t.Fatalf("state after rollback: %+v", s)
	}
	box, _ := secret.New(make([]byte, 32))
	st2, err := store.Open(filepath.Join(e.u.DataDir, "pbcm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var marker string
	st2.GetSetting("marker", &marker)
	if marker != "before" {
		t.Fatalf("database should be from before the update: %q", marker)
	}
	if err := nu.Rollback("again", true); err == nil {
		t.Fatal("nothing left to undo")
	}
}

func TestFailingVersionIsRolledBackAfterThreeTries(t *testing.T) {
	e := newEnv(t, "2.0.0")
	e.u.Upload(bytes.NewReader(e.archive(t, "2.1.0", "amd64", true)))
	if _, err := e.u.Install(); err != nil {
		t.Fatal(err)
	}
	e.u.Store.Close()
	// The old version exiting cleanly for the update isn't a failure.
	if rolled, err := e.u.AfterStop("success"); rolled || err != nil {
		t.Fatalf("clean stop: %v %v", rolled, err)
	}
	RecordStartFailure(e.u.DataDir, "2.1.0", fmt.Errorf("database migration 0006 failed"))
	for i := 1; i < MaxFailures; i++ {
		if rolled, err := e.u.AfterStop("exit-code"); rolled || err != nil {
			t.Fatalf("failure %d: %v %v", i, rolled, err)
		}
	}
	if read(t, e.u.AppDir+"/pbcm") != "pbcm 2.1.0" {
		t.Fatal("not rolled back yet")
	}
	rolled, err := e.u.AfterStop("exit-code")
	if !rolled || err != nil {
		t.Fatalf("third failure: %v %v", rolled, err)
	}
	if read(t, e.u.AppDir+"/pbcm") != "pbcm 2.0.0" {
		t.Fatal("the previous version should be back")
	}
	s, _ := ReadState(e.u.DataDir)
	if s.Phase != RolledBack || s.Manual || !strings.Contains(s.Reason, "didn't start: database migration 0006 failed") {
		t.Fatalf("state: %+v", s)
	}
	// Once rolled back, later failures (of anything) are left to systemd.
	if rolled, _ := e.u.AfterStop("exit-code"); rolled {
		t.Fatal("nothing more to undo")
	}
}

func TestCrashesAfterConfirmingDontRollBack(t *testing.T) {
	e := newEnv(t, "2.0.0")
	e.u.Upload(bytes.NewReader(e.archive(t, "2.1.0", "amd64", true)))
	e.u.Install()
	nu := e.u.as("2.1.0")
	nu.Started(time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 5; i++ {
		if rolled, _ := e.u.AfterStop("signal"); rolled {
			t.Fatal("a confirmed version isn't rolled back on its own")
		}
	}
}

func TestCantUpdateOutsideTheService(t *testing.T) {
	e := newEnv(t, "2.0.0")
	if why := e.u.CantUpdate(); why != "" {
		t.Fatalf("installed copy: %s", why)
	}
	e.u.Supervised = func() bool { return false }
	if why := e.u.CantUpdate(); !strings.Contains(why, "system service") {
		t.Fatalf("not supervised: %s", why)
	}
	e.u.Supervised = func() bool { return true }
	e.u.Executable = "/usr/local/go/bin/pbcm"
	if why := e.u.CantUpdate(); !strings.Contains(why, "isn't the installed one") {
		t.Fatalf("other copy: %s", why)
	}
}

func TestCheckAndDownload(t *testing.T) {
	e := newEnv(t, "2.0.0")
	file := e.archive(t, "2.1.0", "amd64", true)
	var srv *httptest.Server
	status := http.StatusOK
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			fmt.Fprintf(w, `[
			{"tag_name":"v2.2.0-rc.1","prerelease":true,"assets":[{"name":"pbcm-2.2.0-rc.1-linux-amd64.tar.gz","browser_download_url":"%[1]s/rc"}]},
			{"tag_name":"v2.1.0","body":"Release notes","published_at":"2026-10-01T10:00:00Z","assets":[
			  {"name":"pbcm-2.1.0-linux-arm64.tar.gz","browser_download_url":"%[1]s/arm"},
			  {"name":"pbcm-2.1.0-linux-amd64.tar.gz","browser_download_url":"%[1]s/amd","size":1234}]},
			{"tag_name":"v2.0.1","assets":[{"name":"pbcm-2.0.1-linux-amd64.tar.gz","browser_download_url":"%[1]s/old"}]},
			{"tag_name":"v1.2.0","assets":[]},
			{"tag_name":"v2.3.0","draft":true}]`, srv.URL)
		case "/amd":
			w.Write(file)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	e.u.API = srv.URL
	c := e.u.CheckNow(context.Background())
	if c.Error != "" || c.Latest == nil || c.Latest.Version != "2.1.0" || c.Latest.URL != srv.URL+"/amd" || c.Latest.Notes != "Release notes" {
		t.Fatalf("check: %+v %+v", c, c.Latest)
	}
	if e.u.LastCheck().Latest.Version != "2.1.0" {
		t.Fatal("the check is saved")
	}
	if _, err := e.u.Download(context.Background(), "2.0.9"); err == nil {
		t.Fatal("a version other than the latest is refused")
	}
	st, err := e.u.Download(context.Background(), "2.1.0")
	if err != nil || st.Version != "2.1.0" || st.Notes != "Release notes" {
		t.Fatalf("download: %+v %v", st, err)
	}
	// A server on a pre-release is offered pre-releases.
	pre := newEnv(t, "2.1.0-rc.1")
	pre.u.API = srv.URL
	if c := pre.u.CheckNow(context.Background()); c.Latest == nil || c.Latest.Version != "2.2.0-rc.1" {
		t.Fatalf("pre-release: %+v", c)
	}
	status = http.StatusForbidden
	if c := e.u.CheckNow(context.Background()); !strings.HasPrefix(c.Error, "GitHub limited") {
		t.Fatalf("rate limit: %+v", c)
	}
}

// as is the same server after restarting as version v.
func (u *Updater) as(v string) *Updater {
	return &Updater{Files: u.Files, Store: u.Store, Version: v, Keys: u.Keys, API: u.API, Arch: u.Arch,
		Supervised: u.Supervised, Executable: u.Executable}
}
