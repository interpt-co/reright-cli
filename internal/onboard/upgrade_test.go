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
	"os/exec"
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
	return newUpgradeWorldWithHook(t, latest, nil)
}

// newUpgradeWorldWithHook is newUpgradeWorld with the hook program the release carries. Nil means a placeholder.
func newUpgradeWorldWithHook(t *testing.T, latest string, hookNew []byte) *upgradeWorld {
	t.Helper()
	if hookNew == nil {
		hookNew = []byte("new hook " + latest)
	}
	w := &upgradeWorld{t: t, home: t.TempDir(), cliNew: []byte("new reright " + latest), hookNew: hookNew}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(w.home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
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

func TestUpgradeAddsNewClaudeHooks(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	p := Paths{w.home}
	old := `{"model": "x", "hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "` + w.hook + ` pre", "timeout": 30}]}],` +
		` "PostToolUse": [{"matcher": "mcp__reright__wait_for_review", "hooks": [{"type": "command", "command": "` + w.hook + ` post", "timeout": 10}]}],` +
		` "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "` + w.hook + ` prompt", "timeout": 10}]}]}}`
	if err := os.MkdirAll(filepath.Dir(p.Settings()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Settings(), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m := Manifest{HookPath: w.hook}
	m.record(agentClaude).Files = []FileRecord{{Path: p.Settings(), Kinds: []string{"hooks"}, Tags: []string{"hooks:" + hookMarker}, Existed: true, AfterSHA: sha([]byte(old))}}
	if err := saveManifest(p, m); err != nil {
		t.Fatal(err)
	}
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	raw := []byte(w.read(p.Settings()))
	if missing, err := MissingHooks(raw, w.hook); err != nil || len(missing) != 0 {
		t.Fatalf("missing after upgrade %v %v:\n%s", missing, err, raw)
	}
	if !strings.Contains(string(raw), `"model": "x"`) || !strings.Contains(w.out(), "Stop (stop)") {
		t.Fatalf("settings %s\noutput %q", raw, w.out())
	}
	again := w.read(p.Settings())
	w.opts.Force = true
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if w.read(p.Settings()) != again {
		t.Fatal("a second upgrade changed the settings again")
	}
}

func TestUpgradeLeavesSettingsWithoutRerightHooksAlone(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	p := Paths{w.home}
	os.MkdirAll(filepath.Dir(p.Settings()), 0o755)
	os.WriteFile(p.Settings(), []byte(`{"model": "x"}`), 0o644)
	m := Manifest{HookPath: w.hook}
	m.record(agentClaude)
	saveManifest(p, m)
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if got := w.read(p.Settings()); got != `{"model": "x"}` {
		t.Fatalf("settings rewritten: %s", got)
	}
}

func globalHooksPathIn(t *testing.T, home string) string {
	t.Helper()
	out, _ := exec.Command("git", "config", "--file", filepath.Join(home, ".gitconfig"), "--get", "core.hooksPath").Output()
	return strings.TrimSpace(string(out))
}

// The git layer goes on upgrade, also when the programs are already current, because a 0.1 reright ran the
// upgrade that brought this version and did not know to remove it.
func TestUpgradeRemovesTheGitLayerThatPointsAtOurHooks(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.1")
	w.hookOld = "#!/bin/sh\necho 'reright-hook v0.1.1 (linux/amd64)'\n"
	dir := writeOldGitLayer(t, w.home, "")
	w.opts.CheckOnly = true
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if globalHooksPathIn(t, w.home) != dir || !strings.Contains(w.out(), "reright upgrade removes them") {
		t.Fatalf("--check changed the layer or did not mention it:\n%s", w.out())
	}
	w.opts.CheckOnly = false
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if got := globalHooksPathIn(t, w.home); got != "" {
		t.Fatalf("core.hooksPath is still %q", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the wrappers are still there")
	}
	if _, err := os.Stat(filepath.Join(w.home, ".config/reright/git-install.json")); !os.IsNotExist(err) {
		t.Fatal("git-install.json is still there")
	}
	if !strings.Contains(w.out(), "removed the git hooks of an earlier version") || !strings.Contains(w.out(), "up to date") {
		t.Fatalf("output:\n%s", w.out())
	}
}

// A core.hooksPath that someone else set (husky, a dotfiles hooks dir) stays, and so do the files in it.
// The lines the user added there by hand are listed.
func TestUpgradeLeavesSomeoneElsesHooksPath(t *testing.T) {
	w := newUpgradeWorld(t, "v0.1.2")
	theirs := filepath.Join(w.home, "their-hooks")
	os.MkdirAll(theirs, 0o755)
	line := "#!/bin/sh\nif [ -x '" + w.hook + "' ]; then RERIGHT_GIT_PPID=$PPID '" + w.hook + "' git commit-msg \"$@\" || exit $?; fi\n"
	os.WriteFile(filepath.Join(theirs, "commit-msg"), []byte(line), 0o755)
	dir := writeOldGitLayer(t, w.home, theirs)
	before := w.read(filepath.Join(w.home, ".gitconfig"))
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if got := w.read(filepath.Join(w.home, ".gitconfig")); got != before || globalHooksPathIn(t, w.home) != theirs {
		t.Fatalf("their git config changed:\n%s", got)
	}
	if w.read(filepath.Join(theirs, "commit-msg")) != line {
		t.Fatal("their hook file changed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("reright's wrappers are still there")
	}
	if !strings.Contains(w.out(), filepath.Join(theirs, "commit-msg")) {
		t.Fatalf("the hand-added line is not listed:\n%s", w.out())
	}
}

// reright upgrade runs the new hook with "heal", so the hook it just installed writes its own rules text.
func TestUpgradeRunsTheNewHookHeal(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "args")
	hook := []byte("#!/bin/sh\necho \"$@\" >> '" + mark + "'\necho 'reright-hook v0.1.2 (linux/amd64)'\n")
	w := newUpgradeWorldWithHook(t, "v0.1.2", hook)
	if err := Upgrade(context.Background(), w.opts); err != nil {
		t.Fatal(err)
	}
	if got := w.read(mark); !strings.Contains(got, "heal\n") {
		t.Fatalf("the new hook was run with %q, want heal", got)
	}
}
