package store

import (
	"testing"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

func TestRunProgressIsKeptWhileRunning(t *testing.T) {
	s := open(t, t.TempDir())
	r := bundle.Run{ID: "20261001T020000-aaaaaa", JobID: "j1", Status: bundle.Running,
		Progress: &bundle.Progress{Done: 1 << 30, Total: 4 << 30, TotalFrom: "measured", Archive: "media", Folder: 1, Folders: 2}}
	if _, err := s.UpsertRun("c1", r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun("c1", r.ID)
	if err != nil || got.Progress == nil || *got.Progress != *r.Progress {
		t.Fatalf("progress: %+v %v", got, err)
	}
	r.Status, r.Progress = bundle.Success, nil
	s.UpsertRun("c1", r)
	if got, _ := s.GetRun("c1", r.ID); got.Progress != nil {
		t.Fatal("a finished run has no progress")
	}
	// A client that stops reporting progress while still running: the
	// progress shown is never from a finished run.
	r.Status, r.Progress = bundle.Failed, &bundle.Progress{Done: 5}
	s.UpsertRun("c1", r)
	if got, _ := s.GetRun("c1", r.ID); got.Progress != nil {
		t.Fatal("only running runs have progress")
	}
}
