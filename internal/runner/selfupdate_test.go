package runner

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/release"
)

func TestSelfUpdateChecksTheSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	saved := release.Keys
	release.Keys = []release.Key{{ID: "test", Pub: pub}}
	t.Cleanup(func() { release.Keys = saved })

	je := newJobEnv(t)
	os.MkdirAll(je.path("/usr/local/lib/pbcm"), 0o755)
	os.WriteFile(je.path(Path), []byte("old runner"), 0o755)
	newRunner := []byte("new runner")
	m := (&release.Manifest{Version: "2.1.0", Arch: "amd64", Files: map[string]string{"pbcm-runner": release.Hash(newRunner)}}).Encode()
	req := func(sig []byte, runner []byte) *bytes.Reader {
		raw, _ := json.Marshal(UpdateRequest{Manifest: string(m), Signature: string(sig), Runner: runner})
		return bytes.NewReader(raw)
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	for name, r := range map[string]*bytes.Reader{
		"unsigned":       req(nil, newRunner),
		"other key":      req(release.Sign("test", otherPriv, m), newRunner),
		"changed runner": req(release.Sign("test", priv, m), []byte("evil runner")),
	} {
		if err := SelfUpdate(je.Env, r); err == nil {
			t.Errorf("%s: should be refused", name)
		}
		if b, _ := os.ReadFile(je.path(Path)); string(b) != "old runner" {
			t.Fatalf("%s: runner was replaced", name)
		}
	}
	je.out.Reset()
	if err := SelfUpdate(je.Env, req(release.Sign("test", priv, m), newRunner)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(je.path(Path)); string(b) != "new runner" || !strings.Contains(je.out.String(), `"version":"2.1.0"`) {
		t.Fatalf("after update: %q %s", b, je.out.String())
	}
	je.out.Reset()
	StatusSince(je.Env, 0)
	var st bundle.Status
	json.Unmarshal(je.out.Bytes(), &st)
	if st.Runner != release.Hash(newRunner) || st.RunnerVersion == "" {
		t.Fatalf("status reports the runner's hash and version: %+v", st)
	}
}
