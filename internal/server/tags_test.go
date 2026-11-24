package server

import (
	"testing"

	"github.com/bradyloveland/pbcmanager/internal/clients/clienttest"
)

func TestClientTagsEndpoint(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	host := clienttest.New(t)
	id := addClient(t, c, host)
	path := "/api/clients/" + id + "/tags"

	expect(t, c.get("/api/clients/"+id), 200, `"tags":[]`)
	expect(t, c.put(path, map[string]any{"tags": []string{" home  lab", "NAS", "nas"}}), 200, `"tags":["home lab","NAS"]`)
	expect(t, c.get("/api/clients"), 200, `"tags":["home lab","NAS"]`)
	// A check-in doesn't undo them.
	expect(t, c.post("/api/clients/"+id+"/check", nil), 200, "")
	expect(t, c.get("/api/clients/"+id), 200, `"tags":["home lab","NAS"]`)

	expect(t, c.put(path, map[string]any{"tags": []string{"a,b"}}), 400, "can't contain commas")
	expect(t, c.put(path, map[string]any{"tags": []string{}}), 200, `"tags":[]`)
	expect(t, c.put("/api/clients/nope/tags", map[string]any{"tags": []string{"x"}}), 404, "")
}
