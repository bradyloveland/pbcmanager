package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/release"
)

// Missing lists the running version's program files that aren't in the
// program folder, going by its own signed manifest. It's empty for a
// development build, which has no manifest.
func (u *Updater) Missing() []string {
	raw, err1 := os.ReadFile(filepath.Join(u.AppDir, "MANIFEST"))
	sig, err2 := os.ReadFile(filepath.Join(u.AppDir, "MANIFEST.sig"))
	if err1 != nil || err2 != nil {
		return nil
	}
	m, err := release.Verify(raw, sig, u.keys())
	if err != nil || m.Version != u.Version {
		return nil
	}
	var missing []string
	for _, name := range programFiles(m) {
		if _, err := os.Stat(filepath.Join(u.AppDir, name)); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, name)
		}
	}
	return missing
}

// Reinstall downloads the running version's release from GitHub, checks it,
// and installs it again, putting back any missing files. The caller then
// restarts the server, as after an update.
func (u *Updater) Reinstall(ctx context.Context) error {
	if why := u.CantUpdate(); why != "" {
		return &InputError{why}
	}
	url, err := u.assetFor(ctx, u.Version)
	if err != nil {
		return err
	}
	if err := u.claim(); err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "pbcm/"+u.Version)
	resp, err := u.client().Do(req)
	if err != nil {
		u.release()
		return errors.New("couldn't download the release from GitHub: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		u.release()
		return fmt.Errorf("couldn't download the release from GitHub (%s)", resp.Status)
	}
	st, err := u.stage(resp.Body)
	u.release()
	if err != nil {
		return err
	}
	if st.Version != u.Version {
		u.Discard()
		return fmt.Errorf("the downloaded file is version %s, not %s", st.Version, u.Version)
	}
	_, err = u.install(true)
	return err
}

// assetFor finds a release's archive for this server's CPU on GitHub.
func (u *Updater) assetFor(ctx context.Context, version string) (string, error) {
	api := u.API
	if api == "" {
		api = DefaultAPI
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", api+"/releases/tags/v"+version, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "pbcm/"+u.Version)
	resp, err := u.client().Do(req)
	if err != nil {
		return "", errors.New("couldn't reach GitHub. Check that this server can reach the internet, or run install.sh on the server again")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", &InputError{fmt.Sprintf("Version %s isn't published on GitHub, so it can't be reinstalled from there.", version)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered with an error (%s)", resp.Status)
	}
	var r struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&r); err != nil {
		return "", errors.New("GitHub sent an unexpected reply")
	}
	want := "pbcm-" + version + "-linux-" + u.arch() + ".tar.gz"
	for _, a := range r.Assets {
		if a.Name == want {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("the %s release on GitHub has no file for this server (%s)", version, want)
}
