package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"time"

	"github.com/bradyloveland/proxmoxbackupclientwebmanager/internal/version"
	"github.com/bradyloveland/proxmoxbackupclientwebmanager/web"
)

func (s *Server) routes() {
	m := s.mux
	pub, priv := func(fn handler) http.HandlerFunc { return s.api(false, fn) }, func(fn handler) http.HandlerFunc { return s.api(true, fn) }

	m.HandleFunc("GET /api/health", pub(s.apiHealth))
	m.HandleFunc("GET /api/session", pub(s.apiSession))
	m.HandleFunc("POST /api/setup", pub(s.apiSetup))
	m.HandleFunc("POST /api/login", pub(s.apiLogin))
	m.HandleFunc("POST /api/login/totp", pub(s.apiLoginTOTP))
	m.HandleFunc("POST /api/logout", pub(s.apiLogout))

	m.HandleFunc("GET /api/account", priv(s.apiAccount))
	m.HandleFunc("PUT /api/account/username", priv(s.apiAccountUsername))
	m.HandleFunc("POST /api/account/password", priv(s.apiAccountPassword))
	m.HandleFunc("POST /api/account/totp/setup", priv(s.apiTOTPSetup))
	m.HandleFunc("POST /api/account/totp/enable", priv(s.apiTOTPEnable))
	m.HandleFunc("POST /api/account/totp/disable", priv(s.apiTOTPDisable))
	m.HandleFunc("POST /api/account/totp/recovery", priv(s.apiTOTPRecovery))

	m.HandleFunc("GET /api/settings", priv(s.apiSettings))
	m.HandleFunc("PUT /api/settings", priv(s.apiSettingsUpdate))
	m.HandleFunc("GET /api/settings/network", priv(s.apiNetwork))
	m.HandleFunc("PUT /api/settings/network", priv(s.apiNetworkUpdate))
	m.HandleFunc("POST /api/settings/network/confirm", priv(s.apiNetworkConfirm))
	m.HandleFunc("POST /api/settings/network/cancel", priv(s.apiNetworkCancel))
	m.HandleFunc("POST /api/settings/network/regenerate-certificate", priv(s.apiNetworkRegenerate))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
	})
	m.HandleFunc("GET /{$}", s.serveIndex)
	m.HandleFunc("GET /index.html", s.serveIndex)
	m.HandleFunc("GET /assets/{file}", s.serveAsset)
}

// The page is strict about what it loads: only its own scripts and styles,
// with no inline code.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	raw, err := fs.ReadFile(web.Files, "index.html")
	if err != nil {
		http.Error(w, "The web UI is missing from this build.", http.StatusInternalServerError)
		return
	}
	// Asset URLs carry the version so an update never serves a stale script.
	raw = bytes.ReplaceAll(raw, []byte("{{VERSION}}"), []byte(version.Version))
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(raw)
}

var started = time.Now()

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.PathValue("file"))
	raw, err := fs.ReadFile(web.Files, name)
	if err != nil || name == "index.html" {
		http.Error(w, "Not found.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, started, bytes.NewReader(raw))
}
