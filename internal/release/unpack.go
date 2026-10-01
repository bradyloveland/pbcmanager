package release

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Limits for an archive: a release is a few tens of MB.
const (
	maxFile  = 200 << 20
	maxTotal = 400 << 20
)

// ErrNotRelease means the file isn't a PBC Manager release archive.
var ErrNotRelease = errors.New("this isn't a PBC Manager release file (pbcm-<version>-linux-<arch>.tar.gz from GitHub)")

// Unpack extracts a release archive into dir (which must be empty or not
// exist) and checks it: the manifest's signature, that every file is listed
// with the right checksum, and that nothing else is in it. On error dir may
// hold partial files; the caller removes it.
func Unpack(r io.Reader, dir string, keys []Key) (*Manifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, ErrNotRelease
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	top := ""
	var total int64
	files := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrNotRelease
		}
		name := strings.TrimPrefix(h.Name, "./")
		first, rest, _ := strings.Cut(strings.TrimSuffix(name, "/"), "/")
		if top == "" {
			top = first
		}
		// Everything sits in one folder, with no subfolders.
		if first != top || strings.Contains(rest, "/") || !strings.HasPrefix(top, "pbcm-") {
			return nil, ErrNotRelease
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if rest != "" {
				return nil, ErrNotRelease
			}
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("the release file contains %s, which isn't a plain file", name)
		}
		if rest == "" || !nameRE.MatchString(rest) || files[rest] {
			return nil, ErrNotRelease
		}
		if h.Size > maxFile || total+h.Size > maxTotal {
			return nil, errors.New("the release file is too large")
		}
		total += h.Size
		f, err := os.OpenFile(filepath.Join(dir, rest), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(f, io.LimitReader(tr, h.Size))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, ErrNotRelease
		}
		files[rest] = true
	}
	if !files["MANIFEST"] {
		return nil, ErrNotRelease
	}
	return CheckDir(dir, keys)
}

// CheckDir checks an unpacked release folder (see Unpack).
func CheckDir(dir string, keys []Key) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "MANIFEST"))
	if err != nil {
		return nil, ErrNotRelease
	}
	sig, err := os.ReadFile(filepath.Join(dir, "MANIFEST.sig"))
	if err != nil {
		return nil, ErrNotSigned
	}
	m, err := Verify(raw, sig, keys)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := 0
	for _, e := range entries {
		if e.Name() == "MANIFEST" || e.Name() == "MANIFEST.sig" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if err := m.Check(e.Name(), data); err != nil {
			return nil, err
		}
		seen++
	}
	if seen != len(m.Files) {
		return nil, errors.New("the release file is missing some of its files")
	}
	return m, nil
}
