package runner

import (
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

// jobTotal is the size of a job's folders, measured once per run in the
// background and shared by its destinations.
type jobTotal struct {
	mu     sync.Mutex
	bytes  int64
	done   bool
	notify func()
	stop   chan struct{}
}

// measureTotal starts measuring a job's folders: the files a backup reads,
// staying on each folder's filesystem and leaving out the job's excludes.
// It runs at idle priority alongside the backup, so it doesn't delay it.
func (e *Env) measureTotal(job *bundle.Job) *jobTotal {
	t := &jobTotal{stop: make(chan struct{})}
	go func() {
		runtime.LockOSThread() // the priority below is per thread
		defer runtime.UnlockOSThread()
		lowerThreadPriority()
		var sum int64
		for _, s := range job.Shares {
			n, ok := countFolder(e.path(s.Path), job.Excludes, t.stop)
			if !ok {
				return
			}
			sum += n
		}
		t.mu.Lock()
		t.bytes, t.done = sum, true
		notify := t.notify
		t.mu.Unlock()
		if notify != nil {
			notify()
		}
	}()
	return t
}

func (t *jobTotal) get() (int64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bytes, t.done
}

func (t *jobTotal) onDone(f func()) {
	t.mu.Lock()
	t.notify = f
	t.mu.Unlock()
}

// countFolder adds up the sizes of the regular files under root, on root's
// filesystem, that the exclude patterns don't match. Hard-linked files count
// once. ok is false if stop was closed first.
func countFolder(root string, excludes []string, stop <-chan struct{}) (total int64, ok bool) {
	st, err := os.Stat(root)
	if err != nil {
		return 0, true
	}
	dev := deviceOf(st)
	pats := parseExcludes(excludes)
	seen := map[[2]uint64]bool{}
	n := 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if n++; n%512 == 0 {
			select {
			case <-stop:
				ok = false
				return filepath.SkipAll
			default:
			}
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel := strings.TrimPrefix(p, root)
		if root == "/" {
			rel = "/" + rel
		}
		if excluded(pats, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if info, err := d.Info(); err == nil && deviceOf(info) != dev {
				return filepath.SkipDir // another filesystem: the backup leaves it out
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if sys, isStat := info.Sys().(*syscall.Stat_t); isStat && sys.Nlink > 1 {
			key := [2]uint64{uint64(sys.Dev), uint64(sys.Ino)}
			if seen[key] {
				return nil
			}
			seen[key] = true
		}
		total += info.Size()
		return nil
	})
	select {
	case <-stop:
		return total, false
	default:
	}
	return total, true
}

// excludePattern is one --exclude line, matched the way
// proxmox-backup-client's patterns are: like .gitignore, relative to the
// folder. A leading / anchors it to the folder; a pattern without a / matches
// a name at any depth; ** matches any number of folders; a trailing / matches
// folders only. Patterns starting with ! (re-include) are ignored here.
type excludePattern struct {
	parts   []string
	anyDeep bool
	dirOnly bool
}

func parseExcludes(lines []string) []excludePattern {
	var out []excludePattern
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		p := excludePattern{}
		if strings.HasSuffix(l, "/") {
			p.dirOnly = true
			l = strings.TrimRight(l, "/")
		}
		if strings.HasPrefix(l, "/") {
			l = strings.TrimLeft(l, "/")
		} else if !strings.Contains(l, "/") {
			p.anyDeep = true
		}
		if l == "" {
			continue
		}
		p.parts = strings.Split(l, "/")
		out = append(out, p)
	}
	return out
}

// excluded reports whether rel (a path inside the folder, starting with /)
// matches one of the patterns.
func excluded(pats []excludePattern, rel string, isDir bool) bool {
	segs := strings.Split(strings.Trim(rel, "/"), "/")
	for _, p := range pats {
		if p.dirOnly && !isDir {
			continue
		}
		if p.anyDeep {
			if ok, _ := path.Match(p.parts[0], segs[len(segs)-1]); ok {
				return true
			}
			continue
		}
		if matchParts(p.parts, segs) {
			return true
		}
	}
	return false
}

func matchParts(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchParts(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], segs[0]); !ok {
		return false
	}
	return matchParts(pat[1:], segs[1:])
}

// previousSize is the amount of data the last successful run of a job to a
// destination read, or 0.
func (e *Env) previousSize(jobID, destID, skip string) int64 {
	for _, id := range e.runIDs() { // newest first
		if id == skip {
			continue
		}
		r, err := e.loadRun(id)
		if err != nil || r.JobID != jobID || r.DestinationID != destID || r.Status != bundle.Success {
			continue
		}
		if r.Stats != nil && r.Stats.Read > 0 {
			return r.Stats.Read
		}
	}
	return 0
}

// tracker follows proxmox-backup-client's output for one run and keeps the
// run's Progress up to date. It's the command's stdout and stderr, after the
// log file.
type tracker struct {
	mu       sync.Mutex
	e        *Env
	r        *bundle.Run
	total    *jobTotal
	previous int64
	buf      []byte
	finished int64 // bytes of the folders already done
	current  int64
	lastAt   time.Time
	lastN    int64
	closed   bool
}

func newTracker(e *Env, r *bundle.Run, job *bundle.Job, total *jobTotal) *tracker {
	t := &tracker{e: e, r: r, total: total, previous: e.previousSize(job.ID, r.DestinationID, r.ID)}
	r.Progress = &bundle.Progress{Folders: len(job.Shares)}
	t.update()
	total.onDone(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.update()
	})
	return t
}

func (t *tracker) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		t.line(string(t.buf[:i]))
		t.buf = t.buf[i+1:]
	}
	if len(t.buf) > 64<<10 { // no newline in sight: not a line we want
		t.buf = t.buf[:0]
	}
	return len(p), nil
}

func (t *tracker) line(l string) {
	if t.closed {
		return
	}
	kind, archive, n := bundle.ParseProgressLine(l)
	p := t.r.Progress
	switch kind {
	case bundle.LineFolder:
		if archive != p.Archive {
			p.Archive = archive
			if p.Folder < p.Folders {
				p.Folder++
			}
		}
	case bundle.LineProcessed:
		now := t.e.now()
		if !t.lastAt.IsZero() && now.After(t.lastAt) && n >= t.lastN {
			p.Rate = float64(n-t.lastN) / now.Sub(t.lastAt).Seconds()
		}
		t.current, t.lastAt, t.lastN = n, now, n
		p.At = now.Unix()
	case bundle.LineFinished:
		t.finished += n
		t.current, t.lastAt, t.lastN = 0, time.Time{}, 0
	default:
		return
	}
	t.update()
}

// update works out Done and Total and saves the run. Called with mu held.
func (t *tracker) update() {
	if t.closed {
		return
	}
	p := t.r.Progress
	p.Done = t.finished + t.current
	if n, done := t.total.get(); done {
		p.Total, p.TotalFrom, p.Measuring = n, "measured", false
	} else {
		p.Total, p.TotalFrom, p.Measuring = t.previous, "previous", true
		if t.previous == 0 {
			p.TotalFrom = ""
		}
	}
	_ = t.e.saveRun(t.r)
}

// close stops further saves; the run is about to be finished.
func (t *tracker) close() {
	t.mu.Lock()
	t.closed = true
	t.r.Progress = nil
	t.mu.Unlock()
}
