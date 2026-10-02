package onboard

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/release"
)

type upgradeWorld struct {
	t       *testing.T
	home    string
	exe     string
	hook    string
	server  *httptest.Server
	pub     ed25519.PublicKey
	opts    UpgradeOptions
	cliNew  []byte
	hookNew []byte
	hookOld string
}

// newUpgradeWorld serves a signed release at the given version and installs "old" programs in a temp home.
func newUpgradeWorld(t *testing.T, latest string) *upgradeWorld {
	t.Helper()
	w := &upgradeWorld{t: t, home: t.TempDir(), cliNew: []byte("new reright " + latest), hookNew: []byte("new hook " + latest)}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	w.pub = pub
	files := map[string][]byte{
		release.AssetName(cliBinary, "linux", "amd64"):  w.cliNew,
		release.AssetName(hookBinary, "linux", "amd64"): w.hookNew,
		release.VersionFile:                             []byte(latest + "\n"),
	}
	sums := map[string]string{}
	for name, b := range files {
		h := sha256.Sum256(b)
		sums[name] = hex.EncodeToString(h[:])
	}
	table := release.FormatChecksums(sums)
	files[release.ChecksumsName] = table
	files[release.SignatureName] = []byte(release.Sign(priv, table) + "\n")
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		rw.Write(b)
	}))
	t.Cleanup(w.server.Close)

	w.exe = filepath.Join(w.home, "bin", "reright")
	w.hook = filepath.Join(w.home, ".local", "bin", "reright-hook")
	w.hookOld = "#!/bin/sh\necho 'reright-hook v0.1.1 (linux/amd64)'\n"
	for p, body := range map[string]string{w.exe: "old", w.hook: w.hookOld} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w.opts = UpgradeOptions{
		Home: w.home, ReleaseURL: w.server.URL, PublicKey: pub, GOOS: "linux", GOARCH: "amd64",
		Current: "v0.1.1", ExePath: w.exe, Out: &bytes.Buffer{},
	}
	return w
}

func (w *upgradeWorld) read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *upgradeWorld) out() string { return w.opts.Out.(*bytes.Buffer).String() }

func TestUpgradeReplacesBothPrograms(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.exe) != string(w.cliNew) || w.read(w.hook) != string(w.hookNew) {
		t.Fatal("the programs were not replaced with the signed release")
	}
	info, _ := os.Stat(w.exe)
	if info.Mode()&0o111 == 0 {
		t.Error("the new reright is not executable")
	}
	if !strings.Contains(w.out(), "to reright v0.1.2 (was reright v0.1.1)") {
		t.Errorf("output %q", w.out())
	}
}

func TestUpgradeDoesNothingWhenCurrent(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.1")
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.exe) != "old" || w.read(w.hook) != w.hookOld || !strings.Contains(w.out(), "up to date") {
		t.Fatalf("an up to date install was touched: %q", w.out())
	}
	w.opts.Force = true
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.exe) != string(w.cliNew) {
		t.Fatal("--force did not replace the programs")
	}
}

func TestUpgradeCheckOnlyChangesNothing(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	w.opts.CheckOnly = true
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.exe) != "old" || !strings.Contains(w.out(), "v0.1.2 is available") {
		t.Fatalf("check changed files or said the wrong thing: %q", w.out())
	}
}

func TestUpgradeReplacesALocalBuild(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	w.opts.Current = "dev"
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.exe) != string(w.cliNew) {
		t.Fatal("a dev build should be replaced by the release")
	}
}

func TestUpgradeRefusesAnUnsignedOrTamperedRelease(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	other, _, _ := ed25519.GenerateKey(nil)
	w.opts.PublicKey = other
	err := Upgrade(context.Background(), w.opts)
	if err == nil || !strings.Contains(err.Error(), "not signed by the reright release key") {
		t.Fatalf("a release signed by another key was accepted: %v", err)
	}
	if w.read(w.exe) != "old" || w.read(w.hook) != w.hookOld {
		t.Fatal("files changed although the signature was wrong")
	}

	// A binary that does not match its signed checksum is refused too.
	w2 := newUpgradeWorld(t, "v0.1.2")
	w2.server.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "reright_linux_amd64") {
			rw.Write([]byte("tampered"))
			return
		}
		http.NotFound(rw, r)
	})
	if err := Upgrade(context.Background(), w2.opts); err == nil {
		t.Fatal("a tampered download was accepted")
	}
}

func TestUpgradeNeedsAKeyAndAVersionFile(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	w.opts.PublicKey = nil
	if err := Upgrade(context.Background(), w.opts); err == nil || !strings.Contains(err.Error(), "no release key") {
		t.Fatalf("missing key: %v", err)
	}
}

func TestUpgradeWithoutAnInstalledHookOnlyReplacesReright(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	os.Remove(w.hook)
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.exe) != string(w.cliNew) {
		t.Fatal("reright was not replaced")
	}
	if _, err := os.Stat(w.hook); err == nil {
		t.Fatal("a hook that was never installed must not appear")
	}
	if !strings.Contains(w.out(), "reright-hook is not installed") {
		t.Errorf("output %q", w.out())
	}
}

func TestHookVersionReadsTheFlag(t *testing.T) {
	dir := t.TempDir()
	newer := filepath.Join(dir, "new-hook")
	older := filepath.Join(dir, "old-hook")
	os.WriteFile(newer, []byte("#!/bin/sh\necho 'reright-hook v0.1.2 (linux/amd64)'\n"), 0o755)
	os.WriteFile(older, []byte("#!/bin/sh\necho 'reright-hook: missing subcommand' >&2\nexit 2\n"), 0o755)
	if got := hookVersion(context.Background(), newer); got != "v0.1.2" {
		t.Errorf("new hook reported %q", got)
	}
	if got := hookVersion(context.Background(), older); got != "" {
		t.Errorf("a hook without the flag reported %q", got)
	}
}

func TestUpgradeReplacesAnOlderHookEvenWhenRerightIsCurrent(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	w.opts.Current = "v0.1.2"
	w.opts.CheckOnly = true
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.out(), "reright-hook is v0.1.1") || !strings.Contains(w.out(), "Run: reright upgrade") {
		t.Fatalf("check output %q", w.out())
	}
	w.opts.CheckOnly = false
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.hook) != string(w.hookNew) {
		t.Fatal("an older hook was not replaced")
	}
}

func TestUpgradeReplacesAHookThatReportsNoVersion(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	w.opts.Current = "v0.1.2"
	os.WriteFile(w.hook, []byte("#!/bin/sh\necho 'reright-hook: missing subcommand' >&2\nexit 2\n"), 0o755)
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(w.hook) != string(w.hookNew) {
		t.Fatal("a hook from before version reporting was not replaced")
	}
}
