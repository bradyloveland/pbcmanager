package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/version"
	"github.com/bradyloveland/pbcmanager/web"
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

	m.HandleFunc("GET /api/settings/ssh", priv(s.apiSSH))
	m.HandleFunc("GET /api/clients", priv(s.apiClients))
	m.HandleFunc("POST /api/clients", priv(s.apiClientAdd))
	m.HandleFunc("POST /api/clients/probe", priv(s.apiClientProbe))
	m.HandleFunc("GET /api/clients/{id}", priv(s.apiClient))
	m.HandleFunc("POST /api/clients/{id}/check", priv(s.apiClientCheck))
	m.HandleFunc("POST /api/clients/{id}/repair", priv(s.apiClientRepair))
	m.HandleFunc("GET /api/clients/{id}/browse", priv(s.apiClientBrowse))
	m.HandleFunc("POST /api/clients/{id}/remove", priv(s.apiClientRemove))
	m.HandleFunc("GET /api/tasks/{id}", priv(s.apiTask))
	m.HandleFunc("POST /api/clients/{id}/apply", priv(s.apiClientApply))

	m.HandleFunc("GET /api/destinations", priv(s.apiDestinations))
	m.HandleFunc("POST /api/destinations", priv(s.apiDestinationCreate))
	m.HandleFunc("POST /api/destinations/test", priv(s.apiDestinationTest))
	m.HandleFunc("PUT /api/destinations/{id}", priv(s.apiDestinationUpdate))
	m.HandleFunc("DELETE /api/destinations/{id}", priv(s.apiDestinationDelete))

	m.HandleFunc("GET /api/jobs", priv(s.apiJobs))
	m.HandleFunc("POST /api/jobs", priv(s.apiJobCreate))
	m.HandleFunc("GET /api/jobs/{id}", priv(s.apiJob))
	m.HandleFunc("PUT /api/jobs/{id}", priv(s.apiJobUpdate))
	m.HandleFunc("DELETE /api/jobs/{id}", priv(s.apiJobDelete))
	m.HandleFunc("POST /api/jobs/{id}/run", priv(s.apiJobRun))
	m.HandleFunc("POST /api/jobs/{id}/cancel", priv(s.apiJobCancel))
	m.HandleFunc("GET /api/jobs/{id}/snapshots", priv(s.apiJobSnapshots))

	m.HandleFunc("GET /api/runs", priv(s.apiRuns))
	m.HandleFunc("GET /api/runs/{client}/{id}", priv(s.apiRun))
	m.HandleFunc("GET /api/runs/{client}/{id}/log", priv(s.apiRunLog))

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
	// Asset URLs carry a hash of the UI files, so a new build never gets a
	// stale cached script, even when the version number hasn't changed.
	raw = bytes.ReplaceAll(raw, []byte("{{VERSION}}"), []byte(assetTag()))
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(raw)
}

var started = time.Now()

var assetTag = sync.OnceValue(func() string {
	h := sha256.New()
	for _, name := range []string{"app.js", "app.css", "icon.svg", "index.html"} {
		raw, _ := fs.ReadFile(web.Files, name)
		h.Write(raw)
	}
	return version.Version + "-" + hex.EncodeToString(h.Sum(nil))[:10]
})

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
