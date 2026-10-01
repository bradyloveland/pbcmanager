package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/migrate"
)

// maxImport is the largest settings file accepted.
const maxImport = 5 << 20

type importRequest struct {
	File json.RawMessage `json:"file"`
	migrate.Options
}

func (s *Server) importEnv() migrate.Env {
	return migrate.Env{Store: s.store, CurrentAlerts: s.alertSettings(), DefaultBackupID: defaultBackupID}
}

func (s *Server) importPlan(r *http.Request, final bool) (*migrate.Plan, error) {
	var in importRequest
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if len(in.File) == 0 {
		return nil, badRequest("Choose a settings file to import.")
	}
	src, err := migrate.Parse(in.File)
	if err != nil {
		return nil, badRequest("%s.", capitalize(err.Error()))
	}
	return migrate.Prepare(s.importEnv(), src, in.Options, final)
}

func (s *Server) apiImportPreview(w http.ResponseWriter, r *http.Request) (any, error) {
	p, err := s.importPlan(r, false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"plan": p}, nil
}

func (s *Server) apiImport(w http.ResponseWriter, r *http.Request) (any, error) {
	p, err := s.importPlan(r, true)
	if err != nil {
		return nil, err
	}
	if !p.Ready {
		return map[string]any{"plan": p, "imported": false}, nil
	}
	res, err := migrate.Apply(s.store, p)
	if err != nil {
		return nil, err
	}
	if res.Alerts != nil {
		if err := s.saveAlertSettings(*res.Alerts); err != nil {
			return nil, err
		}
	}
	if len(res.Settings) > 0 {
		if err := s.store.SetSettings(res.Settings); err != nil {
			return nil, err
		}
	}
	s.clients.ApplyAsync(res.ClientIDs...)
	s.sizes.Request("dest:*", "backup:*")
	return map[string]any{"plan": p, "imported": true, "result": res}, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// apiExport downloads the settings, without any credentials.
func (s *Server) apiExport(w http.ResponseWriter, r *http.Request) (any, error) {
	now := time.Now()
	e, err := migrate.BuildExport(s.store, s.settingValues(), s.alertSettings(), s.serverName(), now)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("pbcm-settings-%s-%s.json", unsafeName.ReplaceAllString(s.serverName(), "-"), now.Format("2006-01-02"))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	return e, nil
}
