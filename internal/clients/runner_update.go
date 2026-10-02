package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/release"
	"github.com/bradyloveland/pbcmanager/internal/runner"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// serverRunner is this server's copy of pbcm-runner and, for a signed
// release, the manifest that lets clients check it.
type serverRunner struct {
	data      []byte
	hash      string
	manifest  []byte
	signature []byte
	signed    bool
}

// ownRunner is this server's pbcm-runner for clients of a CPU type.
func (m *Manager) ownRunner(arch string) *serverRunner {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if r := m.runnerCache[arch]; r != nil {
		return r
	}
	r := &serverRunner{}
	path := m.runnerFile(arch)
	data, err := os.ReadFile(path)
	if err != nil {
		return r // not cached: it may appear later
	}
	r.data, r.hash = data, release.Hash(data)
	dir := filepath.Dir(path)
	r.manifest, _ = os.ReadFile(filepath.Join(dir, "MANIFEST"))
	r.signature, _ = os.ReadFile(filepath.Join(dir, "MANIFEST.sig"))
	if man, err := release.Verify(r.manifest, r.signature, release.Keys); err == nil && man.Check(filepath.Base(path), data) == nil {
		r.signed = true
	}
	if m.runnerCache == nil {
		m.runnerCache = map[string]*serverRunner{}
	}
	m.runnerCache[arch] = r
	return r
}

// archOf is a client's CPU type, from what it reported ("x86_64" before
// it was known).
func archOf(c *store.Client) string {
	if a := clientArch(c.Arch); a != "" {
		return a
	}
	return ArchAMD64
}

// Runner states shown for each client.
const (
	RunnerCurrent  = "current"
	RunnerUpdating = "updating"
	RunnerOutdated = "outdated" // will be updated when reachable
	RunnerRepair   = "repair"   // needs Repair: too old to self-update, or this server isn't a signed release
	RunnerUnknown  = "unknown"
)

// RunnerState says whether a client's pbcm-runner matches the server's.
func (m *Manager) RunnerState(clientID string) string {
	m.busyMu.Lock()
	hash, seen := m.runnerHashes[clientID]
	updating := m.runnerUpdating[clientID]
	m.busyMu.Unlock()
	arch := ArchAMD64
	if c, err := m.store.GetClient(clientID); err == nil {
		arch = archOf(c)
	}
	own := m.ownRunner(arch)
	switch {
	case updating:
		return RunnerUpdating
	case !seen || own.hash == "":
		return RunnerUnknown
	case hash == own.hash:
		return RunnerCurrent
	case hash == "" || !own.signed:
		return RunnerRepair
	}
	return RunnerOutdated
}

// syncRunner records the client's runner and sends this server's if it differs.
func (m *Manager) syncRunner(ctx context.Context, c *store.Client, st *bundle.Status) {
	m.noteRunner(c.ID, st.Runner)
	own := m.ownRunner(archOf(c))
	if st.Runner == "" || own.hash == "" || st.Runner == own.hash || !own.signed {
		return
	}
	if m.askedRecently("runner:"+c.ID, time.Now()) {
		return
	}
	m.setRunnerUpdating(c.ID, true)
	defer m.setRunnerUpdating(c.ID, false)
	req, _ := json.Marshal(runner.UpdateRequest{Manifest: string(own.manifest), Signature: string(own.signature), Runner: own.data})
	out, err := m.runnerCommandInput(ctx, c, bytes.NewReader(req), "self-update")
	if err != nil {
		slog.Warn("couldn't update pbcm-runner on client", "client", c.Name, "err", err)
		return
	}
	var res struct{ Version, Hash string }
	if json.Unmarshal([]byte(out), &res) == nil && res.Hash == own.hash {
		m.busyMu.Lock()
		m.runnerHashes[c.ID] = res.Hash
		delete(m.measureAsked, "runner:"+c.ID)
		m.busyMu.Unlock()
		if fresh, err := m.store.GetClient(c.ID); err == nil {
			fresh.RunnerVersion = res.Version
			_ = m.store.SaveClient(fresh)
		}
		slog.Info("updated pbcm-runner on client", "client", c.Name, "version", res.Version)
	}
}

// noteRunner records which pbcm-runner a client has.
func (m *Manager) noteRunner(id, hash string) {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if m.runnerHashes == nil {
		m.runnerHashes, m.runnerUpdating = map[string]string{}, map[string]bool{}
	}
	m.runnerHashes[id] = hash
}

func (m *Manager) setRunnerUpdating(id string, on bool) {
	m.busyMu.Lock()
	m.runnerUpdating[id] = on
	m.busyMu.Unlock()
}
