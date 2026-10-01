package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"github.com/bradyloveland/pbcmanager/internal/config"
	"github.com/bradyloveland/pbcmanager/internal/tlscert"
)

func (s *Server) settingValues() map[string]any {
	out := map[string]any{}
	for _, d := range config.Defs {
		switch d.Type {
		case "int":
			out[d.Key] = s.settingInt(d.Key)
		case "bool":
			v, _ := d.Default.(bool)
			var stored bool
			if ok, err := s.store.GetSetting(d.Key, &stored); ok && err == nil {
				v = stored
			}
			out[d.Key] = v
		default:
			out[d.Key] = s.settingString(d.Key)
		}
	}
	return out
}

func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"groups": config.Groups, "defs": config.Defs, "values": s.settingValues(),
		"hostname": s.opts.Hostname}, nil
}

func (s *Server) apiSettingsUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	clean := map[string]any{}
	for key, raw := range in.Values {
		v, err := config.Clean(key, raw)
		if err != nil {
			return nil, err
		}
		clean[key] = v
	}
	if err := s.store.SetSettings(clean); err != nil {
		return nil, err
	}
	slog.Info("settings changed", "count", len(clean))
	return map[string]any{"values": s.settingValues()}, nil
}

// ---------------------------------------------------------------- network

func certInfo(path string) *tlscert.Info {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	i, err := tlscert.Describe(raw)
	if err != nil {
		return nil
	}
	return i
}

func (s *Server) apiNetwork(w http.ResponseWriter, r *http.Request) (any, error) {
	selfCrt, _ := s.certPaths(config.TLSSelfSigned)
	customCrt, _ := s.certPaths(config.TLSCustom)
	s.mu.Lock()
	defer s.mu.Unlock()
	var pendingCert *tlscert.Info
	if s.pending != nil && s.pending.custom != nil {
		pendingCert, _ = tlscert.Describe(s.pending.custom.cert)
	}
	return map[string]any{
		"active":  s.active,
		"pending": s.pendingInfoLocked(r, true),
		"certs": map[string]any{
			"self_signed": certInfo(selfCrt), "custom": certInfo(customCrt), "pending_custom": pendingCert,
		},
		"request": map[string]any{"https": info(r).https, "ip": info(r).ip, "via_pending": info(r).pending},
	}, nil
}

func (s *Server) apiNetworkUpdate(w http.ResponseWriter, r *http.Request) (any, error) {
	var in networkChange
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	return s.changeNetwork(r, in)
}

func (s *Server) apiNetworkConfirm(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct{ Token string }
	if err := decode(r, &in); err != nil {
		return nil, err
	}
	if err := s.confirmNetwork(r, in.Token); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (s *Server) apiNetworkCancel(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"undone": s.revertNetwork("", "undone by the user")}, nil
}

func (s *Server) apiNetworkRegenerate(w http.ResponseWriter, r *http.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		return nil, conflict("Finish the waiting network change first.")
	}
	if err := s.writeSelfSigned(); err != nil {
		return nil, err
	}
	if s.active.TLS == config.TLSSelfSigned {
		cert, err := s.loadCert(config.TLSSelfSigned)
		if err != nil {
			return nil, err
		}
		s.activeCert = cert
	}
	slog.Info("self-signed certificate regenerated")
	crt, _ := s.certPaths(config.TLSSelfSigned)
	return map[string]any{"cert": certInfo(crt)}, nil
}
