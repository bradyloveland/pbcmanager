package server

import (
	"testing"

	"github.com/bradyloveland/pbcmanager/internal/clients/clienttest"
)

func TestClientPackageUpdate(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	host := clienttest.New(t)
	id := addClient(t, c, host)
	path := "/api/clients/" + id + "/package-update"

	// Nothing waiting yet.
	expect(t, c.post(path, map[string]any{"version": "3.4.2-1"}), 400, "isn't waiting any more")

	// Proxmox publishes 3.4.2; Check now notices it.
	host.OfferPackage("3.4.2-1")
	expect(t, c.post("/api/clients/"+id+"/check", nil), 200, "")
	expect(t, c.get("/api/clients/"+id), 200, `"candidate":"3.4.2-1"`)

	// A failed install says why and changes nothing.
	host.FailAptWith("E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 42 (unattended-upgr)")
	expect(t, c.post(path, map[string]any{"version": "3.4.2-1"}), 502, "Another program is installing updates on this machine")
	expect(t, c.get("/api/clients/"+id), 200, `"update_available":true`)

	// It installs, and the client's details are read again.
	host.FailAptWith("")
	r := c.post(path, map[string]any{"version": "3.4.2-1"})
	expect(t, r, 200, `"client_version":"3.4.2"`)
	expect(t, r, 200, `"update_available":false`)
	expect(t, c.post(path, map[string]any{"version": "3.4.2-1"}), 400, "isn't waiting any more")

	// A new major version isn't installed from here.
	host.OfferPackage("4.0.1-1")
	expect(t, c.post("/api/clients/"+id+"/check", nil), 200, "")
	expect(t, c.get("/api/clients/"+id), 200, `"candidate":"4.0.1-1"`)
	expect(t, c.post(path, map[string]any{"version": "4.0.1-1"}), 400, "new major version")
	expect(t, c.post("/api/clients/nope/package-update", map[string]any{"version": "1"}), 404, "")
}
