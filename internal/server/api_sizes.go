package server

import (
	"context"
	"net/http"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/backups"
)

func (s *Server) apiSizes(w http.ResponseWriter, r *http.Request) (any, error) {
	sum, err := backups.Summarise(s.store)
	if err != nil {
		return nil, err
	}
	return map[string]any{"sizes": sum}, nil
}

// apiMetrics is the Dashboard's summary cards.
func (s *Server) apiMetrics(w http.ResponseWriter, r *http.Request) (any, error) {
	m, err := backups.ComputeMetrics(s.store, time.Now(), s.settingInt("history.server_runs"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"metrics": m}, nil
}

// apiSizesCheck asks for destination space and backup sizes to be checked
// now: one destination, one job, or (with neither) everything.
func (s *Server) apiSizesCheck(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		Destination string `json:"destination"`
		Job         string `json:"job"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	switch {
	case in.Destination != "":
		s.sizes.Request("dest:" + in.Destination)
	case in.Job != "":
		s.sizes.Request("backup:" + in.Job)
	default:
		s.sizes.Request("dest:*", "backup:*")
	}
	return map[string]any{"ok": true}, nil
}

// apiClientMeasure asks a client to measure its job folders (or one job's)
// again now.
func (s *Server) apiClientMeasure(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		Job string `json:"job"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := s.clients.MeasureFolders(ctx, r.PathValue("id"), in.Job); err != nil {
		return nil, clientError(err)
	}
	return map[string]any{"ok": true}, nil
}
