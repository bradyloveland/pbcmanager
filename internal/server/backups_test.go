package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/clients/clienttest"
)

// fakePBS answers the server's own status and snapshot list calls.
const fakePBS = `#!/bin/sh
[ "$(cat "$PBS_PASSWORD_FILE")" = "token-secret" ] || { echo "Error: permission check failed." >&2; exit 1; }
case "$1" in
status) echo '{"total":1000,"used":250,"avail":750}' ;;
snapshot) echo '[{"backup-type":"host","backup-id":"nas","backup-time":1790000000,"size":1234,"files":["media.pxar.didx"],"verification":{"state":"ok"}}]' ;;
esac
`

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// addClient sets up a fake client through the API and returns its ID.
func addClient(t *testing.T, c *client, host *clienttest.Host) string {
	t.Helper()
	r := c.post("/api/clients/probe", map[string]any{"address": "127.0.0.1", "port": host.Port()})
	expect(t, r, 200, "fingerprint")
	r = c.post("/api/clients", map[string]any{"name": "NAS", "address": "127.0.0.1", "port": host.Port(),
		"host_key": r.data["host_key"], "login": map[string]any{"user": "root", "password": clienttest.RootPassword}})
	expect(t, r, 200, "task")
	id := r.data["client"].(map[string]any)["id"].(string)
	task := r.data["task"].(string)
	waitFor(t, "setup", 10*time.Second, func() bool { return c.get("/api/tasks/" + task).data["done"] == true })
	if ok := c.get("/api/tasks/" + task).data["ok"]; ok != true {
		t.Fatalf("setup failed: %v", c.get("/api/tasks/"+task).body)
	}
	return id
}

func TestBackupsEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pbc")
	os.WriteFile(bin, []byte(fakePBS), 0o755)
	t.Setenv("PBCM_CLIENT", bin)
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	host := clienttest.New(t)
	clientID := addClient(t, c, host)
	host.Mkdir("/srv/media")
	host.WriteFile("/srv/media/song.flac", strings.Repeat("x", 4096))

	// Destinations: the secret is write-only.
	dest := map[string]any{"name": "Home PBS", "host": "192.0.2.10", "datastore": "store", "username": "nas@pbs",
		"token_name": "nas", "secret": "token-secret", "namespace": "clients/nas"}
	expect(t, c.post("/api/destinations/test", dest), 200, `"avail":750`)
	r := c.post("/api/destinations", dest)
	expect(t, r, 200, `"secret_set":true`)
	if strings.Contains(r.body, "token-secret") {
		t.Fatal("the API returned a secret")
	}
	destID := r.data["destination"].(map[string]any)["id"].(string)
	if raw, _ := e.st.RawDestinationSecret(destID); raw == "" || strings.Contains(raw, "token-secret") {
		t.Fatalf("destination secret stored in plain text: %q", raw)
	}
	expect(t, c.post("/api/destinations", dest), 400, "already used")
	wrong := map[string]any{}
	for k, v := range dest {
		wrong[k] = v
	}
	wrong["secret"] = "nope"
	expect(t, c.post("/api/destinations/test", wrong), 502, "permission check failed")

	// A job: validation, then it's sent to the client.
	job := map[string]any{"client_id": clientID, "name": "media", "shares": []map[string]any{{"path": "/srv/media"}},
		"excludes": "lost+found\n", "schedule": map[string]any{"type": "daily", "time": "02:00", "days": []int{0, 1, 2, 3, 4, 5, 6}},
		"destinations": []string{destID}}
	expect(t, c.post("/api/jobs", map[string]any{"client_id": clientID, "name": "x", "destinations": []string{destID}}), 400, "at least one folder")
	r = c.post("/api/jobs", job)
	expect(t, r, 200, `"backup_id":`)
	jobID := r.data["job"].(map[string]any)["id"].(string)
	if r.data["job"].(map[string]any)["next_run"] == nil {
		t.Fatal("a scheduled job should show its next run")
	}
	waitFor(t, "settings sent", 10*time.Second, func() bool {
		return c.get("/api/clients/" + clientID).data["client"].(map[string]any)["settings_pending"] == false
	})
	if _, err := os.Stat(host.Path("/etc/systemd/system/pbcm-job-" + jobID + ".timer")); err != nil {
		t.Fatal("the client should have a timer for the job")
	}
	expect(t, c.do("DELETE", "/api/destinations/"+destID, nil, nil), 409, "Still used by: media")

	// Run now: the poller collects the result a few seconds later.
	expect(t, c.post("/api/jobs/"+jobID+"/run", nil), 200, "")
	var runID string
	waitFor(t, "the run to be collected", 15*time.Second, func() bool {
		runs := c.get("/api/runs?job=" + jobID).data["runs"].([]any)
		if len(runs) == 0 {
			return false
		}
		run := runs[0].(map[string]any)["run"].(map[string]any)
		runID = run["id"].(string)
		return run["status"] == "success" && run["log_saved"] == true
	})
	r = c.get("/api/runs/" + clientID + "/" + runID + "/log?offset=0")
	expect(t, r, 200, "repo=nas@pbs!nas@192.0.2.10:8007:store ns=clients/nas")
	if r.data["done"] != true {
		t.Fatal("a finished run's log is complete")
	}
	expect(t, c.get("/api/runs/"+clientID+"/"+runID), 200, `"client_name":"NAS"`)

	// Snapshots come from the server's own client.
	r = c.get("/api/jobs/" + jobID + "/snapshots")
	expect(t, r, 200, `"group":"host/`)
	expect(t, r, 200, `"verified":"ok"`)

	// Sizes: destination space and the newest backup come from the
	// server's client; folder sizes are measured on the client.
	waitFor(t, "sizes", 15*time.Second, func() bool {
		r := c.get("/api/sizes")
		return strings.Contains(r.body, `"used":250`) && strings.Contains(r.body, `"backup_bytes":1234`) &&
			strings.Contains(r.body, `"folder_total":4096`)
	})
	host.WriteFile("/srv/media/more.flac", strings.Repeat("y", 1024))
	expect(t, c.post("/api/clients/"+clientID+"/measure", map[string]any{"job": jobID}), 200, "")
	waitFor(t, "the folder to be measured again", 15*time.Second, func() bool {
		return strings.Contains(c.get("/api/sizes").body, `"folder_total":5120`)
	})
	expect(t, c.post("/api/sizes/check", map[string]any{}), 200, "")

	// Editing keeps the key password unless replaced.
	edit := map[string]any{}
	for k, v := range job {
		edit[k] = v
	}
	edit["keyfile"], edit["keyfile_password"] = "/root/key.json", "kp"
	expect(t, c.put("/api/jobs/"+jobID, edit), 200, `"keyfile_password_set":true`)
	edit["keyfile_password"] = ""
	r = c.put("/api/jobs/"+jobID, edit)
	expect(t, r, 200, `"keyfile_password_set":true`)
	if strings.Contains(r.body, `"kp"`) {
		t.Fatal("key password returned")
	}

	expect(t, c.do("DELETE", "/api/jobs/"+jobID, nil, nil), 200, "")
	expect(t, c.do("DELETE", "/api/destinations/"+destID, nil, nil), 200, "")
	waitFor(t, "the job's timer to be removed", 10*time.Second, func() bool {
		_, err := os.Stat(host.Path("/etc/systemd/system/pbcm-job-" + jobID + ".timer"))
		return os.IsNotExist(err)
	})
}

func TestBackupEndpointsNeedSignIn(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	for _, p := range []string{"/api/destinations", "/api/jobs", "/api/runs", "/api/jobs/x/snapshots", "/api/sizes"} {
		expect(t, c.get(p), 401, "")
	}
	c.login()
	expect(t, c.post("/api/jobs/nope/run", nil), 404, "")
	expect(t, c.get("/api/runs/x/y/log"), 404, "")
}
