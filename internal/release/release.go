package release

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	DefaultBaseURL = "https://github.com/interpt-co/reright-cli/releases/latest/download"
	ChecksumsName  = "checksums.txt"
	SignatureName  = "checksums.txt.sig"
)

type Target struct{ OS, Arch string }

var Targets = []Target{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
}

func Supported(goos, goarch string) bool {
	for _, t := range Targets {
		if t.OS == goos && t.Arch == goarch {
			return true
		}
	}
	return false
}

func AssetName(binary, goos, goarch string) string {
	return binary + "_" + goos + "_" + goarch
}

func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("release: public key must be 32 bytes, base64 encoded")
	}
	return ed25519.PublicKey(b), nil
}

func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, errors.New("release: private key is not valid base64")
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	}
	return nil, errors.New("release: private key must be a 32 byte seed or a 64 byte key, base64 encoded")
}

func EncodeKey(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func Sign(priv ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

func Verify(pub ed25519.PublicKey, data []byte, sig string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("release: malformed signature")
	}
	if !ed25519.Verify(pub, data, raw) {
		return errors.New("release: signature does not match the compiled-in release key")
	}
	return nil
}

func FormatChecksums(sums map[string]string) []byte {
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return b.Bytes()
}

func ParseChecksums(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		name = strings.TrimSpace(name)
		if !ok || len(sum) != 64 || name == "" {
			return nil, fmt.Errorf("release: bad checksums line %q", line)
		}
		out[name] = strings.ToLower(sum)
	}
	return out, sc.Err()
}
