package tlscert

import (
	"strings"
	"testing"
)

func TestSelfSignedRoundTrip(t *testing.T) {
	certPEM, keyPEM, err := SelfSigned("nas", []string{"192.0.2.10", "nas.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Check(certPEM, keyPEM); err != nil {
		t.Fatalf("generated pair rejected: %v", err)
	}
	info, err := Describe(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(info.Names, ",")
	for _, want := range []string{"nas", "localhost", "192.0.2.10", "nas.example.net", "127.0.0.1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("certificate names %v missing %s", info.Names, want)
		}
	}
	if !info.SelfSigned || info.Subject != "nas" || len(info.Fingerprint) != 95 {
		t.Fatalf("info: %+v", info)
	}
}

func TestCheckRejectsMismatchedAndJunk(t *testing.T) {
	c1, k1, _ := SelfSigned("a", nil)
	_, k2, _ := SelfSigned("b", nil)
	if _, err := Check(c1, k2); err == nil || !strings.Contains(err.Error(), "doesn't belong") {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := Check([]byte("junk"), k1); err == nil || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("junk cert: %v", err)
	}
	if _, err := Check(c1, []byte("junk")); err == nil || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("junk key: %v", err)
	}
}
