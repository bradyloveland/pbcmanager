package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/alerts"
)

const alertsKey = "alerts"

// storedAlerts is the alerts setting as saved: the password is sealed.
type storedAlerts struct {
	alerts.Settings
	SealedPassword string `json:"password_sealed"`
}

func (s *Server) alertSettings() alerts.Settings {
	st := storedAlerts{Settings: alerts.Defaults()}
	if ok, err := s.store.GetSetting(alertsKey, &st); ok && err == nil {
		st.Settings.Password, _ = s.store.Open(st.SealedPassword)
	}
	return st.Settings
}

func (s *Server) saveAlertSettings(a alerts.Settings) error {
	return s.store.SetSetting(alertsKey, storedAlerts{Settings: a, SealedPassword: s.store.Seal(a.Password)})
}

func alertView(a alerts.Settings) map[string]any {
	return map[string]any{"settings": a, "password_set": a.Password != ""}
}

func alertError(err error) error {
	var in *alerts.InputError
	if errors.As(err, &in) {
		return badRequest("%s", in.Message)
	}
	return err
}

func (s *Server) apiAlertSettings(w http.ResponseWriter, r *http.Request) (any, error) {
	return alertView(s.alertSettings()), nil
}

func (s *Server) apiAlertSettingsUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	var in alerts.Input
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	a, err := alerts.Clean(in, s.alertSettings())
	if err != nil {
		return nil, alertError(err)
	}
	if err := s.saveAlertSettings(a); err != nil {
		return nil, err
	}
	return alertView(a), nil
}

func (s *Server) apiAlertTest(w http.ResponseWriter, r *http.Request) (any, error) {
	var in alerts.Input
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	in.Enabled = true // check it as if alerts were on
	a, err := alerts.Clean(in, s.alertSettings())
	if err != nil {
		return nil, alertError(err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := s.notifier.Test(ctx, a); err != nil {
		return nil, &apiError{http.StatusBadGateway, "Couldn't send the email: " + err.Error() + "."}
	}
	return map[string]any{"ok": true}, nil
}

func (s *Server) apiAlertList(w http.ResponseWriter, r *http.Request) (any, error) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.store.ListAlerts(limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"alerts": list}, nil
}
