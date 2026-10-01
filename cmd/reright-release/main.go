package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/interpt-co/reright-cli/internal/release"
)

const keyEnv = "RERIGHT_RELEASE_KEY"

const usage = `usage: reright-release keygen
       reright-release pubkey
       reright-release sign DIR

pubkey and sign read the private key from $` + keyEnv + ` (base64).
sign writes checksums.txt and checksums.txt.sig for every file in DIR.`

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "reright-release:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch {
	case args[0] == "keygen" && len(args) == 1:
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "private key (keep secret, set as %s): %s\npublic key (compiled into the CLI): %s\n",
			keyEnv, release.EncodeKey(priv.Seed()), release.EncodeKey(priv.Public().(ed25519.PublicKey)))
		return nil
	case args[0] == "pubkey" && len(args) == 1:
		priv, err := privateKey(getenv)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, release.EncodeKey(priv.Public().(ed25519.PublicKey)))
		return nil
	case args[0] == "sign" && len(args) == 2:
		priv, err := privateKey(getenv)
		if err != nil {
			return err
		}
		return sign(args[1], priv)
	}
	return errors.New(usage)
}

func privateKey(getenv func(string) string) (ed25519.PrivateKey, error) {
	v := getenv(keyEnv)
	if v == "" {
		return nil, fmt.Errorf("%s is not set. The release key is kept by a person and is never stored in the repository", keyEnv)
	}
	return release.ParsePrivateKey(v)
}

func sign(dir string, priv ed25519.PrivateKey) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sums := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || e.Name() == release.ChecksumsName || e.Name() == release.SignatureName {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		sums[e.Name()] = hex.EncodeToString(h[:])
	}
	if len(sums) == 0 {
		return fmt.Errorf("no files to sign in %s", dir)
	}
	data := release.FormatChecksums(sums)
	if err := os.WriteFile(filepath.Join(dir, release.ChecksumsName), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, release.SignatureName), []byte(release.Sign(priv, data)+"\n"), 0o644)
}
