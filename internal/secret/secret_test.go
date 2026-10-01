package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	box, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != 32 {
		t.Fatalf("key file: %v %v", info, err)
	}
	sealed := box.Seal("token-secret")
	if !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, "token-secret") {
		t.Fatalf("sealed value looks wrong: %q", sealed)
	}
	if sealed == box.Seal("token-secret") {
		t.Fatal("nonces should differ")
	}
	again, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := again.Open(sealed)
	if err != nil || plain != "token-secret" {
		t.Fatalf("got %q, %v", plain, err)
	}
	if box.Seal("") != "" {
		t.Fatal("empty stays empty")
	}
}

func TestWrongKeyOrTamperingFails(t *testing.T) {
	a, _ := New(make([]byte, 32))
	key := make([]byte, 32)
	key[0] = 1
	b, _ := New(key)
	sealed := a.Seal("x")
	if _, err := b.Open(sealed); err == nil {
		t.Fatal("wrong key should fail")
	}
	if _, err := a.Open(sealed[:len(sealed)-2] + "AA"); err == nil {
		t.Fatal("tampered value should fail")
	}
	if _, err := a.Open("plain"); err == nil {
		t.Fatal("unknown format should fail")
	}
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("short key should fail")
	}
}
