package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/clients"
	"github.com/bradyloveland/pbcmanager/internal/config"
	"github.com/bradyloveland/pbcmanager/internal/release"
	"github.com/bradyloveland/pbcmanager/internal/update"
	"github.com/bradyloveland/pbcmanager/internal/version"
)

// maxUpload is the largest release file accepted.
const maxUpload = 300 << 20

func (s *Server) settingBool(key string) bool {
	d, _ := config.Lookup(key)
	v, _ := d.Default.(bool)
	var stored bool
	if ok, err := s.store.GetSetting(key, &stored); ok && err == nil {
		v = stored
	}
	return v
}

// Restarting is closed when the server should stop so systemd starts the
// version that was just installed.
func (s *Server) Restarting() <-chan struct{} { return s.restart }

// RollbackRequested is the reason, if the restart is to go back to the
// previous version (done once the database is closed).
func (s *Server) RollbackRequested() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rollback
}

// UpdateFiles is where the program and data are, for rolling back.
func (s *Server) UpdateFiles() update.Files { return s.updater.Files }

func (s *Server) requestRestart(rollback string) {
	s.mu.Lock()
	s.rollback = rollback
	s.mu.Unlock()
	// Give the browser its answer before stopping.
	time.AfterFunc(500*time.Millisecond, func() { s.restartOnce.Do(func() { close(s.restart) }) })
}

func updateError(err error) error {
	var in *update.InputError
	if errors.As(err, &in) {
		return badRequest("%s", in.Message)
	}
	if err != nil {
		return &apiError{http.StatusBadGateway, capitalize(err.Error()) + "."}
	}
	return nil
}

var runnerLabel = map[string]string{
	clients.RunnerCurrent:  "Up to date",
	clients.RunnerUpdating: "Updating now",
	clients.RunnerOutdated: "Will be updated at the next check",
	clients.RunnerRepair:   "Needs Repair to update",
	clients.RunnerUnknown:  "Not checked yet",
}

func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	check := s.updater.LastCheck()
	var newer bool
	if check.Latest != nil {
		newer = s.updater.Newer(check.Latest.Version)
	}
	state, _ := update.ReadState(s.opts.DataDir)
	list, err := s.store.ListClients()
	if err != nil {
		return nil, err
	}
	cl := make([]map[string]any, 0, len(list))
	for _, c := range list {
		st := s.clients.RunnerState(c.ID)
		cl = append(cl, map[string]any{"id": c.ID, "name": c.Name, "runner_version": c.RunnerVersion, "runner_state": st, "runner_label": runnerLabel[st]})
	}
	return map[string]any{
		"version": version.Version, "arch": runtime.GOARCH, "cant_update": s.updater.CantUpdate(), "check": check, "newer": newer,
		"staged": s.updater.StagedRelease(), "rollback_to": s.updater.CanRollBack(), "last": state, "clients": cl,
		"signed": s.ownSigned(),
	}, nil
}

// ownSigned reports whether the running server is a signed release (so it
// can send pbcm-runner updates to clients).
func (s *Server) ownSigned() bool {
	_, err := release.CheckDir(s.opts.AppDir, s.keys())
	return err == nil
}

func (s *Server) keys() []release.Key {
	if s.opts.UpdateKeys != nil {
		return s.opts.UpdateKeys
	}
	return release.Keys
}

func (s *Server) apiUpdateCheck(w http.ResponseWriter, r *http.Request) (any, error) {
	c := s.updater.CheckNow(r.Context())
	if c.Error != "" {
		return nil, &apiError{http.StatusBadGateway, c.Error}
	}
	return map[string]any{"check": c, "newer": c.Latest != nil && s.updater.Newer(c.Latest.Version)}, nil
}

func (s *Server) apiUpdateDownload(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		Version string `json:"version"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	st, err := s.updater.Download(ctx, in.Version)
	if err != nil {
		return nil, updateError(err)
	}
	return map[string]any{"staged": st, "newer": s.updater.Newer(st.Version)}, nil
}

func (s *Server) apiUpdateUpload(w http.ResponseWriter, r *http.Request) (any, error) {
	st, err := s.updater.Upload(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, &apiError{http.StatusRequestEntityTooLarge, "That file is too large to be a release."}
		}
		return nil, updateError(err)
	}
	return map[string]any{"staged": st, "newer": s.updater.Newer(st.Version)}, nil
}

func (s *Server) apiUpdateDiscard(w http.ResponseWriter, r *http.Request) (any, error) {
	s.updater.Discard()
	return map[string]any{"ok": true}, nil
}

func (s *Server) apiUpdateInstall(w http.ResponseWriter, r *http.Request) (any, error) {
	to, err := s.updater.Install()
	if err != nil {
		return nil, updateError(err)
	}
	s.requestRestart("")
	return map[string]any{"restarting": true, "version": to}, nil
}

func (s *Server) apiUpdateRollback(w http.ResponseWriter, r *http.Request) (any, error) {
	to := s.updater.CanRollBack()
	if to == "" {
		return nil, badRequest("There's no earlier version to go back to.")
	}
	if why := s.updater.CantUpdate(); why != "" {
		return nil, badRequest("%s", why)
	}
	s.requestRestart("Rolled back by hand from the Updates page.")
	return map[string]any{"restarting": true, "version": to}, nil
}

func (s *Server) apiUpdateDismiss(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"ok": true}, s.updater.Dismiss()
}

// updateLoop checks GitHub once a day and, if turned on, installs new
// versions during the chosen hour.
func (s *Server) updateLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	first := time.After(2 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
		case <-t.C:
		}
		s.autoUpdate(ctx, time.Now())
	}
}

func (s *Server) autoUpdate(ctx context.Context, now time.Time) {
	if !s.settingBool("updates.check") {
		return
	}
	c := s.updater.LastCheck()
	if now.Sub(time.Unix(c.Checked, 0)) >= 24*time.Hour {
		c = s.updater.CheckNow(ctx)
	}
	if !s.settingBool("updates.auto") || c.Latest == nil || !s.updater.Newer(c.Latest.Version) ||
		now.Hour() != s.settingInt("updates.hour") || s.updater.CantUpdate() != "" {
		return
	}
	// Don't keep retrying a version that was already rolled back.
	if st, _ := update.ReadState(s.opts.DataDir); st != nil && st.Phase == update.RolledBack && st.From == c.Latest.Version {
		return
	}
	if _, err := s.updater.Download(ctx, c.Latest.Version); err != nil {
		slog.Warn("automatic update: couldn't download", "version", c.Latest.Version, "err", err)
		return
	}
	if _, err := s.updater.Install(); err != nil {
		slog.Warn("automatic update: couldn't install", "version", c.Latest.Version, "err", err)
		return
	}
	s.requestRestart("")
}
