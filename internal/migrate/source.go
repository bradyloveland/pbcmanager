// Package migrate imports settings: from PBS Backup Manager 1.x (a settings
// export, or its config.json with credentials) and from PBC Manager's own
// export. It also writes that export.
package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bradyloveland/pbcmanager/internal/alerts"
	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

// Kinds of file that can be imported.
const (
	KindV1Export = "1.x-export" // pbs-manager export: no credentials
	KindV1Config = "1.x-config" // /etc/pbs-manager/config.json: with credentials
	KindExport   = "pbcm-export"
)

// ExportFormat marks a PBC Manager settings export.
const ExportFormat = "pbcm-settings"

// Source is an imported file, turned into this version's terms.
type Source struct {
	Kind    string
	Version string // the version that wrote it, if known
	From    string // the machine it came from, if known
	Dests   []SrcDest
	Jobs    []SrcJob
	Clients []SrcClient // PBC Manager exports only
	// Alerts are the email settings (nil if the file has none); the
	// password is set only for a 1.x config.json.
	Alerts   *alerts.Settings
	Settings map[string]json.RawMessage // setting key: value
}

// SrcDest is a destination in the file. Key identifies it within the file.
type SrcDest struct {
	Key   string
	Input backups.DestinationInput
}

// SrcJob is a job in the file.
type SrcJob struct {
	Key       string
	ClientKey string // PBC Manager exports: which SrcClient
	Input     backups.JobInput
	DestKeys  []string
}

// SrcClient is a client in a PBC Manager export.
type SrcClient struct {
	Key, Name, Address string
	Port               int
}

// ErrUnknown means the file isn't something that can be imported.
var ErrUnknown = errors.New("this file isn't a settings export from PBC Manager or PBS Backup Manager 1.x, or a 1.x config.json")

// Parse reads an import file.
func Parse(raw []byte) (*Source, error) {
	var head struct {
		Format        string          `json:"format"`
		FormatVersion json.Number     `json:"format_version"`
		Targets       json.RawMessage `json:"targets"`
		Jobs          json.RawMessage `json:"jobs"`
		Auth          json.RawMessage `json:"auth"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, ErrUnknown
	}
	switch {
	case head.Format == "pbs-manager-config":
		if head.FormatVersion.String() != "1" {
			return nil, fmt.Errorf("this 1.x export uses format %s, which this version doesn't know", head.FormatVersion)
		}
		return parseV1(raw, KindV1Export)
	case head.Format == ExportFormat:
		if head.FormatVersion.String() != "1" {
			return nil, fmt.Errorf("this export uses format %s, which this version doesn't know; update this server first", head.FormatVersion)
		}
		return parseExport(raw)
	case head.Format == "" && head.Targets != nil && head.Jobs != nil:
		return parseV1(raw, KindV1Config)
	}
	return nil, ErrUnknown
}

// ---------------------------------------------------------------- 1.x

type v1File struct {
	AppVersion   string `json:"app_version"`
	ExportedFrom string `json:"exported_from"`
	Targets      []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Datastore   string `json:"datastore"`
		Username    string `json:"username"`
		TokenName   string `json:"token_name"`
		Secret      string `json:"secret"`
		Fingerprint string `json:"fingerprint"`
	} `json:"targets"`
	Jobs []struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		TargetID  string         `json:"target_id"`
		BackupID  string         `json:"backup_id"`
		Shares    []bundle.Share `json:"shares"`
		Excludes  any            `json:"excludes"`
		Schedule  *v1Schedule    `json:"schedule"`
		Change    string         `json:"change_detection_mode"`
		Rate      string         `json:"rate"`
		Keyfile   string         `json:"keyfile"`
		KeyfilePW string         `json:"keyfile_password"`
		Enabled   *bool          `json:"enabled"`
	} `json:"jobs"`
	Email *struct {
		Enabled       bool   `json:"enabled"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		Security      string `json:"security"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		From          string `json:"from_addr"`
		To            string `json:"to_addrs"`
		NotifyFailure *bool  `json:"notify_failure"`
		NotifySuccess bool   `json:"notify_success"`
	} `json:"email"`
	Settings struct {
		KeepRuns int `json:"keep_runs"`
	} `json:"settings"`
}

type v1Schedule struct {
	Type          string `json:"type"`
	Time          string `json:"time"`
	Days          []int  `json:"days"`
	IntervalHours int    `json:"interval_hours"`
}

func parseV1(raw []byte, kind string) (*Source, error) {
	var f v1File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, errors.New("the 1.x file couldn't be read: " + err.Error())
	}
	src := &Source{Kind: kind, Version: f.AppVersion, From: f.ExportedFrom, Settings: map[string]json.RawMessage{}}
	keepSecrets := kind == KindV1Config
	for i, t := range f.Targets {
		in := backups.DestinationInput{Name: t.Name, Host: t.Host, Port: t.Port, Datastore: t.Datastore,
			Username: t.Username, TokenName: t.TokenName, Fingerprint: t.Fingerprint}
		if keepSecrets {
			in.Secret = t.Secret
		}
		src.Dests = append(src.Dests, SrcDest{Key: keyOr(t.ID, "t", i), Input: in})
	}
	for i, j := range f.Jobs {
		in := backups.JobInput{Name: j.Name, BackupID: j.BackupID, Shares: j.Shares, Excludes: j.Excludes,
			ChangeDetection: j.Change, Rate: j.Rate, Keyfile: j.Keyfile, Enabled: j.Enabled, Schedule: v1ToSchedule(j.Schedule)}
		if in.ChangeDetection == "" {
			in.ChangeDetection = "metadata"
		}
		for k := range in.Shares {
			if in.Shares[k].Archive == "" {
				in.Shares[k].Archive = backups.ArchiveFromPath(in.Shares[k].Path)
			}
		}
		if keepSecrets {
			in.KeyfilePassword = j.KeyfilePW
		}
		src.Jobs = append(src.Jobs, SrcJob{Key: keyOr(j.ID, "j", i), Input: in, DestKeys: []string{j.TargetID}})
	}
	if e := f.Email; e != nil {
		a := alerts.Defaults()
		a.Enabled, a.Host, a.Username, a.From, a.To = e.Enabled, e.Host, e.Username, e.From, e.To
		if e.Port != 0 {
			a.Port = e.Port
		}
		if e.Security != "" {
			a.Security = e.Security
		}
		if e.NotifyFailure != nil {
			a.OnFailure = *e.NotifyFailure
		}
		a.OnSuccess = e.NotifySuccess
		if keepSecrets {
			a.Password = e.Password
		}
		src.Alerts = &a
	}
	if n := f.Settings.KeepRuns; n > 0 {
		src.Settings["history.client_runs"], _ = json.Marshal(clamp(n, 20, 10000))
	}
	return src, nil
}

// v1ToSchedule converts a 1.x schedule. The fields are the same (days 0 is
// Monday in both); 1.x filled in missing ones when it read them.
func v1ToSchedule(s *v1Schedule) bundle.Schedule {
	if s == nil || s.Type == "" {
		return bundle.Schedule{Type: "manual", Time: "02:00", Days: allDays(), IntervalHours: 1}
	}
	out := bundle.Schedule{Type: s.Type, Time: s.Time, Days: s.Days, IntervalHours: s.IntervalHours}
	if out.Time == "" {
		out.Time = "02:00"
	}
	if len(out.Days) == 0 {
		out.Days = allDays()
	}
	if out.IntervalHours == 0 {
		out.IntervalHours = 1
	}
	return out
}

func allDays() []int { return []int{0, 1, 2, 3, 4, 5, 6} }

func keyOr(id, prefix string, i int) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return fmt.Sprintf("%s%d", prefix, i)
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
