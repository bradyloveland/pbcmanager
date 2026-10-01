// Package update installs new versions of the server from the web UI: it
// checks GitHub for releases, takes uploaded release files, checks their
// signatures, swaps the program files and rolls back if the new version
// doesn't start.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Phases of an update.
const (
	Installed  = "installed"   // files swapped; waiting for the new version to start
	Confirming = "confirming"  // the new version started; waiting to see it stays up
	Done       = "done"        // the new version is running
	RolledBack = "rolled_back" // the previous version was put back
)

// State is the last update, kept in update.json in the data folder so the
// previous version can read it when it's put back.
type State struct {
	Phase  string `json:"phase"`
	From   string `json:"from"`
	To     string `json:"to"`
	Backup string `json:"backup"` // database copy taken before the update
	At     int64  `json:"at"`
	// Error is why the new version didn't start; Reason why it was rolled back.
	Error  string `json:"error,omitempty"`
	Reason string `json:"reason,omitempty"`
	Manual bool   `json:"manual,omitempty"`
	// Failures counts how often the new version stopped with an error.
	Failures int `json:"failures,omitempty"`
	// Seen is set once the result has been shown and dismissed.
	Seen bool `json:"seen,omitempty"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, "update.json") }

// ReadState returns the last update's state, or nil if there's none.
func ReadState(dataDir string) (*State, error) {
	raw, err := os.ReadFile(statePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func writeState(dataDir string, s *State) error {
	raw, _ := json.MarshalIndent(s, "", "  ")
	tmp := statePath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(dataDir))
}

// RecordStartFailure notes why the server couldn't start, when it's a
// version that was just installed, so the rollback can say why.
func RecordStartFailure(dataDir, version string, cause error) {
	s, err := ReadState(dataDir)
	if err != nil || s == nil || s.To != version || (s.Phase != Installed && s.Phase != Confirming) {
		return
	}
	s.Error = cause.Error()
	_ = writeState(dataDir, s)
}

// MaxFailures is how many times a new version may fail before the previous
// one is put back.
const MaxFailures = 3

// AfterStop is run by systemd (from the previous version's program) each
// time the service stops. result is $SERVICE_RESULT: "success" for a clean
// stop. While an update is unconfirmed, it counts failed runs and puts the
// previous version back after MaxFailures; systemd then starts that one.
// It reports whether it rolled back.
func (f Files) AfterStop(result string) (bool, error) {
	if result == "" || result == "success" {
		return false, nil
	}
	s, err := ReadState(f.DataDir)
	if err != nil || s == nil || (s.Phase != Installed && s.Phase != Confirming) {
		return false, err
	}
	s.Failures++
	if s.Failures < MaxFailures {
		return false, writeState(f.DataDir, s)
	}
	if err := writeState(f.DataDir, s); err != nil {
		return false, err
	}
	reason := fmt.Sprintf("Version %s stopped with an error %d times in a row.", s.To, s.Failures)
	if s.Error != "" {
		reason = fmt.Sprintf("Version %s didn't start: %s", s.To, s.Error)
	}
	return true, f.Rollback(reason, false)
}

func now() int64 { return time.Now().Unix() }
