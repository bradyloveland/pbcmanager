package bundle

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

// Stats are a backup's figures, read from proxmox-backup-client's summary
// lines at the end of each archive. Any figure the client didn't print is
// left at zero (Known says which were found), so a change in the client's
// wording never fails a run.
type Stats struct {
	Read       int64          `json:"read"`       // bytes in the backed-up folders ("of <total>")
	Uploaded   int64          `json:"uploaded"`   // new data sent ("had to backup")
	Compressed int64          `json:"compressed"` // the same, compressed
	Reused     int64          `json:"reused"`     // data reused from the last snapshot
	Files      int            `json:"files"`      // metadata change detection only
	Changed    int            `json:"changed"`    // changed or not reusable
	Seconds    float64        `json:"seconds"`    // the client's "Duration"
	Archives   []ArchiveStats `json:"archives"`
	// Known lists what was found: "sizes", "reused", "files", "duration".
	Known []string `json:"known"`
}

// ArchiveStats are one folder's figures. A folder backed up with metadata
// change detection is uploaded as two parts (.mpxar and .ppxar); they're
// added together under the folder's archive name.
type ArchiveStats struct {
	Name     string `json:"name"`
	Read     int64  `json:"read"`
	Uploaded int64  `json:"uploaded"`
	Reused   int64  `json:"reused"`
}

// ReusedPercent is the share of the data read that didn't need uploading.
func (s *Stats) ReusedPercent() float64 {
	if s == nil || s.Read <= 0 {
		return 0
	}
	return float64(s.Reused) * 100 / float64(s.Read)
}

// Has reports whether a kind of figure was found.
func (s *Stats) Has(kind string) bool {
	if s == nil {
		return false
	}
	for _, k := range s.Known {
		if k == kind {
			return true
		}
	}
	return false
}

const sizeRE = `([0-9]+(?:\.[0-9]+)?) ?([KMGTPE]i?B|B)`

var (
	hadToBackupRE = regexp.MustCompile(`^(\S+?)\.[mp]?pxar: had to backup ` + sizeRE + ` of ` + sizeRE + ` \(compressed ` + sizeRE + `\)`)
	reusedRE      = regexp.MustCompile(`^(\S+?)\.[mp]?pxar: backup was done incrementally, reused ` + sizeRE)
	totalFilesRE  = regexp.MustCompile(`^ *- ([0-9]+) total files`)
	changedRE     = regexp.MustCompile(`^ *- ([0-9]+) changed or non-reusable files`)
	durationRE    = regexp.MustCompile(`^Duration: ([0-9]+(?:\.[0-9]+)?) ?s`)
)

// ParseStats reads the figures from a backup's log. It returns nil if the log
// has none of them (for example, a run that failed before uploading).
func ParseStats(log string) *Stats {
	s := &Stats{Archives: []ArchiveStats{}}
	byName := map[string]int{}
	archive := func(name string) *ArchiveStats {
		i, ok := byName[name]
		if !ok {
			i = len(s.Archives)
			byName[name] = i
			s.Archives = append(s.Archives, ArchiveStats{Name: name})
		}
		return &s.Archives[i]
	}
	known := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if m := hadToBackupRE.FindStringSubmatch(line); m != nil {
			up, read, comp := parseSize(m[2], m[3]), parseSize(m[4], m[5]), parseSize(m[6], m[7])
			a := archive(m[1])
			a.Uploaded += up
			a.Read += read
			s.Uploaded += up
			s.Read += read
			s.Compressed += comp
			known["sizes"] = true
		} else if m := reusedRE.FindStringSubmatch(line); m != nil {
			n := parseSize(m[2], m[3])
			archive(m[1]).Reused += n
			s.Reused += n
			known["reused"] = true
		} else if m := totalFilesRE.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			s.Files += n
			known["files"] = true
		} else if m := changedRE.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			s.Changed += n
		} else if m := durationRE.FindStringSubmatch(line); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			s.Seconds += v // one per destination run; a run log has one
			known["duration"] = true
		}
	}
	if len(known) == 0 {
		return nil
	}
	// A first backup reuses nothing: say so rather than leaving it unknown.
	if known["sizes"] && !known["reused"] {
		known["reused"] = true
	}
	for _, k := range []string{"sizes", "reused", "files", "duration"} {
		if known[k] {
			s.Known = append(s.Known, k)
		}
	}
	return s
}

// parseSize reads "7.63" and "MiB" as bytes. proxmox-backup-client uses
// binary units; KB-style units are read the same way.
func parseSize(num, unit string) int64 {
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{"B": 1, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40, "P": 1 << 50, "E": 1 << 60}[unit[:1]]
	if unit == "B" {
		mult = 1
	}
	return int64(v*mult + 0.5)
}
