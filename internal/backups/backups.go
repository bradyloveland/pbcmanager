// Package backups checks destinations and jobs entered in the UI, builds the
// bundle each client is sent, and talks to PBS from the server (connection
// tests, datastore space, snapshot lists) with the server's own
// proxmox-backup-client.
package backups

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// InputError is shown to the user as-is.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

func bad(format string, a ...any) error { return &InputError{fmt.Sprintf(format, a...)} }

// NewID returns a 12-character ID usable in unit and file names.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var (
	datastoreRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._\-]*$`)
	userRE      = regexp.MustCompile(`^[^\s@!:]+@[A-Za-z0-9._\-]+$`)
	tokenRE     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._\-]*$`)
	hostRE      = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[0-9A-Fa-f]*:[0-9A-Fa-f:.]+|[A-Za-z0-9.\-]+)$`)
	fpRE        = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$`)
	nsRE        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._\-]*(/[A-Za-z0-9_][A-Za-z0-9._\-]*){0,7}$`)
	rateRE      = regexp.MustCompile(`(?i)^\d+(\.\d+)?\s*([KMGTP]i?)?B?$`)
)

func cleanName(s, what string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > 64 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", bad("Give the %s a name of up to 64 characters.", what)
	}
	return s, nil
}

// DestinationInput is the destination form.
type DestinationInput struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Datastore   string `json:"datastore"`
	Namespace   string `json:"namespace"`
	Username    string `json:"username"`
	TokenName   string `json:"token_name"`
	Secret      string `json:"secret"`
	Fingerprint string `json:"fingerprint"`
}

// CleanDestination checks the form. An empty secret keeps existing's.
func CleanDestination(in DestinationInput, existing *store.Destination) (*store.Destination, error) {
	d := &store.Destination{ID: NewID()}
	if existing != nil {
		d.ID, d.CreatedAt, d.Secret = existing.ID, existing.CreatedAt, existing.Secret
	}
	var err error
	if d.Name, err = cleanName(in.Name, "destination"); err != nil {
		return nil, err
	}
	d.Host = strings.TrimSpace(in.Host)
	if d.Host == "" || !hostRE.MatchString(d.Host) {
		return nil, bad("Enter the PBS server's host name or IP address.")
	}
	d.Port = in.Port
	if d.Port == 0 {
		d.Port = 8007
	}
	if d.Port < 1 || d.Port > 65535 {
		return nil, bad("The port must be between 1 and 65535.")
	}
	d.Datastore = strings.TrimSpace(in.Datastore)
	if !datastoreRE.MatchString(d.Datastore) {
		return nil, bad("Enter the datastore name exactly as it appears in PBS.")
	}
	d.Namespace = strings.Trim(strings.TrimSpace(in.Namespace), "/")
	if d.Namespace != "" && !nsRE.MatchString(d.Namespace) {
		return nil, bad("Namespaces are names separated by /, like clients/nas. Leave it blank for the datastore's root.")
	}
	d.Username = strings.TrimSpace(in.Username)
	if !userRE.MatchString(d.Username) {
		return nil, bad("Enter the user with its realm, for example nas-backup@pbs.")
	}
	d.TokenName = strings.TrimSpace(in.TokenName)
	if d.TokenName != "" && !tokenRE.MatchString(d.TokenName) {
		return nil, bad("Token names can use letters, numbers, dots, dashes and underscores.")
	}
	if in.Secret != "" {
		if strings.ContainsAny(in.Secret, "\n\r") {
			return nil, bad("The token secret can't contain line breaks.")
		}
		d.Secret = in.Secret
	}
	if d.Secret == "" {
		return nil, bad("Enter the API token secret (or the user's password if you aren't using a token).")
	}
	d.Fingerprint = strings.ToLower(strings.TrimSpace(in.Fingerprint))
	if d.Fingerprint != "" && !fpRE.MatchString(d.Fingerprint) {
		return nil, bad("The fingerprint should be 32 pairs of hex digits separated by colons.")
	}
	return d, nil
}

// JobInput is the job form.
type JobInput struct {
	ClientID        string          `json:"client_id"`
	Name            string          `json:"name"`
	BackupID        string          `json:"backup_id"`
	Shares          []bundle.Share  `json:"shares"`
	Excludes        any             `json:"excludes"` // list or newline-separated text
	Schedule        bundle.Schedule `json:"schedule"`
	ChangeDetection string          `json:"change_detection"`
	Rate            string          `json:"rate"`
	Keyfile         string          `json:"keyfile"`
	KeyfilePassword string          `json:"keyfile_password"`
	ClearKeyfilePW  bool            `json:"clear_keyfile_password"`
	Enabled         *bool           `json:"enabled"`
	Destinations    []string        `json:"destinations"`
}

var archiveClean = regexp.MustCompile(`[^A-Za-z0-9_\-]+`)

// ArchiveFromPath suggests an archive name for a folder, like 1.x did.
func ArchiveFromPath(p string) string {
	base := filepath.Base(filepath.Clean(p))
	if base == "/" || base == "." {
		base = "root"
	}
	name := strings.Trim(archiveClean.ReplaceAllString(base, "-"), "-_")
	if name == "" {
		name = "root"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]`).MatchString(name) {
		name = "a" + name
	}
	if len(name) > 48 {
		name = name[:48]
	}
	return name
}

// CleanJob checks the job form. defaultBackupID is used when it's blank
// (the client's host name); destinations lists the IDs that exist.
func CleanJob(in JobInput, existing *store.Job, defaultBackupID string, destinations map[string]bool) (*store.Job, error) {
	j := &store.Job{ID: NewID(), ClientID: in.ClientID, Enabled: true}
	if existing != nil {
		j.ID, j.ClientID, j.CreatedAt, j.KeyfilePassword = existing.ID, existing.ClientID, existing.CreatedAt, existing.KeyfilePassword
	}
	var err error
	if j.Name, err = cleanName(in.Name, "job"); err != nil {
		return nil, err
	}
	j.BackupID = strings.TrimSpace(in.BackupID)
	if j.BackupID == "" {
		j.BackupID = defaultBackupID
	}
	if !bundle.BackupIDRE.MatchString(j.BackupID) {
		return nil, bad("The backup ID can use letters, numbers, dots, dashes and underscores.")
	}
	seen := map[string]bool{}
	for _, s := range in.Shares {
		p := strings.TrimSpace(s.Path)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
			return nil, bad("Folder paths must be absolute: %s", p)
		}
		p = filepath.Clean(p)
		a := strings.TrimSpace(s.Archive)
		if a == "" {
			a = ArchiveFromPath(p)
		}
		if !bundle.ArchiveRE.MatchString(a) {
			return nil, bad("Archive name “%s” can only use letters, numbers, dashes and underscores.", a)
		}
		if seen[a] {
			return nil, bad("Two folders share the archive name “%s”. Give each a unique name.", a)
		}
		seen[a] = true
		j.Shares = append(j.Shares, bundle.Share{Path: p, Archive: a})
	}
	if len(j.Shares) == 0 {
		return nil, bad("Add at least one folder to back up.")
	}
	if len(j.Shares) > 100 {
		return nil, bad("A job can back up at most 100 folders.")
	}
	var excludes []string
	switch v := in.Excludes.(type) {
	case string:
		excludes = strings.Split(v, "\n")
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				excludes = append(excludes, s)
			}
		}
	}
	j.Excludes = []string{}
	for _, x := range excludes {
		if x = strings.TrimSpace(x); x != "" {
			if strings.IndexFunc(x, unicode.IsControl) >= 0 {
				return nil, bad("Exclusion patterns can't contain control characters.")
			}
			j.Excludes = append(j.Excludes, x)
		}
	}
	if len(j.Excludes) > 200 {
		return nil, bad("Use at most 200 exclusion patterns.")
	}
	if j.Schedule, err = bundle.CleanSchedule(in.Schedule); err != nil {
		return nil, bad("%s.", capital(err.Error()))
	}
	j.ChangeDetection = in.ChangeDetection
	if j.ChangeDetection == "" {
		j.ChangeDetection = "metadata"
	}
	if j.ChangeDetection != "metadata" && j.ChangeDetection != "data" && j.ChangeDetection != "legacy" {
		return nil, bad("Unknown change detection mode.")
	}
	j.Rate = strings.TrimSpace(in.Rate)
	if j.Rate != "" && !rateRE.MatchString(j.Rate) {
		return nil, bad("Enter the speed limit like 20MiB (per second).")
	}
	j.Keyfile = strings.TrimSpace(in.Keyfile)
	if j.Keyfile != "" && (!strings.HasPrefix(j.Keyfile, "/") || strings.IndexFunc(j.Keyfile, unicode.IsControl) >= 0) {
		return nil, bad("The encryption key file path must be absolute.")
	}
	if in.KeyfilePassword != "" {
		if strings.ContainsAny(in.KeyfilePassword, "\n\r") {
			return nil, bad("The key file password can't contain line breaks.")
		}
		j.KeyfilePassword = in.KeyfilePassword
	}
	if in.ClearKeyfilePW || j.Keyfile == "" {
		j.KeyfilePassword = ""
	}
	if in.Enabled != nil {
		j.Enabled = *in.Enabled
	}
	dseen := map[string]bool{}
	for _, d := range in.Destinations {
		if !destinations[d] {
			return nil, bad("Choose destinations from the list.")
		}
		if !dseen[d] {
			dseen[d] = true
			j.Destinations = append(j.Destinations, d)
		}
	}
	if len(j.Destinations) == 0 {
		return nil, bad("Choose at least one destination to back up to.")
	}
	return j, nil
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// BuildBundle is what a client gets: its jobs and the destinations they use.
func BuildBundle(jobs []*store.Job, dests map[string]*store.Destination, keepRuns, keepDays int) bundle.Bundle {
	b := bundle.Bundle{Version: bundle.Version, Jobs: []bundle.Job{}, Destinations: []bundle.Destination{},
		KeepRuns: keepRuns, KeepDays: keepDays}
	used := map[string]bool{}
	for _, j := range jobs {
		bj := bundle.Job{ID: j.ID, Name: j.Name, BackupID: j.BackupID, Shares: j.Shares, Excludes: j.Excludes,
			ChangeDetection: j.ChangeDetection, Rate: j.Rate, Keyfile: j.Keyfile, KeyfilePassword: j.KeyfilePassword,
			Schedule: j.Schedule, Enabled: j.Enabled}
		for _, id := range j.Destinations {
			if _, ok := dests[id]; ok {
				bj.Destinations = append(bj.Destinations, id)
				used[id] = true
			}
		}
		if len(bj.Destinations) == 0 {
			continue // its destinations were deleted; nothing to back up to
		}
		b.Jobs = append(b.Jobs, bj)
	}
	for id := range used {
		d := dests[id]
		b.Destinations = append(b.Destinations, bundle.Destination{ID: d.ID, Name: d.Name, Repository: d.Repository(),
			Fingerprint: d.Fingerprint, Namespace: d.Namespace, Secret: d.Secret})
	}
	return b
}

// EnsureDir creates a directory for server-side files.
func EnsureDir(dir string) error { return os.MkdirAll(dir, 0o700) }
