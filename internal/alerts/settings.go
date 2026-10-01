// Package alerts sends email when something needs attention: a backup
// failed (or succeeded, if wanted), a scheduled backup didn't run, or a
// client can't be reached.
package alerts

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Settings are the email alert settings. Password is the decrypted SMTP
// password.
type Settings struct {
	Enabled            bool   `json:"enabled"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Security           string `json:"security"` // starttls, ssl, none
	Username           string `json:"username"`
	Password           string `json:"-"`
	From               string `json:"from"`
	To                 string `json:"to"`
	OnFailure          bool   `json:"on_failure"`
	OnSuccess          bool   `json:"on_success"`
	OnMissed           bool   `json:"on_missed"`
	MissedGraceMinutes int    `json:"missed_grace_minutes"`
	OnUnreachable      bool   `json:"on_unreachable"`
	UnreachableMinutes int    `json:"unreachable_minutes"`
	OnFull             bool   `json:"on_full"`
	FullPercent        int    `json:"full_percent"`
}

// Defaults are a fresh install's settings: off until set up, then failures,
// missed backups, outages and nearly full destinations, but not successes.
func Defaults() Settings {
	return Settings{Port: 587, Security: "starttls", OnFailure: true, OnMissed: true, MissedGraceMinutes: 60,
		OnUnreachable: true, UnreachableMinutes: 60, OnFull: true, FullPercent: 90}
}

// Input is the settings form. An empty Password keeps the saved one.
type Input struct {
	Settings
	Password      string `json:"password"`
	ClearPassword bool   `json:"clear_password"`
}

// InputError is shown to the user as-is.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

func bad(format string, a ...any) error { return &InputError{fmt.Sprintf(format, a...)} }

var (
	emailRE = regexp.MustCompile(`^[^@\s,;<>"]+@[^@\s,;<>"]+\.[^@\s,;<>"]+$`)
	hostRE  = regexp.MustCompile(`^[A-Za-z0-9.\-]+$|^\[?[0-9A-Fa-f:.]+\]?$`)
)

// Recipients splits the To field.
func (s Settings) Recipients() []string {
	var out []string
	for _, a := range regexp.MustCompile(`[,;\s]+`).Split(s.To, -1) {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// Clean checks the form against the saved settings.
func Clean(in Input, saved Settings) (Settings, error) {
	s := in.Settings
	s.Password = saved.Password
	if in.Password != "" {
		if strings.ContainsAny(in.Password, "\r\n") {
			return s, bad("The password can't contain line breaks.")
		}
		s.Password = in.Password
	}
	if in.ClearPassword {
		s.Password = ""
	}
	s.Host = strings.TrimSpace(s.Host)
	s.Username = strings.TrimSpace(s.Username)
	s.From = strings.TrimSpace(s.From)
	s.To = strings.TrimSpace(s.To)
	if s.Port == 0 {
		s.Port = map[string]int{"ssl": 465, "none": 25}[s.Security]
		if s.Port == 0 {
			s.Port = 587
		}
	}
	if s.Port < 1 || s.Port > 65535 {
		return s, bad("The SMTP port must be between 1 and 65535.")
	}
	if s.Security == "" {
		s.Security = "starttls"
	}
	if s.Security != "starttls" && s.Security != "ssl" && s.Security != "none" {
		return s, bad("Choose how to secure the connection to the mail server.")
	}
	if s.MissedGraceMinutes == 0 {
		s.MissedGraceMinutes = 60
	}
	if s.MissedGraceMinutes < 10 || s.MissedGraceMinutes > 1440 {
		return s, bad("Wait between 10 minutes and 24 hours before calling a backup missed.")
	}
	if s.UnreachableMinutes == 0 {
		s.UnreachableMinutes = 60
	}
	if s.UnreachableMinutes < 5 || s.UnreachableMinutes > 10080 {
		return s, bad("Wait between 5 minutes and 7 days before reporting a client that can't be reached.")
	}
	if s.FullPercent == 0 {
		s.FullPercent = 90
	}
	if s.FullPercent < 50 || s.FullPercent > 99 {
		return s, bad("Report a destination as nearly full somewhere between 50%% and 99%%.")
	}
	if s.Host != "" && !hostRE.MatchString(s.Host) {
		return s, bad("Enter the mail server's host name or IP address.")
	}
	for _, field := range []string{s.Username, s.From, s.To} {
		if strings.ContainsAny(field, "\r\n") {
			return s, bad("Email settings can't contain line breaks.")
		}
	}
	if s.Enabled {
		if s.Host == "" {
			return s, bad("Enter your mail server (SMTP) to turn on email alerts.")
		}
		if !emailRE.MatchString(s.From) {
			return s, bad("Enter a valid sender address.")
		}
		rcpts := s.Recipients()
		if len(rcpts) == 0 {
			return s, bad("Enter at least one recipient.")
		}
		for _, r := range rcpts {
			if !emailRE.MatchString(r) {
				return s, bad("“%s” isn't a valid email address.", r)
			}
		}
		if s.Username != "" && s.Security == "none" {
			return s, bad("Signing in to the mail server needs STARTTLS or SSL, so your password isn't sent unencrypted.")
		}
	}
	return s, nil
}

// Send delivers one plain-text email.
func Send(ctx context.Context, s Settings, subject, body string) error {
	rcpts := s.Recipients()
	if s.Host == "" || s.From == "" || len(rcpts) == 0 {
		return errors.New("email alerts need a mail server, a sender and at least one recipient")
	}
	addr := net.JoinHostPort(strings.Trim(s.Host, "[]"), strconv.Itoa(s.Port))
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := net.Dialer{Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: strings.Trim(s.Host, "[]"), MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	if s.Security == "ssl" {
		conn, err = tls.DialWithDialer(&dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("can't connect to the mail server %s: %s", addr, simplify(err))
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, tlsCfg.ServerName)
	if err != nil {
		conn.Close()
		return fmt.Errorf("the mail server didn't answer properly: %s", simplify(err))
	}
	defer c.Close()
	host, _ := os.Hostname()
	if host == "" || !strings.Contains(host, ".") {
		host = "localhost"
	}
	if err := c.Hello(host); err != nil {
		return fmt.Errorf("the mail server rejected the greeting: %s", simplify(err))
	}
	if s.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the mail server doesn't offer STARTTLS; choose SSL/TLS or None")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS failed: %s", simplify(err))
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, tlsCfg.ServerName)); err != nil {
			return fmt.Errorf("the mail server didn't accept the username and password: %s", simplify(err))
		}
	}
	if err := c.Mail(s.From); err != nil {
		return fmt.Errorf("the mail server refused the sender %s: %s", s.From, simplify(err))
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("the mail server refused the recipient %s: %s", r, simplify(err))
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("the mail server refused the message: %s", simplify(err))
	}
	msg := buildMessage(s.From, rcpts, subject, body, host)
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the mail server didn't accept the message: %s", simplify(err))
	}
	return c.Quit()
}

func buildMessage(from string, to []string, subject, body, host string) []byte {
	var b strings.Builder
	id := fmt.Sprintf("<%d.pbcm@%s>", time.Now().UnixNano(), host)
	headers := [][2]string{
		{"From", from}, {"To", strings.Join(to, ", ")}, {"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", time.Now().Format(time.RFC1123Z)}, {"Message-ID", id}, {"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"}, {"Content-Transfer-Encoding", "8bit"},
		{"Auto-Submitted", "auto-generated"},
	}
	for _, h := range headers {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		b.WriteString(line + "\r\n")
	}
	return []byte(b.String())
}

func simplify(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && strings.HasPrefix(msg, "dial tcp") {
		msg = msg[i+2:]
	}
	return msg
}
