package migrate

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/alerts"
	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// A 1.x export as app.py 1.2.0 writes it (credentials stripped).
const v1Export = `{
  "format": "pbs-manager-config", "format_version": 1, "app_version": "1.2.0",
  "exported_at": "2026-09-30T12:00:00+00:00", "exported_from": "omv",
  "note": "Credentials are not included.",
  "settings": {"max_concurrent": 1, "keep_runs": 800},
  "server": {"bind": "0.0.0.0", "port": 8099, "tls_cert": "", "tls_key": "", "trusted_proxies": [], "base_path": ""},
  "email": {"enabled": true, "host": "smtp.example.net", "port": 587, "security": "starttls", "username": "nas",
            "from_addr": "nas@example.net", "to_addrs": "me@example.net", "notify_failure": true, "notify_success": false},
  "targets": [{"id": "a1b2c3d4e5f6", "name": "Home PBS", "host": "192.0.2.10", "port": 8007, "datastore": "backup-pool",
               "username": "omv@pbs", "token_name": "omv", "fingerprint": ""}],
  "jobs": [
    {"id": "111111111111", "name": "media", "target_id": "a1b2c3d4e5f6", "backup_id": "omv",
     "shares": [{"path": "/srv/media", "archive": "media"}, {"path": "/srv/photos"}], "excludes": ["*.tmp"],
     "schedule": {"type": "daily", "time": "01:30", "days": [0, 2, 4], "interval_hours": 1},
     "change_detection_mode": "data", "rate": "20MiB", "keyfile": "/root/pbs.key", "enabled": true},
    {"id": "222222222222", "name": "docs", "target_id": "a1b2c3d4e5f6", "backup_id": "omv",
     "shares": [{"path": "/srv/docs", "archive": "docs"}], "schedule": {"type": "hourly", "time": "00:15", "interval_hours": 6}}
  ]
}`

// A 1.x config.json: no format marker, credentials included.
const v1Config = `{
  "server": {"bind": "0.0.0.0", "port": 8099},
  "auth": {"username": "admin", "password_hash": "pbkdf2_sha256$310000$x$y", "totp_secret": "", "recovery_codes": []},
  "email": {"enabled": false, "host": "smtp.example.net", "username": "nas", "password": "mail-secret", "from_addr": "nas@example.net", "to_addrs": "me@example.net"},
  "settings": {"keep_runs": 500},
  "targets": [{"id": "t1", "name": "Home PBS", "host": "192.0.2.10", "port": 8007, "datastore": "backup-pool",
               "username": "omv@pbs", "token_name": "omv", "secret": "token-secret", "fingerprint": ""}],
  "jobs": [{"id": "j1", "name": "media", "target_id": "t1", "backup_id": "omv", "shares": [{"path": "/srv/media", "archive": "media"}],
            "keyfile": "/root/pbs.key", "keyfile_password": "key-secret"}]
}`

func testEnv(t *testing.T) (Env, *store.Client) {
	t.Helper()
	box, _ := secret.New(make([]byte, 32))
	st, err := store.Open(filepath.Join(t.TempDir(), "pbcm.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &store.Client{ID: "c1", Name: "OMV", Address: "192.0.2.20", Port: 22, Status: store.ClientReady, Hostname: "omv"}
	if err := st.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	return Env{Store: st, CurrentAlerts: alerts.Defaults(), DefaultBackupID: func(c *store.Client) string { return c.Hostname }}, c
}

func TestImportV1Export(t *testing.T) {
	env, c := testEnv(t)
	src, err := Parse([]byte(v1Export))
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind != KindV1Export || src.Version != "1.2.0" || src.From != "omv" {
		t.Fatalf("source: %+v", src)
	}
	// Without a client or secrets: the preview says what's needed.
	p, err := Prepare(env, src, Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready || !p.NeedsClient || !strings.Contains(strings.Join(p.Problems, "|"), "Choose the client") {
		t.Fatalf("preview: %+v", p)
	}
	if !p.Destinations[0].SecretNeeded || p.Destinations[0].Repository != "omv@pbs!omv@192.0.2.10:8007:backup-pool" {
		t.Fatalf("destination: %+v", p.Destinations[0])
	}
	if p.Alerts == nil || !p.Alerts.PasswordNeeded || len(p.Settings) != 1 {
		t.Fatalf("alerts and settings: %+v %v", p.Alerts, p.Settings)
	}
	opts := Options{ClientID: c.ID, ImportAlerts: true, ImportSettings: true}
	if p, _ := Prepare(env, src, opts, true); p.Ready || !strings.Contains(strings.Join(p.Problems, "|"), "token secret") {
		t.Fatalf("a missing secret stops the import: %+v", p.Problems)
	}
	opts.Secrets = map[string]string{"a1b2c3d4e5f6": "tok"}
	opts.AlertPassword = "mail"
	p, err = Prepare(env, src, opts, true)
	if err != nil || !p.Ready {
		t.Fatalf("ready: %v %+v", err, p.Problems)
	}
	if p.Jobs[0].KeyfilePassword != "needed" || p.Jobs[0].Enabled || p.Jobs[0].Client != "OMV" {
		t.Fatalf("job plan: %+v", p.Jobs[0])
	}
	r, err := Apply(env.Store, p)
	if err != nil || r.Destinations != 1 || r.Jobs != 2 || r.Alerts == nil || r.Alerts.Password != "mail" || r.Settings["history.client_runs"] != 800 {
		t.Fatalf("apply: %+v %v", r, err)
	}
	jobs, _ := env.Store.ListJobs(c.ID)
	byName := map[string]*store.Job{}
	for _, j := range jobs {
		byName[j.Name] = j
	}
	m := byName["media"]
	if m == nil || m.Enabled || m.BackupID != "omv" || m.Rate != "20MiB" || m.ChangeDetection != "data" || m.Keyfile != "/root/pbs.key" ||
		m.Schedule.Type != "daily" || m.Schedule.Time != "01:30" || len(m.Schedule.Days) != 3 || m.Shares[1].Archive != "photos" || m.Excludes[0] != "*.tmp" {
		t.Fatalf("media job: %+v", m)
	}
	if d := byName["docs"]; d == nil || d.Schedule.Type != "hourly" || d.Schedule.IntervalHours != 6 || len(d.Schedule.Days) != 7 {
		t.Fatalf("docs job: %+v", d)
	}
	dests, _ := env.Store.ListDestinations()
	if len(dests) != 1 || dests[0].Secret != "tok" || dests[0].Namespace != "" {
		t.Fatalf("destination: %+v", dests)
	}

	// Importing again finds everything already here.
	p, _ = Prepare(env, src, opts, true)
	if p.Destinations[0].Existing != "Home PBS" || p.Jobs[0].Skip == "" || p.Jobs[1].Skip == "" {
		t.Fatalf("second import: %+v %+v", p.Destinations, p.Jobs)
	}
}

func TestImportV1ConfigWithCredentials(t *testing.T) {
	env, c := testEnv(t)
	src, err := Parse([]byte(v1Config))
	if err != nil || src.Kind != KindV1Config {
		t.Fatalf("parse: %v %+v", err, src)
	}
	p, err := Prepare(env, src, Options{ClientID: c.ID, ImportAlerts: true, EnableSchedules: true}, true)
	if err != nil || !p.Ready {
		t.Fatalf("plan: %v %+v", err, p.Problems)
	}
	if !p.Destinations[0].SecretIncluded || p.Jobs[0].KeyfilePassword != "included" || !p.Alerts.PasswordIncluded || !p.Jobs[0].Enabled {
		t.Fatalf("credentials from config.json: %+v %+v %+v", p.Destinations[0], p.Jobs[0], p.Alerts)
	}
	r, _ := Apply(env.Store, p)
	if r.Alerts.Password != "mail-secret" {
		t.Fatal("mail password from config.json")
	}
	jobs, _ := env.Store.ListJobs(c.ID)
	if len(jobs) != 1 || jobs[0].KeyfilePassword != "key-secret" || !jobs[0].Enabled || jobs[0].Schedule.Type != "manual" {
		t.Fatalf("job: %+v", jobs[0])
	}
}

func TestExportRoundTripWithoutCredentials(t *testing.T) {
	env, c := testEnv(t)
	src, _ := Parse([]byte(v1Config))
	p, _ := Prepare(env, src, Options{ClientID: c.ID, EnableSchedules: true}, true)
	Apply(env.Store, p)

	a := alerts.Defaults()
	a.Password = "mail-secret"
	e, err := BuildExport(env.Store, map[string]any{"history.client_runs": 700}, a, "server1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(e)
	for _, s := range []string{"token-secret", "key-secret", "mail-secret", "password_hash"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("the export contains %q:\n%s", s, raw)
		}
	}

	// Into a new server where the client has been added again (new ID).
	env2, _ := testEnv(t)
	src2, err := Parse(raw)
	if err != nil || src2.Kind != KindExport {
		t.Fatalf("parse export: %v", err)
	}
	p2, _ := Prepare(env2, src2, Options{ImportSettings: true}, true)
	if p2.Ready || !strings.Contains(strings.Join(p2.Problems, "|"), "token secret") {
		t.Fatalf("secrets must be entered: %+v", p2.Problems)
	}
	p2, _ = Prepare(env2, src2, Options{ImportSettings: true, Secrets: map[string]string{e.Destinations[0].ID: "tok2"}}, true)
	if !p2.Ready || p2.NeedsClient || p2.Jobs[0].Client != "OMV" || p2.Jobs[0].KeyfilePassword != "needed" {
		t.Fatalf("export plan: %+v %+v", p2.Problems, p2.Jobs)
	}
	if r, err := Apply(env2.Store, p2); err != nil || r.Jobs != 1 || r.Settings["history.client_runs"] != 700 {
		t.Fatalf("apply: %+v %v", r, err)
	}

	// A job whose client isn't on the new server is skipped, saying why.
	env3, _ := testEnv(t)
	env3.Store.DeleteClient("c1")
	p3, _ := Prepare(env3, src2, Options{Secrets: map[string]string{e.Destinations[0].ID: "tok2"}}, true)
	if !strings.Contains(p3.Jobs[0].Skip, "Add the client OMV (192.0.2.20) first") {
		t.Fatalf("missing client: %+v", p3.Jobs[0])
	}
}

func TestParseRejectsOtherFiles(t *testing.T) {
	for _, raw := range []string{`not json`, `{"hello": 1}`, `{"format": "pbs-manager-config", "format_version": 2}`} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s should be refused", raw)
		}
	}
}

func TestNameClashesAreRenamed(t *testing.T) {
	env, c := testEnv(t)
	env.Store.SaveDestination(&store.Destination{ID: "x", Name: "Home PBS", Host: "198.51.100.1", Port: 8007, Datastore: "other",
		Username: "a@pbs", Secret: "s"})
	env.Store.SaveJob(&store.Job{ID: "jx", ClientID: c.ID, Name: "media", BackupID: "other", Destinations: []string{"x"}})
	src, _ := Parse([]byte(v1Config))
	p, _ := Prepare(env, src, Options{ClientID: c.ID}, true)
	if !p.Ready || p.Destinations[0].Name != "Home PBS (imported)" || p.Jobs[0].Name != "media (imported)" {
		t.Fatalf("renamed: %+v %+v %v", p.Destinations, p.Jobs, p.Problems)
	}
}
