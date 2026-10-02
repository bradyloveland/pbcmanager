package server

import (
	"testing"

	"github.com/bradyloveland/pbcmanager/internal/clients/clienttest"
)

func TestClientFilesystems(t *testing.T) {
	e := newEnv(t, nil, true)
	c := e.client()
	c.login()
	host := clienttest.New(t)
	id := addClient(t, c, host)
	host.WriteFile("/proc/self/mountinfo", "22 1 179:2 / / rw - ext4 /dev/mmcblk0p2 rw\n24 22 0:21 / /proc rw - proc proc rw\n30 22 179:1 / /boot/firmware rw - vfat /dev/mmcblk0p1 rw\n")
	host.WriteFile("/var/swap", "")
	r := c.get("/api/clients/" + id + "/filesystems")
	expect(t, r, 200, `{"path":"/boot/firmware","type":"vfat","source":"/dev/mmcblk0p1","virtual":false}`)
	expect(t, r, 200, `{"path":"/proc","type":"proc","source":"proc","virtual":true}`)
	expect(t, r, 200, `"root_excludes":["/var/swap"]`)
	expect(t, c.get("/api/clients/nope/filesystems"), 404, "")
}
