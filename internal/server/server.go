// Package server is the web UI and JSON API.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/alerts"
	"github.com/bradyloveland/pbcmanager/internal/auth"
	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/clients"
	"github.com/bradyloveland/pbcmanager/internal/config"
	"github.com/bradyloveland/pbcmanager/internal/sshx"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

const (
	sessionCookie = "pbcm_session"
	csrfHeader    = "X-PBCM"
	maxBody       = 1 << 20
)

// Options configures a Server.
type Options struct {
	ConfigDir string
	// DataDir holds run logs collected from clients.
	DataDir string
	Store   *store.Store
	// FailDelay is how long each failed sign-in waits (1 s by default).
	FailDelay time.Duration
	// ConfirmWindow is how long a network change waits to be confirmed.
	ConfirmWindow time.Duration
	// Hostname overrides os.Hostname (tests).
	Hostname string
	// RunnerPath is the pbcm-runner (linux/amd64) sent to clients. By
	// default it's next to this program.
	RunnerPath string
}

// Server serves the UI and API.
type Server struct {
	opts     Options
	store    *store.Store
	throttle *auth.Throttle
	mux      *http.ServeMux
	http     *http.Server
	clients  *clients.Manager
	pbs      *backups.PBS
	notifier *alerts.Notifier
	pollStop context.CancelFunc

	mu         sync.Mutex
	active     config.Network
	activeCert *tls.Certificate
	pending    *pendingNet
	listeners  map[string]*sniffListener

	authMu  sync.Mutex
	tickets map[string]*loginTicket
	enroll  map[string]*enrollment

	stop chan struct{}
}

// New prepares a server. Call Start to begin listening.
func New(opts Options) (*Server, error) {
	if opts.FailDelay == 0 {
		opts.FailDelay = time.Second
	}
	if opts.ConfirmWindow == 0 {
		opts.ConfirmWindow = 2 * time.Minute
	}
	if opts.Hostname == "" {
		opts.Hostname, _ = os.Hostname()
		opts.Hostname = strings.Split(opts.Hostname, ".")[0]
	}
	s := &Server{
		opts: opts, store: opts.Store, throttle: auth.NewThrottle(),
		listeners: map[string]*sniffListener{},
		tickets:   map[string]*loginTicket{}, enroll: map[string]*enrollment{},
		stop: make(chan struct{}),
	}
	s.throttle.Delay = opts.FailDelay
	if opts.RunnerPath == "" {
		if exe, err := os.Executable(); err == nil {
			opts.RunnerPath = filepath.Join(filepath.Dir(exe), "pbcm-runner")
		}
	}
	id, err := sshx.LoadOrCreateIdentity(filepath.Join(opts.ConfigDir, "ssh", "id_ed25519"), "pbcm-server@"+opts.Hostname)
	if err != nil {
		return nil, fmt.Errorf("server SSH key: %w", err)
	}
	s.clients = clients.New(opts.Store, id, opts.RunnerPath)
	if opts.DataDir == "" {
		opts.DataDir = opts.ConfigDir
	}
	s.opts.DataDir = opts.DataDir
	s.clients.Sync.LogDir = filepath.Join(opts.DataDir, "logs")
	s.clients.Sync.KeepRuns = func() int { return s.settingInt("history.client_runs") }
	s.clients.Sync.KeepDays = func() int { return s.settingInt("history.client_days") }
	s.clients.Sync.KeepServerRuns = func() int { return s.settingInt("history.server_runs") }
	s.pbs = &backups.PBS{}
	s.notifier = &alerts.Notifier{Store: opts.Store, Settings: s.alertSettings, ServerName: s.serverName,
		PublicURL: func() string { return s.settingString("general.public_url") },
		LogPath: func(clientID, runID string) string {
			return filepath.Join(opts.DataDir, "logs", clientID, runID+".log")
		}}
	s.clients.Sync.OnFinished = s.notifier.RunFinished
	s.clients.Sync.OnReachable = s.notifier.ClientReachable
	s.mux = http.NewServeMux()
	s.routes()
	s.http = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	return s, nil
}

// Start loads the network settings and starts listening.
func (s *Server) Start() error {
	n, err := s.loadNetwork()
	if err != nil {
		return fmt.Errorf("network settings: %w", err)
	}
	cert, err := s.loadCert(n.TLS)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.active, s.activeCert = n, cert
	err = s.reconcileLocked()
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("%s Run “pbcm network --reset” to go back to the defaults", err)
	}
	if n.UsesTLS() {
		slog.Info("serving HTTPS", "certificate", n.TLS)
	}
	if n.BasePath != "" {
		slog.Info("serving under a base path", "base_path", n.BasePath+"/")
	}
	if ok, _ := s.store.HasAdmin(); !ok {
		if code, err := EnsureSetupCode(s.opts.ConfigDir, s.store); err == nil && code != "" {
			slog.Warn("setup isn't finished: open the web UI and enter the setup code", "code", code)
		}
	}
	go s.housekeeping()
	ctx, cancel := context.WithCancel(context.Background())
	s.pollStop = cancel
	go s.clients.Poll(ctx)
	go s.notifier.Loop(ctx)
	return nil
}

// Addrs returns the addresses being listened on.
func (s *Server) Addrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.listeners {
		out = append(out, l.Addr().String())
	}
	return out
}

// Shutdown stops listening and waits for requests to finish.
func (s *Server) Shutdown(ctx context.Context) error {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	if s.pollStop != nil {
		s.pollStop()
	}
	s.mu.Lock()
	if s.pending != nil {
		s.pending.timer.Stop()
		s.pending = nil
	}
	for addr, l := range s.listeners {
		l.Close()
		delete(s.listeners, addr)
	}
	s.mu.Unlock()
	return s.http.Shutdown(ctx)
}

func (s *Server) housekeeping() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.store.PurgeSessions(s.sessionIdle()); err != nil {
			slog.Error("purging old sessions", "err", err)
		}
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------- requests

type ctxKey int

const reqKey ctxKey = 0

type reqInfo struct {
	net     config.Network
	pending bool
	ip      string
	https   bool
}

func info(r *http.Request) *reqInfo {
	if v, ok := r.Context().Value(reqKey).(*reqInfo); ok {
		return v
	}
	return &reqInfo{}
}

// ServeHTTP works out which network settings a request came in under, strips
// the base path and hands it to the router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	n, pending, ok := s.profileFor(r)
	if !ok {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		plain := active
		plain.TLS = config.TLSOff
		if r.TLS == nil && active.UsesTLS() && matches(r, plain) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusMovedPermanently)
			return
		}
		http.Error(w, "Not found.", http.StatusNotFound)
		return
	}
	if n.BasePath != "" {
		if r.URL.Path == n.BasePath {
			target := n.BasePath + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		r2 := new(http.Request)
		*r2 = *r
		u := *r.URL
		u.Path = strings.TrimPrefix(r.URL.Path, n.BasePath)
		u.RawPath = ""
		r2.URL = &u
		r = r2
	}
	ri := &reqInfo{net: n, pending: pending}
	ri.ip = clientIP(r, n.TrustedProxies)
	ri.https = isHTTPS(r, n.TrustedProxies)
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqKey, ri)))
}

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

func trusted(addr string, proxies []string) bool {
	ip := net.ParseIP(strings.Trim(addr, "[]"))
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, p := range proxies {
		if _, cidr, err := net.ParseCIDR(p); err == nil {
			if cidr.Contains(ip) {
				return true
			}
		} else if pip := net.ParseIP(p); pip != nil && pip.Equal(ip) {
			return true
		}
	}
	return false
}

func peer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIP is the browser's address: the connecting peer, or the right-most
// untrusted hop in X-Forwarded-For when the peer is a trusted proxy.
func clientIP(r *http.Request, proxies []string) string {
	p := peer(r)
	if !trusted(p, proxies) {
		return p
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		var hops []string
		for _, h := range strings.Split(xff, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
		for i := len(hops) - 1; i >= 0; i-- {
			if !trusted(hops[i], proxies) {
				return hops[i]
			}
		}
		if len(hops) > 0 {
			return hops[0]
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	return p
}

func isHTTPS(r *http.Request, proxies []string) bool {
	if r.TLS != nil {
		return true
	}
	if trusted(peer(r), proxies) {
		proto := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
		return proto == "https"
	}
	return false
}

// -------------------------------------------------------------------- API

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &apiError{http.StatusBadRequest, fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &apiError{http.StatusConflict, fmt.Sprintf(format, args...)}
}

func unauthorized(msg string) error { return &apiError{http.StatusUnauthorized, msg} }

type handler func(w http.ResponseWriter, r *http.Request) (any, error)

// api wraps a JSON endpoint: it checks the anti-CSRF header on writes, the
// session when needed, limits the body and writes the result or error.
func (s *Server) api(needAuth bool, fn handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get(csrfHeader) != "1" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Missing request header."})
			return
		}
		if needAuth && !s.signedIn(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Sign in to continue."})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		v, err := fn(w, r)
		if err != nil {
			var ae *apiError
			var ve *config.ValidationError
			var te *auth.ThrottledError
			switch {
			case errors.As(err, &ae):
				writeJSON(w, ae.status, map[string]string{"error": ae.msg})
			case errors.As(err, &ve):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": ve.Message})
			case errors.As(err, &te):
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": te.Error()})
			default:
				slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Something went wrong on the server. Check the service log (journalctl -u pbcm)."})
			}
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, dst any) error {
	err := json.NewDecoder(r.Body).Decode(dst)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &tooBig):
		return &apiError{http.StatusRequestEntityTooLarge, "That request is too large."}
	case err.Error() == "EOF":
		return badRequest("The request was empty.")
	default:
		return badRequest("The request isn't valid JSON.")
	}
}

// ---------------------------------------------------------------- helpers

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func constantEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) settingInt(key string) int {
	d, _ := config.Lookup(key)
	v, _ := d.Default.(int)
	var stored int
	if ok, err := s.store.GetSetting(key, &stored); ok && err == nil {
		v = stored
	}
	return v
}

func (s *Server) settingString(key string) string {
	d, _ := config.Lookup(key)
	v, _ := d.Default.(string)
	var stored string
	if ok, err := s.store.GetSetting(key, &stored); ok && err == nil {
		v = stored
	}
	return v
}

func (s *Server) serverName() string {
	if n := s.settingString("general.server_name"); n != "" {
		return n
	}
	return s.opts.Hostname
}

func (s *Server) sessionIdle() time.Duration {
	return time.Duration(s.settingInt("security.session_hours")) * time.Hour
}
