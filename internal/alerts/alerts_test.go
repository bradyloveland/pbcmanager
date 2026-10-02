package alerts

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// miniSMTP accepts mail on localhost and keeps each message.
type miniSMTP struct {
	ln       net.Listener
	mu       sync.Mutex
	messages []string
	rcpts    [][]string
}

func newSMTP(t *testing.T) *miniSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &miniSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go m.handle(c)
		}
	}()
	return m
}

func (m *miniSMTP) port() int { return m.ln.Addr().(*net.TCPAddr).Port }

func (m *miniSMTP) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { c.Write([]byte(s + "\r\n")) }
	say("220 test ESMTP")
	var data []string
	var rcpts []string
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				m.mu.Lock()
				m.messages = append(m.messages, strings.Join(data, "\n"))
				m.rcpts = append(m.rcpts, rcpts)
				m.mu.Unlock()
				data, rcpts, inData = nil, nil, false
				say("250 OK")
			} else {
				data = append(data, strings.TrimPrefix(line, "."))
			}
			continue
		}
		switch verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); verb {
		case "EHLO":
			say("250-test")
			say("250 SIZE 10000000")
		case "HELO", "MAIL", "RSET", "NOOP":
			say("250 OK")
		case "RCPT":
			rcpts = append(rcpts, line)
			say("250 OK")
		case "DATA":
			inData = true
			say("354 go ahead")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

func (m *miniSMTP) all() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.messages...)
}

func TestClean(t *testing.T) {
	saved := Defaults()
	saved.Password = "old"
	in := Input{Settings: Settings{Enabled: true, Host: "smtp.example.net", Security: "starttls", From: "nas@example.net",
		To: "me@example.net, you@example.net", Username: "nas"}}
	s, err := Clean(in, saved)
	if err != nil {
		t.Fatal(err)
	}
	if s.Port != 587 || s.Password != "old" || len(s.Recipients()) != 2 || s.MissedGraceMinutes != 60 || s.UnreachableMinutes != 60 {
		t.Fatalf("got %+v", s)
	}
	in.Password = "new"
	if s, _ := Clean(in, saved); s.Password != "new" {
		t.Fatal("a new password replaces the old one")
	}
	in.Password, in.ClearPassword = "", true
	if s, _ := Clean(in, saved); s.Password != "" {
		t.Fatal("clear_password clears it")
	}
	for name, f := range map[string]func(*Input){
		"no host":           func(i *Input) { i.Host = "" },
		"bad sender":        func(i *Input) { i.From = "nas" },
		"no recipients":     func(i *Input) { i.To = " " },
		"bad recipient":     func(i *Input) { i.To = "me@example.net, nope" },
		"password in clear": func(i *Input) { i.Security = "none" },
		"bad security":      func(i *Input) { i.Security = "tls13" },
		"header injection":  func(i *Input) { i.From = "a@b.c\r\nBcc: x@y.z" },
		"short grace":       func(i *Input) { i.MissedGraceMinutes = 1 },
		"full at 100%":      func(i *Input) { i.FullPercent = 100 },
	} {
		bad := in
		bad.ClearPassword = false
		f(&bad)
		if _, err := Clean(bad, saved); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	off := Input{Settings: Settings{Enabled: false}}
	if _, err := Clean(off, saved); err != nil {
		t.Fatalf("turning alerts off needs nothing else: %v", err)
	}
}

func TestSendPlainSMTP(t *testing.T) {
	smtp := newSMTP(t)
	s := Settings{Host: "127.0.0.1", Port: smtp.port(), Security: "none", From: "nas@example.net", To: "a@example.net; b@example.net"}
	if err := Send(context.Background(), s, "Backup failed: médias", "Line one\nLine two", ""); err != nil {
		t.Fatal(err)
	}
	msgs := smtp.all()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Subject: =?utf-8?q?Backup_failed:_m=C3=A9dias?=") ||
		!strings.Contains(msgs[0], "Line two") || !strings.Contains(msgs[0], "To: a@example.net, b@example.net") {
		t.Fatalf("message:\n%s", strings.Join(msgs, "\n----\n"))
	}
	if len(smtp.rcpts[0]) != 2 {
		t.Fatal("both recipients")
	}
	s.Security = "starttls"
	if err := Send(context.Background(), s, "x", "y", ""); err == nil || !strings.Contains(err.Error(), "doesn't offer STARTTLS") {
		t.Fatalf("starttls on a server without it: %v", err)
	}
	s.Port = 1
	if err := Send(context.Background(), s, "x", "y", ""); err == nil || !strings.Contains(err.Error(), "can't connect") {
		t.Fatalf("closed port: %v", err)
	}
}

type env struct {
	n     *Notifier
	st    *store.Store
	sent  []string
	html  []string // the HTML body of each email ("" for plain text)
	mu    sync.Mutex
	clock time.Time
	logs  string
	fail  bool
}

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	st, err := store.Open(filepath.Join(dir, "pbcm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{st: st, clock: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), logs: dir}
	st.Now = func() time.Time { return e.clock }
	settings := Defaults()
	settings.Enabled, settings.Host, settings.From, settings.To = true, "smtp.example.net", "nas@example.net", "me@example.net"
	e.n = &Notifier{Store: st, Settings: func() Settings { return settings }, ServerName: func() string { return "backup-server" },
		PublicURL: func() string { return "https://backups.example.net/" },
		LogPath:   func(c, r string) string { return filepath.Join(dir, r+".log") }, Now: func() time.Time { return e.clock },
		Send: func(ctx context.Context, s Settings, subject, body, html string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.sent = append(e.sent, subject+"\n"+body)
			e.html = append(e.html, html)
			if e.fail {
				return context.DeadlineExceeded
			}
			return nil
		}}
	return e
}

func (e *env) messages() []string {
	e.n.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.sent...)
}

func client() *store.Client {
	return &store.Client{ID: "c1", Name: "NAS", Address: "192.0.2.20", Port: 22, Status: store.ClientReady, Timezone: "UTC"}
}

func TestRunAlerts(t *testing.T) {
	e := newEnv(t)
	c := client()
	os.WriteFile(filepath.Join(e.logs, "r1.log"), []byte("starting\nError: connection refused\n"), 0o600)
	code := 255
	failed := &store.Run{ClientID: "c1", Run: bundle.Run{ID: "r1", JobID: "j1", JobName: "media", DestinationName: "Home PBS", Trigger: "schedule",
		Status: bundle.Failed, Started: e.clock.Add(-time.Hour).Unix(), Ended: e.clock.Add(-50 * time.Minute).Unix(), ExitCode: &code,
		Summary: "Error: connection refused"}, CollectedAt: e.clock.Unix()}
	e.n.RunFinished(c, failed)
	e.n.RunFinished(c, failed) // collected twice: alerted once
	msgs := e.messages()
	if len(msgs) != 1 {
		t.Fatalf("want one alert, got %d", len(msgs))
	}
	m := msgs[0]
	for _, want := range []string{"[PBC Manager] Backup failed: media on NAS (reported late)", "reported late", "Exit code:    255",
		"Result:       Error: connection refused", "https://backups.example.net/#/activity/c1/r1", "Last lines of the log:", "starting"} {
		if !strings.Contains(m, want) {
			t.Errorf("alert missing %q:\n%s", want, m)
		}
	}
	ok := *failed
	ok.ID, ok.Status, ok.CollectedAt = "r2", bundle.Success, ok.Ended+30
	e.n.RunFinished(c, &ok)
	if len(e.messages()) != 1 {
		t.Fatal("successes aren't sent by default")
	}
	alerts, _ := e.st.ListAlerts(10)
	if len(alerts) != 1 || alerts[0].SentAt == 0 || alerts[0].Kind != "failed" {
		t.Fatalf("alert record %+v", alerts[0])
	}
}

func TestRunAlertShowsBackupFigures(t *testing.T) {
	e := newEnv(t)
	on := Defaults()
	on.Enabled, on.Host, on.From, on.To, on.OnSuccess = true, "smtp.example.net", "nas@example.net", "me@example.net", true
	e.n.Settings = func() Settings { return on }
	total, used, avail := int64(4<<40), int64(1<<40), int64(3<<40)
	e.st.PutSize(backups.SizeDest, "d1", backups.Space{Total: &total, Used: &used, Avail: &avail, Checked: 1})
	run := &store.Run{ClientID: "c1", Run: bundle.Run{ID: "r9", JobID: "j1", JobName: "media", DestinationID: "d1", DestinationName: "Home PBS",
		Trigger: "schedule", Status: bundle.Success, Started: e.clock.Add(-time.Hour).Unix(), Ended: e.clock.Add(-50 * time.Minute).Unix(),
		Summary: "Backup finished.", Stats: &bundle.Stats{Read: 25 << 30, Uploaded: 1 << 30, Compressed: 800 << 20, Reused: 24 << 30,
			Files: 1200, Changed: 35, Seconds: 600, Known: []string{"sizes", "reused", "files", "duration"},
			Archives: []bundle.ArchiveStats{{Name: "media", Read: 20 << 30, Uploaded: 1 << 30}, {Name: "photos", Read: 5 << 30}}}},
		CollectedAt: e.clock.Add(-50 * time.Minute).Unix()}
	e.n.RunFinished(client(), run)
	msgs := e.messages()
	if len(msgs) != 1 {
		t.Fatalf("want one email, got %d", len(msgs))
	}
	for _, want := range []string{"Data read:    25 GiB", "Uploaded:     1.0 GiB new data (800 MiB compressed)", "Reused:       24 GiB from the last backup (96%)",
		"Files:        1200, of which 35 new or changed", "Upload time:  10m0s", "media:", "photos:", "Space left:   3.0 TiB free of 4.0 TiB on the destination (25% used)"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("missing %q:\n%s", want, msgs[0])
		}
	}
}

func TestDeliveryErrorsAreRecorded(t *testing.T) {
	e := newEnv(t)
	e.fail = true
	e.n.RunFinished(client(), &store.Run{ClientID: "c1", Run: bundle.Run{ID: "r1", JobName: "media", Status: bundle.Failed}})
	e.messages()
	alerts, _ := e.st.ListAlerts(10)
	if len(alerts) != 1 || alerts[0].SentAt != 0 || alerts[0].Error == "" {
		t.Fatalf("failed delivery should be recorded: %+v", alerts[0])
	}
}

func TestMissedBackups(t *testing.T) {
	e := newEnv(t)
	c := client()
	c.LastContact = e.clock.Unix()
	if err := e.st.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	created := e.clock.AddDate(0, 0, -3)
	e.st.Now = func() time.Time { return created }
	job := &store.Job{ID: "j1", ClientID: "c1", Name: "media", BackupID: "nas", Shares: []bundle.Share{{Path: "/a", Archive: "a"}},
		Schedule: bundle.Schedule{Type: "daily", Time: "02:00", Days: []int{0, 1, 2, 3, 4, 5, 6}}, ChangeDetection: "metadata",
		Enabled: true, Destinations: []string{"d1"}}
	if err := e.st.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	e.st.Now = func() time.Time { return e.clock }
	// It ran yesterday at 02:00 but not today.
	yesterday := time.Date(2026, 9, 30, 2, 0, 5, 0, time.UTC).Unix()
	e.st.UpsertRun("c1", bundle.Run{ID: "20260930T020005-aaaaaa", JobID: "j1", Status: bundle.Success, Started: yesterday, Ended: yesterday + 60})
	e.n.Check()
	msgs := e.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Backup didn't run: media on NAS") || !strings.Contains(msgs[0], "Thu 1 Oct 2026 02:00 UTC") {
		t.Fatalf("missed alert: %v", msgs)
	}
	e.n.Check()
	if len(e.messages()) != 1 {
		t.Fatal("a missed time is reported once")
	}

	// Today's run happening means nothing is missed.
	e2 := newEnv(t)
	e2.st.CreateClient(c)
	e2.st.Now = func() time.Time { return created }
	e2.st.SaveJob(job)
	e2.st.Now = func() time.Time { return e2.clock }
	today := time.Date(2026, 10, 1, 2, 0, 3, 0, time.UTC).Unix()
	e2.st.UpsertRun("c1", bundle.Run{ID: "20261001T020003-bbbbbb", JobID: "j1", Status: bundle.Failed, Started: today, Ended: today + 5})
	e2.n.Check()
	if msgs := e2.messages(); len(msgs) != 0 {
		t.Fatalf("a run that happened isn't missed: %v", msgs)
	}

	// Without contact since the deadline, the server can't know yet.
	e3 := newEnv(t)
	quiet := client()
	quiet.LastContact = time.Date(2026, 10, 1, 2, 30, 0, 0, time.UTC).Unix()
	e3.st.CreateClient(quiet)
	e3.st.Now = func() time.Time { return created }
	e3.st.SaveJob(job)
	e3.st.Now = func() time.Time { return e3.clock }
	e3.n.Check()
	if msgs := e3.messages(); len(msgs) != 0 {
		t.Fatalf("no alert before the server has heard from the client: %v", msgs)
	}
}

func TestMissedUsesTheClientsTimeZone(t *testing.T) {
	e := newEnv(t)
	// 05:00 in Denver is 11:00 UTC. At 11:30 UTC today's run is still inside
	// the hour's grace; read in UTC instead, 05:00 UTC would look missed.
	e.clock = time.Date(2026, 10, 1, 11, 30, 0, 0, time.UTC)
	c := client()
	c.Timezone, c.LastContact = "America/Denver", e.clock.Unix()
	e.st.CreateClient(c)
	e.st.Now = func() time.Time { return e.clock.AddDate(0, 0, -3) }
	e.st.SaveJob(&store.Job{ID: "j1", ClientID: "c1", Name: "media", BackupID: "nas", Shares: []bundle.Share{{Path: "/a", Archive: "a"}},
		Schedule: bundle.Schedule{Type: "daily", Time: "05:00", Days: []int{0, 1, 2, 3, 4, 5, 6}}, ChangeDetection: "metadata",
		Enabled: true, Destinations: []string{"d1"}})
	e.st.Now = func() time.Time { return e.clock }
	yesterday := time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC).Unix()
	e.st.UpsertRun("c1", bundle.Run{ID: "20260930T110000-aaaaaa", JobID: "j1", Status: bundle.Success, Started: yesterday, Ended: yesterday + 9})
	e.n.Check()
	if msgs := e.messages(); len(msgs) != 0 {
		t.Fatalf("still inside the grace period in the client's zone: %v", msgs)
	}
	c.Timezone = "UTC"
	e.st.SaveClient(c)
	e.n.Check()
	if msgs := e.messages(); len(msgs) != 1 {
		t.Fatalf("in UTC the same schedule is overdue, so the zone really matters: %v", msgs)
	}
}

func TestUnreachableAndBack(t *testing.T) {
	e := newEnv(t)
	c := client()
	c.Status, c.StatusDetail = store.ClientUnreachable, "10.0.0.5 didn't answer on port 22."
	c.UnreachableSince = e.clock.Add(-30 * time.Minute).Unix()
	e.st.CreateClient(c)
	e.n.Check()
	if len(e.messages()) != 0 {
		t.Fatal("30 minutes is under the default hour")
	}
	e.clock = e.clock.Add(31 * time.Minute)
	e.n.Check()
	e.n.Check()
	msgs := e.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Can't reach NAS") || !strings.Contains(msgs[0], "didn't answer on port 22") {
		t.Fatalf("outage alert: %v", msgs)
	}
	e.n.ClientReachable(c, c.UnreachableSince)
	msgs = e.messages()
	if len(msgs) != 2 || !strings.Contains(msgs[1], "NAS can be reached again") {
		t.Fatalf("back online: %v", msgs)
	}
	// A short blip that was never reported gets no "back online" email.
	e.n.ClientReachable(c, c.UnreachableSince+999)
	if len(e.messages()) != 2 {
		t.Fatal("no recovery email for an unreported outage")
	}
}

func TestNothingWhenDisabled(t *testing.T) {
	e := newEnv(t)
	off := Defaults()
	e.n.Settings = func() Settings { return off }
	e.n.RunFinished(client(), &store.Run{ClientID: "c1", Run: bundle.Run{ID: "r1", Status: bundle.Failed}})
	if len(e.messages()) != 0 {
		t.Fatal("alerts are off")
	}
}

func TestDestinationNearlyFull(t *testing.T) {
	e := newEnv(t)
	d := &store.Destination{ID: "d1", Name: "Home PBS", Host: "192.0.2.10", Datastore: "store"}
	gib := int64(1) << 30
	since := e.n.Space(d, 80*gib, 100*gib, 0)
	if since != 0 || len(e.messages()) != 0 {
		t.Fatal("80% is under the default 90%")
	}
	since = e.n.Space(d, 93*gib, 100*gib, since)
	since = e.n.Space(d, 94*gib, 100*gib, since)
	msgs := e.messages()
	if since == 0 || len(msgs) != 1 || !strings.Contains(msgs[0], "Destination nearly full: Home PBS (93%)") ||
		!strings.Contains(msgs[0], "7.0 GiB free") {
		t.Fatalf("nearly full: %d %v", since, msgs)
	}
	// Hovering just under the threshold doesn't count as having room again.
	if again := e.n.Space(d, 89*gib, 100*gib, since); again != since {
		t.Fatal("89% is within the margin")
	}
	since = e.n.Space(d, 70*gib, 100*gib, since)
	msgs = e.messages()
	if since != 0 || len(msgs) != 2 || !strings.Contains(msgs[1], "has room again") {
		t.Fatalf("room again: %v", msgs)
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 50 << 20: "50 MiB", 3 << 40: "3.0 TiB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHTMLEmail(t *testing.T) {
	e := newEnv(t)
	c := client()
	c.Name = `NAS <script>alert(1)</script> & "co" 🚀`
	code := 1
	run := &store.Run{ClientID: "c1", Run: bundle.Run{ID: "r5", JobID: "j1", JobName: `media <b>bold</b>`, DestinationName: "Home PBS",
		Trigger: "schedule", Status: bundle.Failed, Started: e.clock.Add(-time.Hour).Unix(), Ended: e.clock.Add(-50 * time.Minute).Unix(),
		ExitCode: &code, Summary: "Error: <bad> & worse"}, CollectedAt: e.clock.Add(-50 * time.Minute).Unix()}
	os.WriteFile(filepath.Join(e.logs, "r5.log"), []byte("line <one>\nError: & two\n"), 0o600)
	e.n.RunFinished(c, run)
	e.messages()
	e.mu.Lock()
	html := e.html[0]
	e.mu.Unlock()
	for _, want := range []string{"BACKUP FAILED", "Backup failed", "#B3261E", "&lt;script&gt;alert(1)&lt;/script&gt; &amp; &#34;co&#34; 🚀",
		"media &lt;b&gt;bold&lt;/b&gt;", "Error: &lt;bad&gt; &amp; worse", "line &lt;one&gt;", `href="https://backups.example.net/#/activity/c1/r5"`, "View the full log",
		"Sent by PBC Manager on backup-server"} {
		if !strings.Contains(html, want) && !(want == "BACKUP FAILED" && strings.Contains(html, ">Backup failed<")) {
			t.Errorf("HTML missing %q", want)
		}
	}
	for _, bad := range []string{"<script>", "<b>bold", "<bad>", "<img", "@import", "http://", "<link"} {
		if strings.Contains(html, bad) {
			t.Errorf("HTML contains %q", bad)
		}
	}
	// Plain text only, when chosen.
	plain := Defaults()
	plain.Enabled, plain.Host, plain.From, plain.To, plain.PlainText = true, "smtp.example.net", "nas@example.net", "me@example.net", true
	e.n.Settings = func() Settings { return plain }
	run2 := *run
	run2.ID = "r6"
	e.n.RunFinished(c, &run2)
	e.messages()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.html) != 2 || e.html[1] != "" {
		t.Fatalf("plain-text only: %d emails, html %q", len(e.html), e.html[len(e.html)-1])
	}
}

func TestMultipartMessage(t *testing.T) {
	m := newSMTP(t)
	s := Settings{Host: "127.0.0.1", Port: m.port(), Security: "none", From: "nas@example.net", To: "me@example.net"}
	long := strings.Repeat("abcdefghij", 40) // a 400-character line
	html := "<!DOCTYPE html><p>" + long + " café</p>"
	if err := Send(context.Background(), s, "Hello", "Text "+long+" café", html); err != nil {
		t.Fatal(err)
	}
	msgs := m.all()
	if len(msgs) != 1 {
		t.Fatalf("messages: %d", len(msgs))
	}
	raw := msgs[0]
	if !strings.Contains(raw, "Content-Type: multipart/alternative; boundary=") || strings.Count(raw, "Content-Transfer-Encoding: quoted-printable") != 2 ||
		strings.Index(raw, "Content-Type: text/plain") > strings.Index(raw, "Content-Type: text/html") {
		t.Fatalf("structure:\n%s", raw)
	}
	for _, line := range strings.Split(raw, "\n") {
		if len(line) > 100 {
			t.Fatalf("line too long for SMTP (%d): %q", len(line), line)
		}
	}
	if !strings.Contains(raw, "caf=C3=A9") {
		t.Error("non-ASCII text is quoted-printable encoded")
	}
}

func TestClientUpdateWaiting(t *testing.T) {
	e := newEnv(t)
	on := Defaults()
	on.Enabled, on.Host, on.From, on.To, on.OnClientUpdate = true, "smtp.example.net", "nas@example.net", "me@example.net", true
	on.OnMissed, on.OnUnreachable = false, false
	e.n.Settings = func() Settings { return on }
	c := client()
	c.Status = store.ClientReady
	e.st.CreateClient(c)
	p := bundle.PackageInfo{Package: "proxmox-backup-client", Installed: "3.4.6-1", Candidate: "3.4.7-1", FromApt: true, Newer: true,
		AvailableSince: e.clock.Add(-10 * 24 * time.Hour).Unix()}
	e.st.PutSize(bundle.PackageCache, c.ID, p)
	e.n.Check()
	if len(e.messages()) != 0 {
		t.Fatal("10 days is under the default 14")
	}
	e.clock = e.clock.Add(5 * 24 * time.Hour)
	e.n.Check()
	e.n.Check()
	msgs := e.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Update waiting: proxmox-backup-client 3.4.7-1 on NAS") || !strings.Contains(msgs[0], "doesn't install packages itself") {
		t.Fatalf("waiting update: %v", msgs)
	}
	if strings.Contains(msgs[0], "new major version") {
		t.Fatal("3.4.7 isn't a major version")
	}
}
