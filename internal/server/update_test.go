package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/release"
	"github.com/bradyloveland/pbcmanager/internal/update"
	"github.com/bradyloveland/pbcmanager/internal/version"
)

func signedRelease(t *testing.T, priv ed25519.PrivateKey, v string) []byte {
	t.Helper()
	files := map[string]string{"pbcm": "pbcm " + v, "pbcm-runner": "runner " + v,
		"CHANGELOG.md": "## [" + v + "] - 2026-10-01\n- Faster.\n"}
	m := &release.Manifest{Version: v, Arch: runtime.GOARCH, Files: map[string]string{}}
	for n, b := range files {
		m.Files[n] = release.Hash([]byte(b))
	}
	raw := m.Encode()
	files["MANIFEST"], files["MANIFEST.sig"] = string(raw), string(release.Sign("test", priv, raw))
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := fmt.Sprintf("pbcm-%s-linux-%s/", v, runtime.GOARCH)
	for n, b := range files {
		tw.WriteHeader(&tar.Header{Name: top + n, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b))})
		tw.Write([]byte(b))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestUpdateFromGitHubAndUpload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	next := "2.99.0"
	file := signedRelease(t, priv, next)
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v%s","body":"Notes from GitHub","assets":[{"name":"pbcm-%s-linux-%s.tar.gz","browser_download_url":"%s/file"}]}]`,
				next, next, runtime.GOARCH, gh.URL)
		case "/file":
			w.Write(file)
		}
	}))
	defer gh.Close()
	app := t.TempDir()
	os.WriteFile(filepath.Join(app, "pbcm"), []byte("pbcm old"), 0o755)
	supervised := true
	e := newEnvOpts(t, nil, true, func(o *Options) {
		o.AppDir, o.Executable, o.UpdateAPI = app, filepath.Join(app, "pbcm"), gh.URL
		o.UpdateKeys = []release.Key{{ID: "test", Pub: pub}}
		o.Supervised = func() bool { return supervised }
	})
	c := e.client()
	expect(t, c.post("/api/update/check", nil), 401, "")
	c.login()

	r := c.get("/api/update")
	expect(t, r, 200, `"version":"`+version.Version+`"`)
	if r.data["cant_update"] != "" {
		t.Fatalf("this test server can update: %v", r.data["cant_update"])
	}
	r = c.post("/api/update/check", nil)
	expect(t, r, 200, `"newer":true`)
	expect(t, r, 200, "Notes from GitHub")

	// Uploads must be signed by a trusted key.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	expect(t, c.do("POST", "/api/update/upload", signedRelease(t, other, next), nil), 400, "isn't signed")
	expect(t, c.do("POST", "/api/update/upload", []byte("junk"), nil), 400, "isn't a PBC Manager release")
	r = c.do("POST", "/api/update/upload", file, nil)
	expect(t, r, 200, `"version":"2.99.0"`)
	expect(t, r, 200, "Faster.")
	expect(t, c.post("/api/update/discard", nil), 200, "")

	r = c.post("/api/update/download", map[string]string{"version": next})
	expect(t, r, 200, "Notes from GitHub")
	supervised = false
	expect(t, c.post("/api/update/install", nil), 400, "system service")
	supervised = true
	expect(t, c.post("/api/update/install", nil), 200, `"restarting":true`)
	select {
	case <-e.srv.Restarting():
	case <-time.After(3 * time.Second):
		t.Fatal("the server should stop so systemd starts the new version")
	}
	if b, _ := os.ReadFile(filepath.Join(app, "pbcm")); string(b) != "pbcm "+next {
		t.Fatalf("installed: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(app, "pbcm.prev")); string(b) != "pbcm old" {
		t.Fatal("the old version is kept")
	}
	if st, _ := update.ReadState(e.dir); st == nil || st.Phase != update.Installed || st.To != next {
		t.Fatalf("state: %+v", st)
	}
	if !strings.Contains(c.get("/api/update").body, `"phase":"installed"`) {
		t.Fatal("the update page shows the update")
	}
	expect(t, c.post("/api/update/rollback", nil), 400, "no earlier version")
}
