package backups

import (
	"sort"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// Metrics are the Dashboard's summary cards, worked out from run history,
// the jobs and the size cache.
type Metrics struct {
	Jobs     JobCounts    `json:"jobs"`
	Week     SuccessRate  `json:"week"`  // the last 7 days
	Month    SuccessRate  `json:"month"` // the last 30 days
	Largest  []JobSize    `json:"largest"`
	Longest  []JobTime    `json:"longest"`
	Daily    []DayCount   `json:"daily"` // the last 14 days, oldest first
	History  HistoryRange `json:"history"`
	Computed int64        `json:"computed"`
}

// JobCounts counts jobs by state, with clients and destinations.
type JobCounts struct {
	Total        int `json:"total"`
	Enabled      int `json:"enabled"`
	Disabled     int `json:"disabled"`
	ByHand       int `json:"by_hand"` // enabled, with no schedule
	Clients      int `json:"clients"`
	Destinations int `json:"destinations"`
}

// SuccessRate is the share of finished runs that succeeded. Cancelled runs
// aren't counted either way.
type SuccessRate struct {
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	Cancelled int      `json:"cancelled"`
	Percent   *float64 `json:"percent"` // nil with no finished runs
	// Partial means run history doesn't reach back the whole period.
	Partial bool `json:"partial"`
}

// JobSize is one of the largest jobs.
type JobSize struct {
	JobID    string `json:"job_id"`
	Name     string `json:"name"`
	Client   string `json:"client"`
	Bytes    int64  `json:"bytes"`
	FromPBS  bool   `json:"from_pbs"` // the newest backup's size; else the folders' size
	Measured int64  `json:"measured"`
}

// JobTime is one of the longest-running jobs over the last 30 days.
type JobTime struct {
	JobID       string `json:"job_id"`
	Name        string `json:"name"`
	Client      string `json:"client"`
	Runs        int    `json:"runs"`
	AvgSeconds  int64  `json:"avg_seconds"`
	MaxSeconds  int64  `json:"max_seconds"`
	MaxRunID    string `json:"max_run_id"`
	MaxClientID string `json:"max_client_id"`
}

// DayCount is the runs that finished on one day (server time).
type DayCount struct {
	Day       string `json:"day"` // 2006-01-02
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

// HistoryRange is what run history the numbers are based on.
type HistoryRange struct {
	Runs   int   `json:"runs"`
	Oldest int64 `json:"oldest"`
	Kept   int   `json:"kept"` // the "Runs kept on this server" setting
}

const topN = 5

// ComputeMetrics works out the Dashboard metrics. keep is the number of
// runs the server keeps, to tell when history is cut short.
func ComputeMetrics(st *store.Store, now time.Time, keep int) (*Metrics, error) {
	m := &Metrics{Largest: []JobSize{}, Longest: []JobTime{}, Daily: []DayCount{}, Computed: now.Unix()}
	jobs, err := st.ListJobs("")
	if err != nil {
		return nil, err
	}
	clients, err := st.ListClients()
	if err != nil {
		return nil, err
	}
	dests, err := st.ListDestinations()
	if err != nil {
		return nil, err
	}
	clientName := map[string]string{}
	for _, c := range clients {
		clientName[c.ID] = c.Name
	}
	m.Jobs = JobCounts{Total: len(jobs), Clients: len(clients), Destinations: len(dests)}
	for _, j := range jobs {
		switch {
		case !j.Enabled:
			m.Jobs.Disabled++
		case j.Schedule.Type == "manual" || j.Schedule.Type == "":
			m.Jobs.Enabled++
			m.Jobs.ByHand++
		default:
			m.Jobs.Enabled++
		}
	}

	count, oldest, err := st.RunHistory()
	if err != nil {
		return nil, err
	}
	m.History = HistoryRange{Runs: count, Oldest: oldest, Kept: keep}
	// History was pruned if the server holds as many runs as it keeps; a
	// period reaching back before the oldest run is then only partly covered.
	pruned := keep > 0 && count >= keep
	monthStart := now.AddDate(0, 0, -30)
	runs, err := st.RunsSince(monthStart.Unix())
	if err != nil {
		return nil, err
	}
	weekStart := now.AddDate(0, 0, -7)
	tally := func(r *SuccessRate, run *store.Run) {
		switch run.Status {
		case bundle.Success:
			r.Succeeded++
		case bundle.Failed:
			r.Failed++
		case bundle.Cancelled:
			r.Cancelled++
		}
	}
	for _, r := range runs {
		tally(&m.Month, r)
		if r.Started >= weekStart.Unix() {
			tally(&m.Week, r)
		}
	}
	for _, p := range []struct {
		rate  *SuccessRate
		start time.Time
	}{{&m.Week, weekStart}, {&m.Month, monthStart}} {
		if n := p.rate.Succeeded + p.rate.Failed; n > 0 {
			pct := float64(p.rate.Succeeded) * 100 / float64(n)
			p.rate.Percent = &pct
		}
		p.rate.Partial = pruned && oldest > p.start.Unix()
	}

	// Runs per day for the last 14 days, by when they finished.
	day0 := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -13)
	idx := map[string]int{}
	for i := 0; i < 14; i++ {
		d := day0.AddDate(0, 0, i).Format("2006-01-02")
		idx[d] = i
		m.Daily = append(m.Daily, DayCount{Day: d})
	}
	for _, r := range runs {
		if r.Ended == 0 {
			continue
		}
		i, ok := idx[time.Unix(r.Ended, 0).In(now.Location()).Format("2006-01-02")]
		if !ok {
			continue
		}
		switch r.Status {
		case bundle.Success:
			m.Daily[i].Succeeded++
		case bundle.Failed:
			m.Daily[i].Failed++
		}
	}

	// Longest running: successful runs over the last 30 days, by average.
	// A job backing up to several destinations makes one run per destination;
	// each counts on its own.
	times := map[string]*JobTime{}
	var order []string
	for _, r := range runs {
		if r.Status != bundle.Success || r.Ended == 0 || r.Ended < r.Started {
			continue
		}
		t := times[r.JobID]
		if t == nil {
			t = &JobTime{JobID: r.JobID, Name: r.JobName, Client: clientName[r.ClientID]}
			times[r.JobID] = t
			order = append(order, r.JobID)
		}
		secs := r.Ended - r.Started
		t.AvgSeconds += secs // the total until it's divided below
		t.Runs++
		if t.MaxRunID == "" || secs > t.MaxSeconds {
			t.MaxSeconds, t.MaxRunID, t.MaxClientID = secs, r.ID, r.ClientID
		}
	}
	for _, id := range order {
		t := times[id]
		t.AvgSeconds /= int64(t.Runs)
		m.Longest = append(m.Longest, *t)
	}
	sort.SliceStable(m.Longest, func(a, b int) bool { return m.Longest[a].AvgSeconds > m.Longest[b].AvgSeconds })
	if len(m.Longest) > topN {
		m.Longest = m.Longest[:topN]
	}

	// Largest: the newest backup's size on PBS, else the folders' size.
	sum, err := Summarise(st)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		js := sum.Jobs[j.ID]
		if js == nil {
			continue
		}
		e := JobSize{JobID: j.ID, Name: j.Name, Client: clientName[j.ClientID]}
		switch {
		case js.BackupBytes != nil:
			e.Bytes, e.FromPBS, e.Measured = *js.BackupBytes, true, js.BackupTime
		case js.FolderBytes != nil:
			e.Bytes, e.Measured = *js.FolderBytes, js.FolderMeasured
		default:
			continue
		}
		m.Largest = append(m.Largest, e)
	}
	sort.SliceStable(m.Largest, func(a, b int) bool { return m.Largest[a].Bytes > m.Largest[b].Bytes })
	if len(m.Largest) > topN {
		m.Largest = m.Largest[:topN]
	}
	return m, nil
}
