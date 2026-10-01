package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/backups"
	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

func backupError(err error) error {
	var in *backups.InputError
	var dup *store.ErrDuplicate
	switch {
	case err == nil:
		return nil
	case errors.As(err, &in):
		return badRequest("%s", in.Message)
	case errors.As(err, &dup):
		return badRequest("That name is already used. Choose another.")
	case errors.Is(err, backups.ErrNoClient):
		return &apiError{http.StatusBadGateway, capitalize(err.Error()) + "."}
	}
	return clientError(err)
}

// --------------------------------------------------------------- destinations

func (s *Server) destView(d *store.Destination) map[string]any {
	jobs, _ := s.store.JobsUsingDestination(d.ID)
	names := []string{}
	for _, j := range jobs {
		names = append(names, j.Name)
	}
	return map[string]any{"id": d.ID, "name": d.Name, "host": d.Host, "port": d.Port, "datastore": d.Datastore,
		"namespace": d.Namespace, "username": d.Username, "token_name": d.TokenName, "fingerprint": d.Fingerprint,
		"secret_set": d.Secret != "", "repository": d.Repository(), "used_by": names}
}

func (s *Server) apiDestinations(w http.ResponseWriter, r *http.Request) (any, error) {
	list, err := s.store.ListDestinations()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(list))
	for i, d := range list {
		out[i] = s.destView(d)
	}
	return map[string]any{"destinations": out}, nil
}

func (s *Server) saveDestination(r *http.Request, existing *store.Destination) (any, error) {
	var in backups.DestinationInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	d, err := backups.CleanDestination(in, existing)
	if err != nil {
		return nil, backupError(err)
	}
	if err := s.store.SaveDestination(d); err != nil {
		return nil, backupError(err)
	}
	s.applyForDestination(d.ID)
	s.sizes.Request("dest:"+d.ID, "backup:*")
	return map[string]any{"destination": s.destView(d)}, nil
}

// applyForDestination re-sends settings to every client with a job using it.
func (s *Server) applyForDestination(id string) {
	jobs, _ := s.store.JobsUsingDestination(id)
	seen := map[string]bool{}
	var ids []string
	for _, j := range jobs {
		if !seen[j.ClientID] {
			seen[j.ClientID] = true
			ids = append(ids, j.ClientID)
		}
	}
	s.clients.ApplyAsync(ids...)
}

func (s *Server) apiDestinationCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.saveDestination(r, nil)
}

func (s *Server) apiDestinationUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	d, err := s.store.GetDestination(r.PathValue("id"))
	if err != nil {
		return nil, &apiError{http.StatusNotFound, "That destination doesn't exist anymore."}
	}
	return s.saveDestination(r, d)
}

func (s *Server) apiDestinationDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	jobs, err := s.store.JobsUsingDestination(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if len(jobs) > 0 {
		names := make([]string, len(jobs))
		for i, j := range jobs {
			names[i] = j.Name
		}
		return nil, conflict("Still used by: %s. Change those jobs to use another destination first.", strings.Join(names, ", "))
	}
	_ = s.store.DeleteSize(backups.SizeDest, r.PathValue("id"))
	return map[string]any{"ok": true}, s.store.DeleteDestination(r.PathValue("id"))
}

func (s *Server) apiDestinationTest(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		backups.DestinationInput
		ID string `json:"id"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	var existing *store.Destination
	if in.ID != "" {
		existing, _ = s.store.GetDestination(in.ID)
	}
	d, err := backups.CleanDestination(in.DestinationInput, existing)
	if err != nil {
		return nil, backupError(err)
	}
	u, err := s.pbs.Status(r.Context(), d)
	if err != nil {
		return nil, &apiError{http.StatusBadGateway, capitalize(err.Error()) + "."}
	}
	return map[string]any{"ok": true, "usage": u}, nil
}

// ----------------------------------------------------------------------- jobs

func (s *Server) jobView(j *store.Job, clients map[string]*store.Client, dests map[string]*store.Destination) map[string]any {
	dnames := []string{}
	for _, id := range j.Destinations {
		if d, ok := dests[id]; ok {
			dnames = append(dnames, d.Name)
		}
	}
	v := map[string]any{"id": j.ID, "client_id": j.ClientID, "name": j.Name, "backup_id": j.BackupID, "shares": j.Shares,
		"excludes": j.Excludes, "schedule": j.Schedule, "change_detection": j.ChangeDetection, "rate": j.Rate,
		"keyfile": j.Keyfile, "keyfile_password_set": j.KeyfilePassword != "", "enabled": j.Enabled,
		"destinations": j.Destinations, "destination_names": dnames, "next_run": nil, "client_name": "", "recent": []any{}}
	loc := time.Local
	if c, ok := clients[j.ClientID]; ok {
		v["client_name"] = c.Name
		loc = c.Location()
		v["timezone"] = c.Timezone
	}
	// Schedules run in the client's time zone.
	if next := bundle.Next(j.Schedule, j.Enabled, time.Now().In(loc)); !next.IsZero() {
		v["next_run"] = next.Unix()
	}
	if runs, err := s.store.ListRuns(store.RunFilter{JobID: j.ID, Limit: 20}); err == nil {
		v["recent"] = runs
	}
	return v
}

func (s *Server) lookups() (map[string]*store.Client, map[string]*store.Destination, error) {
	cl, err := s.store.ListClients()
	if err != nil {
		return nil, nil, err
	}
	dl, err := s.store.ListDestinations()
	if err != nil {
		return nil, nil, err
	}
	cm, dm := map[string]*store.Client{}, map[string]*store.Destination{}
	for _, c := range cl {
		cm[c.ID] = c
	}
	for _, d := range dl {
		dm[d.ID] = d
	}
	return cm, dm, nil
}

func (s *Server) apiJobs(w http.ResponseWriter, r *http.Request) (any, error) {
	jobs, err := s.store.ListJobs(r.URL.Query().Get("client"))
	if err != nil {
		return nil, err
	}
	cm, dm, err := s.lookups()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(jobs))
	for i, j := range jobs {
		out[i] = s.jobView(j, cm, dm)
	}
	return map[string]any{"jobs": out}, nil
}

func (s *Server) apiJob(w http.ResponseWriter, r *http.Request) (any, error) {
	j, err := s.store.GetJob(r.PathValue("id"))
	if err != nil {
		return nil, &apiError{http.StatusNotFound, "That job doesn't exist anymore."}
	}
	cm, dm, err := s.lookups()
	if err != nil {
		return nil, err
	}
	return map[string]any{"job": s.jobView(j, cm, dm)}, nil
}

var backupIDClean = regexp.MustCompile(`[^A-Za-z0-9._\-]+`)

func defaultBackupID(c *store.Client) string {
	id := c.Hostname
	if id == "" {
		id = c.Name
	}
	id = strings.Trim(backupIDClean.ReplaceAllString(id, "-"), "-.")
	if id == "" || !bundle.BackupIDRE.MatchString(id) {
		id = "client-" + c.ID
	}
	return id
}

func (s *Server) saveJob(r *http.Request, existing *store.Job) (any, error) {
	var in backups.JobInput
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if existing != nil {
		in.ClientID = existing.ClientID
	}
	c, err := s.store.GetClient(in.ClientID)
	if err != nil {
		return nil, badRequest("Choose which client this job backs up.")
	}
	cm, dm, err := s.lookups()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for id := range dm {
		known[id] = true
	}
	j, err := backups.CleanJob(in, existing, defaultBackupID(c), known)
	if err != nil {
		return nil, backupError(err)
	}
	if err := s.store.SaveJob(j); err != nil {
		var dup *store.ErrDuplicate
		if errors.As(err, &dup) {
			return nil, badRequest("%s already has a job called “%s”.", c.Name, j.Name)
		}
		return nil, err
	}
	s.clients.ApplyAsync(c.ID)
	s.sizes.Request("backup:" + j.ID)
	return map[string]any{"job": s.jobView(j, cm, dm)}, nil
}

func (s *Server) apiJobCreate(w http.ResponseWriter, r *http.Request) (any, error) {
	return s.saveJob(r, nil)
}

func (s *Server) apiJobUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	j, err := s.store.GetJob(r.PathValue("id"))
	if err != nil {
		return nil, &apiError{http.StatusNotFound, "That job doesn't exist anymore."}
	}
	return s.saveJob(r, j)
}

func (s *Server) apiJobDelete(w http.ResponseWriter, r *http.Request) (any, error) {
	j, err := s.store.GetJob(r.PathValue("id"))
	if err != nil {
		return map[string]any{"ok": true}, nil
	}
	if active, _ := s.store.ListRuns(store.RunFilter{JobID: j.ID, Statuses: []string{bundle.Running}, Limit: 1}); len(active) > 0 {
		return nil, conflict("This job is running. Cancel the run before deleting the job.")
	}
	if err := s.store.DeleteJob(j.ID); err != nil {
		return nil, err
	}
	s.clients.ApplyAsync(j.ClientID)
	return map[string]any{"ok": true}, nil
}

func (s *Server) apiJobRun(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.clients.StartJob(ctx, r.PathValue("id")); err != nil {
		return nil, clientError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (s *Server) apiJobCancel(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := s.clients.CancelJob(ctx, r.PathValue("id")); err != nil {
		return nil, clientError(err)
	}
	return map[string]any{"ok": true}, nil
}

func (s *Server) apiJobSnapshots(w http.ResponseWriter, r *http.Request) (any, error) {
	j, err := s.store.GetJob(r.PathValue("id"))
	if err != nil {
		return nil, &apiError{http.StatusNotFound, "That job doesn't exist anymore."}
	}
	out := []map[string]any{}
	for _, id := range j.Destinations {
		d, err := s.store.GetDestination(id)
		if err != nil {
			continue
		}
		entry := map[string]any{"destination_id": d.ID, "destination_name": d.Name, "group": "host/" + j.BackupID,
			"namespace": d.Namespace, "snapshots": []any{}, "error": ""}
		snaps, err := s.pbs.Snapshots(r.Context(), d, j.BackupID)
		if err != nil {
			entry["error"] = capitalize(err.Error()) + "."
		} else {
			entry["snapshots"] = snaps
		}
		out = append(out, entry)
	}
	return map[string]any{"destinations": out}, nil
}

// ----------------------------------------------------------------------- runs

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	runs, err := s.store.ListRuns(store.RunFilter{ClientID: q.Get("client"), JobID: q.Get("job"), Limit: limit})
	if err != nil {
		return nil, err
	}
	cm, _, _ := s.lookups()
	out := make([]map[string]any, len(runs))
	for i, run := range runs {
		out[i] = map[string]any{"run": run, "client_name": ""}
		if c, ok := cm[run.ClientID]; ok {
			out[i]["client_name"] = c.Name
		}
	}
	return map[string]any{"runs": out}, nil
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) (any, error) {
	run, err := s.store.GetRun(r.PathValue("client"), r.PathValue("id"))
	if err != nil {
		return nil, &apiError{http.StatusNotFound, "That run isn't kept anymore."}
	}
	name := ""
	if c, err := s.store.GetClient(run.ClientID); err == nil {
		name = c.Name
	}
	return map[string]any{"run": run, "client_name": name}, nil
}

func (s *Server) apiRunLog(w http.ResponseWriter, r *http.Request) (any, error) {
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	chunk, err := s.clients.RunLog(ctx, r.PathValue("client"), r.PathValue("id"), offset)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &apiError{http.StatusNotFound, "That run isn't kept anymore."}
	}
	if err != nil {
		return nil, clientError(err)
	}
	return chunk, nil
}

func (s *Server) apiClientApply(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.clients.Apply(ctx, r.PathValue("id")); err != nil {
		return nil, clientError(err)
	}
	return map[string]any{"ok": true}, nil
}
