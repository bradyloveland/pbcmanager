package server

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/clients/clienttest"
)

// mailbox is a tiny SMTP server that keeps every message.
type mailbox struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
}

func newMailbox(t *testing.T) *mailbox {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &mailbox{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(s string) { c.Write([]byte(s + "\r\n")) }
				say("220 test")
				var data []string
				in := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if in {
						if line == "." {
							m.mu.Lock()
							m.msgs = append(m.msgs, strings.Join(data, "\n"))
							m.mu.Unlock()
							data, in = nil, false
							say("250 OK")
						} else {
							data = append(data, line)
						}
						continue
					}
					switch strings.ToUpper(strings.SplitN(line, " ", 2)[0]) {
					case "EHLO":
						say("250 test")
					case "DATA":
						in = true
						say("354 go")
					case "QUIT":
						say("221 bye")
						return
					default:
						say("250 OK")
					}
				}
			}(c)
		}
	}()
	return m
}

func (m *mailbox) port() int { return m.ln.Addr().(*net.TCPAddr).Port }

func (m *mailbox) all() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.msgs...)
}

func TestAlertSettingsAndFailureEmail(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	mail := newMailbox(t)

	r := c.get("/api/alerts/settings")
	expect(t, r, 200, `"on_failure":true`)
	if r.data["settings"].(map[string]any)["enabled"] != false {
		t.Fatal("alerts start off")
	}
	settings := map[string]any{"enabled": true, "host": "127.0.0.1", "port": mail.port(), "security": "none",
		"from": "nas@example.net", "to": "me@example.net", "on_failure": true, "on_missed": true, "on_unreachable": true}
	expect(t, c.put("/api/alerts/settings", map[string]any{"enabled": true, "host": "", "from": "x"}), 400, "Enter your mail server")
	r = c.put("/api/alerts/settings", settings)
	expect(t, r, 200, `"password_set":false`)
	expect(t, c.post("/api/alerts/test", settings), 200, "")
	if msgs := mail.all(); len(msgs) != 1 || !strings.Contains(msgs[0], "Test email from nas") {
		t.Fatalf("test email: %v", msgs)
	}

	// The password is write-only and stored encrypted.
	withPW := map[string]any{}
	for k, v := range settings {
		withPW[k] = v
	}
	withPW["security"], withPW["username"], withPW["password"] = "starttls", "nas", "smtp-secret"
	r = c.put("/api/alerts/settings", withPW)
	expect(t, r, 200, `"password_set":true`)
	if strings.Contains(r.body, "smtp-secret") || strings.Contains(c.get("/api/alerts/settings").body, "smtp-secret") {
		t.Fatal("SMTP password returned by the API")
	}
	var raw map[string]any
	e.st.GetSetting(alertsKey, &raw)
	if s := raw["password_sealed"].(string); s == "" || strings.Contains(s, "smtp-secret") {
		t.Fatalf("password not sealed: %v", raw)
	}
	expect(t, c.put("/api/alerts/settings", settings), 200, `"password_set":true`) // blank keeps it

	// A failed backup on a client sends an alert.
	expect(t, c.put("/api/settings", map[string]any{"values": map[string]any{"general.public_url": "https://backups.example.net"}}), 200, "")
	host := clienttest.New(t)
	clientID := addClient(t, c, host)
	host.Mkdir("/srv/fail-share")
	r = c.post("/api/destinations", map[string]any{"name": "PBS", "host": "192.0.2.10", "datastore": "store",
		"username": "nas@pbs", "token_name": "nas", "secret": "token-secret"})
	destID := r.data["destination"].(map[string]any)["id"].(string)
	r = c.post("/api/jobs", map[string]any{"client_id": clientID, "name": "shares", "shares": []map[string]any{{"path": "/srv/fail-share"}},
		"schedule": map[string]any{"type": "manual"}, "destinations": []string{destID}})
	jobID := r.data["job"].(map[string]any)["id"].(string)
	waitFor(t, "settings sent", 10*time.Second, func() bool {
		return c.get("/api/clients/" + clientID).data["client"].(map[string]any)["settings_pending"] == false
	})
	expect(t, c.post("/api/jobs/"+jobID+"/run", nil), 200, "")
	waitFor(t, "the failure alert", 20*time.Second, func() bool { return len(mail.all()) == 2 })
	msg := mail.all()[1]
	for _, want := range []string{"Backup failed: shares on NAS", "Error: connection refused", "https://backups.example.net/#/activity/" + clientID + "/"} {
		if !strings.Contains(msg, want) {
			t.Errorf("alert missing %q:\n%s", want, msg)
		}
	}
	r = c.get("/api/alerts")
	expect(t, r, 200, `"kind":"failed"`)
	if a := r.data["alerts"].([]any)[0].(map[string]any); a["sent_at"].(float64) == 0 || a["error"] != "" {
		t.Fatalf("alert record %v", a)
	}
}
