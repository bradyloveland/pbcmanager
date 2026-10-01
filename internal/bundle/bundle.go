// Package bundle defines what the server sends to a client (its jobs, the
// destinations they use and the credentials for them) and what a client
// reports back (runs). Both pbcm and pbcm-runner use it, so the two always
// agree on the format.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Version is the bundle format. A runner refuses a newer one.
const Version = 1

// IDRE matches job and destination IDs. They're used in file and systemd
// unit names, so they're kept to lowercase letters and digits.
var IDRE = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// ArchiveRE matches an archive name (the part before .pxar).
var ArchiveRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_\-]{0,63}$`)

// BackupIDRE matches a backup ID (the group is host/<id>).
var BackupIDRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._\-]{0,127}$`)

// Share is one folder in a job.
type Share struct {
	Path    string `json:"path"`
	Archive string `json:"archive"`
}

// Schedule says when a job runs on its own.
type Schedule struct {
	Type          string `json:"type"` // manual, daily, hourly
	Time          string `json:"time"` // HH:MM; for hourly only the minute matters
	Days          []int  `json:"days"` // 0 = Monday … 6 = Sunday
	IntervalHours int    `json:"interval_hours"`
}

// Destination is a PBS datastore as a client needs it.
type Destination struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Repository  string `json:"repository"` // user@realm!token@host:port:datastore
	Fingerprint string `json:"fingerprint"`
	Namespace   string `json:"namespace"`
	Secret      string `json:"secret,omitempty"`
}

// Job is one backup job.
type Job struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	BackupID        string   `json:"backup_id"`
	Shares          []Share  `json:"shares"`
	Excludes        []string `json:"excludes"`
	ChangeDetection string   `json:"change_detection"`
	Rate            string   `json:"rate"`
	Keyfile         string   `json:"keyfile"`
	KeyfilePassword string   `json:"keyfile_password,omitempty"`
	Schedule        Schedule `json:"schedule"`
	Enabled         bool     `json:"enabled"`
	Destinations    []string `json:"destinations"`
}

// Bundle is everything a client needs to back up on its own.
type Bundle struct {
	Version      int           `json:"version"`
	Jobs         []Job         `json:"jobs"`
	Destinations []Destination `json:"destinations"`
	KeepRuns     int           `json:"keep_runs"`
	KeepDays     int           `json:"keep_days"`
}

// Hash identifies a bundle's contents, so the server can tell whether a
// client has the latest settings. It covers secrets too, so a changed token
// is sent again, but reveals nothing about them.
func (b Bundle) Hash() string {
	c := b
	c.Jobs = append([]Job{}, b.Jobs...)
	sort.Slice(c.Jobs, func(i, j int) bool { return c.Jobs[i].ID < c.Jobs[j].ID })
	c.Destinations = append([]Destination{}, b.Destinations...)
	sort.Slice(c.Destinations, func(i, j int) bool { return c.Destinations[i].ID < c.Destinations[j].ID })
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// Check validates a bundle before a runner writes anything from it.
func (b Bundle) Check() error {
	if b.Version < 1 || b.Version > Version {
		return fmt.Errorf("this client's pbcm-runner is older than the server; use Repair to update it")
	}
	dests := map[string]bool{}
	for _, d := range b.Destinations {
		if !IDRE.MatchString(d.ID) {
			return fmt.Errorf("destination ID %q isn't valid", d.ID)
		}
		if d.Repository == "" || strings.ContainsAny(d.Repository, "\n\x00 ") {
			return fmt.Errorf("destination %s has no valid repository", d.Name)
		}
		dests[d.ID] = true
	}
	for _, j := range b.Jobs {
		if !IDRE.MatchString(j.ID) {
			return fmt.Errorf("job ID %q isn't valid", j.ID)
		}
		if !BackupIDRE.MatchString(j.BackupID) {
			return fmt.Errorf("job %s has an invalid backup ID", j.Name)
		}
		if len(j.Shares) == 0 {
			return fmt.Errorf("job %s has no folders", j.Name)
		}
		for _, s := range j.Shares {
			if !strings.HasPrefix(s.Path, "/") || strings.ContainsAny(s.Path, "\n\x00") || !ArchiveRE.MatchString(s.Archive) {
				return fmt.Errorf("job %s has an invalid folder or archive name", j.Name)
			}
		}
		for _, d := range j.Destinations {
			if !dests[d] {
				return fmt.Errorf("job %s uses a destination that wasn't sent", j.Name)
			}
		}
		if _, err := OnCalendar(j.Schedule); err != nil {
			return fmt.Errorf("job %s: %w", j.Name, err)
		}
	}
	return nil
}

// ------------------------------------------------------------------ schedules

var timeRE = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

var dayNames = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// CleanSchedule checks a schedule and fills in defaults.
func CleanSchedule(s Schedule) (Schedule, error) {
	if s.Type == "" {
		s.Type = "manual"
	}
	if s.Time == "" {
		s.Time = "02:00"
	}
	if !timeRE.MatchString(s.Time) {
		return s, fmt.Errorf("enter the time as HH:MM (24-hour)")
	}
	if s.IntervalHours == 0 {
		s.IntervalHours = 1
	}
	days := map[int]bool{}
	for _, d := range s.Days {
		if d < 0 || d > 6 {
			return s, fmt.Errorf("days must be 0 (Monday) to 6 (Sunday)")
		}
		days[d] = true
	}
	s.Days = s.Days[:0:0]
	for d := 0; d < 7; d++ {
		if days[d] {
			s.Days = append(s.Days, d)
		}
	}
	switch s.Type {
	case "manual":
	case "daily":
		if len(s.Days) == 0 {
			return s, fmt.Errorf("pick at least one day of the week")
		}
	case "hourly":
		if s.IntervalHours < 1 || s.IntervalHours > 24 || 24%s.IntervalHours != 0 {
			return s, fmt.Errorf("choose an interval that divides the day evenly (1, 2, 3, 4, 6, 8, 12 or 24 hours)")
		}
	default:
		return s, fmt.Errorf("choose when the job should run")
	}
	return s, nil
}

// OnCalendar turns a schedule into a systemd OnCalendar= value. Manual jobs
// return "" (no timer).
func OnCalendar(s Schedule) (string, error) {
	s, err := CleanSchedule(s)
	if err != nil {
		return "", err
	}
	hh, mm := s.Time[:2], s.Time[3:]
	switch s.Type {
	case "daily":
		names := make([]string, len(s.Days))
		for i, d := range s.Days {
			names[i] = dayNames[d]
		}
		prefix := strings.Join(names, ",") + " "
		if len(s.Days) == 7 {
			prefix = ""
		}
		return prefix + "*-*-* " + hh + ":" + mm + ":00", nil
	case "hourly":
		return fmt.Sprintf("*-*-* 00/%d:%s:00", s.IntervalHours, mm), nil
	}
	return "", nil
}

// Next returns the next scheduled start strictly after t, or the zero time
// for manual or disabled jobs. It matches what OnCalendar tells systemd.
func Next(s Schedule, enabled bool, t time.Time) time.Time {
	s, err := CleanSchedule(s)
	if err != nil || !enabled || s.Type == "manual" {
		return time.Time{}
	}
	var hh, mm int
	fmt.Sscanf(s.Time, "%d:%d", &hh, &mm)
	base := t.Truncate(time.Minute)
	switch s.Type {
	case "hourly":
		c := time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), mm, 0, 0, t.Location())
		if !c.After(t) {
			c = c.Add(time.Hour)
		}
		for i := 0; i < 72; i++ {
			if c.Hour()%s.IntervalHours == 0 {
				return c
			}
			c = c.Add(time.Hour)
		}
	case "daily":
		c := time.Date(base.Year(), base.Month(), base.Day(), hh, mm, 0, 0, t.Location())
		if !c.After(t) {
			c = c.AddDate(0, 0, 1)
		}
		on := map[int]bool{}
		for _, d := range s.Days {
			on[d] = true
		}
		for i := 0; i < 8; i++ {
			if on[(int(c.Weekday())+6)%7] {
				return c
			}
			c = c.AddDate(0, 0, 1)
		}
	}
	return time.Time{}
}

// Prev returns the most recent scheduled start at or before t, or the zero
// time for manual or disabled jobs.
func Prev(s Schedule, enabled bool, t time.Time) time.Time {
	var last time.Time
	c := Next(s, enabled, t.AddDate(0, 0, -8))
	for !c.IsZero() && !c.After(t) {
		last = c
		c = Next(s, enabled, c)
	}
	return last
}

// --------------------------------------------------------------------- runs

// Run statuses.
const (
	Running   = "running"
	Success   = "success"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// Run is one backup of one job to one destination, as recorded on the client.
type Run struct {
	ID              string `json:"id"`
	Group           string `json:"group"` // runs started together (one per destination)
	JobID           string `json:"job_id"`
	JobName         string `json:"job_name"`
	DestinationID   string `json:"destination_id"`
	DestinationName string `json:"destination_name"`
	Trigger         string `json:"trigger"` // schedule or manual
	Status          string `json:"status"`
	Started         int64  `json:"started"`
	Ended           int64  `json:"ended"`
	ExitCode        *int   `json:"exit_code"`
	Summary         string `json:"summary"`
	Updated         int64  `json:"updated"`
	LogSize         int64  `json:"log_size"`
}

// Status is a client's answer to "what happened since …".
type Status struct {
	Runs    []Run  `json:"runs"`
	Applied string `json:"applied"` // hash of the bundle it has
	Now     int64  `json:"now"`
}

// LogChunk is part of a run's log.
type LogChunk struct {
	Text   string `json:"text"`
	Offset int64  `json:"offset"` // where the next read starts
	Size   int64  `json:"size"`
	Done   bool   `json:"done"`
}

// SummarizeError picks the most useful line from a failed run's log.
func SummarizeError(log string) string {
	var lines []string
	for _, l := range strings.Split(log, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(lines[i]), "error") {
			return cut(lines[i], 300)
		}
	}
	if len(lines) > 0 {
		return cut(lines[len(lines)-1], 300)
	}
	return "The backup client exited with an error."
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
