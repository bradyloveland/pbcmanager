package release

import (
	"strconv"
	"strings"
)

// Version is a parsed version number like 2.1.0 or 2.1.0-rc.1.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // "" for a final release
}

// ParseVersion reads "2.1.0", "v2.1.0" or "2.1.0-rc.1". It returns nil if s
// isn't a version.
func ParseVersion(s string) *Version {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return nil
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || (len(p) > 1 && p[0] == '0') {
			return nil
		}
		n[i] = v
	}
	if strings.ContainsAny(pre, " \t\n") || (strings.Contains(s, "-") && pre == "") {
		return nil
	}
	return &Version{n[0], n[1], n[2], pre}
}

func (v *Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare returns -1, 0 or 1 as a is older than, the same as, or newer than
// b. A pre-release (2.1.0-rc.1) comes before its final release (2.1.0).
// Unparseable versions count as oldest.
func Compare(a, b string) int {
	va, vb := ParseVersion(a), ParseVersion(b)
	switch {
	case va == nil && vb == nil:
		return 0
	case va == nil:
		return -1
	case vb == nil:
		return 1
	}
	for _, d := range [3]int{va.Major - vb.Major, va.Minor - vb.Minor, va.Patch - vb.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case va.Pre == vb.Pre:
		return 0
	case va.Pre == "":
		return 1
	case vb.Pre == "":
		return -1
	}
	return comparePre(va.Pre, vb.Pre)
}

// comparePre orders pre-release tags field by field, numbers numerically.
func comparePre(a, b string) int {
	fa, fb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(fa) && i < len(fb); i++ {
		na, ea := strconv.Atoi(fa[i])
		nb, eb := strconv.Atoi(fb[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return sign(na - nb)
			}
		case ea == nil:
			return -1
		case eb == nil:
			return 1
		default:
			if c := strings.Compare(fa[i], fb[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(fa) - len(fb))
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
