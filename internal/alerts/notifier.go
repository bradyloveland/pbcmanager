package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// Notifier decides which alerts to send and sends them.
type Notifier struct {
	Store      *store.Store
	Settings   func() Settings
	ServerName func() string
	// PublicURL is the UI's address for links ("" for none), ending in "/".
	PublicURL func() string
	// LogPath returns where a run's log is saved on the server.
	LogPath func(clientID, runID string) string
	Now     func() time.Time
	// Send delivers an email (Send by default; tests replace it).
	Send func(ctx context.Context, s Settings, subject, body string) error

	wg sync.WaitGroup
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) send() func(context.Context, Settings, string, string) error {
	if n.Send != nil {
		return n.Send
	}
	return Send
}

// Wait waits for alerts being sent in the background (tests).
func (n *Notifier) Wait() { n.wg.Wait() }

// deliver records an alert once (by key) and sends it in the background.
func (n *Notifier) deliver(a *store.Alert, body string) {
	s := n.Settings()
	if !s.Enabled {
		return
	}
	a.Subject = "[PBC Manager] " + a.Subject
	added, err := n.Store.AddAlert(a)
	if err != nil {
		slog.Error("recording alert", "err", err)
		return
	}
	if !added {
		return // already sent
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := n.send()(ctx, s, a.Subject, body+n.footer())
		if err != nil {
			slog.Warn("couldn't send alert email", "subject", a.Subject, "err", err)
		}
		_ = n.Store.AlertDelivered(a.ID, err)
	}()
}

func (n *Notifier) footer() string {
	return "\n--\nSent by PBC Manager on " + n.ServerName() + ". Change alert settings under Alerts in the web UI.\n"
}

func (n *Notifier) link(path string) string {
	if n.PublicURL == nil {
		return ""
	}
	base := n.PublicURL()
	if base == "" {
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/#" + path
}

func fmtTime(ts int64, loc *time.Location) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(ts, 0).In(loc).Format("Mon 2 Jan 2006 15:04 MST")
}

func duration(a, b int64) string {
	if a == 0 || b == 0 {
		return "-"
	}
	d := time.Duration(b-a) * time.Second
	if d < time.Minute {
		return d.String()
	}
	return d.Round(time.Minute).String()
}

// RunFinished alerts about a finished run, if wanted.
func (n *Notifier) RunFinished(c *store.Client, r *store.Run) {
	s := n.Settings()
	var verb string
	switch {
	case r.Status == bundle.Failed && s.OnFailure:
		verb = "failed"
	case r.Status == bundle.Success && s.OnSuccess:
		verb = "succeeded"
	default:
		return
	}
	loc := c.Location()
	late := r.CollectedAt-r.Ended > 600
	var b strings.Builder
	fmt.Fprintf(&b, "The backup job \"%s\" on %s %s.\n", r.JobName, c.Name, verb)
	if late {
		fmt.Fprintf(&b, "\nThis is reported late: the server only heard about it at %s, because it couldn't reach the client or wasn't running. The client ran the backup on its own schedule as usual.\n",
			fmtTime(r.CollectedAt, loc))
	}
	fmt.Fprintf(&b, "\nClient:       %s (%s)\n", c.Name, c.Address)
	fmt.Fprintf(&b, "Destination:  %s\n", r.DestinationName)
	fmt.Fprintf(&b, "Started:      %s (%s)\n", fmtTime(r.Started, loc), map[string]string{"manual": "by hand", "schedule": "scheduled"}[r.Trigger])
	fmt.Fprintf(&b, "Finished:     %s, after %s\n", fmtTime(r.Ended, loc), duration(r.Started, r.Ended))
	if r.ExitCode != nil {
		fmt.Fprintf(&b, "Exit code:    %d\n", *r.ExitCode)
	}
	fmt.Fprintf(&b, "Result:       %s\n", r.Summary)
	if l := n.link("/activity/" + c.ID + "/" + r.ID); l != "" {
		fmt.Fprintf(&b, "\nFull log: %s\n", l)
	}
	if r.Status == bundle.Failed && n.LogPath != nil {
		if tail := lastLines(n.LogPath(c.ID, r.ID), 40); tail != "" {
			b.WriteString("\nLast lines of the log:\n" + strings.Repeat("-", 60) + "\n" + tail)
		}
	}
	subject := fmt.Sprintf("Backup %s: %s on %s", verb, r.JobName, c.Name)
	if late {
		subject += " (reported late)"
	}
	n.deliver(&store.Alert{Key: "run:" + c.ID + ":" + r.ID, Kind: verb, ClientID: c.ID, JobID: r.JobID, RunID: r.ID, Subject: subject}, b.String())
}

func lastLines(path string, n int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// ClientReachable sends a "back online" email if an outage was reported.
func (n *Notifier) ClientReachable(c *store.Client, since int64) {
	if had, _ := n.Store.HasAlert(fmt.Sprintf("unreachable:%s:%d", c.ID, since)); !had {
		return
	}
	loc := c.Location()
	body := fmt.Sprintf("The server can reach %s (%s) again, after losing contact at %s (%s ago).\n\n"+
		"Any backups it ran on its own meanwhile are being collected now; failures among them are reported separately.\n",
		c.Name, c.Address, fmtTime(since, loc), duration(since, n.now().Unix()))
	n.deliver(&store.Alert{Key: fmt.Sprintf("reachable:%s:%d", c.ID, since), Kind: "reachable", ClientID: c.ID,
		Subject: c.Name + " can be reached again"}, body)
}

// Check looks for missed backups and clients that have been unreachable for
// too long. The server calls it every minute.
func (n *Notifier) Check() {
	s := n.Settings()
	if !s.Enabled {
		return
	}
	clients, err := n.Store.ListClients()
	if err != nil {
		return
	}
	now := n.now()
	for _, c := range clients {
		if s.OnUnreachable && c.UnreachableSince > 0 && now.Unix()-c.UnreachableSince >= int64(s.UnreachableMinutes)*60 {
			loc := c.Location()
			body := fmt.Sprintf("The server hasn't been able to reach %s (%s) since %s.\n\nLast error: %s\n\n"+
				"The client keeps running its scheduled backups on its own; the server collects the results once it can reach it again.\n"+
				"Check that the machine is on and that the network or VPN between them works.\n",
				c.Name, c.Address, fmtTime(c.UnreachableSince, loc), c.StatusDetail)
			if l := n.link("/clients/" + c.ID); l != "" {
				body += "\n" + l + "\n"
			}
			n.deliver(&store.Alert{Key: fmt.Sprintf("unreachable:%s:%d", c.ID, c.UnreachableSince), Kind: "unreachable",
				ClientID: c.ID, Subject: "Can't reach " + c.Name}, body)
		}
		if s.OnMissed && c.Status == store.ClientReady {
			n.checkMissed(c, s, now)
		}
	}
}

func (n *Notifier) checkMissed(c *store.Client, s Settings, now time.Time) {
	jobs, err := n.Store.ListJobs(c.ID)
	if err != nil {
		return
	}
	grace := time.Duration(s.MissedGraceMinutes) * time.Minute
	loc := c.Location()
	for _, j := range jobs {
		due := bundle.Prev(j.Schedule, j.Enabled, now.Add(-grace).In(loc))
		// Only judge times after the job existed, and once the server has
		// heard from the client since the grace period ended.
		if due.IsZero() || due.Unix() < j.CreatedAt || c.LastContact < due.Add(grace).Unix() {
			continue
		}
		runs, err := n.Store.ListRuns(store.RunFilter{JobID: j.ID, Limit: 50})
		if err != nil {
			continue
		}
		ran := false
		for _, r := range runs {
			started := time.Unix(r.Started, 0)
			ended := time.Unix(r.Ended, 0)
			inWindow := !started.Before(due.Add(-2*time.Minute)) && !started.After(due.Add(grace))
			// systemd doesn't start a job that's still running from before.
			overlapping := started.Before(due) && (r.Ended == 0 || ended.After(due))
			if inWindow || overlapping {
				ran = true
				break
			}
		}
		if ran {
			continue
		}
		body := fmt.Sprintf("The backup job \"%s\" on %s was due at %s but didn't start within %d minutes.\n\n"+
			"The client was reachable, so it should have run. Common causes: the client was off or asleep at that time, "+
			"its clock is wrong, or its schedule was changed by hand. The next scheduled run is %s.\n",
			j.Name, c.Name, due.Format("Mon 2 Jan 2006 15:04 MST"), s.MissedGraceMinutes,
			bundle.Next(j.Schedule, j.Enabled, now.In(loc)).Format("Mon 2 Jan 15:04 MST"))
		if l := n.link("/jobs/" + j.ID); l != "" {
			body += "\n" + l + "\n"
		}
		n.deliver(&store.Alert{Key: fmt.Sprintf("missed:%s:%d", j.ID, due.Unix()), Kind: "missed", ClientID: c.ID, JobID: j.ID,
			Subject: fmt.Sprintf("Backup didn't run: %s on %s", j.Name, c.Name)}, body)
	}
}

// Loop runs Check every minute until ctx ends.
func (n *Notifier) Loop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.Check()
			_ = n.Store.PruneAlerts(1000)
		}
	}
}

// Test sends a test email with the given settings.
func (n *Notifier) Test(ctx context.Context, s Settings) error {
	return n.send()(ctx, s, "[PBC Manager] Test email from "+n.ServerName(),
		"This is a test from PBC Manager on "+n.ServerName()+".\n\nIf you received it, alerts will reach this address.\n"+n.footer())
}
