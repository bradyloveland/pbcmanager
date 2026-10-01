// pbcm-sign writes the signed MANIFEST of a release folder.
//
//	go run ./tools/pbcm-sign genkey <id>              print a new key pair
//	go run ./tools/pbcm-sign sign <dir> <version> <arch>
//
// sign reads the private key ("id:base64-seed") from PBCM_SIGNING_KEY. Without
// it, the folder gets an unsigned MANIFEST, which installs with install.sh
// but can't be installed from the web UI.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bradyloveland/pbcmanager/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pbcm-sign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "genkey":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Printf("public:  %s:%s\nprivate: %s:%s\n", args[1], base64.StdEncoding.EncodeToString(pub),
			args[1], base64.StdEncoding.EncodeToString(priv.Seed()))
		return nil
	case len(args) == 4 && args[0] == "sign":
		return sign(args[1], args[2], args[3])
	}
	return fmt.Errorf("usage: pbcm-sign genkey <id> | sign <dir> <version> <arch>")
}

func sign(dir, version, arch string) error {
	m := &release.Manifest{Version: version, Arch: arch, Files: map[string]string{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "MANIFEST" || e.Name() == "MANIFEST.sig" {
			continue
		}
		if !e.Type().IsRegular() {
			return fmt.Errorf("%s isn't a plain file", e.Name())
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		m.Files[e.Name()] = release.Hash(data)
	}
	raw := m.Encode()
	if _, err := release.ParseManifest(raw); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST"), raw, 0o644); err != nil {
		return err
	}
	key := os.Getenv("PBCM_SIGNING_KEY")
	if key == "" {
		fmt.Fprintf(os.Stderr, "pbcm-sign: PBCM_SIGNING_KEY isn't set, so %s isn't signed\n", filepath.Base(dir))
		return nil
	}
	id, priv, err := release.ParsePrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "MANIFEST.sig"), release.Sign(id, priv, raw), 0o644)
}
