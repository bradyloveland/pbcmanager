package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/auth"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/config"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/secret"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/store"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/tlscert"
)

const password = "correct-horse-battery"

type env struct {
	t      *testing.T
	dir    string
	st     *store.Store
	srv    *Server
	window time.Duration
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newEnv starts a server on 127.0.0.1 with plain HTTP unless n says
// otherwise. withAdmin creates the admin account.
func newEnv(t *testing.T, n *config.Network, withAdmin bool) *env {
	t.Helper()
	dir := t.TempDir()
	box, err := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "pbcwm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	net := config.Network{Bind: "127.0.0.1", Port: freePort(t), TLS: config.TLSOff, TrustedProxies: []string{}}
	if n != nil {
		net = *n
		if net.Port == 0 {
			net.Port = freePort(t)
		}
	}
	if err := st.SetSetting(networkKey, net); err != nil {
		t.Fatal(err)
	}
	if withAdmin {
		hash, _ := auth.HashPassword(password, 1000)
		if err := st.CreateAdmin("admin", hash); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, dir: dir, st: st, window: 2 * time.Minute}
	e.start()
	return e
}

func (e *env) start() {
	e.t.Helper()
	srv, err := New(Options{ConfigDir: e.dir, Store: e.st, FailDelay: time.Millisecond, ConfirmWindow: e.window, Hostname: "nas"})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.srv = srv
	e.t.Cleanup(func() { e.stop() })
}

func (e *env) stop() {
	if e.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		e.srv.Shutdown(ctx)
		e.srv = nil
	}
}

func (e *env) port() int {
	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	return e.srv.active.Port
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, base string, jar http.CookieJar) *client {
	if jar == nil {
		jar, _ = cookiejar.New(nil)
	}
	return &client{t: t, base: base, http: &http.Client{
		Jar: jar, Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (e *env) client() *client {
	return newClient(e.t, "http://127.0.0.1:"+strconv.Itoa(e.port()), nil)
}

type resp struct {
	status int
	data   map[string]any
	header http.Header
	body   string
}

func (c *client) do(method, path string, body any, headers map[string]string) resp {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if method != http.MethodGet {
		req.Header.Set(csrfHeader, "1")
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, body: string(raw)}
	_ = json.Unmarshal(raw, &out.data)
	return out
}

func (c *client) get(path string) resp            { return c.do("GET", path, nil, nil) }
func (c *client) post(path string, body any) resp { return c.do("POST", path, body, nil) }
func (c *client) put(path string, body any) resp  { return c.do("PUT", path, body, nil) }

func (c *client) login() {
	c.t.Helper()
	r := c.post("/api/login", map[string]string{"username": "admin", "password": password})
	if r.status != 200 {
		c.t.Fatalf("login: %d %v", r.status, r.data)
	}
}

func expect(t *testing.T, r resp, status int, contains string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	if contains != "" && !strings.Contains(r.body, contains) {
		t.Fatalf("body %q doesn't contain %q", r.body, contains)
	}
}

// ------------------------------------------------------------------ setup

func TestSetupFlow(t *testing.T) {
	e := newEnv(t, nil, false)
	c := e.client()
	r := c.get("/api/session")
	if r.data["setup_needed"] != true || r.data["user"] != nil {
		t.Fatalf("session before setup: %v", r.data)
	}
	expect(t, c.post("/api/login", map[string]string{"username": "admin", "password": password}), 409, "Setup isn't finished")

	code, err := EnsureSetupCode(e.dir, e.st)
	if err != nil || len(code) != 14 {
		t.Fatalf("setup code %q, %v", code, err)
	}
	if again, _ := EnsureSetupCode(e.dir, e.st); again != code {
		t.Fatal("setup code should stay the same until used")
	}
	expect(t, c.post("/api/setup", map[string]string{"code": "AAAA-BBBB-CCCC", "username": "admin", "password": password}), 401, "setup code isn't right")
	expect(t, c.post("/api/setup", map[string]string{"code": code, "username": "admin", "password": "short"}), 400, "at least 10")
	expect(t, c.post("/api/setup", map[string]string{"code": code, "username": "x", "password": password}), 400, "Usernames")

	// The code is accepted in lower case, without dashes.
	r = c.post("/api/setup", map[string]string{"code": strings.ToLower(strings.ReplaceAll(code, "-", "")), "username": "boss", "password": password})
	expect(t, r, 200, `"user":"boss"`)
	if r = c.get("/api/account"); r.status != 200 || r.data["username"] != "boss" {
		t.Fatalf("setup should sign in: %v", r.body)
	}
	if _, err := os.Stat(SetupCodePath(e.dir)); !os.IsNotExist(err) {
		t.Fatal("setup code file should be removed")
	}
	expect(t, e.client().post("/api/setup", map[string]string{"code": code, "username": "evil", "password": password}), 409, "already done")
	if c, _ := EnsureSetupCode(e.dir, e.st); c != "" {
		t.Fatal("no setup code once setup is done")
	}
}

func TestSetupCodeGuessingIsThrottled(t *testing.T) {
	e := newEnv(t, nil, false)
	c := e.client()
	for i := 0; i < 5; i++ {
		expect(t, c.post("/api/setup", map[string]string{"code": "WRONG", "username": "admin", "password": password}), 401, "")
	}
	expect(t, c.post("/api/setup", map[string]string{"code": "WRONG", "username": "admin", "password": password}), 429, "Try again")
}

// ------------------------------------------------------------ sign-in etc.

func TestPublicAndProtectedEndpoints(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	expect(t, c.get("/api/health"), 200, `"ok":true`)
	r := c.get("/api/session")
	if r.data["setup_needed"] != false || r.data["server_name"] != "nas" {
		t.Fatalf("session: %v", r.data)
	}
	for _, p := range []string{"/api/account", "/api/settings", "/api/settings/network"} {
		expect(t, c.get(p), 401, "Sign in")
	}
	expect(t, c.get("/api/nope"), 404, `"error"`)
	expect(t, c.do("POST", "/api/login", map[string]string{}, map[string]string{csrfHeader: ""}), 403, "Missing request header")
}

func TestLoginLogoutAndCookie(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	r := c.post("/api/login", map[string]string{"username": "ADMIN", "password": password})
	expect(t, r, 200, `"user":"admin"`)
	cookie := r.header.Get("Set-Cookie")
	for _, want := range []string{"pbcwm_session=", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("cookie %q missing %s", cookie, want)
		}
	}
	if strings.Contains(cookie, "Secure") {
		t.Error("plain HTTP without a trusted proxy shouldn't set Secure")
	}
	expect(t, c.get("/api/account"), 200, "")
	expect(t, c.post("/api/logout", nil), 200, "")
	expect(t, c.get("/api/account"), 401, "")
}

func TestWrongPasswordIsThrottled(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	for i := 0; i < 5; i++ {
		expect(t, c.post("/api/login", map[string]string{"username": "admin", "password": "nope"}), 401, "don't match")
	}
	expect(t, c.post("/api/login", map[string]string{"username": "admin", "password": password}), 429, "Try again")
}

func TestSessionsSurviveRestart(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	e.stop()
	e.start()
	expect(t, c.get("/api/account"), 200, "")
}

func TestPasswordAndUsernameChanges(t *testing.T) {
	e := newEnv(t, nil, true)
	a, b := e.client(), e.client()
	a.login()
	b.login()
	expect(t, a.post("/api/account/password", map[string]string{"current": "wrong", "new": "new-password-123"}), 400, "incorrect")
	expect(t, a.post("/api/account/password", map[string]string{"current": password, "new": "short"}), 400, "at least 10")
	expect(t, a.post("/api/account/password", map[string]string{"current": password, "new": "new-password-123"}), 200, "")
	expect(t, a.get("/api/account"), 200, "")
	expect(t, b.get("/api/account"), 401, "") // other sessions signed out
	expect(t, a.put("/api/account/username", map[string]string{"username": "boss", "password": "new-password-123"}), 200, `"username":"boss"`)
	c := e.client()
	expect(t, c.post("/api/login", map[string]string{"username": "boss", "password": "new-password-123"}), 200, "")
}

func totpNow(t *testing.T, secret string) (string, int64) {
	step := time.Now().Unix() / 30
	code, err := auth.TOTPCode(secret, step, 6)
	if err != nil {
		t.Fatal(err)
	}
	return code, step
}

func TestTwoStepFlow(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	expect(t, c.post("/api/account/totp/setup", map[string]string{"password": "nope"}), 400, "incorrect")
	r := c.post("/api/account/totp/setup", map[string]string{"password": password})
	expect(t, r, 200, "qr_svg")
	if !strings.HasPrefix(r.data["qr_svg"].(string), "<svg") {
		t.Fatal("expected an SVG QR code")
	}
	secret := strings.ReplaceAll(r.data["secret"].(string), " ", "")
	if !strings.Contains(r.data["uri"].(string), "PBC%20Web%20Manager") {
		t.Fatalf("uri %v", r.data["uri"])
	}
	expect(t, c.post("/api/account/totp/enable", map[string]string{"code": "000000"}), 400, "doesn't match")
	code, _ := totpNow(t, secret)
	r = c.post("/api/account/totp/enable", map[string]string{"code": code})
	expect(t, r, 200, "recovery_codes")
	recovery := r.data["recovery_codes"].([]any)
	if len(recovery) != 10 {
		t.Fatalf("want 10 recovery codes, got %d", len(recovery))
	}

	// Password alone isn't enough any more.
	f := e.client()
	r = f.post("/api/login", map[string]string{"username": "admin", "password": password})
	expect(t, r, 200, "totp_required")
	expect(t, f.get("/api/account"), 401, "")
	ticket := r.data["ticket"].(string)
	// The enrolment code was used, so it can't be replayed.
	expect(t, f.post("/api/login/totp", map[string]string{"ticket": ticket, "code": code}), 401, "isn't valid")
	expect(t, f.post("/api/login/totp", map[string]string{"ticket": "bogus", "code": code}), 401, "timed out")

	// A recovery code works once.
	rc := recovery[0].(string)
	r = f.post("/api/login/totp", map[string]string{"ticket": ticket, "code": strings.ToUpper(rc)})
	expect(t, r, 200, `"recovery_used":true`)
	if r.data["recovery_left"].(float64) != 9 {
		t.Fatalf("recovery_left %v", r.data["recovery_left"])
	}
	g := e.client()
	r = g.post("/api/login", map[string]string{"username": "admin", "password": password})
	expect(t, g.post("/api/login/totp", map[string]string{"ticket": r.data["ticket"].(string), "code": rc}), 401, "")

	// Turning it off needs a valid second factor.
	expect(t, c.post("/api/account/totp/disable", map[string]string{"password": password, "code": "123456"}), 400, "Enter a current code")
	r = c.post("/api/account/totp/disable", map[string]string{"password": password, "code": recovery[1].(string)})
	expect(t, r, 200, `"totp_enabled":false`)
	raw, _ := e.st.RawAdminTOTP()
	if raw != "" {
		t.Fatal("TOTP secret should be cleared")
	}
}

func TestTOTPSecretIsEncryptedAtRest(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	r := c.post("/api/account/totp/setup", map[string]string{"password": password})
	secret := strings.ReplaceAll(r.data["secret"].(string), " ", "")
	code, _ := totpNow(t, secret)
	expect(t, c.post("/api/account/totp/enable", map[string]string{"code": code}), 200, "")
	raw, _ := e.st.RawAdminTOTP()
	if raw == "" || strings.Contains(raw, secret) {
		t.Fatalf("stored TOTP column %q", raw)
	}
}

// --------------------------------------------------------------- settings

func TestSettings(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	r := c.get("/api/settings")
	expect(t, r, 200, `"key":"security.session_hours"`)
	expect(t, r, 200, `"groups":[{"key":"general","label":"General"}`)
	expect(t, c.put("/api/settings", map[string]any{"values": map[string]any{"security.session_hours": 0}}), 400, "between 1 and 720")
	expect(t, c.put("/api/settings", map[string]any{"values": map[string]any{"bogus": 1}}), 400, "Unknown setting")
	r = c.put("/api/settings", map[string]any{"values": map[string]any{"security.session_hours": 48, "general.server_name": "backup-server"}})
	expect(t, r, 200, `"security.session_hours":48`)
	if s := c.get("/api/session"); s.data["server_name"] != "backup-server" {
		t.Fatalf("server name not applied: %v", s.data)
	}
}

func TestIndexAssetsAndHeaders(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	r := c.get("/")
	expect(t, r, 200, "assets/app.js?v=")
	if strings.Contains(r.body, "{{VERSION}}") {
		t.Fatal("version placeholder not replaced")
	}
	csp := r.header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP %q", csp)
	}
	for _, h := range []string{"X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
		if r.header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	expect(t, c.get("/assets/app.js"), 200, "use strict")
	expect(t, c.get("/assets/app.css"), 200, "--accent")
	expect(t, c.get("/assets/nope.js"), 404, "")
	expect(t, c.get("/elsewhere"), 404, "")
}

// ------------------------------------------------------------------ proxies

func TestBasePath(t *testing.T) {
	e := newEnv(t, &config.Network{Bind: "127.0.0.1", TLS: config.TLSOff, BasePath: "/backups", TrustedProxies: []string{}}, true)
	c := e.client()
	r := c.get("/backups")
	if r.status != 301 || r.header.Get("Location") != "/backups/" {
		t.Fatalf("redirect: %d %s", r.status, r.header.Get("Location"))
	}
	expect(t, c.get("/backups/api/health"), 200, "")
	expect(t, c.get("/backups/"), 200, "<!doctype html>")
	expect(t, c.get("/api/health"), 404, "")
}

func TestTrustedProxyHeaders(t *testing.T) {
	e := newEnv(t, &config.Network{Bind: "127.0.0.1", TLS: config.TLSOff, TrustedProxies: []string{"127.0.0.1"}}, true)
	c := e.client()
	r := c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password},
		map[string]string{"X-Forwarded-Proto": "https"})
	expect(t, r, 200, "")
	if !strings.Contains(r.header.Get("Set-Cookie"), "Secure") {
		t.Fatal("HTTPS at a trusted proxy should mark the cookie Secure")
	}
	// Throttling follows the forwarded client, not the proxy.
	for i := 0; i < 5; i++ {
		c.do("POST", "/api/login", map[string]string{"username": "admin", "password": "x"}, map[string]string{"X-Forwarded-For": "198.51.100.7"})
	}
	expect(t, c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password},
		map[string]string{"X-Forwarded-For": "198.51.100.7"}), 429, "")
	expect(t, c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password},
		map[string]string{"X-Forwarded-For": "198.51.100.8"}), 200, "")
}

func TestForwardedHeadersIgnoredFromUntrustedPeer(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	r := c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password},
		map[string]string{"X-Forwarded-Proto": "https"})
	expect(t, r, 200, "")
	if strings.Contains(r.header.Get("Set-Cookie"), "Secure") {
		t.Fatal("untrusted X-Forwarded-Proto must be ignored")
	}
	// Faking a different X-Forwarded-For each time doesn't dodge throttling.
	for i := 0; i < 5; i++ {
		c.do("POST", "/api/login", map[string]string{"username": "admin", "password": "x"},
			map[string]string{"X-Forwarded-For": "198.51.100." + strconv.Itoa(i)})
	}
	expect(t, c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password},
		map[string]string{"X-Forwarded-For": "198.51.100.99"}), 429, "")
}

// ------------------------------------------------------------------- HTTPS

func TestSelfSignedHTTPSAndRedirect(t *testing.T) {
	e := newEnv(t, &config.Network{Bind: "127.0.0.1", TLS: config.TLSSelfSigned, TrustedProxies: []string{}}, true)
	port := strconv.Itoa(e.port())
	s := newClient(t, "https://127.0.0.1:"+port, nil)
	expect(t, s.get("/api/health"), 200, "")
	r := s.post("/api/login", map[string]string{"username": "admin", "password": password})
	if !strings.Contains(r.header.Get("Set-Cookie"), "Secure") {
		t.Fatal("HTTPS should set a Secure cookie")
	}
	p := newClient(t, "http://127.0.0.1:"+port, nil)
	r = p.get("/settings?x=1")
	if r.status != 301 || r.header.Get("Location") != "https://127.0.0.1:"+port+"/settings?x=1" {
		t.Fatalf("plain HTTP should redirect: %d %q", r.status, r.header.Get("Location"))
	}
	if _, err := os.Stat(filepath.Join(e.dir, "tls", "self-signed.key")); err != nil {
		t.Fatal("self-signed key should be created")
	}
}

// --------------------------------------------------------- network changes

func networkBody(n config.Network) map[string]any {
	return map[string]any{"bind": n.Bind, "port": n.Port, "tls": n.TLS, "base_path": n.BasePath, "trusted_proxies": n.TrustedProxies}
}

func TestNetworkChangeConfirmedFromNewAddress(t *testing.T) {
	e := newEnv(t, nil, true)
	jar, _ := cookiejar.New(nil) // cookies ignore the port, like browsers
	old := newClient(t, "http://127.0.0.1:"+strconv.Itoa(e.port()), jar)
	old.login()
	newPort := freePort(t)
	r := old.put("/api/settings/network", networkBody(config.Network{Bind: "127.0.0.1", Port: newPort, TLS: config.TLSOff}))
	expect(t, r, 200, "pending")
	pending := r.data["pending"].(map[string]any)
	if url := pending["url"].(string); url != "http://127.0.0.1:"+strconv.Itoa(newPort)+"/" {
		t.Fatalf("new url %q", url)
	}
	token := pending["token"].(string)
	expect(t, old.put("/api/settings/network", networkBody(config.Network{Port: freePort(t), TLS: config.TLSOff})), 409, "waiting to be confirmed")

	fresh := newClient(t, "http://127.0.0.1:"+strconv.Itoa(newPort), jar)
	s := fresh.get("/api/session")
	if p := s.data["network_pending"].(map[string]any); p["here"] != true {
		t.Fatalf("new address should know it's the pending one: %v", p)
	}
	expect(t, old.post("/api/settings/network/confirm", map[string]string{"token": token}), 409, "Open the new address")
	expect(t, fresh.post("/api/settings/network/confirm", map[string]string{"token": "wrong"}), 409, "no network change")
	expect(t, fresh.post("/api/settings/network/confirm", map[string]string{"token": token}), 200, "")

	if _, err := net.DialTimeout("tcp", old.base[len("http://"):], 300*time.Millisecond); err == nil {
		t.Fatal("old port should be closed after confirming")
	}
	var saved config.Network
	e.st.GetSetting(networkKey, &saved)
	if saved.Port != newPort {
		t.Fatalf("saved port %d", saved.Port)
	}
	expect(t, fresh.get("/api/account"), 200, "")
}

func TestBasePathChangeConfirmedUnderNewPath(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	r := c.put("/api/settings/network", networkBody(config.Network{Bind: "127.0.0.1", Port: e.port(), TLS: config.TLSOff, BasePath: "/backups"}))
	expect(t, r, 200, "pending")
	pending := r.data["pending"].(map[string]any)
	if !strings.HasSuffix(pending["url"].(string), "/backups/") {
		t.Fatalf("url %v", pending["url"])
	}
	token := pending["token"].(string)
	// Both paths work while the change waits.
	expect(t, c.get("/api/account"), 200, "")
	expect(t, c.get("/backups/api/account"), 200, "")
	if c.get("/api/session").data["network_pending"].(map[string]any)["here"] != false {
		t.Fatal("the old path isn't the new address")
	}
	if c.get("/backups/api/session").data["network_pending"].(map[string]any)["here"] != true {
		t.Fatal("the new path is the new address")
	}
	expect(t, c.post("/api/settings/network/confirm", map[string]string{"token": token}), 409, "Open the new address")
	expect(t, c.post("/backups/api/settings/network/confirm", map[string]string{"token": token}), 200, "")
	expect(t, c.get("/api/account"), 404, "")
	expect(t, c.get("/backups/api/account"), 200, "")

	// And back to no base path: the old /backups/ keeps working until confirmed.
	r = c.put("/backups/api/settings/network", networkBody(config.Network{Bind: "127.0.0.1", Port: e.port(), TLS: config.TLSOff}))
	expect(t, r, 200, "pending")
	token = r.data["pending"].(map[string]any)["token"].(string)
	expect(t, c.get("/backups/api/account"), 200, "")
	expect(t, c.get("/api/account"), 200, "")
	expect(t, c.post("/backups/api/settings/network/confirm", map[string]string{"token": token}), 409, "Open the new address")
	expect(t, c.post("/api/settings/network/confirm", map[string]string{"token": token}), 200, "")
	expect(t, c.get("/backups/api/account"), 404, "")
}

func TestNetworkChangeUndoneWhenNotConfirmed(t *testing.T) {
	e := newEnv(t, nil, true)
	e.stop()
	e.window = 300 * time.Millisecond
	e.start()
	c := e.client()
	c.login()
	oldPort := e.port()
	newPort := freePort(t)
	expect(t, c.put("/api/settings/network", networkBody(config.Network{Bind: "127.0.0.1", Port: newPort, TLS: config.TLSOff})), 200, "pending")
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(newPort), time.Second); err != nil {
		t.Fatal("new port should be open while pending")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(newPort), 300*time.Millisecond); err == nil {
		t.Fatal("new port should close when the change is undone")
	}
	var saved config.Network
	e.st.GetSetting(networkKey, &saved)
	if saved.Port != oldPort || e.port() != oldPort {
		t.Fatal("settings should stay as they were")
	}
	expect(t, c.get("/api/account"), 200, "")
	if p := c.get("/api/session").data["network_pending"]; p != nil {
		t.Fatalf("nothing should be pending: %v", p)
	}
}

func TestNetworkChangeCanBeUndone(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	newPort := freePort(t)
	expect(t, c.put("/api/settings/network", networkBody(config.Network{Bind: "127.0.0.1", Port: newPort, TLS: config.TLSOff})), 200, "pending")
	expect(t, c.post("/api/settings/network/cancel", nil), 200, `"undone":true`)
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(newPort), 300*time.Millisecond); err == nil {
		t.Fatal("new port should be closed")
	}
}

func TestTrustedProxiesApplyWithoutConfirming(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	n := config.Network{Bind: "127.0.0.1", Port: e.port(), TLS: config.TLSOff, TrustedProxies: []string{"192.0.2.10", "172.16.0.0/12"}}
	expect(t, c.put("/api/settings/network", networkBody(n)), 200, `"applied":true`)
	expect(t, c.put("/api/settings/network", map[string]any{"port": e.port(), "tls": "off", "bind": "127.0.0.1",
		"trusted_proxies": []string{"proxy.lan"}}), 400, "isn't an IP address")
	r := c.get("/api/settings/network")
	if got := r.data["active"].(map[string]any)["trusted_proxies"].([]any); len(got) != 2 {
		t.Fatalf("trusted proxies %v", got)
	}
}

func TestNetworkChangeToBusyPortFails(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	expect(t, c.put("/api/settings/network", networkBody(config.Network{Bind: "127.0.0.1", Port: port, TLS: config.TLSOff})), 400, "already using that port")
	expect(t, c.get("/api/account"), 200, "")
	if c.get("/api/session").data["network_pending"] != nil {
		t.Fatal("a failed change shouldn't stay pending")
	}
}

func TestSwitchToOwnCertificateOnSamePort(t *testing.T) {
	e := newEnv(t, nil, true)
	port := strconv.Itoa(e.port())
	jar, _ := cookiejar.New(nil)
	plain := newClient(t, "http://127.0.0.1:"+port, jar)
	plain.login()
	certPEM, keyPEM, _ := tlscert.SelfSigned("backup.example.net", nil)
	_, otherKey, _ := tlscert.SelfSigned("x", nil)

	body := networkBody(config.Network{Bind: "127.0.0.1", Port: e.port(), TLS: config.TLSCustom})
	expect(t, plain.put("/api/settings/network", body), 400, "Paste a certificate")
	body["cert_pem"], body["key_pem"] = string(certPEM), string(otherKey)
	expect(t, plain.put("/api/settings/network", body), 400, "doesn't belong")
	body["key_pem"] = string(keyPEM)
	r := plain.put("/api/settings/network", body)
	expect(t, r, 200, "pending")
	token := r.data["pending"].(map[string]any)["token"].(string)

	// The same port now answers HTTPS with the uploaded certificate, and
	// still answers plain HTTP for the old settings.
	conn, err := tls.Dial("tcp", "127.0.0.1:"+port, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "backup.example.net" {
		t.Fatalf("served certificate %q", cn)
	}
	conn.Close()
	expect(t, plain.get("/api/account"), 200, "")

	secure := newClient(t, "https://127.0.0.1:"+port, jar)
	expect(t, secure.post("/api/settings/network/confirm", map[string]string{"token": token}), 200, "")
	if _, err := os.Stat(filepath.Join(e.dir, "tls", "custom.crt")); err != nil {
		t.Fatal("custom certificate should be saved on confirm")
	}
	r = plain.get("/")
	if r.status != 301 || !strings.HasPrefix(r.header.Get("Location"), "https://") {
		t.Fatalf("plain HTTP should now redirect: %d", r.status)
	}

	// After a restart the saved certificate is used.
	e.stop()
	e.start()
	conn, err = tls.Dial("tcp", "127.0.0.1:"+port, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; cn != "backup.example.net" {
		t.Fatalf("after restart served %q", cn)
	}
}

func TestRegenerateSelfSigned(t *testing.T) {
	e := newEnv(t, &config.Network{Bind: "127.0.0.1", TLS: config.TLSSelfSigned, TrustedProxies: []string{}}, true)
	addr := "127.0.0.1:" + strconv.Itoa(e.port())
	fingerprint := func() string {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return string(conn.ConnectionState().PeerCertificates[0].Signature)
	}
	before := fingerprint()
	c := newClient(t, "https://"+addr, nil)
	c.login()
	expect(t, c.post("/api/settings/network/regenerate-certificate", nil), 200, "fingerprint")
	if fingerprint() == before {
		t.Fatal("new certificate should be served straight away")
	}
}
