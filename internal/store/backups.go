package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
)

// --------------------------------------------------------------- destinations

// Destination is a PBS datastore. Secret is decrypted.
type Destination struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Datastore   string `json:"datastore"`
	Namespace   string `json:"namespace"`
	Username    string `json:"username"`
	TokenName   string `json:"token_name"`
	Secret      string `json:"-"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   int64  `json:"created_at"`
}

// Repository is the PBS_REPOSITORY value.
func (d *Destination) Repository() string {
	host := d.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	auth := d.Username
	if d.TokenName != "" {
		auth += "!" + d.TokenName
	}
	return auth + "@" + host + ":" + strconv.Itoa(d.Port) + ":" + d.Datastore
}

const destCols = `id, name, host, port, datastore, namespace, username, token_name, secret, fingerprint, created_at`

func (s *Store) scanDest(row interface{ Scan(...any) error }) (*Destination, error) {
	var d Destination
	var sealed string
	err := row.Scan(&d.ID, &d.Name, &d.Host, &d.Port, &d.Datastore, &d.Namespace, &d.Username, &d.TokenName, &sealed, &d.Fingerprint, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Secret, err = s.box.Open(sealed)
	return &d, err
}

func destDup(err error) error {
	if err != nil && strings.Contains(err.Error(), "destinations.name") {
		return &ErrDuplicate{"name"}
	}
	return err
}

// SaveDestination inserts or updates a destination.
func (s *Store) SaveDestination(d *Destination) error {
	if d.CreatedAt == 0 {
		d.CreatedAt = s.Now().Unix()
	}
	_, err := s.db.Exec(`INSERT INTO destinations (`+destCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, host=excluded.host, port=excluded.port, datastore=excluded.datastore,
		namespace=excluded.namespace, username=excluded.username, token_name=excluded.token_name, secret=excluded.secret,
		fingerprint=excluded.fingerprint`,
		d.ID, d.Name, d.Host, d.Port, d.Datastore, d.Namespace, d.Username, d.TokenName, s.box.Seal(d.Secret), d.Fingerprint, d.CreatedAt)
	return destDup(err)
}

// GetDestination returns one destination.
func (s *Store) GetDestination(id string) (*Destination, error) {
	return s.scanDest(s.db.QueryRow(`SELECT `+destCols+` FROM destinations WHERE id = ?`, id))
}

// ListDestinations returns every destination by name.
func (s *Store) ListDestinations() ([]*Destination, error) {
	rows, err := s.db.Query(`SELECT ` + destCols + ` FROM destinations ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Destination{}
	for rows.Next() {
		d, err := s.scanDest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDestination removes a destination.
func (s *Store) DeleteDestination(id string) error {
	_, err := s.db.Exec(`DELETE FROM destinations WHERE id = ?`, id)
	return err
}

// RawDestinationSecret returns the stored (encrypted) secret, for tests.
func (s *Store) RawDestinationSecret(id string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT secret FROM destinations WHERE id = ?`, id).Scan(&v)
	return v, err
}

// ----------------------------------------------------------------------- jobs

// Job is a backup job on one client. KeyfilePassword is decrypted.
type Job struct {
	ID              string          `json:"id"`
	ClientID        string          `json:"client_id"`
	Name            string          `json:"name"`
	BackupID        string          `json:"backup_id"`
	Shares          []bundle.Share  `json:"shares"`
	Excludes        []string        `json:"excludes"`
	Schedule        bundle.Schedule `json:"schedule"`
	ChangeDetection string          `json:"change_detection"`
	Rate            string          `json:"rate"`
	Keyfile         string          `json:"keyfile"`
	KeyfilePassword string          `json:"-"`
	Enabled         bool            `json:"enabled"`
	Destinations    []string        `json:"destinations"`
	CreatedAt       int64           `json:"created_at"`
}

const jobCols = `id, client_id, name, backup_id, shares, excludes, schedule, change_detection, rate, keyfile,
	keyfile_password, enabled, destinations, created_at`

func (s *Store) scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var shares, excludes, schedule, dests, sealed string
	var enabled int
	err := row.Scan(&j.ID, &j.ClientID, &j.Name, &j.BackupID, &shares, &excludes, &schedule, &j.ChangeDetection, &j.Rate,
		&j.Keyfile, &sealed, &enabled, &dests, &j.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Enabled = enabled != 0
	for _, f := range []struct {
		raw string
		dst any
	}{{shares, &j.Shares}, {excludes, &j.Excludes}, {schedule, &j.Schedule}, {dests, &j.Destinations}} {
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return nil, err
		}
	}
	j.KeyfilePassword, err = s.box.Open(sealed)
	return &j, err
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// SaveJob inserts or updates a job.
func (s *Store) SaveJob(j *Job) error {
	if j.CreatedAt == 0 {
		j.CreatedAt = s.Now().Unix()
	}
	if j.Excludes == nil {
		j.Excludes = []string{}
	}
	_, err := s.db.Exec(`INSERT INTO jobs (`+jobCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, backup_id=excluded.backup_id, shares=excluded.shares,
		excludes=excluded.excludes, schedule=excluded.schedule, change_detection=excluded.change_detection, rate=excluded.rate,
		keyfile=excluded.keyfile, keyfile_password=excluded.keyfile_password, enabled=excluded.enabled,
		destinations=excluded.destinations`,
		j.ID, j.ClientID, j.Name, j.BackupID, mustJSON(j.Shares), mustJSON(j.Excludes), mustJSON(j.Schedule), j.ChangeDetection,
		j.Rate, j.Keyfile, s.box.Seal(j.KeyfilePassword), boolInt(j.Enabled), mustJSON(j.Destinations), j.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "jobs.client_id, jobs.name") {
		return &ErrDuplicate{"name"}
	}
	return err
}

// GetJob returns one job.
func (s *Store) GetJob(id string) (*Job, error) {
	return s.scanJob(s.db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
}

// ListJobs returns jobs, all of them or one client's ("" for all).
func (s *Store) ListJobs(clientID string) ([]*Job, error) {
	q := `SELECT ` + jobCols + ` FROM jobs`
	var args []any
	if clientID != "" {
		q += ` WHERE client_id = ?`
		args = append(args, clientID)
	}
	rows, err := s.db.Query(q+` ORDER BY name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := s.scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// DeleteJob removes a job (its run history stays).
func (s *Store) DeleteJob(id string) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	return err
}

// JobsUsingDestination returns the jobs that send to a destination.
func (s *Store) JobsUsingDestination(destID string) ([]*Job, error) {
	all, err := s.ListJobs("")
	if err != nil {
		return nil, err
	}
	var out []*Job
	for _, j := range all {
		for _, d := range j.Destinations {
			if d == destID {
				out = append(out, j)
				break
			}
		}
	}
	return out, nil
}

// ----------------------------------------------------------------------- runs

// Run is a run as the server knows it.
type Run struct {
	bundle.Run
	ClientID    string `json:"client_id"`
	CollectedAt int64  `json:"collected_at"`
	LogSaved    bool   `json:"log_saved"`
}

const runCols = `client_id, id, job_id, group_id, job_name, destination_id, destination_name, trigger, status, started,
	ended, exit_code, summary, log_size, updated, collected_at, log_saved, stats, progress`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var code sql.NullInt64
	var saved int
	var stats, progress string
	err := row.Scan(&r.ClientID, &r.ID, &r.JobID, &r.Group, &r.JobName, &r.DestinationID, &r.DestinationName, &r.Trigger,
		&r.Status, &r.Started, &r.Ended, &code, &r.Summary, &r.LogSize, &r.Updated, &r.CollectedAt, &saved, &stats, &progress)
	if progress != "" && r.Status == bundle.Running {
		var p bundle.Progress
		if json.Unmarshal([]byte(progress), &p) == nil {
			r.Progress = &p
		}
	}
	if stats != "" {
		var s bundle.Stats
		if json.Unmarshal([]byte(stats), &s) == nil {
			r.Stats = &s
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if code.Valid {
		c := int(code.Int64)
		r.ExitCode = &c
	}
	r.LogSaved = saved != 0
	return &r, err
}

// UpsertRun stores a run reported by a client. It returns the previous
// status ("" if new) so callers can tell when a run finished.
func (s *Store) UpsertRun(clientID string, r bundle.Run) (string, error) {
	var prev string
	err := s.db.QueryRow(`SELECT status FROM runs WHERE client_id = ? AND id = ?`, clientID, r.ID).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var code any
	if r.ExitCode != nil {
		code = *r.ExitCode
	}
	stats := ""
	if r.Stats != nil {
		raw, _ := json.Marshal(r.Stats)
		stats = string(raw)
	}
	progress := ""
	if r.Progress != nil && r.Status == bundle.Running {
		raw, _ := json.Marshal(r.Progress)
		progress = string(raw)
	}
	_, err = s.db.Exec(`INSERT INTO runs (`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)
		ON CONFLICT(client_id, id) DO UPDATE SET status=excluded.status, ended=excluded.ended, exit_code=excluded.exit_code,
		summary=excluded.summary, log_size=excluded.log_size, updated=excluded.updated, collected_at=excluded.collected_at,
		stats=excluded.stats, progress=excluded.progress`,
		clientID, r.ID, r.JobID, r.Group, r.JobName, r.DestinationID, r.DestinationName, r.Trigger, r.Status, r.Started,
		r.Ended, code, r.Summary, r.LogSize, r.Updated, s.Now().Unix(), stats, progress)
	return prev, err
}

// GetRun returns one run.
func (s *Store) GetRun(clientID, id string) (*Run, error) {
	return scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE client_id = ? AND id = ?`, clientID, id))
}

// RunFilter narrows ListRuns.
type RunFilter struct {
	ClientID, JobID string
	Statuses        []string
	Limit           int
}

// ListRuns returns runs, newest first.
func (s *Store) ListRuns(f RunFilter) ([]*Run, error) {
	q := `SELECT ` + runCols + ` FROM runs WHERE 1=1`
	var args []any
	if f.ClientID != "" {
		q += ` AND client_id = ?`
		args = append(args, f.ClientID)
	}
	if f.JobID != "" {
		q += ` AND job_id = ?`
		args = append(args, f.JobID)
	}
	if len(f.Statuses) > 0 {
		q += ` AND status IN (?` + strings.Repeat(",?", len(f.Statuses)-1) + `)`
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkLogSaved records that a finished run's full log is stored on the server.
func (s *Store) MarkLogSaved(clientID, id string) error {
	_, err := s.db.Exec(`UPDATE runs SET log_saved = 1 WHERE client_id = ? AND id = ?`, clientID, id)
	return err
}

// DeleteClientRuns removes a client's run history.
func (s *Store) DeleteClientRuns(clientID string) error {
	_, err := s.db.Exec(`DELETE FROM runs WHERE client_id = ?`, clientID)
	return err
}

// PruneRuns keeps the newest keep runs.
func (s *Store) PruneRuns(keep int) ([]*Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs ORDER BY id DESC LIMIT -1 OFFSET ?`, keep)
	if err != nil {
		return nil, err
	}
	var old []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		old = append(old, r)
	}
	rows.Close()
	for _, r := range old {
		if _, err := s.db.Exec(`DELETE FROM runs WHERE client_id = ? AND id = ?`, r.ClientID, r.ID); err != nil {
			return nil, err
		}
	}
	return old, nil
}

// RunsSince returns every run that started at or after since, newest first.
// Run history is pruned (5000 runs by default), so this stays small.
func (s *Store) RunsSince(since int64) ([]*Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs WHERE started >= ? ORDER BY started DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunHistory says how many runs the server keeps and when the oldest started.
func (s *Store) RunHistory() (count int, oldest int64, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(started), 0) FROM runs`).Scan(&count, &oldest)
	return count, oldest, err
}
