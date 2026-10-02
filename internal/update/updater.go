package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/release"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// DefaultAPI is the repository's GitHub releases API.
const DefaultAPI = "https://api.github.com/repos/bradyloveland/pbcmanager"

// Latest is the newest release on GitHub for this server.
type Latest struct {
	Version   string `json:"version"`
	Notes     string `json:"notes"`
	Published int64  `json:"published"`
	URL       string `json:"url"`  // the archive for this server's CPU
	Page      string `json:"page"` // the release's GitHub page
	Size      int64  `json:"size"`
}

// Check is the result of the last check for updates.
type Check struct {
	Latest  *Latest `json:"latest"`
	Checked int64   `json:"checked"`
	Error   string  `json:"error"`
}

// Staged is a release that's been downloaded or uploaded and checked, ready
// to install.
type Staged struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
}

// Updater checks for, stages and installs releases.
type Updater struct {
	Files
	Store   *store.Store
	Version string        // the running version
	Keys    []release.Key // trusted signing keys (release.Keys by default)
	API     string        // GitHub API base (DefaultAPI by default)
	HTTP    *http.Client
	Arch    string // runtime.GOARCH by default
	// Supervised reports whether systemd will start the new version after
	// this one exits. Updating needs it.
	Supervised func() bool
	// Executable is this program's path (os.Executable by default).
	Executable string

	mu      sync.Mutex
	busy    bool
	confirm *time.Timer
}

func (u *Updater) keys() []release.Key {
	if u.Keys != nil {
		return u.Keys
	}
	return release.Keys
}

func (u *Updater) arch() string {
	if u.Arch != "" {
		return u.Arch
	}
	return runtime.GOARCH
}

func (u *Updater) client() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (u *Updater) stagedDir() string { return filepath.Join(u.DataDir, "updates", "staged") }

// InputError is shown to the user as-is.
type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }

// CantUpdate says why this server can't update itself from the web UI, or
// "" if it can.
func (u *Updater) CantUpdate() string {
	if u.Supervised != nil && !u.Supervised() {
		return "This server isn't running as the pbcm system service, so it can't restart itself into a new version. Updates from the web UI work on servers installed with install.sh."
	}
	exe, err := u.Executable, error(nil)
	if exe == "" {
		exe, err = os.Executable()
	}
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
	}
	app, _ := filepath.EvalSymlinks(u.AppDir)
	if err != nil || filepath.Dir(exe) != app || filepath.Base(exe) != "pbcm" {
		return "This copy of pbcm isn't the installed one in " + u.AppDir + ", so it can't be updated from the web UI."
	}
	probe := filepath.Join(u.AppDir, ".write-test")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return "This server can't write to " + u.AppDir + ". Re-run install.sh to fix the folder's owner."
	}
	os.Remove(probe)
	return ""
}

// Started is called once the server is up. A version that was just
// installed counts as working after it has stayed up for settle.
func (u *Updater) Started(settle time.Duration) {
	s, err := ReadState(u.DataDir)
	if err != nil || s == nil || s.To != u.Version || (s.Phase != Installed && s.Phase != Confirming) {
		return
	}
	s.Phase = Confirming
	_ = writeState(u.DataDir, s)
	u.mu.Lock()
	u.confirm = time.AfterFunc(settle, func() {
		if s, err := ReadState(u.DataDir); err == nil && s != nil && s.Phase == Confirming && s.To == u.Version {
			s.Phase, s.Error = Done, ""
			_ = writeState(u.DataDir, s)
			slog.Info("update finished", "from", s.From, "to", s.To)
		}
	})
	u.mu.Unlock()
}

// Stop cancels the pending confirmation (on shutdown).
func (u *Updater) Stop() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.confirm != nil {
		u.confirm.Stop()
	}
}

// CheckNow asks GitHub for the newest release. A final release is offered
// to every server; pre-releases only to servers already on one.
func (u *Updater) CheckNow(ctx context.Context) Check {
	c := Check{Checked: now()}
	latest, err := u.fetchLatest(ctx)
	if err != nil {
		c.Error = capital(err.Error()) + "."
	} else {
		c.Latest = latest
	}
	if err := u.Store.SetSetting("update_check", c); err != nil {
		slog.Error("saving update check", "err", err)
	}
	return c
}

// LastCheck returns the saved result of the last check.
func (u *Updater) LastCheck() Check {
	var c Check
	_, _ = u.Store.GetSetting("update_check", &c)
	return c
}

func (u *Updater) fetchLatest(ctx context.Context) (*Latest, error) {
	api := u.API
	if api == "" {
		api = DefaultAPI
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", api+"/releases?per_page=30", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "pbcm/"+u.Version)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach GitHub to check for updates. Check that this server can reach the internet, or upload a release file instead")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("GitHub limited how often this server can check. Try again in an hour")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered the update check with an error (%s)", resp.Status)
	}
	var list []struct {
		Tag        string `json:"tag_name"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Published  string `json:"published_at"`
		Page       string `json:"html_url"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&list); err != nil {
		return nil, errors.New("GitHub sent an unexpected reply to the update check")
	}
	cur := release.ParseVersion(u.Version)
	var best *Latest
	for _, r := range list {
		v := release.ParseVersion(r.Tag)
		if r.Draft || v == nil || v.Major != 2 || (v.Pre != "" && (cur == nil || cur.Pre == "")) {
			continue
		}
		if best != nil && release.Compare(v.String(), best.Version) <= 0 {
			continue
		}
		want := "pbcm-" + v.String() + "-linux-" + u.arch() + ".tar.gz"
		for _, a := range r.Assets {
			if a.Name == want {
				t, _ := time.Parse(time.RFC3339, r.Published)
				best = &Latest{Version: v.String(), Notes: strings.TrimSpace(r.Body), Published: t.Unix(), URL: a.URL, Page: r.Page, Size: a.Size}
			}
		}
	}
	return best, nil // nil: no releases for this server yet
}

// Newer reports whether v is newer than the running version.
func (u *Updater) Newer(v string) bool { return release.Compare(v, u.Version) > 0 }

func (u *Updater) claim() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy {
		return &InputError{"An update is already being downloaded or installed."}
	}
	u.busy = true
	return nil
}

func (u *Updater) release() {
	u.mu.Lock()
	u.busy = false
	u.mu.Unlock()
}

// Download fetches the newest release from GitHub and stages it.
func (u *Updater) Download(ctx context.Context, version string) (*Staged, error) {
	c := u.LastCheck()
	if c.Latest == nil || c.Latest.Version != version || c.Latest.URL == "" {
		return nil, &InputError{"Check for updates again first; the newest release has changed."}
	}
	if err := u.claim(); err != nil {
		return nil, err
	}
	defer u.release()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.Latest.URL, nil)
	req.Header.Set("User-Agent", "pbcm/"+u.Version)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, errors.New("couldn't download the release from GitHub: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("couldn't download the release from GitHub (%s)", resp.Status)
	}
	st, err := u.stage(resp.Body)
	if err != nil {
		return nil, err
	}
	if st.Version != version {
		return nil, fmt.Errorf("the downloaded file is version %s, not %s", st.Version, version)
	}
	if c.Latest.Notes != "" {
		st.Notes = c.Latest.Notes
	}
	return st, nil
}

// Upload stages an uploaded release file.
func (u *Updater) Upload(r io.Reader) (*Staged, error) {
	if err := u.claim(); err != nil {
		return nil, err
	}
	defer u.release()
	return u.stage(r)
}

// stage unpacks and checks a release into the staging folder.
func (u *Updater) stage(r io.Reader) (*Staged, error) {
	base := filepath.Join(u.DataDir, "updates")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(base, "incoming-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	m, err := release.Unpack(r, tmp, u.keys())
	if err != nil {
		if errors.Is(err, release.ErrNotSigned) || errors.Is(err, release.ErrNotRelease) {
			return nil, &InputError{capital(err.Error()) + "."}
		}
		return nil, &InputError{"The release file was refused: " + err.Error() + "."}
	}
	if m.Arch != u.arch() {
		return nil, &InputError{fmt.Sprintf("That release is for %s servers, and this one is %s. Download pbcm-%s-linux-%s.tar.gz instead.", m.Arch, u.arch(), m.Version, u.arch())}
	}
	if _, ok := m.Files["pbcm"]; !ok {
		return nil, &InputError{"The release file doesn't contain the pbcm program."}
	}
	os.RemoveAll(u.stagedDir())
	if err := os.Rename(tmp, u.stagedDir()); err != nil {
		return nil, err
	}
	changelog, _ := os.ReadFile(filepath.Join(u.stagedDir(), "CHANGELOG.md"))
	return &Staged{Version: m.Version, Notes: notesFor(changelog, m.Version)}, nil
}

// StagedRelease returns the staged release, checked again, or nil.
func (u *Updater) StagedRelease() *Staged {
	m, err := release.CheckDir(u.stagedDir(), u.keys())
	if err != nil {
		return nil
	}
	changelog, _ := os.ReadFile(filepath.Join(u.stagedDir(), "CHANGELOG.md"))
	return &Staged{Version: m.Version, Notes: notesFor(changelog, m.Version)}
}

// Discard removes the staged release.
func (u *Updater) Discard() { os.RemoveAll(u.stagedDir()) }

// Install swaps in the staged release. The caller then stops the server so
// systemd starts the new version. Only newer versions are installed this
// way; going back is Rollback.
func (u *Updater) Install() (string, error) { return u.install(false) }

// install swaps in the staged release; same allows the running version
// itself (Reinstall).
func (u *Updater) install(same bool) (string, error) {
	if why := u.CantUpdate(); why != "" {
		return "", &InputError{why}
	}
	if err := u.claim(); err != nil {
		return "", err
	}
	defer u.release()
	m, err := release.CheckDir(u.stagedDir(), u.keys())
	if err != nil {
		return "", &InputError{"There's no checked release ready to install. Download or upload it again."}
	}
	if !u.Newer(m.Version) && !(same && m.Version == u.Version) {
		return "", &InputError{fmt.Sprintf("Version %s isn't newer than the running %s.", m.Version, u.Version)}
	}
	backups := filepath.Join(u.DataDir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		return "", err
	}
	backup := filepath.Join(backups, backupName(u.Version, time.Now()))
	if err := u.Store.Backup(backup); err != nil {
		return "", fmt.Errorf("couldn't back up the database before updating: %w", err)
	}
	pruneBackups(backups)
	installed, added, err := u.swapIn(u.stagedDir(), m)
	if err != nil {
		return "", err
	}
	if err := writeState(u.DataDir, &State{Phase: Installed, From: u.Version, To: m.Version, Backup: backup, At: now(),
		Files: installed, Added: added}); err != nil {
		return "", err
	}
	u.Discard()
	slog.Info("update installed; restarting", "from", u.Version, "to", m.Version)
	return m.Version, nil
}

// CanRollBack returns the version a manual rollback would go back to, or "".
func (u *Updater) CanRollBack() string {
	s, err := ReadState(u.DataDir)
	if err != nil || s == nil || s.Phase == RolledBack || s.To != u.Version || s.From == s.To || !u.HasPrevious() {
		return "" // nothing to go back to (a reinstall leaves the same version)
	}
	if _, err := os.Stat(s.Backup); err != nil {
		return ""
	}
	return s.From
}

// Dismiss marks the last update's result as seen.
func (u *Updater) Dismiss() error {
	s, err := ReadState(u.DataDir)
	if err != nil || s == nil {
		return err
	}
	s.Seen = true
	return writeState(u.DataDir, s)
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
