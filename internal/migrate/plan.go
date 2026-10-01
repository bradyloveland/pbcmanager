package migrate

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bradyloveland/pbcmanager/internal/alerts"
	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/config"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// Options are the choices made on the import form.
type Options struct {
	// ClientID is the client a 1.x file's jobs belong to (1.x managed one
	// machine). PBC Manager exports match clients by address instead.
	ClientID         string            `json:"client_id"`
	Secrets          map[string]string `json:"secrets"`           // destination key: token secret
	KeyfilePasswords map[string]string `json:"keyfile_passwords"` // job key: password
	AlertPassword    string            `json:"alert_password"`
	ImportAlerts     bool              `json:"import_alerts"`
	ImportSettings   bool              `json:"import_settings"`
	// EnableSchedules turns imported jobs' schedules on. Off by default so
	// the old server and this one don't both back up the same folders.
	EnableSchedules bool `json:"enable_schedules"`
}

// Env is what the plan needs from the server.
type Env struct {
	Store           *store.Store
	CurrentAlerts   alerts.Settings
	DefaultBackupID func(*store.Client) string
}

// Plan says what an import will do.
type Plan struct {
	Kind         string      `json:"kind"`
	Version      string      `json:"version"`
	From         string      `json:"from"`
	NeedsClient  bool        `json:"needs_client"`
	Client       string      `json:"client"` // name of the chosen client
	Destinations []PlanDest  `json:"destinations"`
	Jobs         []PlanJob   `json:"jobs"`
	Alerts       *PlanAlerts `json:"alerts"`
	Settings     []string    `json:"settings"` // labels of settings that would change
	// Problems must be fixed before importing.
	Problems []string `json:"problems"`
	Ready    bool     `json:"ready"`

	newDests []*store.Destination
	newJobs  []*store.Job
	alerts   *alerts.Settings
	settings map[string]any
}

// PlanDest is one destination in the file.
type PlanDest struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Repository string `json:"repository"`
	// Existing is the name of a destination already here that's the same
	// datastore and user; it's used instead of adding another.
	Existing       string `json:"existing"`
	SecretIncluded bool   `json:"secret_included"`
	SecretNeeded   bool   `json:"secret_needed"`
}

// PlanJob is one job in the file.
type PlanJob struct {
	Key     string `json:"key"`
	Name    string `json:"name"`     // as imported (renamed if the name's taken)
	OldName string `json:"old_name"` // as in the file
	Client  string `json:"client"`
	Folders int    `json:"folders"`
	// Skip says why the job won't be imported.
	Skip string `json:"skip"`
	// KeyfilePassword: "included", "needed" (it uses a key file but the
	// file has no password) or "" (no key file).
	KeyfilePassword string `json:"keyfile_password"`
	Enabled         bool   `json:"enabled"`
}

// PlanAlerts summarises the email settings in the file.
type PlanAlerts struct {
	Enabled          bool   `json:"enabled"`
	Host             string `json:"host"`
	To               string `json:"to"`
	PasswordIncluded bool   `json:"password_included"`
	PasswordNeeded   bool   `json:"password_needed"`
}

// Prepare works out the import. With final set, missing secrets are
// problems; otherwise they're shown as fields to fill in.
func Prepare(env Env, src *Source, opts Options, final bool) (*Plan, error) {
	p := &Plan{Kind: src.Kind, Version: src.Version, From: src.From, Destinations: []PlanDest{}, Jobs: []PlanJob{},
		Settings: []string{}, Problems: []string{}, settings: map[string]any{}}
	problem := func(format string, a ...any) { p.Problems = append(p.Problems, fmt.Sprintf(format, a...)) }
	st := env.Store

	existingDests, err := st.ListDestinations()
	if err != nil {
		return nil, err
	}
	destNames := map[string]bool{}
	for _, d := range existingDests {
		destNames[strings.ToLower(d.Name)] = true
	}
	known := map[string]bool{}
	for _, d := range existingDests {
		known[d.ID] = true
	}
	destIDs := map[string]string{} // file key: ID here

	for _, sd := range src.Dests {
		in := sd.Input
		pd := PlanDest{Key: sd.Key, Name: in.Name, SecretIncluded: in.Secret != ""}
		if s := strings.TrimSpace(opts.Secrets[sd.Key]); s != "" {
			in.Secret = s
		}
		probe, err := backups.CleanDestination(withSecret(in), nil)
		if err != nil {
			problem("Destination “%s”: %s", in.Name, err.Error())
			p.Destinations = append(p.Destinations, pd)
			continue
		}
		pd.Repository = probe.Repository()
		if same := sameDest(existingDests, probe); same != nil {
			pd.Existing = same.Name
			destIDs[sd.Key] = same.ID
			p.Destinations = append(p.Destinations, pd)
			continue
		}
		if in.Secret == "" {
			pd.SecretNeeded = true
			if final {
				problem("Enter the token secret (or password) for the destination “%s”.", in.Name)
			}
		}
		in.Name = freeName(in.Name, destNames)
		pd.Name = in.Name
		d, err := backups.CleanDestination(withSecret(in), nil)
		if err != nil {
			problem("Destination “%s”: %s", in.Name, err.Error())
		} else {
			if in.Secret == "" {
				d.Secret = ""
			}
			destIDs[sd.Key] = d.ID
			known[d.ID] = true
			p.newDests = append(p.newDests, d)
		}
		p.Destinations = append(p.Destinations, pd)
	}

	clients, err := st.ListClients()
	if err != nil {
		return nil, err
	}
	var chosen *store.Client
	if src.Kind != KindExport {
		p.NeedsClient = true
		for _, c := range clients {
			if c.ID == opts.ClientID {
				chosen = c
				p.Client = c.Name
			}
		}
		if chosen == nil && len(src.Jobs) > 0 {
			if len(clients) == 0 {
				problem("Add the client these backups belong to first (the machine 1.x ran on), then import again.")
			} else {
				problem("Choose the client these backups belong to.")
			}
		}
	}
	srcClients := map[string]SrcClient{}
	for _, c := range src.Clients {
		srcClients[c.Key] = c
	}
	jobNames := map[string]map[string]bool{} // client ID: lower-case names taken
	existingJobs, err := st.ListJobs("")
	if err != nil {
		return nil, err
	}
	for _, j := range existingJobs {
		if jobNames[j.ClientID] == nil {
			jobNames[j.ClientID] = map[string]bool{}
		}
		jobNames[j.ClientID][strings.ToLower(j.Name)] = true
	}

	for _, sj := range src.Jobs {
		in := sj.Input
		pj := PlanJob{Key: sj.Key, OldName: in.Name, Name: in.Name, Folders: len(in.Shares)}
		c := chosen
		if src.Kind == KindExport {
			c = matchClient(clients, srcClients[sj.ClientKey])
			if c == nil {
				sc := srcClients[sj.ClientKey]
				pj.Skip = fmt.Sprintf("Add the client %s (%s) first, then import again.", sc.Name, sc.Address)
				p.Jobs = append(p.Jobs, pj)
				continue
			}
		}
		if c == nil {
			p.Jobs = append(p.Jobs, pj)
			continue
		}
		pj.Client = c.Name
		if dup := sameJob(existingJobs, c.ID, in); dup != nil {
			pj.Skip = fmt.Sprintf("Already on this server as “%s”.", dup.Name)
			p.Jobs = append(p.Jobs, pj)
			continue
		}
		in.ClientID = c.ID
		in.Destinations = nil
		for _, k := range sj.DestKeys {
			if id, ok := destIDs[k]; ok {
				in.Destinations = append(in.Destinations, id)
			} else {
				problem("Job “%s” uses a destination that isn't in the file.", in.Name)
			}
		}
		if jobNames[c.ID] == nil {
			jobNames[c.ID] = map[string]bool{}
		}
		in.Name = freeName(in.Name, jobNames[c.ID])
		pj.Name = in.Name
		if pw := opts.KeyfilePasswords[sj.Key]; pw != "" {
			in.KeyfilePassword = pw
		}
		switch {
		case strings.TrimSpace(in.Keyfile) == "":
		case in.KeyfilePassword != "":
			pj.KeyfilePassword = "included"
		default:
			pj.KeyfilePassword = "needed"
		}
		enabled := in.Enabled == nil || *in.Enabled
		enabled = enabled && opts.EnableSchedules
		in.Enabled = &enabled
		pj.Enabled = enabled
		j, err := backups.CleanJob(in, nil, env.DefaultBackupID(c), known)
		if err != nil {
			problem("Job “%s”: %s", pj.OldName, err.Error())
		} else {
			p.newJobs = append(p.newJobs, j)
		}
		p.Jobs = append(p.Jobs, pj)
	}

	if src.Alerts != nil {
		a := *src.Alerts
		pw := a.Password
		if opts.AlertPassword != "" {
			pw = opts.AlertPassword
		}
		p.Alerts = &PlanAlerts{Enabled: a.Enabled, Host: a.Host, To: a.To, PasswordIncluded: a.Password != "",
			PasswordNeeded: a.Username != "" && pw == ""}
		if opts.ImportAlerts {
			saved := env.CurrentAlerts
			if pw != "" {
				saved.Password = ""
			}
			cleaned, err := alerts.Clean(alerts.Input{Settings: a, Password: pw}, saved)
			var in *alerts.InputError
			switch {
			case errors.As(err, &in):
				problem("Email alerts: %s", in.Message)
			case err != nil:
				return nil, err
			default:
				p.alerts = &cleaned
			}
		}
	}

	keys := make([]string, 0, len(src.Settings))
	for k := range src.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d, ok := config.Lookup(k)
		if !ok {
			continue
		}
		v, err := config.Clean(k, src.Settings[k])
		if err != nil {
			continue // a value this version doesn't accept keeps the current one
		}
		p.Settings = append(p.Settings, d.Label)
		if opts.ImportSettings {
			p.settings[k] = v
		}
	}
	p.Ready = len(p.Problems) == 0
	return p, nil
}

// withSecret fills in a stand-in secret so the rest of a destination can be
// checked before its secret is entered.
func withSecret(in backups.DestinationInput) backups.DestinationInput {
	if in.Secret == "" {
		in.Secret = "-"
	}
	return in
}

func sameDest(list []*store.Destination, d *store.Destination) *store.Destination {
	for _, e := range list {
		if strings.EqualFold(e.Host, d.Host) && e.Port == d.Port && e.Datastore == d.Datastore && e.Namespace == d.Namespace &&
			e.Username == d.Username && e.TokenName == d.TokenName {
			return e
		}
	}
	return nil
}

func sameJob(list []*store.Job, clientID string, in backups.JobInput) *store.Job {
	for _, j := range list {
		if j.ClientID != clientID || j.BackupID != strings.TrimSpace(in.BackupID) || len(j.Shares) != len(in.Shares) {
			continue
		}
		same := true
		for i := range j.Shares {
			if j.Shares[i].Path != strings.TrimRight(in.Shares[i].Path, "/") && j.Shares[i].Path != in.Shares[i].Path {
				same = false
			}
		}
		if same {
			return j
		}
	}
	return nil
}

func matchClient(list []*store.Client, sc SrcClient) *store.Client {
	if sc.Address == "" && sc.Name == "" {
		return nil
	}
	for _, c := range list {
		if strings.EqualFold(c.Address, sc.Address) && (sc.Port == 0 || c.Port == sc.Port) {
			return c
		}
	}
	for _, c := range list {
		if sc.Name != "" && strings.EqualFold(c.Name, sc.Name) {
			return c
		}
	}
	return nil
}

// freeName returns name, or name with " (imported)" and a number if it's
// taken, and marks it taken.
func freeName(name string, taken map[string]bool) string {
	n := name
	for i := 1; taken[strings.ToLower(n)]; i++ {
		n = name + " (imported)"
		if i > 1 {
			n = fmt.Sprintf("%s (imported %d)", name, i)
		}
	}
	taken[strings.ToLower(n)] = true
	return n
}

// Result is what Apply did. The caller saves Alerts (sealing the password)
// and Settings.
type Result struct {
	Destinations int              `json:"destinations"`
	Jobs         int              `json:"jobs"`
	ClientIDs    []string         `json:"-"`
	Alerts       *alerts.Settings `json:"-"`
	Settings     map[string]any   `json:"-"`
}

// Apply saves a ready plan's destinations and jobs.
func Apply(st *store.Store, p *Plan) (*Result, error) {
	if !p.Ready {
		return nil, errors.New("the import isn't ready")
	}
	r := &Result{Alerts: p.alerts, Settings: p.settings}
	for _, d := range p.newDests {
		if err := st.SaveDestination(d); err != nil {
			return r, fmt.Errorf("saving destination %s: %w", d.Name, err)
		}
		r.Destinations++
	}
	seen := map[string]bool{}
	for _, j := range p.newJobs {
		if err := st.SaveJob(j); err != nil {
			return r, fmt.Errorf("saving job %s: %w", j.Name, err)
		}
		r.Jobs++
		if !seen[j.ClientID] {
			seen[j.ClientID] = true
			r.ClientIDs = append(r.ClientIDs, j.ClientID)
		}
	}
	return r, nil
}
