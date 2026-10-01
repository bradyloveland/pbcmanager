package server

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/config"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/tlscert"
)

// A network change that could lock the user out is "pending" until it's
// confirmed from the new address. Until then the server answers on both the
// old and the new settings; if nobody confirms in time it goes back.

type pendingNet struct {
	net     config.Network
	cert    *tls.Certificate
	custom  *customPEM // a newly uploaded certificate, saved on confirm
	token   string
	expires time.Time
	timer   *time.Timer
}

type customPEM struct{ cert, key []byte }

const networkKey = "network"

func (s *Server) tlsDir() string { return filepath.Join(s.opts.ConfigDir, "tls") }

func (s *Server) loadNetwork() (config.Network, error) {
	n := config.DefaultNetwork()
	if _, err := s.store.GetSetting(networkKey, &n); err != nil {
		return n, err
	}
	if n.TrustedProxies == nil {
		n.TrustedProxies = []string{}
	}
	return n.Clean()
}

// ensureSelfSigned creates the self-signed certificate if it doesn't exist.
func (s *Server) ensureSelfSigned() error {
	crt := filepath.Join(s.tlsDir(), "self-signed.crt")
	if _, err := os.Stat(crt); err == nil {
		return nil
	}
	return s.writeSelfSigned()
}

func (s *Server) writeSelfSigned() error {
	host, names := tlscert.LocalNames()
	certPEM, keyPEM, err := tlscert.SelfSigned(host, names)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.tlsDir(), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.tlsDir(), "self-signed.key"), keyPEM, 0o600); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.tlsDir(), "self-signed.crt"), certPEM, 0o644)
}

func (s *Server) certPaths(mode string) (string, string) {
	name := "self-signed"
	if mode == config.TLSCustom {
		name = "custom"
	}
	return filepath.Join(s.tlsDir(), name+".crt"), filepath.Join(s.tlsDir(), name+".key")
}

func (s *Server) loadCert(mode string) (*tls.Certificate, error) {
	if mode == config.TLSOff {
		return nil, nil
	}
	if mode == config.TLSSelfSigned {
		if err := s.ensureSelfSigned(); err != nil {
			return nil, err
		}
	}
	crtPath, keyPath := s.certPaths(mode)
	certPEM, err1 := os.ReadFile(crtPath)
	keyPEM, err2 := os.ReadFile(keyPath)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("the %s certificate files are missing from %s", mode, s.tlsDir())
	}
	pair, err := tlscert.Check(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the %s certificate in %s can't be used: %w", mode, s.tlsDir(), err)
	}
	return &pair, nil
}

// getCertificate picks the certificate for a TLS handshake on port: the
// pending one if a pending change uses HTTPS on that port, otherwise the
// active one.
func (s *Server) getCertificate(port int) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if p := s.pending; p != nil && p.net.Port == port && p.cert != nil {
			return p.cert, nil
		}
		if s.activeCert != nil {
			return s.activeCert, nil
		}
		return nil, errors.New("HTTPS is turned off")
	}
}

func (s *Server) tlsAllowed(port int) func() bool {
	return func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if p := s.pending; p != nil && p.net.Port == port && p.cert != nil {
			return true
		}
		return s.active.Port == port && s.activeCert != nil
	}
}

// requiredAddrs lists the addresses to listen on for the active (and any
// pending) settings. A port with an every-interface listener needs nothing
// else, because that listener already covers each specific address.
func (s *Server) requiredAddrs() map[string]int {
	nets := []config.Network{s.active}
	if s.pending != nil {
		nets = append(nets, s.pending.net)
	}
	all := map[int]bool{}
	for _, n := range nets {
		if n.Bind == "" {
			all[n.Port] = true
		}
	}
	out := map[string]int{}
	for _, n := range nets {
		if all[n.Port] && n.Bind != "" {
			continue
		}
		out[n.ListenAddr()] = n.Port
	}
	return out
}

// reconcileLocked opens and closes listeners to match requiredAddrs. On
// failure it puts back the listeners it had. s.mu must be held.
func (s *Server) reconcileLocked() error {
	want := s.requiredAddrs()
	previous := map[string]int{}
	for addr, l := range s.listeners {
		previous[addr] = l.port
	}
	// Close first: moving from 127.0.0.1:8099 to every interface on 8099
	// can't open the new listener while the old one holds the port.
	for addr, l := range s.listeners {
		if _, ok := want[addr]; !ok {
			l.Close()
			delete(s.listeners, addr)
		}
	}
	for addr, port := range want {
		if _, ok := s.listeners[addr]; ok {
			continue
		}
		if err := s.listenLocked(addr, port); err != nil {
			for a, l := range s.listeners {
				if _, keep := previous[a]; !keep {
					l.Close()
					delete(s.listeners, a)
				}
			}
			for a, p := range previous {
				if _, ok := s.listeners[a]; !ok {
					if rerr := s.listenLocked(a, p); rerr != nil {
						slog.Error("could not reopen previous listener", "addr", a, "err", rerr)
					}
				}
			}
			return listenError(addr, err)
		}
	}
	return nil
}

func (s *Server) listenLocked(addr string, port int) error {
	raw, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: s.getCertificate(port),
		NextProtos:     []string{"http/1.1"},
	}
	l := newSniffListener(raw, port, cfg, s.tlsAllowed(port))
	s.listeners[addr] = l
	go func() {
		if err := s.http.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			slog.Debug("listener stopped", "addr", addr, "err", err)
		}
	}()
	slog.Info("listening", "addr", raw.Addr().String())
	return nil
}

func listenError(addr string, err error) error {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return badRequest("Can't use %s: another program is already using that port.", addr)
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return badRequest("Can't use %s: that address doesn't belong to this machine.", addr)
	case errors.Is(err, syscall.EACCES):
		return badRequest("Can't use %s: this service isn't allowed to open that port.", addr)
	}
	return badRequest("Can't listen on %s: %s", addr, err)
}

// ----------------------------------------------------------- request match

// matches reports whether a request arrived in a way that fits n: on n's port
// and address, with or without TLS as n says, and under n's base path.
func matches(r *http.Request, n config.Network) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return false
	}
	host, portStr, err := net.SplitHostPort(local.String())
	if err != nil {
		return false
	}
	if port, _ := strconv.Atoi(portStr); port != n.Port {
		return false
	}
	if n.Bind != "" {
		want, got := net.ParseIP(n.Bind), net.ParseIP(strings.Split(host, "%")[0])
		if want == nil || got == nil || !want.Equal(got) {
			return false
		}
	}
	if (r.TLS != nil) != n.UsesTLS() {
		return false
	}
	return underBase(r.URL.Path, n.BasePath)
}

func underBase(path, base string) bool {
	return base == "" || path == base || strings.HasPrefix(path, base+"/")
}

// profileFor returns the settings a request arrived under. If it fits both
// the active and the pending settings, the one with the longer base path wins
// (so /old/... still reaches the old settings when the new one has none), and
// on a tie the pending one does.
func (s *Server) profileFor(r *http.Request) (config.Network, bool, bool) {
	s.mu.Lock()
	active, pending := s.active, s.pending
	s.mu.Unlock()
	inActive := matches(r, active)
	if pending != nil && matches(r, pending.net) {
		if !inActive || len(pending.net.BasePath) >= len(active.BasePath) {
			return pending.net, true, true
		}
	}
	if inActive {
		return active, false, true
	}
	return config.Network{}, false, false
}

// --------------------------------------------------------- change / confirm

type networkChange struct {
	config.Network
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

func (s *Server) changeNetwork(r *http.Request, in networkChange) (map[string]any, error) {
	next, err := in.Network.Clean()
	if err != nil {
		return nil, err
	}
	var upload *customPEM
	var uploaded *tls.Certificate
	if strings.TrimSpace(in.CertPEM) != "" || strings.TrimSpace(in.KeyPEM) != "" {
		if strings.TrimSpace(in.CertPEM) == "" || strings.TrimSpace(in.KeyPEM) == "" {
			return nil, badRequest("Paste both the certificate and its private key.")
		}
		pair, err := tlscert.Check([]byte(in.CertPEM), []byte(in.KeyPEM))
		if err != nil {
			return nil, badRequest("Can't use that certificate: %s.", err)
		}
		upload, uploaded = &customPEM{[]byte(in.CertPEM), []byte(in.KeyPEM)}, &pair
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		return nil, conflict("Another network change is waiting to be confirmed. Keep it or undo it first.")
	}
	var cert *tls.Certificate
	switch {
	case next.TLS == config.TLSCustom && uploaded != nil:
		cert = uploaded
	case next.TLS == config.TLSOff:
	default:
		if cert, err = s.loadCert(next.TLS); err != nil {
			if next.TLS == config.TLSCustom {
				return nil, badRequest("Paste a certificate and private key to use your own certificate.")
			}
			return nil, err
		}
	}
	certChanged := next.TLS == config.TLSCustom && uploaded != nil
	if !s.active.NeedsConfirm(next) && !certChanged {
		if err := s.store.SetSetting(networkKey, next); err != nil {
			return nil, err
		}
		s.active = next
		slog.Info("network settings changed", "trusted_proxies", next.TrustedProxies)
		return map[string]any{"applied": true}, nil
	}

	p := &pendingNet{net: next, cert: cert, custom: upload, token: randomToken(18),
		expires: time.Now().Add(s.opts.ConfirmWindow)}
	s.pending = p
	if err := s.reconcileLocked(); err != nil {
		s.pending = nil
		return nil, err
	}
	p.timer = time.AfterFunc(s.opts.ConfirmWindow, func() { s.revertNetwork(p.token, "not confirmed in time") })
	slog.Info("trying new network settings", "listen", next.ListenAddr(), "tls", next.TLS, "base_path", next.BasePath)
	return map[string]any{"pending": s.pendingInfoLocked(r, true)}, nil
}

func (s *Server) confirmNetwork(r *http.Request, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending
	if p == nil || !constantEqual(p.token, token) {
		return conflict("There's no network change waiting to be confirmed. It may have been undone because it wasn't confirmed in time.")
	}
	if !info(r).pending {
		return conflict("Open the new address to confirm the change. This page was loaded from the old one.")
	}
	if p.custom != nil {
		crt, key := s.certPaths(config.TLSCustom)
		if err := os.MkdirAll(s.tlsDir(), 0o700); err != nil {
			return err
		}
		if err := writeFileAtomic(key, p.custom.key, 0o600); err != nil {
			return err
		}
		if err := writeFileAtomic(crt, p.custom.cert, 0o644); err != nil {
			return err
		}
	}
	if err := s.store.SetSetting(networkKey, p.net); err != nil {
		return err
	}
	p.timer.Stop()
	s.active, s.activeCert, s.pending = p.net, p.cert, nil
	if err := s.reconcileLocked(); err != nil {
		slog.Error("closing old listeners", "err", err)
	}
	slog.Info("network settings confirmed", "listen", p.net.ListenAddr(), "tls", p.net.TLS, "base_path", p.net.BasePath)
	return nil
}

func (s *Server) revertNetwork(token, why string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending
	if p == nil || (token != "" && p.token != token) {
		return false
	}
	p.timer.Stop()
	s.pending = nil
	if err := s.reconcileLocked(); err != nil {
		slog.Error("going back to previous network settings", "err", err)
	}
	slog.Info("network change undone", "reason", why)
	return true
}

// pendingInfoLocked describes the pending change, including whether r came
// in through it. The new address is only included for signed-in users.
func (s *Server) pendingInfoLocked(r *http.Request, signedIn bool) map[string]any {
	p := s.pending
	if p == nil {
		return nil
	}
	info := map[string]any{
		"expires_in": max(0, int(time.Until(p.expires).Seconds())),
		"here":       info(r).pending,
	}
	if signedIn {
		info["url"] = newURL(r, p.net)
		info["token"] = p.token
		info["network"] = p.net
	}
	return info
}

// newURL guesses the address of the UI under n, from the host the browser
// used now.
func newURL(r *http.Request, n config.Network) string {
	scheme := "http"
	if n.UsesTLS() {
		scheme = "https"
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if n.Bind != "" {
		host = n.Bind
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if !(scheme == "https" && n.Port == 443) && !(scheme == "http" && n.Port == 80) {
		host += ":" + strconv.Itoa(n.Port)
	}
	return scheme + "://" + host + n.BasePath + "/"
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
