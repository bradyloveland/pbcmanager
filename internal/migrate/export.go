package migrate

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/alerts"
	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/store"
	"github.com/bradyloveland/pbcmanager/internal/version"
)

// Export is a PBC Manager settings export. It never holds credentials:
// token secrets, key file passwords and the mail password are left out
// (they're json:"-" on their types), as are sign-in details.
type Export struct {
	Format        string                     `json:"format"`
	FormatVersion int                        `json:"format_version"`
	Version       string                     `json:"app_version"`
	ExportedAt    string                     `json:"exported_at"`
	ExportedFrom  string                     `json:"exported_from"`
	Note          string                     `json:"note"`
	Settings      map[string]json.RawMessage `json:"settings"`
	Alerts        *alerts.Settings           `json:"alerts"`
	Destinations  []ExportDest               `json:"destinations"`
	Clients       []ExportClient             `json:"clients"`
	Jobs          []ExportJob                `json:"jobs"`
}

// ExportDest is a destination without its secret.
type ExportDest struct {
	ID string `json:"id"`
	backups.DestinationInput
}

// ExportClient identifies a client; it has to be added again on the new
// server (over SSH) before its jobs can be imported.
type ExportClient struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// ExportJob is a job without its key file password.
type ExportJob struct {
	ID string `json:"id"`
	backups.JobInput
}

// BuildExport writes the export. settings are the Settings page values.
func BuildExport(st *store.Store, settings map[string]any, a alerts.Settings, serverName string, now time.Time) (*Export, error) {
	e := &Export{Format: ExportFormat, FormatVersion: 1, Version: version.Version, ExportedAt: now.UTC().Format(time.RFC3339),
		ExportedFrom: serverName, Settings: map[string]json.RawMessage{},
		Note:         "Credentials aren't included: token secrets, key file passwords and the mail password are asked for when importing.",
		Destinations: []ExportDest{}, Clients: []ExportClient{}, Jobs: []ExportJob{}}
	for k, v := range settings {
		e.Settings[k], _ = json.Marshal(v)
	}
	a.Password = ""
	e.Alerts = &a
	dests, err := st.ListDestinations()
	if err != nil {
		return nil, err
	}
	for _, d := range dests {
		e.Destinations = append(e.Destinations, ExportDest{ID: d.ID, DestinationInput: backups.DestinationInput{Name: d.Name, Host: d.Host,
			Port: d.Port, Datastore: d.Datastore, Namespace: d.Namespace, Username: d.Username, TokenName: d.TokenName, Fingerprint: d.Fingerprint}})
	}
	clients, err := st.ListClients()
	if err != nil {
		return nil, err
	}
	for _, c := range clients {
		e.Clients = append(e.Clients, ExportClient{ID: c.ID, Name: c.Name, Address: c.Address, Port: c.Port})
	}
	jobs, err := st.ListJobs("")
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		enabled := j.Enabled
		e.Jobs = append(e.Jobs, ExportJob{ID: j.ID, JobInput: backups.JobInput{ClientID: j.ClientID, Name: j.Name, BackupID: j.BackupID,
			Shares: j.Shares, Excludes: j.Excludes, Schedule: j.Schedule, ChangeDetection: j.ChangeDetection, Rate: j.Rate,
			Keyfile: j.Keyfile, Enabled: &enabled, Destinations: j.Destinations}})
	}
	return e, nil
}

func parseExport(raw []byte) (*Source, error) {
	var e Export
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, errors.New("the export couldn't be read: " + err.Error())
	}
	src := &Source{Kind: KindExport, Version: e.Version, From: e.ExportedFrom, Settings: e.Settings, Alerts: e.Alerts}
	if src.Settings == nil {
		src.Settings = map[string]json.RawMessage{}
	}
	for i, d := range e.Destinations {
		in := d.DestinationInput
		in.Secret = ""
		src.Dests = append(src.Dests, SrcDest{Key: keyOr(d.ID, "d", i), Input: in})
	}
	for _, c := range e.Clients {
		src.Clients = append(src.Clients, SrcClient{Key: c.ID, Name: c.Name, Address: c.Address, Port: c.Port})
	}
	for i, j := range e.Jobs {
		in := j.JobInput
		in.KeyfilePassword = ""
		src.Jobs = append(src.Jobs, SrcJob{Key: keyOr(j.ID, "j", i), ClientKey: in.ClientID, Input: in, DestKeys: in.Destinations})
	}
	if src.Alerts != nil {
		src.Alerts.Password = ""
	}
	return src, nil
}
