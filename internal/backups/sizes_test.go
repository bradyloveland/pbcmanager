package backups

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/secret"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// The fake client reports whatever "used" says, and one snapshot per job.
const fakeSizesPBS = `#!/bin/sh
dir=$(dirname "$0")
case "$1" in
status) echo "{\"total\":1000,\"used\":$(cat "$dir/used"),\"avail\":100}" ;;
snapshot) [ "$3" = "host/gone" ] && { echo "Error: no such group" >&2; exit 1; }
  echo '[{"backup-time":1790000500,"size":700},{"backup-time":1790000000,"size":600}]' ;;
esac
`

func sizesEnv(t *testing.T) (*store.Store, *Tracker, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pbc")
	os.WriteFile(bin, []byte(fakeSizesPBS), 0o755)
	os.WriteFile(filepath.Join(dir, "used"), []byte("500"), 0o600)
	box, _ := secret.New(make([]byte, 32))
	st, err := store.Open(filepath.Join(dir, "db.sqlite"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SaveDestination(&store.Destination{ID: "d1", Name: "Home PBS", Host: "192.0.2.10", Port: 8007, Datastore: "store",
		Username: "nas@pbs", TokenName: "nas", Secret: "tok"})
	st.CreateClient(&store.Client{ID: "c1", Name: "NAS", Address: "192.0.2.20", Port: 22})
	for _, j := range []*store.Job{
		{ID: "j1", ClientID: "c1", Name: "media", BackupID: "nas", Shares: []bundle.Share{{Path: "/srv", Archive: "srv"}, {Path: "/srv/media", Archive: "media"}},
			Schedule: bundle.Schedule{Type: "manual"}, Enabled: true, Destinations: []string{"d1"}},
		{ID: "j2", ClientID: "c1", Name: "old", BackupID: "gone", Shares: []bundle.Share{{Path: "/srv/media", Archive: "media"}},
			Schedule: bundle.Schedule{Type: "manual"}, Enabled: true, Destinations: []string{"d1"}},
	} {
		if err := st.SaveJob(j); err != nil {
			t.Fatal(err)
		}
	}
	tr := &Tracker{Store: st, PBS: &PBS{Bin: bin},
		SpaceEvery: func() time.Duration { return 15 * time.Minute }, BackupEvery: func() time.Duration { return time.Hour }}
	return st, tr, dir
}

func TestTrackerChecksSpaceAndBackups(t *testing.T) {
	st, tr, dir := sizesEnv(t)
	clock := time.Unix(1790001000, 0)
	tr.Now = func() time.Time { return clock }
	var seen []int
	tr.OnSpace = func(d *store.Destination, s *Space) { seen = append(seen, s.Percent()) }
	tr.Tick(context.Background())
	sum, err := Summarise(st)
	if err != nil {
		t.Fatal(err)
	}
	sp := sum.Destinations["d1"]
	if sp.Error != "" || *sp.Used != 500 || sp.Percent() != 50 || len(seen) != 1 {
		t.Fatalf("space: %+v %v", sp, seen)
	}
	j1 := sum.Jobs["j1"]
	if *j1.BackupBytes != 700 || j1.BackupTime != 1790000500 || j1.Backups["d1"].Count != 2 {
		t.Fatalf("latest backup: %+v", j1)
	}
	if e := sum.Jobs["j2"].Backups["d1"].Error; !strings.Contains(e, "No such group") {
		t.Fatalf("snapshot error: %q", e)
	}
	if *sum.BackupTotal != 700 {
		t.Fatalf("backup total: %v", *sum.BackupTotal)
	}

	// Nothing's due five minutes later, unless asked.
	os.WriteFile(filepath.Join(dir, "used"), []byte("950"), 0o600)
	clock = clock.Add(5 * time.Minute)
	tr.Tick(context.Background())
	if len(seen) != 1 {
		t.Fatal("space was checked before it was due")
	}
	tr.Request("dest:d1")
	tr.Tick(context.Background())
	if len(seen) != 2 || seen[1] != 95 {
		t.Fatalf("requested check: %v", seen)
	}
	clock = clock.Add(16 * time.Minute)
	tr.Tick(context.Background())
	if len(seen) != 3 {
		t.Fatal("space is checked again once it's due")
	}
}

func TestSummaryCountsNestedAndSharedFoldersOnce(t *testing.T) {
	st, _, _ := sizesEnv(t)
	n := func(v int64) *int64 { return &v }
	st.PutSize(SizeFolder, FolderKey("c1", "/srv"), bundle.FolderSize{Path: "/srv", Bytes: n(1000), Measured: 100})
	st.PutSize(SizeFolder, FolderKey("c1", "/srv/media"), bundle.FolderSize{Path: "/srv/media", Bytes: n(400), Measured: 200})
	sum, err := Summarise(st)
	if err != nil {
		t.Fatal(err)
	}
	// /srv/media is inside /srv and in both jobs: it's counted once, in /srv.
	if *sum.FolderTotal != 1000 || sum.FolderPending != 0 || sum.FolderMeasured != 100 {
		t.Fatalf("total: %+v", sum)
	}
	if *sum.Jobs["j1"].FolderBytes != 1400 || *sum.Jobs["j2"].FolderBytes != 400 {
		t.Fatalf("per job: %+v %+v", sum.Jobs["j1"], sum.Jobs["j2"])
	}
}
