package runner

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

func TestExcludePatterns(t *testing.T) {
	pats := parseExcludes([]string{"/var/swap", "lost+found", "**/.recycle", "*.tmp", "/var/cache/apt/archives", "cache/", "!/var/keep", "", "  "})
	for rel, want := range map[string]bool{
		"/var/swap":               true,
		"/srv/var/swap":           false, // anchored to the folder
		"/lost+found":             true,
		"/data/lost+found":        true, // a name: any depth
		"/a/b/.recycle":           true,
		"/.recycle":               true,
		"/x/file.tmp":             true,
		"/x/file.tmpl":            false,
		"/var/cache/apt/archives": true,
		"/var/cache/apt":          false,
		"/home/cache":             true, // a folder named cache
		"/var/keep":               false,
	} {
		if got := excluded(pats, rel, true); got != want {
			t.Errorf("%s: got %v", rel, got)
		}
	}
	if excluded(pats, "/home/cache", false) {
		t.Error("a trailing / only matches folders")
	}
}

func TestCountFolderUsesTheJobsExcludes(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/a.bin", strings.Repeat("x", 1000))
	write(t, root+"/sub/b.bin", strings.Repeat("x", 500))
	write(t, root+"/sub/skip.tmp", strings.Repeat("x", 9999))
	write(t, root+"/var/swap", strings.Repeat("x", 9999))
	os.Link(root+"/a.bin", root+"/sub/same.bin") // a hard link counts once
	os.Symlink(root+"/a.bin", root+"/link")      // a link isn't a file's data
	n, ok := countFolder(root, []string{"*.tmp", "/var/swap"}, make(chan struct{}))
	if !ok || n != 1500 {
		t.Fatalf("got %d %v", n, ok)
	}
	stop := make(chan struct{})
	close(stop)
	for i := 0; i < 600; i++ { // enough entries to notice stop
		write(t, root+"/many/"+strings.Repeat("f", 1+i%50)+string(rune('a'+i%26))+".x", "")
	}
	if _, ok := countFolder(root, nil, stop); ok {
		t.Fatal("stop should end the count")
	}
}

func TestTrackerFollowsTheClientsProgress(t *testing.T) {
	je := newJobEnv(t)
	clock := time.Unix(1_800_000_000, 0)
	je.Now = func() time.Time { return clock }
	job := &bundle.Job{ID: "j1", Shares: []bundle.Share{{Path: "/srv/media", Archive: "media"}, {Path: "/srv/docs", Archive: "docs"}}}
	// An earlier run to the same destination read 2 GiB.
	old := &bundle.Run{ID: "20260101T000000-aaaaaa", JobID: "j1", DestinationID: "d1", Status: bundle.Success, Stats: &bundle.Stats{Read: 2 << 30}}
	os.MkdirAll(je.runDir(old.ID), 0o700)
	je.saveRun(old)
	r := &bundle.Run{ID: "20260102T000000-bbbbbb", JobID: "j1", DestinationID: "d1", Status: bundle.Running}
	os.MkdirAll(je.runDir(r.ID), 0o700)
	total := &jobTotal{stop: make(chan struct{})}
	tr := newTracker(je.Env, r, job, total)
	saved := func() *bundle.Progress {
		t.Helper()
		got, err := je.loadRun(r.ID)
		if err != nil || got.Progress == nil {
			t.Fatalf("no progress saved: %+v %v", got, err)
		}
		return got.Progress
	}
	if p := saved(); p.Total != 2<<30 || p.TotalFrom != "previous" || !p.Measuring || p.Folders != 2 {
		t.Fatalf("until it's measured, the last run's size is the total: %+v", p)
	}
	feed := func(s string) { tr.Write([]byte(s)) }
	feed("Upload directory '/srv/media' to 'nas@pbs@pbs:store' as media.mpxar.didx\nprocessed 100 MiB in 1m, uploaded 10 MiB\n")
	clock = clock.Add(time.Minute)
	feed("processed 400 MiB in 2m, upl")
	feed("oaded 40 MiB\n")
	p := saved()
	if p.Archive != "media" || p.Folder != 1 || p.Done != 400<<20 || p.Rate != float64(300<<20)/60 {
		t.Fatalf("first folder: %+v", p)
	}
	// The measurement finishes: the total becomes exact.
	total.mu.Lock()
	total.bytes, total.done = 3<<30, true
	total.mu.Unlock()
	total.notify()
	if p := saved(); p.Total != 3<<30 || p.TotalFrom != "measured" || p.Measuring {
		t.Fatalf("measured: %+v", p)
	}
	feed("media.mpxar: had to backup 1 MiB of 2 MiB (compressed 1 MiB) in 1 s\n")
	feed("media.ppxar: had to backup 50 MiB of 1 GiB (compressed 40 MiB) in 130 s\n")
	feed("Upload directory '/srv/docs' to 'nas@pbs@pbs:store' as docs.pxar.didx\nprocessed 512 MiB in 1m, uploaded 1 MiB\n")
	if p := saved(); p.Archive != "docs" || p.Folder != 2 || p.Done != 1<<30+512<<20 {
		t.Fatalf("second folder adds to the first: %+v", p)
	}
	tr.close()
	feed("processed 1 GiB in 2m, uploaded 1 MiB\n")
	if got, _ := je.loadRun(r.ID); got.Progress.Done != 1<<30+512<<20 {
		t.Fatal("nothing is saved after close")
	}
	if r.Progress != nil {
		t.Fatal("a finished run has no progress")
	}
}

func TestRunRecordsNoProgressOnceFinished(t *testing.T) {
	je := newJobEnv(t)
	os.MkdirAll(je.path("/srv/media"), 0o755)
	os.MkdirAll(je.path("/root"), 0o755)
	os.WriteFile(je.path("/root/k.json"), []byte("{}"), 0o600)
	if err := je.apply(t, testBundle("/srv/media")); err != nil {
		t.Fatal(err)
	}
	if err := RunJob(je.Env, "j1", make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	for _, r := range readRuns(t, je) {
		if r.Progress != nil || r.Status != bundle.Success {
			t.Fatalf("finished run: %+v", r)
		}
	}
}
