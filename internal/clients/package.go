package clients

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// packageEvery is how often a client's package info is read.
const packageEvery = 24 * time.Hour

// Package returns what's known about a client's proxmox-backup-client.
func (m *Manager) Package(clientID string) *bundle.PackageInfo {
	var p bundle.PackageInfo
	if ok, err := m.store.GetSize(bundle.PackageCache, clientID, &p); !ok || err != nil {
		return nil
	}
	return &p
}

// syncPackage reads a client's package info if it's due (or force is set).
// Runners from before 2.1.0 don't have the command, which is fine.
func (m *Manager) syncPackage(ctx context.Context, c *store.Client, force bool) {
	prev := m.Package(c.ID)
	if !force && prev != nil && time.Since(time.Unix(prev.Checked, 0)) < packageEvery {
		return
	}
	out, err := m.runnerCommand(ctx, c, "package-info")
	if err != nil {
		if !strings.Contains(err.Error(), "unknown command") {
			slog.Debug("couldn't read the client's package info", "client", c.Name, "err", err)
		}
		return
	}
	var p bundle.PackageInfo
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		return
	}
	p.Checked = time.Now().Unix()
	if p.UpdateAvailable() {
		p.AvailableSince = p.Checked
		if prev != nil && prev.UpdateAvailable() && prev.Candidate == p.Candidate && prev.AvailableSince > 0 {
			p.AvailableSince = prev.AvailableSince
		}
	}
	if err := m.store.PutSize(bundle.PackageCache, c.ID, p); err != nil {
		slog.Error("saving package info", "err", err)
	}
}

// UpdatePackage upgrades a client's proxmox-backup-client to version, the
// update its package info shows, then reads the client's details again. The
// runner refuses a new major version and won't update during a backup.
func (m *Manager) UpdatePackage(ctx context.Context, id, version string) (*store.Client, error) {
	c, err := m.store.GetClient(id)
	if err != nil {
		return nil, err
	}
	if p := m.Package(c.ID); !p.UpdateAvailable() || p.Candidate != version {
		return c, &InputError{Message: "That update isn't waiting any more. Reload the page to see the latest."}
	} else if p.MajorUpdate() {
		return c, &InputError{Message: "This is a new major version, which usually comes with an upgrade of the operating system. Follow Proxmox's upgrade notes and update it on the machine itself."}
	}
	if !m.claim(c.ID) {
		return c, &InputError{Message: "The server is already working on this client. Try again in a minute."}
	}
	defer m.release(c.ID)
	if _, err := m.runnerCommand(ctx, c, "package-update", version); err != nil {
		if strings.Contains(err.Error(), "unknown command") {
			err = &InputError{Message: "This client's pbcm-runner is too old to install updates. It updates itself within a few minutes; try again then."}
		}
		m.syncPackage(ctx, c, true) // what's waiting may have changed
		return c, err
	}
	slog.Info("updated proxmox-backup-client", "client", c.Name, "version", version)
	return c, m.refresh(ctx, c)
}
