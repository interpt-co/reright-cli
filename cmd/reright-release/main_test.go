package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/release"
)

func TestKeygenPubkeySign(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"keygen"}, func(string) string { return "" }, &out); err != nil {
		t.Fatal(err)
	}
	var priv, pub string
	for _, line := range strings.Split(out.String(), "\n") {
		if _, v, ok := strings.Cut(line, "): "); ok && strings.HasPrefix(line, "private") {
			priv = v
		} else if ok {
			pub = v
		}
	}
	env := func(k string) string {
		if k == keyEnv {
			return priv
		}
		return ""
	}
	out.Reset()
	if err := run([]string{"pubkey"}, env, &out); err != nil || strings.TrimSpace(out.String()) != pub {
		t.Fatalf("pubkey %q want %q: %v", out.String(), pub, err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "reright-hook_linux_amd64"), []byte("binary"), 0o644)
	if err := run([]string{"sign", dir}, env, &out); err != nil {
		t.Fatal(err)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, release.ChecksumsName))
	sig, _ := os.ReadFile(filepath.Join(dir, release.SignatureName))
	pk, _ := release.ParsePublicKey(pub)
	if err := release.Verify(pk, sums, string(sig)); err != nil {
		t.Fatal(err)
	}
	table, _ := release.ParseChecksums(sums)
	if len(table) != 1 || table["reright-hook_linux_amd64"] == "" {
		t.Fatalf("checksums: %s", sums)
	}
	if err := run([]string{"sign", dir}, env, &out); err != nil {
		t.Fatalf("second sign should skip its own output files: %v", err)
	}
	sums2, _ := os.ReadFile(filepath.Join(dir, release.ChecksumsName))
	if !bytes.Equal(sums, sums2) {
		t.Fatal("re-signing changed the checksums")
	}
}

func TestSignNeedsKey(t *testing.T) {
	err := run([]string{"sign", t.TempDir()}, func(string) string { return "" }, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), keyEnv) {
		t.Fatalf("got %v", err)
	}
	if err := run([]string{"bogus"}, func(string) string { return "" }, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown command accepted")
	}
}
