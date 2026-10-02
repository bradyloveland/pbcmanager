package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/sshx"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// SyncConfig tunes how the server keeps clients in step.
type SyncConfig struct {
	LogDir string // finished run logs are saved here, per client
	// KeepRuns and KeepDays limit run history on clients; KeepServerRuns
	// limits it on the server.
	KeepRuns, KeepDays, KeepServerRuns func() int
	// ActiveEvery is how often a client with a running backup is checked;
	// IdleEvery how often the others are.
	ActiveEvery, IdleEvery time.Duration
	// OnFinished is called for every run that finished since the last check
	// (alerts hook in here).
	OnFinished func(c *store.Client, r *store.Run)
	// OnReachable is called when a client that couldn't be reached answers
	// again; since is when contact was lost.
	OnReachable func(c *store.Client, since int64)
	// FolderEvery is how often each backed-up folder's size is measured
	// again (0 for never, except when asked).
	FolderEvery func() time.Duration
}

// backOnline clears a client's outage, telling the alerts hook. The caller
// saves the client.
func (m *Manager) backOnline(c *store.Client) {
	if c.UnreachableSince == 0 {
		return
	}
	since := c.UnreachableSince
	c.UnreachableSince = 0
	if m.Sync.OnReachable != nil {
		snapshot := *c
		go m.Sync.OnReachable(&snapshot, since)
	}
}

func (m *Manager) syncLock(id string) *sync.Mutex {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if m.syncMus == nil {
		m.syncMus = map[string]*sync.Mutex{}
	}
	if m.syncMus[id] == nil {
		m.syncMus[id] = &sync.Mutex{}
	}
	return m.syncMus[id]
}

// Bundle builds what a client should have.
func (m *Manager) Bundle(clientID string) (bundle.Bundle, error) {
	jobs, err := m.store.ListJobs(clientID)
	if err != nil {
		return bundle.Bundle{}, err
	}
	list, err := m.store.ListDestinations()
	if err != nil {
		return bundle.Bundle{}, err
	}
	dests := map[string]*store.Destination{}
	for _, d := range list {
		dests[d.ID] = d
	}
	keepRuns, keepDays := 500, 90
	if m.Sync.KeepRuns != nil {
		keepRuns = m.Sync.KeepRuns()
	}
	if m.Sync.KeepDays != nil {
		keepDays = m.Sync.KeepDays()
	}
	return backups.BuildBundle(jobs, dests, keepRuns, keepDays), nil
}

// Pending reports whether a client is missing the latest settings.
func (m *Manager) Pending(c *store.Client) bool {
	b, err := m.Bundle(c.ID)
	return err == nil && b.Hash() != c.AppliedHash
}

// Apply sends a client its jobs, credentials and schedules.
func (m *Manager) Apply(ctx context.Context, clientID string) error {
	lock := m.syncLock(clientID)
	lock.Lock()
	defer lock.Unlock()
	return m.applyLocked(ctx, clientID)
}

func (m *Manager) applyLocked(ctx context.Context, clientID string) error {
	c, err := m.store.GetClient(clientID)
	if err != nil {
		return err
	}
	b, err := m.Bundle(clientID)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(b)
	out, err := m.runnerCommandInput(ctx, c, strings.NewReader(string(raw)), "apply")
	if fresh, gerr := m.store.GetClient(clientID); gerr == nil {
		c = fresh
	}
	if err != nil {
		c.ApplyError = err.Error()
		_ = m.store.SaveClient(c)
		return err
	}
	var res struct {
		Applied string `json:"applied"`
	}
	if json.Unmarshal([]byte(out), &res) != nil || res.Applied != b.Hash() {
		c.ApplyError = "the client didn't confirm the new settings"
		_ = m.store.SaveClient(c)
		return errors.New(c.ApplyError)
	}
	c.AppliedHash, c.ApplyError, c.LastContact = res.Applied, "", time.Now().Unix()
	slog.Info("settings sent to client", "client", c.Name, "jobs", len(b.Jobs))
	m.wake(c.ID) // check it again soon, which measures any new folders
	return m.store.SaveClient(c)
}

// ApplyAsync sends settings in the background; failures are recorded on the
// client and retried by the poller.
func (m *Manager) ApplyAsync(clientIDs ...string) {
	for _, id := range clientIDs {
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := m.Apply(ctx, id); err != nil {
				slog.Warn("couldn't send settings to client", "client", id, "err", err)
			}
		}(id)
	}
}

func (m *Manager) jobClient(jobID string) (*store.Job, *store.Client, error) {
	j, err := m.store.GetJob(jobID)
	if err != nil {
		return nil, nil, err
	}
	c, err := m.store.GetClient(j.ClientID)
	return j, c, err
}

// StartJob runs a job now. If the client doesn't have the latest settings
// yet, they're sent first.
func (m *Manager) StartJob(ctx context.Context, jobID string) error {
	j, c, err := m.jobClient(jobID)
	if err != nil {
		return err
	}
	if !j.Enabled {
		return &InputError{"This job is disabled, so it can't run. Enable it first."}
	}
	if m.Pending(c) {
		if err := m.Apply(ctx, c.ID); err != nil {
			return fmt.Errorf("couldn't send the latest settings to %s first: %w", c.Name, err)
		}
	}
	if _, err := m.runnerCommand(ctx, c, "start", j.ID); err != nil {
		return err
	}
	m.wake(c.ID)
	return nil
}

// CancelJob stops a running job.
func (m *Manager) CancelJob(ctx context.Context, jobID string) error {
	j, c, err := m.jobClient(jobID)
	if err != nil {
		return err
	}
	_, err = m.runnerCommand(ctx, c, "cancel", j.ID)
	m.wake(c.ID)
	return err
}

// RunLog reads part of a run's log: from the server's copy once the run has
// finished and been collected, otherwise from the client.
func (m *Manager) RunLog(ctx context.Context, clientID, runID string, offset int64) (*bundle.LogChunk, error) {
	r, err := m.store.GetRun(clientID, runID)
	if err != nil {
		return nil, err
	}
	if r.LogSaved {
		f, err := os.Open(m.logPath(clientID, runID))
		if err == nil {
			defer f.Close()
			st, _ := f.Stat()
			if offset < 0 || offset > st.Size() {
				offset = 0
			}
			f.Seek(offset, io.SeekStart)
			buf, _ := io.ReadAll(io.LimitReader(f, 512<<10))
			return &bundle.LogChunk{Text: strings.ToValidUTF8(string(buf), "�"), Offset: offset + int64(len(buf)),
				Size: st.Size(), Done: true}, nil
		}
	}
	c, err := m.store.GetClient(clientID)
	if err != nil {
		return nil, err
	}
	out, err := m.runnerCommand(ctx, c, "log", runID, strconv.FormatInt(offset, 10))
	if err != nil {
		return nil, err
	}
	var chunk bundle.LogChunk
	if err := json.Unmarshal([]byte(out), &chunk); err != nil {
		return nil, errors.New("pbcm-runner sent an unexpected reply")
	}
	return &chunk, nil
}

func (m *Manager) logPath(clientID, runID string) string {
	return filepath.Join(m.Sync.LogDir, clientID, runID+".log")
}

// SyncClient collects runs from a client, saves finished logs and re-sends
// settings if the client doesn't have the latest. It returns the runs that
// finished since the last check.
func (m *Manager) SyncClient(ctx context.Context, clientID string) ([]*store.Run, error) {
	lock := m.syncLock(clientID)
	lock.Lock()
	defer lock.Unlock()
	c, err := m.store.GetClient(clientID)
	if err != nil {
		return nil, err
	}
	since := c.RunCursor - 120 // overlap, so clock skew never drops a run
	if since < 0 {
		since = 0
	}
	out, err := m.runnerCommand(ctx, c, "status", strconv.FormatInt(since, 10))
	if err != nil {
		var hk *sshx.HostKeyChangedError
		if fresh, gerr := m.store.GetClient(clientID); gerr == nil {
			c = fresh
		}
		if errors.As(err, &hk) {
			c.Status, c.OfferedKey = store.ClientHostKeyChanged, sshx.FormatKey(hk.Offered)
			c.StatusDetail = "The client's SSH host key has changed. If it was reinstalled, check the new key and use Repair; otherwise, investigate before trusting it."
		} else if c.Status == store.ClientReady || c.Status == store.ClientUnreachable {
			c.Status, c.StatusDetail = store.ClientUnreachable, err.Error()
			if c.UnreachableSince == 0 {
				c.UnreachableSince = time.Now().Unix()
			}
		}
		_ = m.store.SaveClient(c)
		return nil, err
	}
	var st bundle.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return nil, errors.New("pbcm-runner sent an unexpected reply")
	}
	var finished []*store.Run
	for _, r := range st.Runs {
		prev, err := m.store.UpsertRun(c.ID, r)
		if err != nil {
			return nil, err
		}
		if r.Status != bundle.Running && prev != r.Status && (prev == "" || prev == bundle.Running) {
			if sr, err := m.store.GetRun(c.ID, r.ID); err == nil {
				finished = append(finished, sr)
			}
		}
	}
	// Save the full log of every finished run the server doesn't have yet.
	unsaved, _ := m.store.ListRuns(store.RunFilter{ClientID: c.ID, Statuses: []string{bundle.Success, bundle.Failed, bundle.Cancelled}, Limit: 200})
	for _, r := range unsaved {
		if !r.LogSaved {
			if err := m.saveLog(ctx, c, r); err != nil {
				slog.Debug("couldn't save run log", "run", r.ID, "err", err)
			}
		}
	}
	if fresh, gerr := m.store.GetClient(clientID); gerr == nil {
		c = fresh
	}
	if st.Now > c.RunCursor {
		c.RunCursor = st.Now
	}
	c.AppliedHash, c.LastContact = st.Applied, time.Now().Unix()
	if st.RunnerVersion != "" {
		c.RunnerVersion = st.RunnerVersion
	}
	if c.Status == store.ClientUnreachable {
		c.Status, c.StatusDetail = store.ClientReady, ""
	}
	m.backOnline(c)
	if err := m.store.SaveClient(c); err != nil {
		return nil, err
	}
	if m.Pending(c) && c.Status == store.ClientReady {
		if err := m.applyLocked(ctx, c.ID); err != nil {
			slog.Warn("couldn't send settings to client", "client", c.Name, "err", err)
		}
	}
	m.syncSizes(ctx, c, st.Sizes)
	if c.Status == store.ClientReady {
		m.syncRunner(ctx, c, &st)
		m.syncPackage(ctx, c, false)
	}
	if m.Sync.OnFinished != nil {
		for _, r := range finished {
			m.Sync.OnFinished(c, r)
		}
	}
	return finished, nil
}

func (m *Manager) saveLog(ctx context.Context, c *store.Client, r *store.Run) error {
	path := m.logPath(c.ID, r.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	var offset int64
	for i := 0; i < 400; i++ { // up to 200 MiB
		out, err := m.runnerCommand(ctx, c, "log", r.ID, strconv.FormatInt(offset, 10))
		if err != nil {
			f.Close()
			return err
		}
		var chunk bundle.LogChunk
		if err := json.Unmarshal([]byte(out), &chunk); err != nil {
			f.Close()
			return err
		}
		f.WriteString(chunk.Text)
		if chunk.Offset <= offset || chunk.Offset >= chunk.Size {
			break
		}
		offset = chunk.Offset
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return m.store.MarkLogSaved(c.ID, r.ID)
}

// wake makes the poller check a client soon (after Run now or Cancel).
func (m *Manager) wake(clientID string) {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if m.nextSync == nil {
		m.nextSync = map[string]time.Time{}
	}
	m.nextSync[clientID] = time.Now().Add(3 * time.Second)
}

// syncing marks a client whose check is in progress.
var syncing = time.Unix(1<<40, 0)

// Poll keeps every ready client in step until ctx ends.
func (m *Manager) Poll(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var lastPrune time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		list, err := m.store.ListClients()
		if err != nil {
			continue
		}
		now := time.Now()
		for _, c := range list {
			if c.Status != store.ClientReady && c.Status != store.ClientUnreachable {
				continue
			}
			m.busyMu.Lock()
			if m.nextSync == nil {
				m.nextSync = map[string]time.Time{}
			}
			if due, seen := m.nextSync[c.ID]; seen && now.Before(due) {
				m.busyMu.Unlock()
				continue
			}
			m.nextSync[c.ID] = syncing
			m.busyMu.Unlock()
			go func(c *store.Client) {
				cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				_, err := m.SyncClient(cctx, c.ID)
				every := m.Sync.IdleEvery
				if err == nil {
					if active, _ := m.store.ListRuns(store.RunFilter{ClientID: c.ID, Statuses: []string{bundle.Running}, Limit: 1}); len(active) > 0 || m.isMeasuring(c.ID) {
						every = m.Sync.ActiveEvery
					}
				}
				m.busyMu.Lock()
				if m.nextSync[c.ID].Equal(syncing) { // not woken early meanwhile
					m.nextSync[c.ID] = time.Now().Add(every)
				}
				m.busyMu.Unlock()
			}(c)
		}
		if keep := m.Sync.KeepServerRuns; keep != nil && now.Sub(lastPrune) > 10*time.Minute {
			lastPrune = now
			if old, err := m.store.PruneRuns(keep()); err == nil {
				for _, r := range old {
					os.Remove(m.logPath(r.ClientID, r.ID))
				}
			}
		}
	}
}

// syncSizes saves the folder sizes a client reported and asks it to measure
// the job folders that are new or out of date.
func (m *Manager) syncSizes(ctx context.Context, c *store.Client, sizes []bundle.FolderSize) {
	have := map[string]bundle.FolderSize{}
	measuring := false
	for _, f := range sizes {
		have[f.Path] = f
		measuring = measuring || f.Measuring
		if err := m.store.PutSize(backups.SizeFolder, backups.FolderKey(c.ID, f.Path), f); err != nil {
			slog.Error("saving folder size", "err", err)
		}
	}
	m.setMeasuring(c.ID, measuring)
	if m.Sync.FolderEvery == nil {
		return
	}
	every := m.Sync.FolderEvery()
	if every <= 0 {
		return
	}
	jobs, err := m.store.ListJobs(c.ID)
	if err != nil {
		return
	}
	now := time.Now()
	var due []string
	seen := map[string]bool{}
	for _, j := range jobs {
		for _, sh := range j.Shares {
			p := sh.Path
			if seen[p] {
				continue
			}
			seen[p] = true
			f, ok := have[p]
			if ok && (f.Measuring || now.Sub(time.Unix(f.Measured, 0)) < every) {
				continue
			}
			if m.askedRecently(c.ID+":"+p, now) {
				continue
			}
			due = append(due, p)
		}
	}
	if len(due) > 0 {
		if err := m.measure(ctx, c, due); err != nil {
			slog.Debug("couldn't start measuring folders", "client", c.Name, "err", err)
		} else {
			m.setMeasuring(c.ID, true)
			m.wake(c.ID)
		}
	}
}

// askedRecently stops the server asking for the same folder over and over
// when the client can't start measuring it.
func (m *Manager) askedRecently(key string, now time.Time) bool {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if m.measureAsked == nil {
		m.measureAsked = map[string]time.Time{}
	}
	if t, ok := m.measureAsked[key]; ok && now.Sub(t) < time.Hour {
		return true
	}
	m.measureAsked[key] = now
	return false
}

func (m *Manager) measure(ctx context.Context, c *store.Client, paths []string) error {
	_, err := m.runnerCommand(ctx, c, append([]string{"measure"}, paths...)...)
	if err != nil && strings.Contains(err.Error(), `unknown command "measure"`) {
		return &InputError{c.Name + " has an older pbcm-runner that can't measure folders. Use Repair on the client to update it."}
	}
	return err
}

// MeasureFolders asks a client to measure its job folders again now (all of
// them, or only jobID's), then collects what's finished.
func (m *Manager) MeasureFolders(ctx context.Context, clientID, jobID string) error {
	c, err := m.store.GetClient(clientID)
	if err != nil {
		return err
	}
	if c.Status != store.ClientReady {
		return &InputError{c.Name + " isn't ready, so its folders can't be measured right now."}
	}
	jobs, err := m.store.ListJobs(c.ID)
	if err != nil {
		return err
	}
	var paths []string
	seen := map[string]bool{}
	for _, j := range jobs {
		if jobID != "" && j.ID != jobID {
			continue
		}
		for _, sh := range j.Shares {
			if !seen[sh.Path] {
				seen[sh.Path] = true
				paths = append(paths, sh.Path)
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	if err := m.measure(ctx, c, paths); err != nil {
		return err
	}
	m.setMeasuring(c.ID, true)
	m.wake(c.ID)
	return nil
}

// A client measuring folders is checked as often as one running a backup,
// so the sizes show up soon after they're done.
func (m *Manager) setMeasuring(clientID string, on bool) {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	if m.measuring == nil {
		m.measuring = map[string]bool{}
	}
	m.measuring[clientID] = on
}

func (m *Manager) isMeasuring(clientID string) bool {
	m.busyMu.Lock()
	defer m.busyMu.Unlock()
	return m.measuring[clientID]
}
