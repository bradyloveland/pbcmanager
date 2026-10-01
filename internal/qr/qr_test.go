package qr

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// testdata/golden.json holds matrices from the 1.x generator, whose output
// was decoded with OpenCV across versions 1-15. Matching it exactly means this
// port produces readable codes.
func TestMatchesVersion1Output(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text string   `json:"text"`
		Rows []string `json:"rows"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		m, err := Matrix(c.Text)
		if err != nil {
			t.Fatal(err)
		}
		if len(m) != len(c.Rows) {
			t.Fatalf("%d chars: size %d, want %d", len(c.Text), len(m), len(c.Rows))
		}
		for y, row := range m {
			var b strings.Builder
			for _, v := range row {
				if v {
					b.WriteByte('1')
				} else {
					b.WriteByte('0')
				}
			}
			if b.String() != c.Rows[y] {
				t.Fatalf("%d chars: row %d differs", len(c.Text), y)
			}
		}
	}
}

func TestVersionGrowsWithLength(t *testing.T) {
	for _, c := range []struct{ length, size int }{{2, 21}, {20, 25}, {137, 49}, {400, 77}} {
		m, err := Matrix(strings.Repeat("a", c.length))
		if err != nil || len(m) != c.size {
			t.Errorf("length %d: size %d (%v), want %d", c.length, len(m), err, c.size)
		}
	}
	if _, err := Matrix(strings.Repeat("a", 2000)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("want ErrTooLong, got %v", err)
	}
}

func TestFinderAndTimingPatterns(t *testing.T) {
	m, _ := Matrix("otpauth://totp/x?secret=JBSWY3DPEHPK3PXP")
	n := len(m)
	for _, o := range [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}} {
		ox, oy := o[0], o[1]
		for d := 0; d < 7; d++ {
			if !(m[oy][ox+d] && m[oy+6][ox+d] && m[oy+d][ox] && m[oy+d][ox+6]) {
				t.Fatal("finder border not dark")
			}
		}
		if !m[oy+3][ox+3] {
			t.Fatal("finder centre not dark")
		}
	}
	for i := 8; i < n-8; i++ {
		if m[6][i] != (i%2 == 0) || m[i][6] != (i%2 == 0) {
			t.Fatal("timing pattern doesn't alternate")
		}
	}
}

func TestSVG(t *testing.T) {
	svg, err := SVG("hello")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<svg", `viewBox="0 0 29 29"`, `fill="#000"`} {
		if !strings.Contains(svg, want) {
			t.Errorf("svg missing %q", want)
		}
	}
}
