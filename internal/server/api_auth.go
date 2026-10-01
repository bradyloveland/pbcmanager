package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/auth"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/qr"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/store"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/version"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@\-]{1,63}$`)

const minPassword = 10

type loginTicket struct {
	expires  time.Time
	attempts int
}

type enrollment struct {
	secret  string
	expires time.Time
}

// ------------------------------------------------------------- setup code

const setupAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// SetupCodePath is where the one-time setup code is kept until setup is done.
func SetupCodePath(configDir string) string { return filepath.Join(configDir, "setup-code") }

// EnsureSetupCode returns the setup code, creating it if needed. It returns
// "" once setup has been completed.
func EnsureSetupCode(configDir string, st *store.Store) (string, error) {
	if ok, err := st.HasAdmin(); err != nil || ok {
		return "", err
	}
	path := SetupCodePath(configDir)
	if raw, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw)), nil
	}
	b := make([]byte, 12)
	for i := range b {
		b[i] = setupAlphabet[randInt(len(setupAlphabet))]
	}
	code := fmt.Sprintf("%s-%s-%s", b[:4], b[4:8], b[8:])
	if err := writeFileAtomic(path, []byte(code+"\n"), 0o600); err != nil {
		return "", err
	}
	return code, nil
}

func normalizeSetupCode(c string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(c)))
}

func randInt(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}

// --------------------------------------------------------------- sessions

func (s *Server) sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) signedIn(r *http.Request) bool {
	ok, err := s.store.CheckSession(s.sessionToken(r), s.sessionIdle())
	if err != nil {
		slog.Error("checking session", "err", err)
	}
	return ok
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) error {
	token := randomToken(32)
	if err := s.store.CreateSession(token, info(r).ip, r.UserAgent()); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: info(r).https, MaxAge: 31 * 24 * 3600})
	return nil
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: info(r).https, MaxAge: -1})
}

// ----------------------------------------------------------------- public

func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"ok": true, "version": version.Version}, nil
}

func (s *Server) apiSession(w http.ResponseWriter, r *http.Request) (any, error) {
	hasAdmin, err := s.store.HasAdmin()
	if err != nil {
		return nil, err
	}
	signedIn := hasAdmin && s.signedIn(r)
	out := map[string]any{"user": nil, "server_name": s.serverName(), "setup_needed": !hasAdmin,
		"version": version.Version, "name": version.Name, "short_name": version.ShortName}
	if signedIn {
		a, err := s.store.GetAdmin()
		if err != nil {
			return nil, err
		}
		out["user"] = a.Username
	}
	s.mu.Lock()
	out["network_pending"] = s.pendingInfoLocked(r, signedIn)
	s.mu.Unlock()
	return out, nil
}

type setupRequest struct {
	Code     string `json:"code"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) (any, error) {
	if ok, err := s.store.HasAdmin(); err != nil {
		return nil, err
	} else if ok {
		return nil, conflict("Setup is already done. Sign in instead.")
	}
	ip := info(r).ip
	if err := s.throttle.Check(ip); err != nil {
		return nil, err
	}
	var in setupRequest
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	want, err := EnsureSetupCode(s.opts.ConfigDir, s.store)
	if err != nil {
		return nil, err
	}
	if !constantEqual(normalizeSetupCode(in.Code), normalizeSetupCode(want)) {
		s.throttle.Fail(ip)
		slog.Warn("wrong setup code", "ip", ip)
		return nil, unauthorized("That setup code isn't right. It was shown when the server was installed; run “sudo pbcwm setup-code” on the server to see it again.")
	}
	username := strings.TrimSpace(in.Username)
	if !usernameRE.MatchString(username) {
		return nil, badRequest("Usernames are 2-64 characters: letters, numbers, dots, dashes, underscores or @.")
	}
	if len(in.Password) < minPassword {
		return nil, badRequest("Use at least %d characters for the password.", minPassword)
	}
	hash, err := auth.HashPassword(in.Password, auth.DefaultIterations)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreateAdmin(username, hash); err != nil {
		return nil, conflict("%s", err)
	}
	os.Remove(SetupCodePath(s.opts.ConfigDir))
	s.throttle.Clear(ip)
	if err := s.startSession(w, r); err != nil {
		return nil, err
	}
	slog.Info("setup completed", "user", username, "ip", ip)
	return map[string]any{"user": username}, nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) (any, error) {
	ip := info(r).ip
	if err := s.throttle.Check(ip); err != nil {
		return nil, err
	}
	var in loginRequest
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	a, err := s.store.GetAdmin()
	if errors.Is(err, store.ErrNoAdmin) {
		return nil, conflict("Setup isn't finished yet. Reload the page to set up the admin account.")
	}
	if err != nil {
		return nil, err
	}
	nameOK := constantEqual(strings.ToLower(strings.TrimSpace(in.Username)), strings.ToLower(a.Username))
	if !(auth.VerifyPassword(in.Password, a.PasswordHash) && nameOK) {
		s.throttle.Fail(ip)
		slog.Warn("failed sign-in", "user", in.Username, "ip", ip)
		return nil, unauthorized("That username and password don't match.")
	}
	if a.TOTPSecret != "" {
		ticket := randomToken(24)
		s.authMu.Lock()
		for k, t := range s.tickets {
			if time.Now().After(t.expires) {
				delete(s.tickets, k)
			}
		}
		s.tickets[ticket] = &loginTicket{expires: time.Now().Add(5 * time.Minute)}
		s.authMu.Unlock()
		return map[string]any{"totp_required": true, "ticket": ticket}, nil
	}
	s.throttle.Clear(ip)
	if err := s.startSession(w, r); err != nil {
		return nil, err
	}
	slog.Info("signed in", "user", a.Username, "ip", ip)
	return map[string]any{"user": a.Username}, nil
}

// consumeSecondFactor checks a TOTP or recovery code and records its use so
// it can't be used again. It returns "totp", "recovery" or "".
func (s *Server) consumeSecondFactor(code string) (string, error) {
	kind := ""
	_, err := s.store.UpdateAdmin(func(a *store.Admin) error {
		if step, ok := auth.TOTPMatch(a.TOTPSecret, code, a.TOTPLastStep, time.Now()); ok {
			a.TOTPLastStep = step
			kind = "totp"
			return nil
		}
		if len(auth.NormalizeRecovery(code)) == 10 {
			digest := auth.HashRecovery(code)
			for i, stored := range a.RecoveryCodes {
				if constantEqual(stored, digest) {
					a.RecoveryCodes = append(a.RecoveryCodes[:i:i], a.RecoveryCodes[i+1:]...)
					kind = "recovery"
					return nil
				}
			}
		}
		return errNoMatch
	})
	if errors.Is(err, errNoMatch) {
		return "", nil
	}
	return kind, err
}

var errNoMatch = errors.New("no match")

type totpLoginRequest struct {
	Ticket string `json:"ticket"`
	Code   string `json:"code"`
}

func (s *Server) apiLoginTOTP(w http.ResponseWriter, r *http.Request) (any, error) {
	ip := info(r).ip
	if err := s.throttle.Check(ip); err != nil {
		return nil, err
	}
	var in totpLoginRequest
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	s.authMu.Lock()
	t := s.tickets[in.Ticket]
	if t == nil || time.Now().After(t.expires) {
		delete(s.tickets, in.Ticket)
		s.authMu.Unlock()
		return nil, unauthorized("Your sign-in timed out. Enter your password again.")
	}
	s.authMu.Unlock()
	kind, err := s.consumeSecondFactor(in.Code)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		s.authMu.Lock()
		t.attempts++
		if t.attempts >= 5 {
			delete(s.tickets, in.Ticket)
		}
		s.authMu.Unlock()
		s.throttle.Fail(ip)
		slog.Warn("wrong verification code", "ip", ip)
		return nil, unauthorized("That code isn't valid. Codes change every 30 seconds, so check your device's clock if this keeps happening.")
	}
	s.authMu.Lock()
	delete(s.tickets, in.Ticket)
	s.authMu.Unlock()
	s.throttle.Clear(ip)
	if err := s.startSession(w, r); err != nil {
		return nil, err
	}
	a, err := s.store.GetAdmin()
	if err != nil {
		return nil, err
	}
	slog.Info("signed in", "user", a.Username, "method", kind, "ip", ip)
	return map[string]any{"user": a.Username, "recovery_used": kind == "recovery", "recovery_left": len(a.RecoveryCodes)}, nil
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) (any, error) {
	if tok := s.sessionToken(r); tok != "" {
		if err := s.store.DeleteSession(tok); err != nil {
			return nil, err
		}
	}
	s.clearCookie(w, r)
	return map[string]any{"ok": true}, nil
}

// ---------------------------------------------------------------- account

func (s *Server) accountInfo() (map[string]any, error) {
	a, err := s.store.GetAdmin()
	if err != nil {
		return nil, err
	}
	return map[string]any{"username": a.Username, "totp_enabled": a.TOTPSecret != "",
		"recovery_left": len(a.RecoveryCodes)}, nil
}

func (s *Server) requirePassword(password string) (*store.Admin, error) {
	a, err := s.store.GetAdmin()
	if err != nil {
		return nil, err
	}
	if !auth.VerifyPassword(password, a.PasswordHash) {
		return nil, badRequest("Your current password is incorrect.")
	}
	return a, nil
}

func (s *Server) apiAccount(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.accountInfo()
}

func (s *Server) apiAccountUsername(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct{ Username, Password string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if _, err := s.requirePassword(in.Password); err != nil {
		return nil, err
	}
	username := strings.TrimSpace(in.Username)
	if !usernameRE.MatchString(username) {
		return nil, badRequest("Usernames are 2-64 characters: letters, numbers, dots, dashes, underscores or @.")
	}
	if _, err := s.store.UpdateAdmin(func(a *store.Admin) error { a.Username = username; return nil }); err != nil {
		return nil, err
	}
	return s.accountInfo()
}

func (s *Server) apiAccountPassword(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct{ Current, New string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if _, err := s.requirePassword(in.Current); err != nil {
		return nil, err
	}
	if len(in.New) < minPassword {
		return nil, badRequest("Use at least %d characters for the new password.", minPassword)
	}
	hash, err := auth.HashPassword(in.New, auth.DefaultIterations)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.UpdateAdmin(func(a *store.Admin) error { a.PasswordHash = hash; return nil }); err != nil {
		return nil, err
	}
	if err := s.store.DeleteOtherSessions(s.sessionToken(r)); err != nil {
		return nil, err
	}
	slog.Info("password changed", "ip", info(r).ip)
	return map[string]any{"ok": true}, nil
}

func (s *Server) apiTOTPSetup(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct{ Password string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	a, err := s.requirePassword(in.Password)
	if err != nil {
		return nil, err
	}
	secret := auth.NewTOTPSecret()
	s.authMu.Lock()
	s.enroll[store.HashToken(s.sessionToken(r))] = &enrollment{secret: secret, expires: time.Now().Add(10 * time.Minute)}
	s.authMu.Unlock()
	uri := auth.TOTPURI(secret, version.ShortName, fmt.Sprintf("%s (%s)", a.Username, s.serverName()))
	svg, err := qr.SVG(uri)
	if err != nil {
		return nil, err
	}
	return map[string]any{"secret": auth.GroupSecret(secret), "uri": uri, "qr_svg": svg}, nil
}

func (s *Server) apiTOTPEnable(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct{ Code string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	key := store.HashToken(s.sessionToken(r))
	s.authMu.Lock()
	e := s.enroll[key]
	s.authMu.Unlock()
	if e == nil || time.Now().After(e.expires) {
		return nil, badRequest("Setup timed out. Start again to get a new QR code.")
	}
	step, ok := auth.TOTPMatch(e.secret, in.Code, 0, time.Now())
	if !ok {
		return nil, badRequest("That code doesn't match. Make sure you scanned the code shown here and your phone's clock is set automatically.")
	}
	codes := auth.NewRecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.HashRecovery(c)
	}
	if _, err := s.store.UpdateAdmin(func(a *store.Admin) error {
		a.TOTPSecret, a.TOTPLastStep, a.RecoveryCodes = e.secret, step, hashes
		return nil
	}); err != nil {
		return nil, err
	}
	s.authMu.Lock()
	delete(s.enroll, key)
	s.authMu.Unlock()
	if err := s.store.DeleteOtherSessions(s.sessionToken(r)); err != nil {
		return nil, err
	}
	slog.Info("two-step verification turned on")
	out, err := s.accountInfo()
	if err != nil {
		return nil, err
	}
	out["recovery_codes"] = codes
	return out, nil
}

func (s *Server) apiTOTPDisable(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct{ Password, Code string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if _, err := s.requirePassword(in.Password); err != nil {
		return nil, err
	}
	kind, err := s.consumeSecondFactor(in.Code)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		return nil, badRequest("Enter a current code from your authenticator app, or a recovery code.")
	}
	if _, err := s.store.UpdateAdmin(func(a *store.Admin) error {
		a.TOTPSecret, a.TOTPLastStep, a.RecoveryCodes = "", 0, []string{}
		return nil
	}); err != nil {
		return nil, err
	}
	slog.Info("two-step verification turned off")
	return s.accountInfo()
}

func (s *Server) apiTOTPRecovery(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct{ Password, Code string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if _, err := s.requirePassword(in.Password); err != nil {
		return nil, err
	}
	kind, err := s.consumeSecondFactor(in.Code)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		return nil, badRequest("Enter a current code from your authenticator app, or a recovery code.")
	}
	codes := auth.NewRecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.HashRecovery(c)
	}
	if _, err := s.store.UpdateAdmin(func(a *store.Admin) error { a.RecoveryCodes = hashes; return nil }); err != nil {
		return nil, err
	}
	out, err := s.accountInfo()
	if err != nil {
		return nil, err
	}
	out["recovery_codes"] = codes
	return out, nil
}
