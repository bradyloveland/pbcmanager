package bundle

import (
	"strconv"
	"strings"
)

// PackageCache is the server's cache kind for each client's PackageInfo
// (key: client ID).
const PackageCache = "package"

// PackageInfo says which proxmox-backup-client a client has and whether its
// package lists offer a newer one. It's read-only: PBC Manager never
// installs or upgrades packages; the client's own updates do that.
type PackageInfo struct {
	Package   string `json:"package"`   // proxmox-backup-client or proxmox-backup-client-static
	Installed string `json:"installed"` // the installed version, such as 3.4.7-1
	Candidate string `json:"candidate"` // the newest in the client's package lists ("" if none)
	// FromApt is false when the client wasn't installed as a package.
	FromApt bool `json:"from_apt"`
	// Newer is dpkg's verdict that Candidate is newer than Installed.
	Newer bool `json:"newer"`
	// ListsUpdated is when the client last ran "apt update", so "newest"
	// is only as fresh as that.
	ListsUpdated int64 `json:"lists_updated"`
	Checked      int64 `json:"checked"`
	// AvailableSince is when the server first saw Candidate as an update.
	AvailableSince int64 `json:"available_since"`
}

// UpdateAvailable reports whether a newer version can be installed.
func (p *PackageInfo) UpdateAvailable() bool {
	return p != nil && p.FromApt && p.Newer && p.Candidate != ""
}

// MajorUpdate reports whether the newer version is a new major version
// (say 4.x for a client on 3.x), which usually comes with an OS upgrade.
func (p *PackageInfo) MajorUpdate() bool {
	return p.UpdateAvailable() && major(p.Candidate) > major(p.Installed)
}

// major is the leading number of a Debian version, after any epoch.
func major(v string) int {
	if i := strings.Index(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	end := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(v)
	}
	n, _ := strconv.Atoi(v[:end])
	return n
}
