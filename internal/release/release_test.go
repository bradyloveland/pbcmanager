package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T, id string) (Key, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Key{ID: id, Pub: pub}, priv
}

func TestBuiltinKeysParse(t *testing.T) {
	if len(Keys) == 0 {
		t.Fatal("no release keys built in")
	}
}

type entry struct {
	name, body string
	typ        byte
}

func archive(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: 0o755, Size: int64(len(e.body))}
		if typ != tar.TypeReg {
			h.Size = 0
			h.Linkname = "/etc/passwd"
		}
		tw.WriteHeader(h)
		if typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// signed builds a release archive's entries for files, signed with priv.
func signed(id string, priv ed25519.PrivateKey, version string, files map[string]string) []entry {
	m := &Manifest{Version: version, Arch: "amd64", Files: map[string]string{}}
	top := "pbcm-" + version + "-linux-amd64/"
	es := []entry{{name: top, typ: tar.TypeDir}}
	for n, b := range files {
		m.Files[n] = Hash([]byte(b))
		es = append(es, entry{name: top + n, body: b})
	}
	raw := m.Encode()
	es = append(es, entry{name: top + "MANIFEST", body: string(raw)})
	if priv != nil {
		es = append(es, entry{name: top + "MANIFEST.sig", body: string(Sign(id, priv, raw))})
	}
	return es
}

func TestUnpackSignedRelease(t *testing.T) {
	k, priv := testKey(t, "test")
	dir := filepath.Join(t.TempDir(), "x")
	m, err := Unpack(bytes.NewReader(archive(t, signed("test", priv, "2.1.0", map[string]string{"pbcm": "server", "pbcm-runner": "runner"}))), dir, []Key{k})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "2.1.0" || m.Arch != "amd64" || len(m.Files) != 2 {
		t.Fatalf("manifest: %+v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pbcm-runner")); string(b) != "runner" {
		t.Fatal("files should be extracted")
	}
	if m.Check("pbcm-runner", []byte("runner")) != nil || m.Check("pbcm-runner", []byte("evil")) == nil {
		t.Fatal("Check")
	}
}

func TestUnpackRefusesBadArchives(t *testing.T) {
	k, priv := testKey(t, "test")
	_, otherPriv := testKey(t, "test")
	good := signed("test", priv, "2.1.0", map[string]string{"pbcm": "server"})
	tampered := append([]entry{}, good...)
	for i, e := range tampered {
		if strings.HasSuffix(e.name, "/pbcm") {
			tampered[i].body = "evil"
		}
	}
	cases := map[string]struct {
		entries []entry
		want    string
	}{
		"unsigned":       {signed("test", nil, "2.1.0", map[string]string{"pbcm": "s"}), "isn't signed"},
		"wrong key":      {signed("test", otherPriv, "2.1.0", map[string]string{"pbcm": "s"}), "isn't signed"},
		"changed file":   {tampered, "doesn't match"},
		"extra file":     {append(append([]entry{}, good...), entry{name: "pbcm-2.1.0-linux-amd64/extra", body: "x"}), "isn't part of this release"},
		"path traversal": {append([]entry{{name: "pbcm-2.1.0-linux-amd64/../../etc/cron.d/x", body: "x"}}, good...), "isn't a PBC Manager release"},
		"subfolder":      {append([]entry{{name: "pbcm-2.1.0-linux-amd64/sub/x", body: "x"}}, good...), "isn't a PBC Manager release"},
		"symlink":        {append([]entry{{name: "pbcm-2.1.0-linux-amd64/link", typ: tar.TypeSymlink}}, good...), "isn't a plain file"},
		"two folders":    {append(append([]entry{}, good...), entry{name: "other/x", body: "x"}), "isn't a PBC Manager release"},
		"no manifest":    {[]entry{{name: "pbcm-2.1.0-linux-amd64/pbcm", body: "x"}}, "isn't a PBC Manager release"},
	}
	for name, c := range cases {
		_, err := Unpack(bytes.NewReader(archive(t, c.entries)), filepath.Join(t.TempDir(), "x"), []Key{k})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
	if _, err := Unpack(strings.NewReader("not gzip"), t.TempDir(), []Key{k}); !errors.Is(err, ErrNotRelease) {
		t.Errorf("not gzip: %v", err)
	}
}

func TestKeyRotation(t *testing.T) {
	oldK, _ := testKey(t, "old")
	newK, newPriv := testKey(t, "new")
	m := (&Manifest{Version: "2.2.0", Arch: "arm64", Files: map[string]string{"pbcm": Hash([]byte("x"))}}).Encode()
	sig := Sign("new", newPriv, m)
	if _, err := Verify(m, sig, []Key{oldK}); err == nil {
		t.Fatal("a key that isn't trusted is refused")
	}
	if _, err := Verify(m, sig, []Key{oldK, newK}); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(append(m, '\n'), sig, []Key{newK}); err == nil {
		t.Fatal("a changed manifest is refused")
	}
}

func TestParseKeys(t *testing.T) {
	if _, _, err := ParsePrivateKey("k1:bm9wZQ=="); err == nil {
		t.Fatal("a short seed is refused")
	}
	if _, err := ParseKey("Bad ID:abc"); err == nil {
		t.Fatal("a bad id is refused")
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2.0.0", "2.0.1", -1}, {"2.1.0", "2.0.9", 1}, {"v2.1.0", "2.1.0", 0}, {"10.0.0", "9.9.9", 1},
		{"2.1.0-rc.1", "2.1.0", -1}, {"2.1.0-rc.2", "2.1.0-rc.10", -1}, {"2.1.0-rc.1", "2.1.0-beta.1", 1},
		{"2.0.0-dev", "2.0.0", -1}, {"garbage", "2.0.0", -1}, {"2.0.0", "2.0", 1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "2.0", "2.0.0.0", "02.0.0", "2.0.0-", "a.b.c"} {
		if ParseVersion(bad) != nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}
