// Package qr is a minimal QR code generator: byte mode, error correction
// level M, versions 1-15. That's enough for otpauth:// URIs. It's a port of
// the 1.x generator and is tested against its output.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

type eccGroup struct{ count, size int }

// version: EC codewords per block and the data block groups.
var eccM = map[int]struct {
	ec     int
	groups []eccGroup
}{
	1: {10, []eccGroup{{1, 16}}}, 2: {16, []eccGroup{{1, 28}}}, 3: {26, []eccGroup{{1, 44}}},
	4: {18, []eccGroup{{2, 32}}}, 5: {24, []eccGroup{{2, 43}}}, 6: {16, []eccGroup{{4, 27}}},
	7: {18, []eccGroup{{4, 31}}}, 8: {22, []eccGroup{{2, 38}, {2, 39}}}, 9: {22, []eccGroup{{3, 36}, {2, 37}}},
	10: {26, []eccGroup{{4, 43}, {1, 44}}}, 11: {30, []eccGroup{{1, 50}, {4, 51}}},
	12: {22, []eccGroup{{6, 36}, {2, 37}}}, 13: {22, []eccGroup{{8, 37}, {1, 38}}},
	14: {24, []eccGroup{{4, 40}, {5, 41}}}, 15: {24, []eccGroup{{5, 41}, {5, 42}}},
}

var align = map[int][]int{
	1: {}, 2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30}, 6: {6, 34}, 7: {6, 22, 38},
	8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50}, 11: {6, 30, 54}, 12: {6, 32, 58},
	13: {6, 34, 62}, 14: {6, 26, 46, 66}, 15: {6, 26, 48, 70},
}

// ErrTooLong means the text doesn't fit in a version 15 code.
var ErrTooLong = errors.New("data too long for a version 15 QR code")

func gfMul(x, y int) int {
	z := 0
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= ((y >> i) & 1) * x
	}
	return z
}

func rsDivisor(degree int) []int {
	result := make([]int, degree)
	result[degree-1] = 1
	root := 1
	for i := 0; i < degree; i++ {
		for j := 0; j < degree; j++ {
			result[j] = gfMul(result[j], root)
			if j+1 < degree {
				result[j] ^= result[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return result
}

func rsRemainder(data, divisor []int) []int {
	result := make([]int, len(divisor))
	for _, b := range data {
		factor := b ^ result[0]
		result = append(result[1:], 0)
		for i, coef := range divisor {
			result[i] ^= gfMul(coef, factor)
		}
	}
	return result
}

func encodeCodewords(payload []byte) (int, []int, error) {
	version := 0
	for v := 1; v <= 15; v++ {
		capacity := 0
		for _, g := range eccM[v].groups {
			capacity += g.count * g.size
		}
		ccBits := 8
		if v >= 10 {
			ccBits = 16
		}
		if 4+ccBits+8*len(payload) <= capacity*8 {
			version = v
			break
		}
	}
	if version == 0 {
		return 0, nil, ErrTooLong
	}
	spec := eccM[version]
	capacity := 0
	for _, g := range spec.groups {
		capacity += g.count * g.size
	}
	ccBits := 8
	if version >= 10 {
		ccBits = 16
	}

	var bits []int
	put := func(value, length int) {
		for i := length - 1; i >= 0; i-- {
			bits = append(bits, (value>>i)&1)
		}
	}
	put(0b0100, 4)
	put(len(payload), ccBits)
	for _, b := range payload {
		put(int(b), 8)
	}
	put(0, min(4, capacity*8-len(bits)))
	put(0, (8-len(bits)%8)%8)
	pad := 0xEC
	for len(bits) < capacity*8 {
		put(pad, 8)
		pad ^= 0xEC ^ 0x11
	}
	data := make([]int, len(bits)/8)
	for i := range data {
		for _, bit := range bits[i*8 : i*8+8] {
			data[i] = data[i]<<1 | bit
		}
	}

	type block struct{ data, ec []int }
	var blocks []block
	divisor := rsDivisor(spec.ec)
	pos := 0
	for _, g := range spec.groups {
		for i := 0; i < g.count; i++ {
			chunk := data[pos : pos+g.size]
			pos += g.size
			blocks = append(blocks, block{chunk, rsRemainder(chunk, divisor)})
		}
	}
	maxLen := 0
	for _, b := range blocks {
		maxLen = max(maxLen, len(b.data))
	}
	var out []int
	for i := 0; i < maxLen; i++ {
		for _, b := range blocks {
			if i < len(b.data) {
				out = append(out, b.data[i])
			}
		}
	}
	for i := 0; i < spec.ec; i++ {
		for _, b := range blocks {
			out = append(out, b.ec[i])
		}
	}
	return version, out, nil
}

type matrix struct {
	version, size int
	mod, fn       [][]bool
}

func newMatrix(version int) *matrix {
	size := 17 + 4*version
	m := &matrix{version: version, size: size, mod: make([][]bool, size), fn: make([][]bool, size)}
	for i := range m.mod {
		m.mod[i] = make([]bool, size)
		m.fn[i] = make([]bool, size)
	}
	return m
}

func (m *matrix) setFn(x, y int, dark bool) {
	m.mod[y][x] = dark
	m.fn[y][x] = true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (m *matrix) drawFunctionPatterns() {
	s := m.size
	for i := 0; i < s; i++ {
		m.setFn(6, i, i%2 == 0)
		m.setFn(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {s - 4, 3}, {3, s - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < s && y >= 0 && y < s {
					d := max(abs(dx), abs(dy))
					m.setFn(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	pos := align[m.version]
	n := len(pos)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if (i == 0 && j == 0) || (i == 0 && j == n-1) || (i == n-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					m.setFn(pos[i]+dx, pos[j]+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	m.drawFormat(0)
	if m.version >= 7 {
		rem := m.version
		for i := 0; i < 12; i++ {
			rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
		}
		bits := m.version<<12 | rem
		for i := 0; i < 18; i++ {
			dark := (bits>>i)&1 == 1
			a, b := s-11+i%3, i/3
			m.setFn(a, b, dark)
			m.setFn(b, a, dark)
		}
	}
}

func (m *matrix) drawFormat(mask int) {
	data := mask // level M is 00
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>i)&1 == 1 }
	s := m.size
	for i := 0; i < 6; i++ {
		m.setFn(8, i, bit(i))
	}
	m.setFn(8, 7, bit(6))
	m.setFn(8, 8, bit(7))
	m.setFn(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		m.setFn(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		m.setFn(s-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.setFn(8, s-15+i, bit(i))
	}
	m.setFn(8, s-8, true)
}

func (m *matrix) drawCodewords(data []int) {
	s, i := m.size, 0
	total := len(data) * 8
	for right := s - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < s; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := ((right + 1) & 2) == 0
				y := vert
				if upward {
					y = s - 1 - vert
				}
				if !m.fn[y][x] && i < total {
					m.mod[y][x] = (data[i>>3]>>(7-(i&7)))&1 == 1
					i++
				}
			}
		}
	}
}

func maskCond(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

func (m *matrix) applyMask(mask int) {
	for y := 0; y < m.size; y++ {
		for x := 0; x < m.size; x++ {
			if !m.fn[y][x] && maskCond(mask, x, y) {
				m.mod[y][x] = !m.mod[y][x]
			}
		}
	}
}

func (m *matrix) penalty() int {
	s, mod, score := m.size, m.mod, 0
	lines := make([][]bool, 0, 2*s)
	lines = append(lines, mod...)
	for x := 0; x < s; x++ {
		col := make([]bool, s)
		for y := 0; y < s; y++ {
			col[y] = mod[y][x]
		}
		lines = append(lines, col)
	}
	patA := []bool{true, false, true, true, true, false, true, false, false, false, false}
	patB := make([]bool, len(patA))
	for i := range patA {
		patB[i] = patA[len(patA)-1-i]
	}
	equal := func(a, b []bool) bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	for _, line := range lines {
		run := 0
		var prev *bool
		for idx := range line {
			v := line[idx]
			if prev != nil && v == *prev {
				run++
			} else {
				if run >= 5 {
					score += 3 + run - 5
				}
				run = 1
				prev = &line[idx]
			}
		}
		if run >= 5 {
			score += 3 + run - 5
		}
		for i := 0; i < len(line)-10; i++ {
			seg := line[i : i+11]
			if equal(seg, patA) || equal(seg, patB) {
				score += 40
			}
		}
	}
	for y := 0; y < s-1; y++ {
		for x := 0; x < s-1; x++ {
			c := mod[y][x]
			if c == mod[y][x+1] && c == mod[y+1][x] && c == mod[y+1][x+1] {
				score += 3
			}
		}
	}
	dark := 0
	for _, row := range mod {
		for _, v := range row {
			if v {
				dark++
			}
		}
	}
	score += 10 * (abs(dark*100/(s*s)-50) / 5)
	return score
}

// Matrix returns the QR modules for text, true meaning dark.
func Matrix(text string) ([][]bool, error) {
	version, codewords, err := encodeCodewords([]byte(text))
	if err != nil {
		return nil, err
	}
	var best *matrix
	bestScore := 0
	for mask := 0; mask < 8; mask++ {
		m := newMatrix(version)
		m.drawFunctionPatterns()
		m.drawCodewords(codewords)
		m.applyMask(mask)
		m.drawFormat(mask)
		if score := m.penalty(); best == nil || score < bestScore {
			best, bestScore = m, score
		}
	}
	return best.mod, nil
}

// SVG renders text as a QR code with a 4-module quiet zone.
func SVG(text string) (string, error) {
	modules, err := Matrix(text)
	if err != nil {
		return "", err
	}
	const border = 4
	full := len(modules) + 2*border
	var path strings.Builder
	for y, row := range modules {
		for x, dark := range row {
			if dark {
				fmt.Fprintf(&path, "M%d,%dh1v1h-1z", x+border, y+border)
			}
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code">`+
		`<rect width="%d" height="%d" fill="#fff"/><path d="%s" fill="#000"/></svg>`,
		full, full, full, full, path.String()), nil
}
