package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeasureInTheBackgroundAndReport(t *testing.T) {
	je := newJobEnv(t)
	je.apply(t, testBundle("/srv/media"))
	os.MkdirAll(je.path("/srv/media/sub"), 0o755)
	os.WriteFile(je.path("/srv/media/a.bin"), make([]byte, 1000), 0o644)
	os.WriteFile(je.path("/srv/media/sub/b.bin"), make([]byte, 234), 0o644)

	je.out.Reset()
	if err := Measure(je.Env, []string{"/srv/media/", "/srv/missing"}); err != nil {
		t.Fatal(err)
	}
	calls := je.callLog()
	if !strings.Contains(calls, "systemd-run --quiet --collect --unit=pbcm-measure-") || !strings.Contains(calls, "--property=IOSchedulingClass=idle /usr/local/lib/pbcm/pbcm-runner measure-run /srv/media") {
		t.Fatalf("measuring should run as an idle background unit:\n%s", calls)
	}
	runs := readStatus(t, je)
	for _, s := range runs.Sizes {
		if !s.Measuring {
			t.Fatalf("started measurements show as measuring: %+v", s)
		}
	}
	// What the unit does:
	if err := MeasureRun(je.Env, "/srv/media"); err != nil {
		t.Fatal(err)
	}
	MeasureRun(je.Env, "/srv/missing")
	got := map[string]string{}
	for _, s := range readStatus(t, je).Sizes {
		if s.Measuring {
			t.Fatalf("finished measurement still measuring: %+v", s)
		}
		if s.Bytes != nil {
			got[s.Path] = "bytes"
			if *s.Bytes < 1234 {
				t.Fatalf("size %d", *s.Bytes)
			}
		} else {
			got[s.Path] = s.Error
		}
	}
	if got["/srv/media"] != "bytes" || !strings.Contains(got["/srv/missing"], "not found") {
		t.Fatalf("sizes %v", got)
	}
	if err := Measure(je.Env, []string{"relative"}); err == nil {
		t.Fatal("relative paths refused")
	}
}

func TestWalkBytesMatchesFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "x/y"), 0o755)
	os.WriteFile(filepath.Join(dir, "x/y/f"), make([]byte, 4096), 0o644)
	os.WriteFile(filepath.Join(dir, "g"), make([]byte, 10), 0o644)
	os.Symlink(filepath.Join(dir, "g"), filepath.Join(dir, "link"))
	if n, err := walkBytes(dir); err != nil || n != 4106 {
		t.Fatalf("walk %d %v", n, err)
	}
}

func readStatus(t *testing.T, je *jobEnv) struct {
	Sizes []struct {
		Path      string
		Bytes     *int64
		Error     string
		Measuring bool
	}
} {
	t.Helper()
	je.out.Reset()
	if err := StatusSince(je.Env, 0); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Sizes []struct {
			Path      string
			Bytes     *int64
			Error     string
			Measuring bool
		}
	}
	json.Unmarshal(je.out.Bytes(), &st)
	return st
}
