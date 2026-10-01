package clients

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Task is a long-running job on a client (setup, repair) whose output the
// browser follows.
type Task struct {
	ID       string
	ClientID string
	Kind     string
	seq      int

	mu       sync.Mutex
	lines    []string
	partial  string
	done     bool
	ok       bool
	errMsg   string
	finished time.Time
}

// TaskView is a snapshot of a task from a line offset.
type TaskView struct {
	ID       string   `json:"id"`
	ClientID string   `json:"client_id"`
	Kind     string   `json:"kind"`
	Lines    []string `json:"lines"`
	Offset   int      `json:"offset"`
	Done     bool     `json:"done"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error"`
}

// Logf adds a line.
func (t *Task) Logf(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
}

// Write lets a task collect command output, line by line.
func (t *Task) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := t.partial + strings.ReplaceAll(string(p), "\r", "")
	parts := strings.Split(text, "\n")
	t.partial = parts[len(parts)-1]
	t.lines = append(t.lines, parts[:len(parts)-1]...)
	if len(t.lines) > 5000 {
		t.lines = append([]string{"… earlier output trimmed …"}, t.lines[len(t.lines)-4000:]...)
	}
	return len(p), nil
}

func (t *Task) finish(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.partial != "" {
		t.lines = append(t.lines, t.partial)
		t.partial = ""
	}
	t.done, t.ok, t.finished = true, err == nil, time.Now()
	if err != nil {
		t.errMsg = err.Error()
	}
}

// View returns the lines from offset on.
func (t *Task) View(offset int) TaskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	if offset < 0 || offset > len(t.lines) {
		offset = 0
	}
	return TaskView{ID: t.ID, ClientID: t.ClientID, Kind: t.Kind, Lines: append([]string{}, t.lines[offset:]...),
		Offset: len(t.lines), Done: t.done, OK: t.ok, Error: t.errMsg}
}

// LastError returns the last line starting with "ERROR:", for summaries.
func (t *Task) LastError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.lines) - 1; i >= 0; i-- {
		if s, ok := strings.CutPrefix(t.lines[i], "ERROR: "); ok {
			return s
		}
	}
	return ""
}

type tasks struct {
	mu    sync.Mutex
	items map[string]*Task
	seq   int
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (ts *tasks) start(clientID, kind string) *Task {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for id, t := range ts.items {
		t.mu.Lock()
		old := t.done && time.Since(t.finished) > time.Hour
		t.mu.Unlock()
		if old {
			delete(ts.items, id)
		}
	}
	ts.seq++
	t := &Task{ID: newID(), ClientID: clientID, Kind: kind, seq: ts.seq}
	ts.items[t.ID] = t
	return t
}

func (ts *tasks) get(id string) *Task {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.items[id]
}

// latest returns the newest task for a client, if one is still kept.
func (ts *tasks) latest(clientID string) *Task {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var best *Task
	for _, t := range ts.items {
		if t.ClientID == clientID && (best == nil || t.seq > best.seq) {
			best = t
		}
	}
	return best
}
