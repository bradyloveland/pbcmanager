package backups

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// Kinds of cached sizes.
const (
	SizeDest   = "dest"   // key: destination ID
	SizeBackup = "backup" // key: job ID + ":" + destination ID
	SizeFolder = "folder" // key: client ID + ":" + path
)

// Space is a destination's datastore space.
type Space struct {
	Total   *int64 `json:"total"`
	Used    *int64 `json:"used"`
	Avail   *int64 `json:"avail"`
	Error   string `json:"error"`
	Checked int64  `json:"checked"`
	// FullSince is when it went over the nearly-full alert threshold.
	FullSince int64 `json:"full_since"`
}

// Percent is how full the datastore is, or -1 if unknown.
func (s Space) Percent() int {
	if s.Total == nil || s.Used == nil || *s.Total <= 0 {
		return -1
	}
	return int(*s.Used * 100 / *s.Total)
}

// Latest is the newest snapshot of a job on one destination.
type Latest struct {
	Bytes   *int64 `json:"bytes"`
	Time    int64  `json:"time"`
	Count   int    `json:"count"`
	Error   string `json:"error"`
	Checked int64  `json:"checked"`
}

// FolderKey is the cache key for a folder on a client.
func FolderKey(clientID, path string) string { return clientID + ":" + path }

// Tracker keeps destination space and latest backup sizes up to date, using
// the server's own proxmox-backup-client. (Folder sizes are measured on the
// clients and collected with their status.)
type Tracker struct {
	Store       *store.Store
	PBS         *PBS
	SpaceEvery  func() time.Duration
	BackupEvery func() time.Duration
	// OnSpace sees each new space reading before it's saved (alerts).
	OnSpace func(d *store.Destination, s *Space)
	Now     func() time.Time

	mu     sync.Mutex
	forced map[string]bool
	wake   chan struct{}
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Request asks for keys ("dest:<id>", "backup:<job>", "dest:*", "backup:*")
// to be checked soon.
func (t *Tracker) Request(keys ...string) {
	t.mu.Lock()
	if t.forced == nil {
		t.forced = map[string]bool{}
	}
	for _, k := range keys {
		t.forced[k] = true
	}
	t.mu.Unlock()
	select {
	case t.wakeCh() <- struct{}{}:
	default:
	}
}

func (t *Tracker) wakeCh() chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.wake == nil {
		t.wake = make(chan struct{}, 1)
	}
	return t.wake
}

func (t *Tracker) take(kind, id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	hit := t.forced[kind+":"+id] || t.forced[kind+":*"]
	delete(t.forced, kind+":"+id)
	return hit
}

// Loop checks what's due every 30 seconds (or sooner when requested).
func (t *Tracker) Loop(ctx context.Context) {
	wake := t.wakeCh()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		t.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-wake:
		}
	}
}

// Tick checks everything that's due once.
func (t *Tracker) Tick(ctx context.Context) {
	dests, err := t.Store.ListDestinations()
	if err != nil {
		return
	}
	now := t.now()
	byID := map[string]*store.Destination{}
	for _, d := range dests {
		byID[d.ID] = d
		var s Space
		t.Store.GetSize(SizeDest, d.ID, &s)
		if t.take("dest", d.ID) || now.Sub(time.Unix(s.Checked, 0)) >= t.SpaceEvery() {
			t.checkSpace(ctx, d, s)
		}
	}
	jobs, err := t.Store.ListJobs("")
	if err != nil {
		return
	}
	forcedAll := t.take("backup", "*")
	for _, j := range jobs {
		forced := t.take("backup", j.ID) || forcedAll
		for _, did := range j.Destinations {
			d := byID[did]
			if d == nil {
				continue
			}
			var l Latest
			key := j.ID + ":" + d.ID
			t.Store.GetSize(SizeBackup, key, &l)
			if forced || now.Sub(time.Unix(l.Checked, 0)) >= t.BackupEvery() {
				t.checkLatest(ctx, j, d, key)
			}
		}
	}
	t.mu.Lock()
	delete(t.forced, "dest:*")
	t.mu.Unlock()
}

func (t *Tracker) checkSpace(ctx context.Context, d *store.Destination, prev Space) {
	s := Space{Checked: t.now().Unix(), FullSince: prev.FullSince}
	u, err := t.PBS.Status(ctx, d)
	if errors.Is(err, ErrNoClient) {
		s.Error = "This server doesn't have proxmox-backup-client, so it can't check."
	} else if err != nil {
		s.Error = sentence(err)
	} else {
		s.Total, s.Used, s.Avail = u.Total, u.Used, u.Avail
	}
	if t.OnSpace != nil && s.Error == "" {
		t.OnSpace(d, &s)
	}
	if err := t.Store.PutSize(SizeDest, d.ID, s); err != nil {
		slog.Error("saving destination space", "err", err)
	}
}

func (t *Tracker) checkLatest(ctx context.Context, j *store.Job, d *store.Destination, key string) {
	l := Latest{Checked: t.now().Unix()}
	snaps, err := t.PBS.Snapshots(ctx, d, j.BackupID)
	if errors.Is(err, ErrNoClient) {
		l.Error = "This server doesn't have proxmox-backup-client, so it can't check."
	} else if err != nil {
		l.Error = sentence(err)
	} else {
		l.Count = len(snaps)
		if len(snaps) > 0 {
			l.Bytes, l.Time = snaps[0].Size, snaps[0].Time
		} else {
			zero := int64(0)
			l.Bytes = &zero
		}
	}
	t.Store.PutSize(SizeBackup, key, l)
}

// JobSizes summarises one job for the dashboard.
type JobSizes struct {
	FolderBytes    *int64            `json:"folder_bytes"`
	FolderComplete bool              `json:"folder_complete"`
	FolderMeasured int64             `json:"folder_measured"`
	Measuring      bool              `json:"measuring"`
	FolderErrors   []string          `json:"folder_errors"`
	BackupBytes    *int64            `json:"backup_bytes"` // newest snapshot on its first destination with one
	BackupTime     int64             `json:"backup_time"`
	Backups        map[string]Latest `json:"backups"` // by destination ID
}

// Summary is everything the dashboard shows about sizes.
type Summary struct {
	Jobs           map[string]*JobSizes `json:"jobs"`
	Destinations   map[string]Space     `json:"destinations"`
	FolderTotal    *int64               `json:"folder_total"`
	FolderPending  int                  `json:"folder_pending"`
	FolderMeasured int64                `json:"folder_measured"`
	BackupTotal    *int64               `json:"backup_total"`
	Measuring      bool                 `json:"measuring"`
}

// Summarise builds the dashboard summary from the cache.
func Summarise(st *store.Store) (*Summary, error) {
	jobs, err := st.ListJobs("")
	if err != nil {
		return nil, err
	}
	folders, _ := st.ListSizes(SizeFolder)
	backupsRaw, _ := st.ListSizes(SizeBackup)
	destsRaw, _ := st.ListSizes(SizeDest)
	dests, err := st.ListDestinations()
	if err != nil {
		return nil, err
	}
	out := &Summary{Jobs: map[string]*JobSizes{}, Destinations: map[string]Space{}}
	for _, d := range dests {
		if raw, ok := destsRaw[d.ID]; ok {
			var s Space
			json.Unmarshal(raw, &s)
			out.Destinations[d.ID] = s
		}
	}
	folder := func(key string) (bundle.FolderSize, bool) {
		var f bundle.FolderSize
		raw, ok := folders[key]
		if ok {
			json.Unmarshal(raw, &f)
		}
		return f, ok
	}
	// Totals count each folder once, even if several jobs back it up, and
	// skip folders inside another one on the same client.
	perClient := map[string][]string{}
	var backupTotal int64
	haveBackup := false
	for _, j := range jobs {
		js := &JobSizes{Backups: map[string]Latest{}, FolderErrors: []string{}, FolderComplete: true}
		var sum int64
		known := 0
		for _, sh := range j.Shares {
			perClient[j.ClientID] = append(perClient[j.ClientID], sh.Path)
			f, ok := folder(FolderKey(j.ClientID, sh.Path))
			if ok && f.Measuring {
				js.Measuring = true
			}
			if ok && f.Error != "" {
				js.FolderErrors = append(js.FolderErrors, sh.Path+": "+f.Error)
			}
			if ok && f.Bytes != nil {
				sum += *f.Bytes
				known++
				if js.FolderMeasured == 0 || f.Measured < js.FolderMeasured {
					js.FolderMeasured = f.Measured
				}
			} else {
				js.FolderComplete = false
			}
		}
		if known > 0 {
			js.FolderBytes = &sum
		}
		for _, did := range j.Destinations {
			var l Latest
			if raw, ok := backupsRaw[j.ID+":"+did]; ok {
				json.Unmarshal(raw, &l)
				js.Backups[did] = l
				if js.BackupBytes == nil && l.Bytes != nil && l.Error == "" {
					js.BackupBytes, js.BackupTime = l.Bytes, l.Time
				}
			}
		}
		if js.BackupBytes != nil {
			backupTotal += *js.BackupBytes
			haveBackup = true
		}
		out.Jobs[j.ID] = js
	}
	if haveBackup {
		out.BackupTotal = &backupTotal
	}
	var total int64
	measured := 0
	for clientID, paths := range perClient {
		for _, p := range topLevel(paths) {
			f, ok := folder(FolderKey(clientID, p))
			if ok && f.Measuring {
				out.Measuring = true
			}
			if ok && f.Bytes != nil {
				total += *f.Bytes
				measured++
				if out.FolderMeasured == 0 || f.Measured < out.FolderMeasured {
					out.FolderMeasured = f.Measured
				}
			} else {
				out.FolderPending++
			}
		}
	}
	if measured > 0 {
		out.FolderTotal = &total
	}
	return out, nil
}

// topLevel drops paths nested inside another path in the list.
func topLevel(paths []string) []string {
	sort.Strings(paths)
	var out []string
	for i, p := range paths {
		if i > 0 && p == paths[i-1] {
			continue
		}
		nested := false
		for _, q := range paths {
			if q != p && strings.HasPrefix(p, strings.TrimSuffix(q, "/")+"/") {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, p)
		}
	}
	return out
}

// sentence turns an error into something to show on its own.
func sentence(err error) string {
	msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(err.Error()), "Error:"))
	return capital(strings.TrimSuffix(msg, ".")) + "."
}
