// Package release signs and checks release files.
//
// Each release archive holds a MANIFEST listing the SHA-256 of every other
// file in it, and MANIFEST.sig, an ed25519 signature of the manifest. The
// public keys that may sign releases are built into pbcm and pbcm-runner, so
// a downloaded or uploaded archive, and a runner sent to a client, can be
// checked without trusting where it came from.
package release

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Key is a public key that may sign releases.
type Key struct {
	ID  string
	Pub ed25519.PublicKey
}

// Keys are the trusted signing keys. More than one lets a key be replaced:
// releases signed with the new key are trusted by versions that know both.
var Keys = mustKeys(builtinKeys)

// ExtraKey adds a trusted key at build time ("id:base64"), for CI tests of
// the updater. Release builds leave it empty.
var ExtraKey string

func init() {
	if ExtraKey != "" {
		Keys = append(Keys, mustKeys([]string{ExtraKey})...)
	}
}

func mustKeys(list []string) []Key {
	var out []Key
	for _, s := range list {
		k, err := ParseKey(s)
		if err != nil {
			panic(err)
		}
		out = append(out, k)
	}
	return out
}

// ParseKey reads "id:base64-public-key".
func ParseKey(s string) (Key, error) {
	id, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil || len(raw) != ed25519.PublicKeySize || !idRE.MatchString(id) {
		return Key{}, fmt.Errorf("bad release key %q", s)
	}
	return Key{ID: id, Pub: raw}, nil
}

// ParsePrivateKey reads "id:base64-seed", the form kept in the signing secret.
func ParsePrivateKey(s string) (string, ed25519.PrivateKey, error) {
	id, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil || len(raw) != ed25519.SeedSize || !idRE.MatchString(id) {
		return "", nil, errors.New("the signing key should look like id:base64-seed")
	}
	return id, ed25519.NewKeyFromSeed(raw), nil
}

var (
	idRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,40}$`)
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,100}$`)
)

// Manifest lists a release's files.
type Manifest struct {
	Version string
	Arch    string
	Files   map[string]string // name: SHA-256 hex
}

const manifestHead = "pbcm-manifest 1"

// Encode writes the manifest in its signed form.
func (m *Manifest) Encode() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\nversion %s\narch %s\n", manifestHead, m.Version, m.Arch)
	names := make([]string, 0, len(m.Files))
	for n := range m.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "sha256 %s %s\n", m.Files[n], n)
	}
	return b.Bytes()
}

// ParseManifest reads a manifest. Check its signature first (Verify).
func ParseManifest(raw []byte) (*Manifest, error) {
	m := &Manifest{Files: map[string]string{}}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			if line != manifestHead {
				return nil, errors.New("not a release manifest")
			}
			first = false
			continue
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[0] == "version":
			m.Version = f[1]
		case len(f) == 2 && f[0] == "arch":
			m.Arch = f[1]
		case len(f) == 3 && f[0] == "sha256" && len(f[1]) == 64 && nameRE.MatchString(f[2]):
			if _, err := hex.DecodeString(f[1]); err != nil {
				return nil, errors.New("bad checksum in the release manifest")
			}
			m.Files[f[2]] = f[1]
		default:
			return nil, errors.New("the release manifest has a line it doesn't understand")
		}
	}
	if first || ParseVersion(m.Version) == nil || m.Arch == "" || len(m.Files) == 0 {
		return nil, errors.New("the release manifest is incomplete")
	}
	return m, nil
}

const sigHead = "pbcm-signature 1"

// Sign signs a manifest.
func Sign(id string, key ed25519.PrivateKey, manifest []byte) []byte {
	sig := ed25519.Sign(key, manifest)
	return []byte(fmt.Sprintf("%s %s %s\n", sigHead, id, base64.StdEncoding.EncodeToString(sig)))
}

// ErrNotSigned means a release has no valid signature from a trusted key.
var ErrNotSigned = errors.New("this release isn't signed by the PBC Manager project, so it wasn't installed")

// Verify checks a manifest's signature against keys and parses it.
func Verify(manifest, sig []byte, keys []Key) (*Manifest, error) {
	f := strings.Fields(string(sig))
	if len(f) != 4 || f[0]+" "+f[1] != sigHead {
		return nil, ErrNotSigned
	}
	raw, err := base64.StdEncoding.DecodeString(f[3])
	if err != nil {
		return nil, ErrNotSigned
	}
	for _, k := range keys {
		if k.ID == f[2] && ed25519.Verify(k.Pub, manifest, raw) {
			return ParseManifest(manifest)
		}
	}
	return nil, ErrNotSigned
}

// Hash is the SHA-256 hex of data.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Check confirms that data is the named file of the manifest.
func (m *Manifest) Check(name string, data []byte) error {
	want, ok := m.Files[name]
	if !ok {
		return fmt.Errorf("%s isn't part of this release", name)
	}
	if Hash(data) != want {
		return fmt.Errorf("%s doesn't match the release's checksum; the file is damaged or was changed", name)
	}
	return nil
}
