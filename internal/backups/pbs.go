package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bradyloveland/pbcmanager/internal/bundle"
	"github.com/bradyloveland/pbcmanager/internal/store"
)

// PBS runs the server's own proxmox-backup-client. It never reads backup
// data; it only asks PBS about datastores and snapshots.
type PBS struct {
	// Bin is the client to run; by default proxmox-backup-client on PATH
	// (PBCM_CLIENT overrides it, for development and tests).
	Bin string
}

// ErrNoClient means the server has no proxmox-backup-client.
var ErrNoClient = errors.New("this server doesn't have proxmox-backup-client, so it can't contact PBS to test the connection or list snapshots. " +
	"You can still save the destination, and backups aren't affected: they run on the clients. " +
	"To fix this, re-run the server's installer or install the proxmox-backup-client package")

func (p *PBS) bin() (string, error) {
	bin := p.Bin
	if bin == "" {
		bin = os.Getenv("PBCM_CLIENT")
	}
	if bin == "" {
		bin = "proxmox-backup-client"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", ErrNoClient
	}
	return path, nil
}

func (p *PBS) run(ctx context.Context, d *store.Destination, timeout time.Duration, args ...string) (string, error) {
	bin, err := p.bin()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "pbcm-pbs-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte(d.Secret), 0o600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "PBS_REPOSITORY="+d.Repository(), "PBS_PASSWORD_FILE="+secret)
	if d.Fingerprint != "" {
		cmd.Env = append(cmd.Env, "PBS_FINGERPRINT="+d.Fingerprint)
	}
	if d.Namespace != "" {
		cmd.Env = append(cmd.Env, "PBS_NAMESPACE="+d.Namespace)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("the PBS server didn't answer within %d seconds", int(timeout.Seconds()))
	}
	if err != nil {
		return "", errors.New(bundle.SummarizeError(stderr.String() + "\n" + stdout.String()))
	}
	return stdout.String(), nil
}

// Usage is a datastore's space in bytes.
type Usage struct {
	Total *int64 `json:"total"`
	Used  *int64 `json:"used"`
	Avail *int64 `json:"avail"`
}

// Status checks the connection and reads the datastore's space.
func (p *PBS) Status(ctx context.Context, d *store.Destination) (*Usage, error) {
	out, err := p.run(ctx, d, 45*time.Second, "status", "--output-format", "json")
	if err != nil {
		return nil, err
	}
	var u Usage
	if err := json.Unmarshal([]byte(out), &u); err != nil {
		return &Usage{}, nil
	}
	return &u, nil
}

// Snapshot is one backup on the server.
type Snapshot struct {
	Time      int64    `json:"time"`
	Size      *int64   `json:"size"`
	Files     []string `json:"files"`
	Verified  string   `json:"verified"`
	Protected bool     `json:"protected"`
	Comment   string   `json:"comment"`
}

// Snapshots lists a backup group's snapshots, newest first.
func (p *PBS) Snapshots(ctx context.Context, d *store.Destination, backupID string) ([]Snapshot, error) {
	out, err := p.run(ctx, d, 60*time.Second, "snapshot", "list", "host/"+backupID, "--output-format", "json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Time         int64  `json:"backup-time"`
		Size         *int64 `json:"size"`
		Protected    bool   `json:"protected"`
		Comment      string `json:"comment"`
		Verification *struct {
			State string `json:"state"`
		} `json:"verification"`
		Files []json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, errors.New("the PBS server sent an unexpected reply")
	}
	snaps := make([]Snapshot, 0, len(raw))
	for _, r := range raw {
		s := Snapshot{Time: r.Time, Size: r.Size, Protected: r.Protected, Comment: r.Comment, Files: []string{}}
		if r.Verification != nil {
			s.Verified = r.Verification.State
		}
		for _, f := range r.Files {
			var name string
			if json.Unmarshal(f, &name) != nil {
				var obj struct {
					Filename string `json:"filename"`
				}
				json.Unmarshal(f, &obj)
				name = obj.Filename
			}
			s.Files = append(s.Files, name)
		}
		snaps = append(snaps, s)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time > snaps[j].Time })
	return snaps, nil
}
