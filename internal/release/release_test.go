package release

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	data := []byte("abc\n")
	sig := Sign(priv, data)
	if err := Verify(pub, data, sig); err != nil {
		t.Fatal(err)
	}
	if Verify(pub, []byte("abd\n"), sig) == nil {
		t.Fatal("tampered data verified")
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if Verify(other, data, sig) == nil {
		t.Fatal("wrong key verified")
	}
	if Verify(pub, data, "not base64!") == nil || Verify(pub, data, "AAAA") == nil {
		t.Fatal("malformed signature verified")
	}
}

func TestKeyParsing(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	got, err := ParsePrivateKey(EncodeKey(priv.Seed()))
	if err != nil || !got.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatalf("seed: %v", err)
	}
	got, err = ParsePrivateKey(EncodeKey(priv))
	if err != nil || !got.Equal(priv) {
		t.Fatalf("full key: %v", err)
	}
	if _, err := ParsePrivateKey(EncodeKey([]byte("short"))); err == nil {
		t.Fatal("short private key accepted")
	}
	if p, err := ParsePublicKey(EncodeKey(pub)); err != nil || !p.Equal(pub) {
		t.Fatalf("public: %v", err)
	}
	if _, err := ParsePublicKey(""); err == nil {
		t.Fatal("empty public key accepted")
	}
}

func TestChecksumsRoundTrip(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	data := FormatChecksums(map[string]string{"z_file": b, "a_file": a})
	if string(data) != a+"  a_file\n"+b+"  z_file\n" {
		t.Fatalf("format: %q", data)
	}
	got, err := ParseChecksums(data)
	if err != nil || got["a_file"] != a || got["z_file"] != b {
		t.Fatalf("parse: %v %v", got, err)
	}
	if _, err := ParseChecksums([]byte("short  x\n")); err == nil {
		t.Fatal("bad line accepted")
	}
}

func TestSupportedAndAssetName(t *testing.T) {
	if !Supported("darwin", "arm64") || Supported("windows", "amd64") {
		t.Fatal("supported targets wrong")
	}
	if AssetName("reright-hook", "linux", "amd64") != "reright-hook_linux_amd64" {
		t.Fatal("asset name")
	}
}
