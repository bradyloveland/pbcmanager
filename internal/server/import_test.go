package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/clients/clienttest"
)

const v1ConfigFile = `{
  "server": {"bind": "0.0.0.0", "port": 8099},
  "auth": {"username": "admin", "password_hash": "pbkdf2_sha256$310000$x$y"},
  "email": {"enabled": false, "host": "smtp.example.net", "from_addr": "nas@example.net", "to_addrs": "me@example.net"},
  "settings": {"keep_runs": 900},
  "targets": [{"id": "t1", "name": "Home PBS", "host": "192.0.2.10", "port": 8007, "datastore": "store",
               "username": "nas@pbs", "token_name": "nas", "secret": "token-secret"}],
  "jobs": [{"id": "j1", "name": "media", "target_id": "t1", "backup_id": "nas", "shares": [{"path": "/srv/media", "archive": "media"}],
            "schedule": {"type": "daily", "time": "02:00", "days": [0,1,2,3,4,5,6]}, "enabled": true}]
}`

func TestImportFrom1xAndExport(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	expect(t, c.get("/api/settings/export"), 401, "")
	c.login()
	host := clienttest.New(t)
	clientID := addClient(t, c, host)
	host.Mkdir("/srv/media")

	file := json.RawMessage(v1ConfigFile)
	expect(t, c.post("/api/settings/import/preview", map[string]any{"file": json.RawMessage(`{"hello":1}`)}), 400, "isn't a settings export")
	r := c.post("/api/settings/import/preview", map[string]any{"file": file})
	expect(t, r, 200, `"needs_client":true`)
	expect(t, r, 200, "Choose the client")
	r = c.post("/api/settings/import", map[string]any{"file": file, "client_id": clientID, "import_alerts": true, "import_settings": true})
	expect(t, r, 200, `"imported":true`)
	if strings.Contains(r.body, "token-secret") {
		t.Fatal("the reply contains a secret")
	}
	jobs := c.get("/api/jobs").data["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs: %v", jobs)
	}
	j := jobs[0].(map[string]any)
	if j["name"] != "media" || j["enabled"] != false || j["backup_id"] != "nas" {
		t.Fatalf("imported job: %v", j)
	}
	if v := c.get("/api/settings").data["values"].(map[string]any)["history.client_runs"]; v != float64(900) {
		t.Fatalf("settings: %v", v)
	}
	waitFor(t, "settings sent", 10*time.Second, func() bool {
		return c.get("/api/clients/" + clientID).data["client"].(map[string]any)["settings_pending"] == false
	})

	// The imported job is disabled, so it can't be run until it's enabled.
	jobID := j["id"].(string)
	expect(t, c.post("/api/jobs/"+jobID+"/run", nil), 400, "This job is disabled")
	expect(t, c.post("/api/jobs/"+jobID+"/enabled", map[string]any{}), 400, "Say whether")
	expect(t, c.post("/api/jobs/nope/enabled", map[string]any{"enabled": true}), 404, "")
	expect(t, c.post("/api/jobs/"+jobID+"/enabled", map[string]any{"enabled": true}), 200, `"enabled":true`)
	waitFor(t, "the schedule on the client", 10*time.Second, func() bool {
		_, err := os.Stat(host.Path("/etc/systemd/system/pbcm-job-" + jobID + ".timer"))
		return err == nil
	})
	expect(t, c.post("/api/jobs/"+jobID+"/enabled", map[string]any{"enabled": false}), 200, `"enabled":false`)
	waitFor(t, "the schedule to be removed", 10*time.Second, func() bool {
		_, err := os.Stat(host.Path("/etc/systemd/system/pbcm-job-" + jobID + ".timer"))
		return os.IsNotExist(err)
	})

	r = c.get("/api/settings/export")
	expect(t, r, 200, `"format":"pbcm-settings"`)
	if !strings.Contains(r.header.Get("Content-Disposition"), "pbcm-settings-nas-") {
		t.Fatalf("download name: %q", r.header.Get("Content-Disposition"))
	}
	if strings.Contains(r.body, "token-secret") {
		t.Fatal("the export contains a secret")
	}
	// Importing it straight back finds everything already here.
	r = c.post("/api/settings/import/preview", map[string]any{"file": json.RawMessage(r.body)})
	expect(t, r, 200, "Already on this server")
}
