package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/backups"
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
	// Send delivers an email (Send by default; tests replace it). html is
	// "" for a plain-text email.
	Send func(ctx context.Context, s Settings, subject, text, html string) error

	wg sync.WaitGroup
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) send() func(context.Context, Settings, string, string, string) error {
	if n.Send != nil {
		return n.Send
	}
	return Send
}

// Wait waits for alerts being sent in the background (tests).
func (n *Notifier) Wait() { n.wg.Wait() }

// bodies renders an email as text and, unless plain text is chosen, HTML.
func (n *Notifier) bodies(s Settings, e *Email) (text, html string) {
	text = e.Text("\n--\n" + n.footer() + "\n")
	if !s.PlainText {
		var err error
		if html, err = e.HTML(n.footer()); err != nil {
			slog.Error("building the HTML email", "err", err)
			html = ""
		}
	}
	return text, html
}

// deliver records an alert once (by key) and sends it in the background.
func (n *Notifier) deliver(a *store.Alert, e *Email) {
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
	text, html := n.bodies(s, e)
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err := n.send()(ctx, s, a.Subject, text, html)
		if err != nil {
			slog.Warn("couldn't send alert email", "subject", a.Subject, "err", err)
		}
		_ = n.Store.AlertDelivered(a.ID, err)
	}()
}

func (n *Notifier) footer() string {
	return "Sent by PBC Manager on " + n.ServerName() + ". Change alert settings under Alerts in the web UI."
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
	e := &Email{Tone: "ok", Label: "Backup succeeded", Headline: fmt.Sprintf("%s on %s %s", r.JobName, c.Name, verb),
		Paras: []string{fmt.Sprintf("The backup job \"%s\" on %s %s.", r.JobName, c.Name, verb)}}
	if verb == "failed" {
		e.Tone, e.Label = "bad", "Backup failed"
	}
	if late {
		e.Paras = append(e.Paras, fmt.Sprintf("This is reported late: the server only heard about it at %s, because it couldn't reach the client or wasn't running. The client ran the backup on its own schedule as usual.",
			fmtTime(r.CollectedAt, loc)))
	}
	e.Rows = []Row{
		{Label: "Client", Value: fmt.Sprintf("%s (%s)", c.Name, c.Address)},
		{Label: "Destination", Value: r.DestinationName},
		{Label: "Started", Value: fmt.Sprintf("%s (%s)", fmtTime(r.Started, loc), map[string]string{"manual": "by hand", "schedule": "scheduled"}[r.Trigger])},
		{Label: "Finished", Value: fmt.Sprintf("%s, after %s", fmtTime(r.Ended, loc), duration(r.Started, r.Ended))},
	}
	if r.ExitCode != nil {
		e.Rows = append(e.Rows, Row{Label: "Exit code", Value: fmt.Sprint(*r.ExitCode)})
	}
	e.Rows = append(e.Rows, Row{Label: "Result", Value: r.Summary})
	e.Rows = append(e.Rows, statsRows(r.Stats)...)
	if sp := n.space(r.DestinationID); sp != "" {
		e.Rows = append(e.Rows, Row{Label: "Space left", Value: sp})
	}
	if l := n.link("/activity/" + c.ID + "/" + r.ID); l != "" {
		e.Link, e.LinkText = l, "View the full log"
	}
	if r.Status == bundle.Failed && n.LogPath != nil {
		e.Log = lastLines(n.LogPath(c.ID, r.ID), 40)
	}
	subject := fmt.Sprintf("Backup %s: %s on %s", verb, r.JobName, c.Name)
	if late {
		subject += " (reported late)"
	}
	n.deliver(&store.Alert{Key: "run:" + c.ID + ":" + r.ID, Kind: verb, ClientID: c.ID, JobID: r.JobID, RunID: r.ID, Subject: subject}, e)
}

// statsRows are the backup's figures, as far as the client printed them.
func statsRows(st *bundle.Stats) []Row {
	if st == nil {
		return nil
	}
	var rows []Row
	if st.Has("sizes") {
		rows = append(rows, Row{Label: "Data read", Value: Bytes(st.Read)},
			Row{Label: "Uploaded", Value: fmt.Sprintf("%s new data (%s compressed)", Bytes(st.Uploaded), Bytes(st.Compressed))})
	}
	if st.Has("reused") && st.Read > 0 {
		rows = append(rows, Row{Label: "Reused", Value: fmt.Sprintf("%s from the last backup (%.0f%%)", Bytes(st.Reused), st.ReusedPercent())})
	}
	if st.Has("files") {
		rows = append(rows, Row{Label: "Files", Value: fmt.Sprintf("%d, of which %d new or changed", st.Files, st.Changed)})
	}
	if st.Has("duration") && st.Seconds > 0 {
		rows = append(rows, Row{Label: "Upload time", Value: (time.Duration(st.Seconds * float64(time.Second))).Round(time.Second / 10).String()})
	}
	if len(st.Archives) > 1 {
		for _, a := range st.Archives {
			rows = append(rows, Row{Label: a.Name, Value: fmt.Sprintf("read %s, uploaded %s", Bytes(a.Read), Bytes(a.Uploaded)), Indent: true})
		}
	}
	return rows
}

// space describes a destination's free space from the last check, or "".
func (n *Notifier) space(destID string) string {
	var sp backups.Space
	if ok, err := n.Store.GetSize(backups.SizeDest, destID, &sp); !ok || err != nil || sp.Error != "" || sp.Total == nil || sp.Avail == nil {
		return ""
	}
	return fmt.Sprintf("%s free of %s on the destination (%d%% used)", Bytes(*sp.Avail), Bytes(*sp.Total), sp.Percent())
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
	e := &Email{Tone: "ok", Label: "Client is back", Headline: c.Name + " can be reached again", Paras: []string{
		fmt.Sprintf("The server can reach %s (%s) again, after losing contact at %s (%s ago).", c.Name, c.Address, fmtTime(since, loc), duration(since, n.now().Unix())),
		"Any backups it ran on its own meanwhile are being collected now; failures among them are reported separately."}}
	if l := n.link("/clients/" + c.ID); l != "" {
		e.Link, e.LinkText = l, "Open the client"
	}
	n.deliver(&store.Alert{Key: fmt.Sprintf("reachable:%s:%d", c.ID, since), Kind: "reachable", ClientID: c.ID,
		Subject: c.Name + " can be reached again"}, e)
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
			e := &Email{Tone: "bad", Label: "Can't reach client", Headline: "Can't reach " + c.Name, Paras: []string{
				fmt.Sprintf("The server hasn't been able to reach %s (%s) since %s.", c.Name, c.Address, fmtTime(c.UnreachableSince, loc)),
				"The client keeps running its scheduled backups on its own; the server collects the results once it can reach it again. Check that the machine is on and that the network or VPN between them works."},
				Rows: []Row{{Label: "Last error", Value: c.StatusDetail}}}
			if l := n.link("/clients/" + c.ID); l != "" {
				e.Link, e.LinkText = l, "Open the client"
			}
			n.deliver(&store.Alert{Key: fmt.Sprintf("unreachable:%s:%d", c.ID, c.UnreachableSince), Kind: "unreachable",
				ClientID: c.ID, Subject: "Can't reach " + c.Name}, e)
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
		e := &Email{Tone: "warn", Label: "Backup didn't run", Headline: fmt.Sprintf("%s on %s didn't run", j.Name, c.Name), Paras: []string{
			fmt.Sprintf("The backup job \"%s\" on %s was due at %s but didn't start within %d minutes.", j.Name, c.Name, due.Format("Mon 2 Jan 2006 15:04 MST"), s.MissedGraceMinutes),
			fmt.Sprintf("The client was reachable, so it should have run. Common causes: the client was off or asleep at that time, its clock is wrong, or its schedule was changed by hand. The next scheduled run is %s.",
				bundle.Next(j.Schedule, j.Enabled, now.In(loc)).Format("Mon 2 Jan 15:04 MST"))}}
		if l := n.link("/jobs/" + j.ID); l != "" {
			e.Link, e.LinkText = l, "Open the job"
		}
		n.deliver(&store.Alert{Key: fmt.Sprintf("missed:%s:%d", j.ID, due.Unix()), Kind: "missed", ClientID: c.ID, JobID: j.ID,
			Subject: fmt.Sprintf("Backup didn't run: %s on %s", j.Name, c.Name)}, e)
	}
}

// Space is told each new space reading for a destination. fullSince is
// when it went over the nearly-full threshold (0 if it wasn't); the new value
// is returned for saving. It alerts once each time a destination fills up,
// and once more when there's room again.
func (n *Notifier) Space(d *store.Destination, used, total, fullSince int64) int64 {
	if total <= 0 {
		return fullSince
	}
	s := n.Settings()
	pct := float64(used) * 100 / float64(total)
	rows := []Row{{Label: "Destination", Value: d.Name}, {Label: "Datastore", Value: fmt.Sprintf("%s on %s", d.Datastore, d.Host)},
		{Label: "Used", Value: fmt.Sprintf("%s of %s (%.0f%%)", Bytes(used), Bytes(total), pct)}, {Label: "Free", Value: Bytes(total - used)}}
	link := n.link("/destinations")
	switch {
	case pct >= float64(s.FullPercent) && fullSince == 0:
		fullSince = n.now().Unix()
		if s.OnFull {
			e := &Email{Tone: "warn", Label: "Destination nearly full", Headline: fmt.Sprintf("%s is %.0f%% full", d.Name, pct), Rows: rows, Paras: []string{
				fmt.Sprintf("The datastore %s on the PBS server %s (destination \"%s\") is %.0f%% full: %s used of %s, %s free.", d.Datastore, d.Host, d.Name, pct, Bytes(used), Bytes(total), Bytes(total-used)),
				"Backups to it will fail once it's full. Free up space by pruning old backups and running garbage collection on the PBS server, or give the datastore more room."}}
			if link != "" {
				e.Link, e.LinkText = link, "Open Destinations"
			}
			n.deliver(&store.Alert{Key: fmt.Sprintf("full:%s:%d", d.ID, fullSince), Kind: "full",
				Subject: fmt.Sprintf("Destination nearly full: %s (%.0f%%)", d.Name, pct)}, e)
		}
	case fullSince != 0 && pct < float64(s.FullPercent)-2:
		// A little below the threshold, so hovering around it doesn't
		// send an email every check.
		if had, _ := n.Store.HasAlert(fmt.Sprintf("full:%s:%d", d.ID, fullSince)); had {
			e := &Email{Tone: "ok", Label: "Destination has room again", Headline: d.Name + " has room again", Rows: rows, Paras: []string{
				fmt.Sprintf("The datastore %s on %s (destination \"%s\") is now %.0f%% full, %s free.", d.Datastore, d.Host, d.Name, pct, Bytes(total-used))}}
			if link != "" {
				e.Link, e.LinkText = link, "Open Destinations"
			}
			n.deliver(&store.Alert{Key: fmt.Sprintf("roomy:%s:%d", d.ID, fullSince), Kind: "space_ok",
				Subject: "Destination has room again: " + d.Name}, e)
		}
		fullSince = 0
	}
	return fullSince
}

// Bytes formats a size the way the web UI does (1 KiB = 1024 bytes).
func Bytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
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

// Test sends a test email with the given settings. It's laid out like a
// real alert, so it shows what they'll look like.
func (n *Notifier) Test(ctx context.Context, s Settings) error {
	e := &Email{Tone: "info", Label: "Test email", Headline: "Test email from " + n.ServerName(), Paras: []string{
		"This is a test from PBC Manager on " + n.ServerName() + ".",
		"If you received it, alerts will reach this address. Alerts look like this one, with a coloured bar saying what happened and the details below."},
		Rows: []Row{{Label: "Mail server", Value: fmt.Sprintf("%s, port %d (%s)", s.Host, s.Port, map[string]string{"starttls": "STARTTLS", "ssl": "SSL/TLS", "none": "no encryption"}[s.Security])},
			{Label: "Sending to", Value: strings.Join(s.Recipients(), ", ")}}}
	if l := n.link("/alerts"); l != "" {
		e.Link, e.LinkText = l, "Open alert settings"
	}
	text, html := n.bodies(s, e)
	return n.send()(ctx, s, "[PBC Manager] Test email from "+n.ServerName(), text, html)
}
