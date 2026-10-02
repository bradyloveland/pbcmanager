package runner

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Mount is one mounted filesystem on the client.
type Mount struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Source string `json:"source"`
	// Virtual is true for filesystems with nothing worth backing up (proc,
	// tmpfs, container layers and the like).
	Virtual bool `json:"virtual"`
}

// Filesystems is what the job form needs to know about a client's disks:
// a backup stays on the filesystem of each folder it's given, so other
// filesystems mounted below a folder are left out.
type Filesystems struct {
	Mounts []Mount `json:"mounts"`
	// RootExcludes are paths worth skipping in a backup of "/" that exist on
	// this client: swap files, package caches and temporary files.
	RootExcludes []string `json:"root_excludes"`
	// Docker is set when /var/lib/docker exists. It isn't suggested as an
	// exclude, because it holds volumes as well as images.
	Docker bool `json:"docker"`
}

// virtualTypes are filesystem types with nothing to back up.
var virtualTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true, "ramfs": true,
	"cgroup": true, "cgroup2": true, "securityfs": true, "pstore": true, "bpf": true, "debugfs": true,
	"tracefs": true, "mqueue": true, "hugetlbfs": true, "configfs": true, "fusectl": true, "autofs": true,
	"binfmt_misc": true, "efivarfs": true, "rpc_pipefs": true, "nsfs": true, "nfsd": true,
	"overlay": true, "squashfs": true, "fuse.lxcfs": true, "fuse.gvfsd-fuse": true, "fuse.portal": true,
}

// rootExcludeCandidates are suggested for a backup of "/" when they exist.
var rootExcludeCandidates = []string{"/var/swap", "/swapfile", "/swap.img", "/var/cache/apt/archives", "/var/tmp", "/lost+found"}

// ListFilesystems reads the client's mounts and the suggested excludes.
func ListFilesystems(env *Env) (*Filesystems, error) {
	f, err := os.Open(env.path("/proc/self/mountinfo"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	res := &Filesystems{Mounts: []Mount{}, RootExcludes: []string{}}
	seen := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		m, ok := parseMountinfo(sc.Text())
		if !ok {
			continue
		}
		// A later mount on the same path hides the earlier one.
		if i, dup := seen[m.Path]; dup {
			res.Mounts[i] = m
			continue
		}
		seen[m.Path] = len(res.Mounts)
		res.Mounts = append(res.Mounts, m)
	}
	sort.Slice(res.Mounts, func(i, j int) bool { return res.Mounts[i].Path < res.Mounts[j].Path })

	add := func(p string) {
		for _, x := range res.RootExcludes {
			if x == p {
				return
			}
		}
		res.RootExcludes = append(res.RootExcludes, p)
	}
	for _, p := range swapFiles(env) {
		add(p)
	}
	for _, p := range rootExcludeCandidates {
		if _, err := os.Lstat(env.path(p)); err == nil {
			add(p)
		}
	}
	if st, err := os.Stat(env.path("/var/lib/docker")); err == nil && st.IsDir() {
		res.Docker = true
	}
	return res, nil
}

// parseMountinfo reads one line of /proc/self/mountinfo:
//
//	36 35 98:0 /mnt1 /mnt/parent rw,noatime master:1 - ext3 /dev/root rw,errors=continue
func parseMountinfo(line string) (Mount, bool) {
	f := strings.Fields(line)
	sep := -1
	for i := 6; i < len(f); i++ {
		if f[i] == "-" {
			sep = i
			break
		}
	}
	if len(f) < 5 || sep < 0 || sep+2 >= len(f) {
		return Mount{}, false
	}
	m := Mount{Path: unescapeMount(f[4]), Type: f[sep+1], Source: unescapeMount(f[sep+2])}
	m.Virtual = virtualTypes[m.Type]
	return m, true
}

// unescapeMount undoes the kernel's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// swapFiles lists the swap files in use, from /proc/swaps.
func swapFiles(env *Env) []string {
	raw, err := os.ReadFile(env.path("/proc/swaps"))
	if err != nil {
		return nil
	}
	var out []string
	for i, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 || f[1] != "file" {
			continue
		}
		out = append(out, unescapeMount(f[0]))
	}
	return out
}
