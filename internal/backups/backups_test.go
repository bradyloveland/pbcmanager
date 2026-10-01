package backups

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

func destInput() DestinationInput {
	return DestinationInput{Name: "Home PBS", Host: "192.0.2.10", Datastore: "backup-pool", Username: "nas@pbs",
		TokenName: "nas", Secret: "tok", Fingerprint: strings.Repeat("AB:", 31) + "AB", Namespace: "/clients/nas/"}
}

func TestCleanDestination(t *testing.T) {
	d, err := CleanDestination(destInput(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Port != 8007 || d.Namespace != "clients/nas" || d.Fingerprint != strings.Repeat("ab:", 31)+"ab" ||
		d.Repository() != "nas@pbs!nas@192.0.2.10:8007:backup-pool" || len(d.ID) != 12 {
		t.Fatalf("got %+v %s", d, d.Repository())
	}
	in := destInput()
	in.Secret = ""
	if _, err := CleanDestination(in, nil); err == nil || !strings.Contains(err.Error(), "token secret") {
		t.Fatalf("new destination needs a secret: %v", err)
	}
	kept, err := CleanDestination(in, d)
	if err != nil || kept.Secret != "tok" || kept.ID != d.ID {
		t.Fatalf("empty secret keeps the stored one: %+v %v", kept, err)
	}
	v6 := destInput()
	v6.Host = "2001:db8::10"
	d6, _ := CleanDestination(v6, nil)
	if d6.Repository() != "nas@pbs!nas@[2001:db8::10]:8007:backup-pool" {
		t.Fatalf("ipv6 repository %s", d6.Repository())
	}
	for _, f := range []func(*DestinationInput){
		func(i *DestinationInput) { i.Host = "bad host" },
		func(i *DestinationInput) { i.Datastore = "a b" },
		func(i *DestinationInput) { i.Username = "nouser" },
		func(i *DestinationInput) { i.TokenName = "a!b" },
		func(i *DestinationInput) { i.Fingerprint = "ab:cd" },
		func(i *DestinationInput) { i.Namespace = "a//b" },
		func(i *DestinationInput) { i.Secret = "a\nb" },
		func(i *DestinationInput) { i.Port = 70000 },
	} {
		in := destInput()
		f(&in)
		if _, err := CleanDestination(in, nil); err == nil {
			t.Errorf("%+v should be rejected", in)
		}
	}
}

func jobInput() JobInput {
	on := true
	return JobInput{ClientID: "c1", Name: "media", Shares: []bundle.Share{{Path: "/srv/dev-disk/Media/"}, {Path: " "}},
		Excludes: "lost+found\n\n**/*.tmp\n", Schedule: bundle.Schedule{Type: "daily", Time: "02:00", Days: []int{0, 1}},
		Destinations: []string{"d1", "d1"}, Enabled: &on}
}

func TestCleanJob(t *testing.T) {
	dests := map[string]bool{"d1": true, "d2": true}
	j, err := CleanJob(jobInput(), nil, "nas", dests)
	if err != nil {
		t.Fatal(err)
	}
	if j.BackupID != "nas" || len(j.Shares) != 1 || j.Shares[0].Path != "/srv/dev-disk/Media" || j.Shares[0].Archive != "Media" ||
		strings.Join(j.Excludes, "|") != "lost+found|**/*.tmp" || len(j.Destinations) != 1 || j.ChangeDetection != "metadata" {
		t.Fatalf("got %+v", j)
	}
	in := jobInput()
	in.Keyfile, in.KeyfilePassword = "/root/k.json", "kp"
	j, _ = CleanJob(in, nil, "nas", dests)
	in.KeyfilePassword = ""
	kept, _ := CleanJob(in, j, "nas", dests)
	if kept.KeyfilePassword != "kp" || kept.ID != j.ID {
		t.Fatal("an empty key password keeps the stored one")
	}
	in.ClearKeyfilePW = true
	if cleared, _ := CleanJob(in, j, "nas", dests); cleared.KeyfilePassword != "" {
		t.Fatal("clear_keyfile_password should clear it")
	}
	for name, f := range map[string]func(*JobInput){
		"no folders":      func(i *JobInput) { i.Shares = nil },
		"relative":        func(i *JobInput) { i.Shares = []bundle.Share{{Path: "srv"}} },
		"dup archive":     func(i *JobInput) { i.Shares = []bundle.Share{{Path: "/a/x"}, {Path: "/b/x"}} },
		"bad archive":     func(i *JobInput) { i.Shares = []bundle.Share{{Path: "/a", Archive: "a b"}} },
		"no destinations": func(i *JobInput) { i.Destinations = nil },
		"unknown dest":    func(i *JobInput) { i.Destinations = []string{"zz"} },
		"bad schedule":    func(i *JobInput) { i.Schedule = bundle.Schedule{Type: "hourly", IntervalHours: 5} },
		"bad rate":        func(i *JobInput) { i.Rate = "fast" },
		"bad keyfile":     func(i *JobInput) { i.Keyfile = "key.json" },
		"bad backup id":   func(i *JobInput) { i.BackupID = "a b" },
		"no name":         func(i *JobInput) { i.Name = " " },
	} {
		in := jobInput()
		f(&in)
		if _, err := CleanJob(in, nil, "nas", dests); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if ArchiveFromPath("/") != "root" || ArchiveFromPath("/srv/.hidden") != "hidden" || ArchiveFromPath("/srv/My Files") != "My-Files" {
		t.Fatal("archive names")
	}
}

func TestBuildBundle(t *testing.T) {
	d1 := &store.Destination{ID: "d1", Name: "Home", Host: "192.0.2.10", Port: 8007, Datastore: "s", Username: "u@pbs", Secret: "x"}
	jobs := []*store.Job{
		{ID: "j1", Name: "a", BackupID: "nas", Shares: []bundle.Share{{Path: "/a", Archive: "a"}}, Destinations: []string{"d1", "gone"},
			Schedule: bundle.Schedule{Type: "manual"}, Enabled: true},
		{ID: "j2", Name: "b", BackupID: "nas", Shares: []bundle.Share{{Path: "/b", Archive: "b"}}, Destinations: []string{"gone"},
			Schedule: bundle.Schedule{Type: "manual"}},
	}
	b := BuildBundle(jobs, map[string]*store.Destination{"d1": d1}, 500, 90)
	if len(b.Jobs) != 1 || len(b.Jobs[0].Destinations) != 1 || len(b.Destinations) != 1 || b.Destinations[0].Secret != "x" ||
		b.Destinations[0].Repository != "u@pbs@192.0.2.10:8007:s" {
		t.Fatalf("bundle %+v", b)
	}
	if err := b.Check(); err != nil {
		t.Fatal(err)
	}
}

const fakePBS = `#!/bin/sh
[ "$(cat "$PBS_PASSWORD_FILE")" = "good" ] || { echo "Error: permission check failed." >&2; exit 1; }
case "$1" in
status) echo '{"total":1000,"used":250,"avail":750}' ;;
snapshot) echo '[{"backup-type":"host","backup-id":"nas","backup-time":100,"size":5,"files":[{"filename":"a.mpxar.didx"}],"verification":{"state":"ok"}},{"backup-time":200,"size":7,"files":["b.mpxar.didx"],"protected":true}]' ;;
esac
`

func TestPBSWrapper(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pbc")
	os.WriteFile(bin, []byte(fakePBS), 0o755)
	p := &PBS{Bin: bin}
	d := &store.Destination{Host: "192.0.2.10", Port: 8007, Datastore: "s", Username: "u@pbs", Secret: "good"}
	u, err := p.Status(context.Background(), d)
	if err != nil || *u.Total != 1000 || *u.Avail != 750 {
		t.Fatalf("status %+v %v", u, err)
	}
	snaps, err := p.Snapshots(context.Background(), d, "nas")
	if err != nil || len(snaps) != 2 || snaps[0].Time != 200 || !snaps[0].Protected || snaps[1].Verified != "ok" || snaps[1].Files[0] != "a.mpxar.didx" {
		t.Fatalf("snapshots %+v %v", snaps, err)
	}
	d.Secret = "wrong"
	if _, err := p.Status(context.Background(), d); err == nil || !strings.Contains(err.Error(), "permission check failed") {
		t.Fatalf("bad secret: %v", err)
	}
	missing := &PBS{Bin: "/nonexistent/pbc"}
	if _, err := missing.Status(context.Background(), d); err != ErrNoClient {
		t.Fatalf("missing client: %v", err)
	}
}
