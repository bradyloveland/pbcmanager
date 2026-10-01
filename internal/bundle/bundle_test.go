package bundle

import (
	"strings"
	"testing"
	"time"
)

func TestOnCalendar(t *testing.T) {
	cases := []struct {
		s    Schedule
		want string
	}{
		{Schedule{Type: "manual"}, ""},
		{Schedule{Type: "daily", Time: "02:30", Days: []int{0, 1, 2, 3, 4, 5, 6}}, "*-*-* 02:30:00"},
		{Schedule{Type: "daily", Time: "23:05", Days: []int{4, 0, 2}}, "Mon,Wed,Fri *-*-* 23:05:00"},
		{Schedule{Type: "hourly", Time: "00:15", IntervalHours: 6}, "*-*-* 00/6:15:00"},
		{Schedule{Type: "hourly", Time: "07:45", IntervalHours: 1}, "*-*-* 00/1:45:00"},
	}
	for _, c := range cases {
		got, err := OnCalendar(c.s)
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q, %v; want %q", c.s, got, err, c.want)
		}
	}
	for _, bad := range []Schedule{
		{Type: "daily", Time: "02:00"},
		{Type: "hourly", IntervalHours: 5},
		{Type: "weekly"},
		{Type: "daily", Time: "25:00", Days: []int{1}},
		{Type: "daily", Days: []int{7}},
	} {
		if _, err := OnCalendar(bad); err == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
}

func TestNextMatchesTheTimer(t *testing.T) {
	loc := time.UTC
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04", s, loc); return v }
	// 2026-10-01 is a Thursday.
	now := at("2026-10-01 10:20")
	cases := []struct {
		s    Schedule
		want string
	}{
		{Schedule{Type: "daily", Time: "02:00", Days: []int{0, 1, 2, 3, 4, 5, 6}}, "2026-10-02 02:00"},
		{Schedule{Type: "daily", Time: "11:00", Days: []int{3}}, "2026-10-01 11:00"},
		{Schedule{Type: "daily", Time: "09:00", Days: []int{3}}, "2026-10-08 09:00"},
		{Schedule{Type: "daily", Time: "09:00", Days: []int{5, 6}}, "2026-10-03 09:00"},
		{Schedule{Type: "hourly", Time: "00:15", IntervalHours: 6}, "2026-10-01 12:15"},
		{Schedule{Type: "hourly", Time: "00:30", IntervalHours: 1}, "2026-10-01 10:30"},
		{Schedule{Type: "hourly", Time: "00:10", IntervalHours: 1}, "2026-10-01 11:10"},
	}
	for _, c := range cases {
		got := Next(c.s, true, now)
		if got.Format("2006-01-02 15:04") != c.want {
			t.Errorf("%+v: got %s, want %s", c.s, got.Format("2006-01-02 15:04"), c.want)
		}
	}
	if !Next(Schedule{Type: "manual"}, true, now).IsZero() || !Next(cases[0].s, false, now).IsZero() {
		t.Error("manual or paused jobs have no next run")
	}
}

func validBundle() Bundle {
	return Bundle{Version: 1,
		Destinations: []Destination{{ID: "d1", Name: "Home PBS", Repository: "nas@pbs!nas@192.0.2.10:8007:store", Secret: "s"}},
		Jobs: []Job{{ID: "j1", Name: "media", BackupID: "nas", Shares: []Share{{Path: "/srv/media", Archive: "media"}},
			Schedule: Schedule{Type: "daily", Time: "02:00", Days: []int{0}}, Enabled: true, Destinations: []string{"d1"}}}}
}

func TestCheckAndHash(t *testing.T) {
	b := validBundle()
	if err := b.Check(); err != nil {
		t.Fatal(err)
	}
	h := b.Hash()
	b2 := validBundle()
	if b2.Hash() != h {
		t.Fatal("same bundle, different hash")
	}
	b2.Destinations[0].Secret = "new"
	if b2.Hash() == h {
		t.Fatal("a changed secret must change the hash")
	}
	bad := []func(*Bundle){
		func(b *Bundle) { b.Version = 2 },
		func(b *Bundle) { b.Jobs[0].ID = "../etc" },
		func(b *Bundle) { b.Jobs[0].Shares[0].Path = "relative" },
		func(b *Bundle) { b.Jobs[0].Shares[0].Archive = "a b" },
		func(b *Bundle) { b.Jobs[0].Destinations = []string{"missing"} },
		func(b *Bundle) { b.Destinations[0].ID = "D1;" },
		func(b *Bundle) { b.Jobs[0].BackupID = "bad id" },
	}
	for i, f := range bad {
		b := validBundle()
		f(&b)
		if err := b.Check(); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}

func TestSummarizeError(t *testing.T) {
	log := "Starting backup\nUpload 1 GiB\nError: connection reset by peer\nDuration: 3s\n"
	if got := SummarizeError(log); got != "Error: connection reset by peer" {
		t.Fatalf("got %q", got)
	}
	if got := SummarizeError("only line\n"); got != "only line" {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(SummarizeError(""), "exited with an error") {
		t.Fatal("empty log")
	}
}

func TestPrev(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04", s, time.UTC); return v }
	daily := Schedule{Type: "daily", Time: "02:00", Days: []int{0, 1, 2, 3, 4, 5, 6}}
	if got := Prev(daily, true, at("2026-10-01 10:20")); got != at("2026-10-01 02:00") {
		t.Fatalf("daily prev %s", got)
	}
	if got := Prev(daily, true, at("2026-10-01 02:00")); got != at("2026-10-01 02:00") {
		t.Fatalf("prev includes t itself: %s", got)
	}
	weekly := Schedule{Type: "daily", Time: "09:00", Days: []int{0}} // Mondays
	if got := Prev(weekly, true, at("2026-10-01 10:20")); got != at("2026-09-28 09:00") {
		t.Fatalf("weekly prev %s", got)
	}
	hourly := Schedule{Type: "hourly", Time: "00:15", IntervalHours: 6}
	if got := Prev(hourly, true, at("2026-10-01 10:20")); got != at("2026-10-01 06:15") {
		t.Fatalf("hourly prev %s", got)
	}
	if !Prev(Schedule{Type: "manual"}, true, at("2026-10-01 10:20")).IsZero() || !Prev(daily, false, at("2026-10-01 10:20")).IsZero() {
		t.Fatal("manual or paused jobs have no previous run")
	}
}
