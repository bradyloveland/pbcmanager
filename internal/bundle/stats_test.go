package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures are real proxmox-backup-client output (4.2.7 and 3.4.7)
// from backups against a PBS container.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pbc", name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// near allows for the client rounding sizes to three decimals.
func near(a int64, b float64) bool { d := float64(a) - b; return d > -2048 && d < 2048 }

func TestParseStats(t *testing.T) {
	const KiB, MiB = 1024, 1024 * 1024
	cases := []struct {
		name                string
		read, up, reused    float64
		files, changed      int
		archives            int
		hasFiles, hasReused bool
	}{
		// First backup in metadata mode: everything uploaded, nothing reused.
		{"4.2-meta-first", 7.63*MiB + 5.558*KiB, 7.63*MiB + 5.558*KiB, 0, 0, 0, 1, false, true},
		// Nothing changed: only padding uploaded, all of the data reused.
		{"4.2-meta-same", 7.63*MiB + 5.558*KiB, 32 + 5.558*KiB, 7.63 * MiB, 40, 0, 1, true, true},
		// A file added, one changed, one removed.
		{"4.2-meta-changed", 8.298*MiB + 5.559*KiB, 3.72*MiB + 5.559*KiB, 4.578 * MiB, 40, 18, 1, true, true},
		{"4.2-data-mode", 7.916*MiB + 5.559*KiB, 4.483*MiB + 5.559*KiB, 3.434 * MiB, 0, 0, 1, false, true},
		{"4.2-legacy-same", 7.92 * MiB, 0, 7.92 * MiB, 0, 0, 1, false, true},
		{"4.2-two-archives", 7.916*MiB + 5.559*KiB + 439.719*KiB + 2.101*KiB, 7.916*MiB + 5.559*KiB + 439.719*KiB + 2.101*KiB, 0, 0, 0, 2, false, true},
		{"3.4-meta-first", 7.63*MiB + 5.421*KiB, 7.63*MiB + 5.421*KiB, 0, 0, 0, 1, false, true},
		{"3.4-legacy-same", 7.92 * MiB, 7.92 * MiB, 0, 0, 0, 1, false, true},
	}
	for _, c := range cases {
		s := ParseStats(fixture(t, c.name))
		if s == nil {
			t.Errorf("%s: no stats", c.name)
			continue
		}
		if !near(s.Read, c.read) || !near(s.Uploaded, c.up) || !near(s.Reused, c.reused) || s.Files != c.files || s.Changed != c.changed ||
			len(s.Archives) != c.archives || s.Has("files") != c.hasFiles || s.Has("reused") != c.hasReused || !s.Has("duration") || s.Seconds <= 0 {
			t.Errorf("%s: got read %d up %d reused %d files %d changed %d archives %d known %v (%.2fs)", c.name,
				s.Read, s.Uploaded, s.Reused, s.Files, s.Changed, len(s.Archives), s.Known, s.Seconds)
		}
	}
	two := ParseStats(fixture(t, "4.2-two-archives"))
	if two.Archives[0].Name != "media" || two.Archives[1].Name != "docs" {
		t.Errorf("archives are named after the folder's archive: %+v", two.Archives)
	}
	if p := ParseStats(fixture(t, "4.2-meta-same")).ReusedPercent(); p < 99 || p > 100 {
		t.Errorf("reused percent: %.1f", p)
	}
	for _, name := range []string{"4.2-missing-path", "4.2-bad-token"} {
		if s := ParseStats(fixture(t, name)); s != nil {
			t.Errorf("%s: a failed run has no figures, got %+v", name, s)
		}
	}
	if ParseStats("something new: 5 GB done") != nil {
		t.Error("unrelated lines are ignored")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[[2]string]int64{{"32", "B"}: 32, {"1.5", "KiB"}: 1536, {"2", "MiB"}: 2 << 20, {"1", "GiB"}: 1 << 30, {"0", "B"}: 0} {
		if got := parseSize(in[0], in[1]); got != want {
			t.Errorf("%v: %d, want %d", in, got, want)
		}
	}
}

func TestParseProgressLine(t *testing.T) {
	for line, want := range map[string]struct {
		kind, archive string
		n             int64
	}{
		"Upload directory '/srv/my media' to 'a@pbs@host:store' as media.pxar.didx": {LineFolder, "media", 0},
		"Upload directory '/srv/m' to 'repo' as media.mpxar.didx":                   {LineFolder, "media", 0},
		"processed 1.5 GiB in 2m 3s, uploaded 345 MiB":                              {LineProcessed, "", 3 << 29},
		"processed 512 B in 1m, uploaded 0 B\r":                                     {LineProcessed, "", 512},
		"media.ppxar: had to backup 1.5 MiB of 6 MiB (compressed 1 MiB) in 0.02 s":  {LineFinished, "media", 6 << 20},
		"root.pxar: had to backup 0 B of 2 GiB (compressed 0 B) in 9 s":             {LineFinished, "root", 2 << 30},
		"media.mpxar: had to backup 1 MiB of 2 MiB (compressed 1 MiB) in 1 s":       {"", "", 0},
		"Starting backup: host/nas/2026-10-01T00:00:00Z":                            {"", "", 0},
	} {
		k, a, n := ParseProgressLine(line)
		if k != want.kind || a != want.archive || n != want.n {
			t.Errorf("%q: got %q %q %d", line, k, a, n)
		}
	}
}
