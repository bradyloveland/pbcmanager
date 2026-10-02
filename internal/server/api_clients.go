package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/clients"
	"github.com/bradyloveland/pbcmanager/internal/sshx"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// clientError turns client-side failures into messages for the user.
func clientError(err error) error {
	var in *clients.InputError
	var fe *sshx.FriendlyError
	var hk *sshx.HostKeyChangedError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return &apiError{http.StatusNotFound, "That client doesn't exist anymore."}
	case errors.As(err, &in):
		return badRequest("%s", in.Message)
	case errors.As(err, &hk):
		return &apiError{http.StatusConflict, "The client's SSH host key has changed (it now shows " + sshx.Fingerprint(hk.Offered) +
			"). If it was reinstalled, check the new key and use Repair; otherwise, investigate before trusting it."}
	case errors.As(err, &fe):
		return &apiError{http.StatusBadGateway, fe.Message}
	}
	return &apiError{http.StatusBadGateway, capitalize(err.Error()) + "."}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}

func clientView(c *store.Client) map[string]any {
	v := map[string]any{
		"id": c.ID, "name": c.Name, "address": c.Address, "port": c.Port, "status": c.Status,
		"status_detail": c.StatusDetail, "os_id": c.OSID, "os_pretty": c.OSPretty, "os_codename": c.OSCodename,
		"arch": c.Arch, "hostname": c.Hostname, "systemd_version": c.SystemdVersion, "client_version": c.ClientVersion,
		"runner_version": c.RunnerVersion, "server_here": c.ServerHere, "last_contact": c.LastContact,
		"created_at": c.CreatedAt, "host_key_fingerprint": "", "offered_fingerprint": "", "timezone": c.Timezone,
		"unreachable_since": c.UnreachableSince,
	}
	if k, err := sshx.ParseKey(c.HostKey); err == nil {
		v["host_key_fingerprint"] = sshx.Fingerprint(k)
		v["host_key_type"] = k.Type()
	}
	if c.OfferedKey != "" {
		if k, err := sshx.ParseKey(c.OfferedKey); err == nil {
			v["offered_fingerprint"] = sshx.Fingerprint(k)
			v["offered_key"] = c.OfferedKey
		}
	}
	return v
}

func (s *Server) clientViewFull(c *store.Client) map[string]any {
	v := clientView(c)
	v["settings_pending"] = s.clients.Pending(c)
	v["apply_error"] = c.ApplyError
	v["package"] = packageView(s.clients.Package(c.ID))
	return v
}

func (s *Server) apiClients(w http.ResponseWriter, r *http.Request) (any, error) {
	list, err := s.store.ListClients()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(list))
	for i, c := range list {
		out[i] = s.clientViewFull(c)
	}
	return map[string]any{"clients": out}, nil
}

func (s *Server) apiClient(w http.ResponseWriter, r *http.Request) (any, error) {
	c, err := s.store.GetClient(r.PathValue("id"))
	if err != nil {
		return nil, clientError(err)
	}
	out := map[string]any{"client": s.clientViewFull(c)}
	if t := s.clients.LatestTask(c.ID); t != nil {
		v := t.View(0)
		out["task"] = map[string]any{"id": v.ID, "kind": v.Kind, "done": v.Done, "ok": v.OK}
	}
	return out, nil
}

func (s *Server) apiClientProbe(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	res, err := s.clients.Probe(r.Context(), in.Address, in.Port)
	return res, clientError(err)
}

func (s *Server) apiClientAdd(w http.ResponseWriter, r *http.Request) (any, error) {
	var in clients.AddRequest
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	c, t, err := s.clients.Add(in)
	if err != nil {
		return nil, clientError(err)
	}
	return map[string]any{"client": clientView(c), "task": t.ID}, nil
}

func (s *Server) apiClientCheck(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	c, err := s.clients.Check(ctx, r.PathValue("id"))
	if c == nil {
		return nil, clientError(err)
	}
	out := map[string]any{"client": clientView(c)}
	if err != nil {
		out["error"] = clientError(err).Error()
	}
	return out, nil
}

func (s *Server) apiClientRepair(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		Login   clients.Login `json:"login"`
		HostKey string        `json:"host_key"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	t, err := s.clients.Repair(r.PathValue("id"), in.Login, in.HostKey)
	if err != nil {
		return nil, clientError(err)
	}
	return map[string]any{"task": t.ID}, nil
}

func (s *Server) apiClientBrowse(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	l, err := s.clients.Browse(ctx, r.PathValue("id"), path)
	return l, clientError(err)
}

func (s *Server) apiClientRemove(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		Uninstall   bool `json:"uninstall"`
		KeepHistory bool `json:"keep_history"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := s.clients.Remove(ctx, r.PathValue("id"), in.Uninstall, in.KeepHistory)
	if err != nil {
		return nil, clientError(err)
	}
	return map[string]any{"ok": true, "output": out}, nil
}

func (s *Server) apiTask(w http.ResponseWriter, r *http.Request) (any, error) {
	t := s.clients.Task(r.PathValue("id"))
	if t == nil {
		return nil, &apiError{http.StatusNotFound, "That task has finished and its log is no longer kept."}
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	return t.View(offset), nil
}

func (s *Server) apiSSH(w http.ResponseWriter, r *http.Request) (any, error) {
	id := s.clients.Identity()
	return map[string]any{"public_key": id.AuthorizedKey, "fingerprint": id.Fingerprint}, nil
}
