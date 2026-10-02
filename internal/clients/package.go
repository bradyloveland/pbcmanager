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
