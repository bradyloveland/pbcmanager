package backups

import (
	"fmt"
	"testing"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

func TestMetrics(t *testing.T) {
	st, _, _ := sizesEnv(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	day := func(d int) int64 { return now.AddDate(0, 0, -d).Unix() }
	n := 0
	add := func(job, status string, started int64, secs int64) {
		n++
		r := bundle.Run{ID: fmt.Sprintf("r%02d", n), JobID: job, JobName: map[string]string{"j1": "media", "j2": "old"}[job],
			DestinationID: "d1", DestinationName: "Home PBS", Status: status, Started: started}
		if status != bundle.Running {
			r.Ended = started + secs
		}
		if _, err := st.UpsertRun("c1", r); err != nil {
			t.Fatal(err)
		}
	}
	// No runs yet: rates are unknown, not 0%.
	m, err := ComputeMetrics(st, now, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if m.Week.Percent != nil || m.Jobs.Total != 2 || m.Jobs.Clients != 1 || len(m.Daily) != 14 || len(m.Longest) != 0 {
		t.Fatalf("empty: %+v", m)
	}

	add("j1", bundle.Success, day(1), 600)
	add("j1", bundle.Success, day(2), 1200)
	add("j1", bundle.Failed, day(3), 30)
	add("j1", bundle.Cancelled, day(4), 10)
	add("j2", bundle.Success, day(10), 60)
	add("j2", bundle.Failed, day(20), 5)
	add("j2", bundle.Running, now.Unix()-100, 0)
	add("j1", bundle.Success, day(40), 9999) // before the 30-day window

	m, err = ComputeMetrics(st, now, 5000)
	if err != nil {
		t.Fatal(err)
	}
	// 7 days: 2 succeeded, 1 failed (cancelled and running left out).
	if m.Week.Succeeded != 2 || m.Week.Failed != 1 || m.Week.Cancelled != 1 || m.Week.Percent == nil || int(*m.Week.Percent) != 66 {
		t.Fatalf("week: %+v %v", m.Week, m.Week.Percent)
	}
	if m.Month.Succeeded != 3 || m.Month.Failed != 2 || int(*m.Month.Percent) != 60 || m.Month.Partial {
		t.Fatalf("month: %+v", m.Month)
	}
	if len(m.Longest) != 2 || m.Longest[0].Name != "media" || m.Longest[0].AvgSeconds != 900 || m.Longest[0].MaxSeconds != 1200 || m.Longest[0].MaxRunID != "r02" {
		t.Fatalf("longest: %+v", m.Longest)
	}
	var daily int
	for _, d := range m.Daily {
		daily += d.Succeeded + d.Failed
	}
	if daily != 4 || m.Daily[13].Day != "2026-10-15" { // 1, 2, 3 and 10 days ago; 20 is outside 14 days
		t.Fatalf("daily: %d %+v", daily, m.Daily)
	}

	// History cut short: the server holds as many runs as it keeps (8), and
	// seen from 25 days ago the oldest run is inside that 30-day window.
	m, _ = ComputeMetrics(st, now.AddDate(0, 0, -25), 8)
	if !m.Month.Partial {
		t.Fatalf("partial history: %+v", m.History)
	}
	// With room to keep more, history is complete however old it is.
	if m, _ = ComputeMetrics(st, now.AddDate(0, 0, -25), 5000); m.Month.Partial {
		t.Fatal("history isn't cut short when the server keeps more runs than it has")
	}

	// Largest: newest backup on PBS, else the folders' size.
	nb := func(v int64) *int64 { return &v }
	st.PutSize(SizeFolder, FolderKey("c1", "/srv"), bundle.FolderSize{Path: "/srv", Bytes: nb(5000), Measured: 1})
	st.PutSize(SizeFolder, FolderKey("c1", "/srv/media"), bundle.FolderSize{Path: "/srv/media", Bytes: nb(100), Measured: 1})
	st.PutSize(SizeBackup, "j2:d1", Latest{Bytes: nb(9000), Time: 5, Count: 1})
	m, _ = ComputeMetrics(st, now, 5000)
	if len(m.Largest) != 2 || m.Largest[0].Name != "old" || !m.Largest[0].FromPBS || m.Largest[1].Bytes != 5100 || m.Largest[1].FromPBS {
		t.Fatalf("largest: %+v", m.Largest)
	}

	// Disabled and by-hand jobs.
	j, _ := st.GetJob("j2")
	j.Enabled = false
	st.SaveJob(j)
	m, _ = ComputeMetrics(st, now, 5000)
	if m.Jobs.Disabled != 1 || m.Jobs.ByHand != 1 || m.Jobs.Enabled != 1 {
		t.Fatalf("job counts: %+v", m.Jobs)
	}
}
