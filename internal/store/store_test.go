package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/secret"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	box, err := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "test.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsApplyOnceAndReopen(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	v, err := s.SchemaVersion()
	if err != nil || v < 1 {
		t.Fatalf("version %d, %v", v, err)
	}
	if err := s.SetSetting("general.server_name", "nas"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2 := open(t, dir)
	var name string
	if ok, err := s2.GetSetting("general.server_name", &name); !ok || err != nil || name != "nas" {
		t.Fatalf("setting lost on reopen: %v %v %q", ok, err, name)
	}
	var missing int
	if ok, _ := s2.GetSetting("nope", &missing); ok {
		t.Fatal("missing setting reported as set")
	}
}

func TestAdminSecretsEncryptedAtRest(t *testing.T) {
	s := open(t, t.TempDir())
	if _, err := s.GetAdmin(); !errors.Is(err, ErrNoAdmin) {
		t.Fatalf("want ErrNoAdmin, got %v", err)
	}
	if err := s.CreateAdmin("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAdmin("other", "hash"); err == nil {
		t.Fatal("second admin should fail")
	}
	_, err := s.UpdateAdmin(func(a *Admin) error {
		a.TOTPSecret = "JBSWY3DPEHPK3PXP"
		a.RecoveryCodes = []string{"h1", "h2"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := s.RawAdminTOTP()
	if raw == "" || strings.Contains(raw, "JBSWY3DPEHPK3PXP") {
		t.Fatalf("TOTP secret stored in plain text: %q", raw)
	}
	a, err := s.GetAdmin()
	if err != nil || a.TOTPSecret != "JBSWY3DPEHPK3PXP" || len(a.RecoveryCodes) != 2 {
		t.Fatalf("got %+v, %v", a, err)
	}
	_, err = s.UpdateAdmin(func(a *Admin) error {
		a.Username = "changed"
		return errors.New("stop")
	})
	if err == nil {
		t.Fatal("error from fn should be returned")
	}
	if a, _ := s.GetAdmin(); a.Username != "admin" {
		t.Fatal("failed update should not be saved")
	}
}

func TestSessions(t *testing.T) {
	s := open(t, t.TempDir())
	clock := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return clock }
	for _, tok := range []string{"a", "b", "c"} {
		if err := s.CreateSession(tok, "10.0.0.1", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := s.CheckSession("a", time.Hour); !ok {
		t.Fatal("fresh session should be valid")
	}
	if ok, _ := s.CheckSession("zzz", time.Hour); ok {
		t.Fatal("unknown token accepted")
	}
	clock = clock.Add(30 * time.Minute)
	s.CheckSession("a", time.Hour) // refreshes a
	clock = clock.Add(40 * time.Minute)
	if ok, _ := s.CheckSession("a", time.Hour); !ok {
		t.Fatal("activity should keep a session alive")
	}
	if ok, _ := s.CheckSession("b", time.Hour); ok {
		t.Fatal("idle session should expire")
	}
	if err := s.DeleteOtherSessions("a"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountSessions(); n != 1 {
		t.Fatalf("want 1 session left, got %d", n)
	}
	s.DeleteSession("a")
	if ok, _ := s.CheckSession("a", time.Hour); ok {
		t.Fatal("deleted session accepted")
	}
}
