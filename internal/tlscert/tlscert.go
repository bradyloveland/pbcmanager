// Package tlscert creates the self-signed certificate a fresh install uses,
// checks uploaded certificates, and describes certificates for the UI.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

// SelfSigned creates a certificate valid for ten years for the given host
// names and addresses, plus localhost.
func SelfSigned(commonName string, hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"PBC Web Manager (self-signed)"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	seen := map[string]bool{}
	for _, h := range append([]string{commonName, "localhost", "127.0.0.1", "::1"}, hosts...) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// LocalNames returns this machine's hostname and its non-loopback addresses,
// for the self-signed certificate.
func LocalNames() (string, []string) {
	host, _ := os.Hostname()
	host = strings.TrimSuffix(host, ".")
	var names []string
	if short, _, ok := strings.Cut(host, "."); ok && short != "" {
		names = append(names, short)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
				names = append(names, ipn.IP.String())
			}
		}
	}
	if host == "" {
		host = "localhost"
	}
	return host, names
}

// Check validates a PEM certificate chain and private key pair.
func Check(certPEM, keyPEM []byte) (tls.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "private key does not match"):
			return pair, errors.New("the private key doesn't belong to this certificate")
		case strings.Contains(msg, "failed to find any PEM data in certificate"):
			return pair, errors.New("the certificate isn't in PEM format (it should start with -----BEGIN CERTIFICATE-----)")
		case strings.Contains(msg, "failed to find any PEM data in key"), strings.Contains(msg, "failed to find PEM block with type ending in \"PRIVATE KEY\""):
			return pair, errors.New("the private key isn't in PEM format (it should start with -----BEGIN … PRIVATE KEY-----)")
		}
		return pair, fmt.Errorf("the certificate and key can't be used: %s", msg)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return pair, fmt.Errorf("the certificate can't be read: %s", err)
	}
	if time.Now().After(leaf.NotAfter) {
		return pair, fmt.Errorf("the certificate expired on %s", leaf.NotAfter.Format("2 Jan 2006"))
	}
	pair.Leaf = leaf
	return pair, nil
}

// Info describes a certificate for the Settings page.
type Info struct {
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	Names       []string `json:"names"`
	NotBefore   int64    `json:"not_before"`
	NotAfter    int64    `json:"not_after"`
	Fingerprint string   `json:"fingerprint"`
	SelfSigned  bool     `json:"self_signed"`
}

// Describe returns details of the first certificate in certPEM.
func Describe(certPEM []byte) (*Info, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("no certificate")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(c.Raw)
	hexes := make([]string, len(sum))
	for i, b := range sum {
		hexes[i] = fmt.Sprintf("%02X", b)
	}
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	sort.Strings(names)
	return &Info{
		Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName, Names: names,
		NotBefore: c.NotBefore.Unix(), NotAfter: c.NotAfter.Unix(),
		Fingerprint: strings.Join(hexes, ":"),
		SelfSigned:  c.Subject.String() == c.Issuer.String(),
	}, nil
}
