package githook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary stands in for reright-hook: the 0.1 wrappers call it as "BIN git commit-msg FILE".
func TestMain(m *testing.M) {
	if os.Getenv("GITHOOK_TEST_BINARY") == "1" {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, Env{Getenv: os.Getenv}))
	}
	os.Exit(m.Run())
}

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// setupHome gives each test its own home and global git config, so the real ones are never read or written.
func setupHome(t *testing.T) (home, gitconfig string) {
	t.Helper()
	gitAvailable(t)
	home = t.TempDir()
	gitconfig = filepath.Join(home, "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", home)
	return home, gitconfig
}

type repo struct {
	t    *testing.T
	home string
	dir  string
	env  []string
}

func newRepo(t *testing.T, home string) *repo {
	t.Helper()
	r := &repo{t: t, home: home, dir: filepath.Join(home, "work")}
	os.MkdirAll(r.dir, 0o755)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "HOME", "XDG_CONFIG_HOME", "GIT_DIR", "GIT_EDITOR", "GIT_INDEX_FILE", "GIT_WORK_TREE", "RERIGHT_GIT_PPID", passEnv:
			continue
		}
		r.env = append(r.env, kv)
	}
	r.env = append(r.env, "HOME="+home, "GITHOOK_TEST_BINARY=1", "GIT_EDITOR=true")
	r.mustGit("init", "-q", "-b", "main")
	r.mustGit("config", "user.email", "a@example.com")
	r.mustGit("config", "user.name", "A")
	return r
}

func (r *repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *repo) mustGit(args ...string) string {
	r.t.Helper()
	out, err := r.git(args...)
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func (r *repo) commit(msg string) (string, error) {
	r.t.Helper()
	p := filepath.Join(r.dir, "f.txt")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append(b, 'x'), 0o644)
	r.mustGit("add", "f.txt")
	return r.git("commit", "-q", "-m", msg)
}

// repoHook writes a hook into the repository that logs each run and exits with code.
func (r *repo) repoHook(dir, name string, code string) string {
	r.t.Helper()
	log := filepath.Join(r.home, filepath.Base(dir)+"-"+name+".log")
	os.MkdirAll(dir, 0o755)
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return log
}

func runs(log string) int {
	b, _ := os.ReadFile(log)
	return strings.Count(string(b), "\n")
}

func hooksPath(t *testing.T) string {
	t.Helper()
	out, _ := exec.Command("git", "config", "--global", "--get", "core.hooksPath").Output()
	return strings.TrimSpace(string(out))
}

func testBinary(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// A machine still has the 0.1 wrappers and a new reright-hook: commits keep working, and the repository's
// own hooks run exactly once, including a failing one that must still stop the commit.
func TestOldWrapperWithNewHookKeepsCommitsWorking(t *testing.T) {
	home, _ := setupHome(t)
	if _, err := legacyInstall(home, testBinary(t)); err != nil {
		t.Fatal(err)
	}
	r := newRepo(t, home)
	hooks := filepath.Join(r.dir, ".git", "hooks")
	msgLog := r.repoHook(hooks, "commit-msg", "0")
	preLog := r.repoHook(hooks, "pre-commit", "0")
	if out, err := r.commit("No approval needed now"); err != nil {
		t.Fatalf("the commit failed through the old wrapper: %v %s", err, out)
	}
	if runs(msgLog) != 1 || runs(preLog) != 1 {
		t.Fatalf("repo hooks ran %d (commit-msg) and %d (pre-commit) times, want 1 each", runs(msgLog), runs(preLog))
	}
	r.repoHook(hooks, "commit-msg", "1")
	if _, err := r.commit("Blocked by the repo hook"); err == nil {
		t.Fatal("a failing repo commit-msg hook no longer stops the commit")
	}
}

func TestPassThroughRunsTheRepoHook(t *testing.T) {
	home, _ := setupHome(t)
	r := newRepo(t, home)
	env := Env{Getenv: func(k string) string { return "" }, Cwd: r.dir, Home: home}
	var out, errOut bytes.Buffer
	if code := Main([]string{"git", "commit-msg", ".git/COMMIT_EDITMSG"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("no repo hook: exit %d (%s)", code, errOut.String())
	}
	log := r.repoHook(filepath.Join(r.dir, ".git", "hooks"), "commit-msg", "3")
	if code := Main([]string{"git", "commit-msg", "MSG"}, nil, &out, &errOut, env); code != 3 {
		t.Fatalf("the repo hook's exit code was not passed on: %d", code)
	}
	if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != "MSG" {
		t.Fatalf("the repo hook did not get the arguments: %q", b)
	}

	husky := r.repoHook(filepath.Join(r.dir, ".husky"), "commit-msg", "0")
	r.mustGit("config", "core.hooksPath", ".husky")
	if code := Main([]string{"commit-msg", "MSG"}, nil, &out, &errOut, env); code != 0 || runs(husky) != 1 {
		t.Fatalf("the local core.hooksPath was not used: exit %d, runs %d", code, runs(husky))
	}

	// The wrappers run the repo hook themselves after calling reright-hook, so it must not run twice.
	before := runs(husky)
	wrapped := env
	wrapped.Getenv = func(k string) string {
		if k == "RERIGHT_GIT_PPID" {
			return "123"
		}
		return ""
	}
	if code := Main([]string{"git", "commit-msg", "MSG"}, nil, &out, &errOut, wrapped); code != 0 || runs(husky) != before {
		t.Fatalf("called from a wrapper: exit %d, the repo hook ran %d more times", code, runs(husky)-before)
	}
	for _, args := range [][]string{nil, {"git"}, {"git", "../x"}} {
		if code := Main(args, nil, &out, &errOut, env); code != 0 {
			t.Fatalf("%v: usage error exits %d; it must never fail a commit", args, code)
		}
	}
}

func TestPassThroughNeverRunsAWrapper(t *testing.T) {
	home, _ := setupHome(t)
	if _, err := legacyInstall(home, testBinary(t)); err != nil {
		t.Fatal(err)
	}
	r := newRepo(t, home)
	r.mustGit("config", "core.hooksPath", filepath.Join(home, ".config", "reright", "git-hooks"))
	var out, errOut bytes.Buffer
	env := Env{Getenv: func(string) string { return "" }, Cwd: r.dir, Home: home}
	if code := Main([]string{"git", "commit-msg", "MSG"}, nil, &out, &errOut, env); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// The usual case: the layer set the global core.hooksPath. Removing it unsets that, deletes the wrappers and
// the state file, and the repository's hooks then run natively.
func TestRemoveUnsetsOurHooksPath(t *testing.T) {
	home, gitconfig := setupHome(t)
	if _, err := legacyInstall(home, testBinary(t)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".config", "reright", "git.json"), []byte(`{"mode":"always"}`), 0o600)
	if info, err := Status(home); err != nil || !info.Active || !info.Installed || !Present(home) {
		t.Fatalf("before: %+v %v", info, err)
	}
	notes, err := Remove(home)
	if err != nil {
		t.Fatal(err)
	}
	if hooksPath(t) != "" {
		t.Fatalf("core.hooksPath still set to %s", hooksPath(t))
	}
	if _, err := os.Stat(gitconfig); !os.IsNotExist(err) {
		t.Fatalf("the git config reright created is still there: %v", err)
	}
	for _, p := range []string{filepath.Join(home, ".config", "reright", "git-hooks"), filepath.Join(home, ".config", "reright", "git-install.json"), filepath.Join(home, ".config", "reright", "git.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s left behind", p)
		}
	}
	if info, err := Status(home); err != nil || info.Active || info.Installed || Present(home) {
		t.Fatalf("after: %+v %v", info, err)
	}
	if len(notes) == 0 {
		t.Fatal("no note says what was done")
	}
	r := newRepo(t, home)
	log := r.repoHook(filepath.Join(r.dir, ".git", "hooks"), "commit-msg", "0")
	if out, err := r.commit("Native"); err != nil || runs(log) != 1 {
		t.Fatalf("after removal: %v %s, runs %d", err, out, runs(log))
	}
}

func TestRemoveRestoresTheEarlierConfigByteForByte(t *testing.T) {
	home, gitconfig := setupHome(t)
	before := []byte("[user]\n\tname = Me\n# my comment\n")
	os.WriteFile(gitconfig, before, 0o644)
	if _, err := legacyInstall(home, "/x/reright-hook"); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(home); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(gitconfig); !bytes.Equal(after, before) {
		t.Fatalf("config not restored:\n%s", after)
	}
}

func TestRemoveAfterTheUserEditedTheConfigOnlyUnsetsOurKey(t *testing.T) {
	home, gitconfig := setupHome(t)
	if _, err := legacyInstall(home, "/x/reright-hook"); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "config", "--global", "user.name", "Later").Run()
	if _, err := Remove(home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(gitconfig)
	if strings.Contains(string(b), "hooksPath") || !strings.Contains(string(b), "Later") {
		t.Fatalf("%s", b)
	}
}

// Someone else's core.hooksPath (husky, a dotfiles hooks dir) was never changed by reright, and removal leaves
// it and its files alone. It lists the lines the user added by hand.
func TestRemoveLeavesSomeoneElsesHooksPath(t *testing.T) {
	home, gitconfig := setupHome(t)
	theirs := filepath.Join(home, "their-hooks")
	os.MkdirAll(theirs, 0o755)
	cfg := []byte("[core]\n\thooksPath = " + theirs + "\n")
	os.WriteFile(gitconfig, cfg, 0o644)
	res, err := legacyInstall(home, "/x/reright-hook")
	if err != nil || res.ManualDir != theirs {
		t.Fatalf("%+v %v", res, err)
	}
	line := []byte("#!/bin/sh\nif [ -x '/x/reright-hook' ]; then RERIGHT_GIT_PPID=$PPID '/x/reright-hook' git commit-msg \"$@\" || exit $?; fi\n")
	os.WriteFile(filepath.Join(theirs, "commit-msg"), line, 0o755)
	if info, _ := Status(home); info.Active {
		t.Fatal("someone else's hooks path counts as the layer")
	}
	notes, err := Remove(home)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(gitconfig); !bytes.Equal(b, cfg) {
		t.Fatalf("their git config changed:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(theirs, "commit-msg")); !bytes.Equal(b, line) {
		t.Fatal("their hook file changed")
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, filepath.Join(theirs, "commit-msg")) || strings.Contains(joined, "prepare-commit-msg") {
		t.Fatalf("notes: %s", joined)
	}
	if Present(home) {
		t.Fatal("the wrappers or the state file are left")
	}
}

func TestRemoveWithoutALayerIsANoOp(t *testing.T) {
	home, gitconfig := setupHome(t)
	os.WriteFile(gitconfig, []byte("[user]\n\tname = Me\n"), 0o644)
	if Present(home) {
		t.Fatal("a fresh home has a layer")
	}
	notes, err := Remove(home)
	if err != nil || len(notes) != 0 {
		t.Fatalf("%v %v", notes, err)
	}
	if b, _ := os.ReadFile(gitconfig); string(b) != "[user]\n\tname = Me\n" {
		t.Fatalf("%s", b)
	}
}

func TestRemoveRecoversFromADamagedStateFile(t *testing.T) {
	home, gitconfig := setupHome(t)
	os.WriteFile(gitconfig, []byte("[user]\n\tname = Me\n"), 0o644)
	if _, err := legacyInstall(home, "/x/reright-hook"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".config", "reright", "git-install.json"), []byte("{oops"), 0o600)
	if _, err := Remove(home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(gitconfig)
	if strings.Contains(string(b), "hooksPath") || !strings.Contains(string(b), "Me") {
		t.Fatalf("%s", b)
	}
	if Present(home) {
		t.Fatal("the layer is still there")
	}
}
