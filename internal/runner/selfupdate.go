package runner

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/bradyloveland/pbcmanager/internal/release"
)

// UpdateRequest is what the server sends to self-update: the release's
// signed manifest and the new pbcm-runner.
type UpdateRequest struct {
	Manifest  string `json:"manifest"`
	Signature string `json:"signature"`
	Runner    []byte `json:"runner"`
}

// SelfUpdate replaces pbcm-runner with a new version, after checking it's
// part of a release signed by a key built into this runner. The server is
// never trusted for this on its own.
func SelfUpdate(env *Env, in io.Reader) error {
	var req UpdateRequest
	if err := json.NewDecoder(io.LimitReader(in, 100<<20)).Decode(&req); err != nil {
		return errors.New("the update request isn't valid")
	}
	m, err := release.Verify([]byte(req.Manifest), []byte(req.Signature), release.Keys)
	if err != nil {
		return err
	}
	if err := m.Check("pbcm-runner", req.Runner); err != nil {
		return err
	}
	if err := writeAtomic(env.path(Path), req.Runner, 0o755); err != nil {
		return err
	}
	return json.NewEncoder(env.Stdout).Encode(map[string]string{"version": m.Version, "hash": release.Hash(req.Runner)})
}

// selfHash identifies the installed runner, so the server can tell when it
// needs updating ("" if it can't be read).
func (e *Env) selfHash() string {
	data, err := os.ReadFile(e.path(Path))
	if err != nil {
		return ""
	}
	return release.Hash(data)
}
