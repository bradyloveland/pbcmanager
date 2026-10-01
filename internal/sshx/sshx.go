// Package sshx is the server's SSH client: its own key, probing a client's
// host key, connecting with a pinned host key, and running commands.
package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// Identity is the server's own SSH key.
type Identity struct {
	Signer ssh.Signer
	// AuthorizedKey is the public key as one authorized_keys line (no newline).
	AuthorizedKey string
	Fingerprint   string
}

// LoadOrCreateIdentity reads the ed25519 key at path, creating it if needed.
func LoadOrCreateIdentity(path, comment string) (*Identity, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, comment)
		if err != nil {
			return nil, err
		}
		raw = pem.EncodeToMemory(block)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("the server's SSH key in %s can't be read: %w", path, err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + comment
	return &Identity{Signer: signer, AuthorizedKey: line, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}, nil
}

// FormatKey returns a public key in authorized_keys form ("type base64").
func FormatKey(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// ParseKey parses a key stored with FormatKey.
func ParseKey(s string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s))
	return k, err
}

// Fingerprint is the SHA-256 fingerprint OpenSSH shows ("SHA256:…").
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

// HostKeyChangedError means the client presented a different host key from
// the pinned one.
type HostKeyChangedError struct{ Offered ssh.PublicKey }

func (e *HostKeyChangedError) Error() string {
	return "the client's SSH host key has changed (it now shows " + Fingerprint(e.Offered) + ")"
}

// FriendlyError is shown to the user as-is.
type FriendlyError struct{ Message string }

func (e *FriendlyError) Error() string { return e.Message }

var errProbe = errors.New("probe done")

func dialTCP(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, explainDial(addr, err)
	}
	return conn, nil
}

func explainDial(addr string, err error) error {
	host, port, _ := net.SplitHostPort(addr)
	var dnsErr *net.DNSError
	var nerr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return &FriendlyError{fmt.Sprintf("Can't find %s. Check the name, or use its IP address.", host)}
	case errors.Is(err, syscall.ECONNREFUSED):
		return &FriendlyError{fmt.Sprintf("%s refused the connection on port %s. Check that SSH is running and the port is right.", host, port)}
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return &FriendlyError{fmt.Sprintf("%s can't be reached from this server. Check the address and your network or VPN.", host)}
	case errors.As(err, &nerr) && nerr.Timeout():
		return &FriendlyError{fmt.Sprintf("%s didn't answer on port %s. Check the address and that the machine is on and reachable (is the VPN up?).", host, port)}
	}
	return &FriendlyError{fmt.Sprintf("Can't connect to %s: %s", addr, err)}
}

// Probe connects just far enough to learn the host key, without logging in.
func Probe(ctx context.Context, addr string, timeout time.Duration) (ssh.PublicKey, error) {
	conn, err := dialTCP(ctx, addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	var key ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "pbcm-probe",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			key = k
			return errProbe
		},
		Timeout: timeout,
	}
	_, _, _, err = ssh.NewClientConn(conn, addr, cfg)
	if key != nil {
		return key, nil
	}
	return nil, &FriendlyError{fmt.Sprintf("%s answered, but not as an SSH server: %s", addr, err)}
}

// Target says how to reach and sign in to a client.
type Target struct {
	Addr    string
	HostKey ssh.PublicKey
	User    string
	Auth    []ssh.AuthMethod
	Timeout time.Duration
}

// PasswordAuth tries a password both ways servers ask for one.
func PasswordAuth(password string) []ssh.AuthMethod {
	return []ssh.AuthMethod{
		ssh.Password(password),
		ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = password
			}
			return answers, nil
		}),
	}
}

// KeyAuth signs in with the server's key.
func KeyAuth(id *Identity) []ssh.AuthMethod {
	return []ssh.AuthMethod{ssh.PublicKeys(id.Signer)}
}

func hostKeyAlgorithms(k ssh.PublicKey) []string {
	switch k.Type() {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	default:
		return []string{k.Type()}
	}
}

// Dial connects and signs in, refusing any host key but the pinned one.
func Dial(ctx context.Context, t Target) (*ssh.Client, error) {
	if t.Timeout == 0 {
		t.Timeout = 15 * time.Second
	}
	conn, err := dialTCP(ctx, t.Addr, t.Timeout)
	if err != nil {
		return nil, err
	}
	var changed *HostKeyChangedError
	cfg := &ssh.ClientConfig{
		User: t.User,
		Auth: t.Auth,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if bytes.Equal(k.Marshal(), t.HostKey.Marshal()) {
				return nil
			}
			changed = &HostKeyChangedError{Offered: k}
			return changed
		},
		HostKeyAlgorithms: hostKeyAlgorithms(t.HostKey),
		Timeout:           t.Timeout,
	}
	_ = conn.SetDeadline(time.Now().Add(t.Timeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, t.Addr, cfg)
	if err != nil {
		conn.Close()
		if changed != nil {
			return nil, changed
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, &FriendlyError{fmt.Sprintf("%s didn't accept the sign-in for user %q.", t.Addr, t.User)}
		}
		if strings.Contains(err.Error(), "no common algorithm for host key") {
			// The client no longer offers the pinned key type: treat as changed.
			return nil, &FriendlyError{"The client no longer offers the SSH host key that was trusted. Check the client, then use Repair to trust its current key."}
		}
		return nil, &FriendlyError{fmt.Sprintf("SSH sign-in to %s failed: %s", t.Addr, err)}
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// Run runs cmd in a new session. It returns the exit code; err is only set
// when the command couldn't be run at all.
func Run(c *ssh.Client, cmd string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	sess, err := c.NewSession()
	if err != nil {
		return -1, err
	}
	defer sess.Close()
	sess.Stdin, sess.Stdout, sess.Stderr = stdin, stdout, stderr
	err = sess.Run(cmd)
	var exit *ssh.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitStatus(), nil
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return -1, errors.New("the connection dropped before the command finished")
	}
	return -1, err
}

// Output runs cmd and collects its output.
func Output(c *ssh.Client, cmd string, stdin io.Reader) (stdout, stderr string, code int, err error) {
	var o, e bytes.Buffer
	code, err = Run(c, cmd, stdin, &o, &e)
	return o.String(), e.String(), code, err
}

// Quote quotes s for a POSIX shell.
func Quote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == '=' || r == ':' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes and joins words into one command line.
func Join(words ...string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = Quote(w)
	}
	return strings.Join(q, " ")
}
