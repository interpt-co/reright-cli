package githook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func logHook(r *repo, dir, name string) string {
	log := filepath.Join(r.home, name+".log")
	script := "#!/bin/sh\n{ echo \"" + name + " $*\"; cat; } >> " + log + "\n"
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755)
	return log
}

func readLog(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func TestOtherRepoHooksStillFireAndAreRemovedWithUninstall(t *testing.T) {
	r := newRepo(t)
	hooks := filepath.Join(r.dir, ".git", "hooks")
	logs := map[string]string{}
	for _, n := range []string{"pre-commit", "post-commit", "post-checkout", "pre-push", "post-merge", "reference-transaction"} {
		logs[n] = logHook(r, hooks, n)
	}
	remote := filepath.Join(r.home, "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r.mustGit("remote", "add", "origin", remote)
	r.stage("a", "1")
	r.approve("First")
	r.mustGit("commit", "-m", "First")
	r.mustGit("checkout", "-q", "-b", "topic")
	r.mustGit("push", "-q", "origin", "topic")

	for _, n := range []string{"pre-commit", "post-commit", "post-checkout", "pre-push"} {
		if got := readLog(logs[n]); !strings.Contains(got, n) {
			t.Errorf("%s did not run through the wrapper: %q", n, got)
		}
	}
	if got := readLog(logs["pre-push"]); !strings.Contains(got, "origin") || !strings.Contains(got, "refs/heads/topic") {
		t.Errorf("pre-push lost its arguments or stdin: %q", got)
	}
	if !strings.Contains(readLog(logs["reference-transaction"]), "prepared") {
		t.Errorf("reference-transaction did not get its argument and stdin: %q", readLog(logs["reference-transaction"]))
	}

	if err := Uninstall(r.home); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("git", "config", "--file", filepath.Join(r.home, "gitconfig"), "--get", "core.hooksPath").Output(); len(out) != 0 {
		t.Fatalf("hooksPath still set: %s", out)
	}
	if _, err := os.Stat(filepath.Join(r.home, ".config", "reright", "git-hooks")); !os.IsNotExist(err) {
		t.Fatal("wrappers left behind")
	}
	before := len(readLog(logs["pre-commit"]))
	r.stage("b", "2")
	r.mustGit("commit", "-m", "Second, no reright now")
	if len(readLog(logs["pre-commit"])) == before {
		t.Fatal("the repo hook no longer runs natively after uninstall")
	}
}

func TestChainedHookFailureStillBlocks(t *testing.T) {
	r := newRepo(t)
	chainHook(r, filepath.Join(r.dir, ".git", "hooks", "pre-commit"), "pc.log", 1)
	r.stage("a", "1")
	r.approve("Blocked")
	if _, err := r.git("commit", "-m", "Blocked"); err == nil {
		t.Fatal("a failing pre-commit hook no longer blocks the commit")
	}
}

func breakBinary(t *testing.T, r *repo) string {
	t.Helper()
	bin := filepath.Join(r.home, "broken-hook")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho started >> "+filepath.Join(r.home, "started.log")+"\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(r.home, bin); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestMissingBinaryDoesNotBreakCommitsAndStillChains(t *testing.T) {
	r := newRepo(t)
	bin := breakBinary(t, r)
	os.Remove(bin)
	chain := logHook(r, filepath.Join(r.dir, ".git", "hooks"), "commit-msg")
	r.stage("a", "1")
	out, err := r.git("commit", "-m", "No binary")
	if err != nil {
		t.Fatalf("a missing binary broke the commit: %v %s", err, out)
	}
	if !strings.Contains(out, "missing or not executable") {
		t.Errorf("no warning: %s", out)
	}
	if !strings.Contains(readLog(chain), "commit-msg") {
		t.Errorf("the repo hook was not chained: %q", readLog(chain))
	}
}

func TestBypassAndModeOffDoNotStartTheBinary(t *testing.T) {
	r := newRepo(t)
	breakBinary(t, r)
	started := filepath.Join(r.home, "started.log")
	r.stage("a", "1")
	r.extraEnv = []string{"CLAUDECODE=1", "RERIGHT_BYPASS=1"}
	r.mustGit("commit", "-m", "Bypass")
	if _, err := os.Stat(started); err == nil {
		t.Fatal("the binary was started under RERIGHT_BYPASS=1")
	}
	r.extraEnv = nil
	os.WriteFile(filepath.Join(r.home, ".config", "reright", "git.json"), []byte("{\n  \"mode\": \"off\"\n}\n"), 0o644)
	r.stage("b", "2")
	r.mustGit("commit", "-m", "Off")
	if _, err := os.Stat(started); err == nil {
		t.Fatal("the binary was started with mode off")
	}
	os.Remove(filepath.Join(r.home, ".config", "reright", "git.json"))
	r.stage("c", "3")
	if _, err := r.git("commit", "-m", "On"); err == nil {
		t.Fatal("a binary that fails should still block when checks are on")
	}
}

func TestCorruptStateIsAnInstallErrorAndUninstallRecovers(t *testing.T) {
	home, gitconfig := setupHome(t)
	if _, err := Install(home, "/x/hook"); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, ".config", "reright", "git-install.json")
	os.WriteFile(state, []byte("{oops"), 0o600)
	if _, err := Install(home, "/x/hook"); err == nil || !strings.Contains(err.Error(), "uninstall") {
		t.Fatalf("a corrupt state file must be an install error: %v", err)
	}
	notes, err := UninstallNotes(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Error("recovery said nothing")
	}
	if b, _ := os.ReadFile(gitconfig); strings.Contains(string(b), "hooksPath") {
		t.Fatalf("core.hooksPath is orphaned:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "reright", "git-hooks")); !os.IsNotExist(err) {
		t.Fatal("wrappers left behind")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("corrupt state file left behind")
	}
}

func TestLostStateStillUnsetsOurHooksPath(t *testing.T) {
	home, gitconfig := setupHome(t)
	os.WriteFile(gitconfig, []byte("[user]\n\tname = Me\n"), 0o644)
	if _, err := Install(home, "/x/hook"); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(home, ".config", "reright", "git-install.json"))
	if err := Uninstall(home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(gitconfig)
	if strings.Contains(string(b), "hooksPath") || !strings.Contains(string(b), "Me") {
		t.Fatalf("%s", b)
	}
}

func TestUninstallListsManualLinesTheUserAdded(t *testing.T) {
	home, gitconfig := setupHome(t)
	theirs := filepath.Join(home, "their-hooks")
	os.MkdirAll(theirs, 0o755)
	os.WriteFile(gitconfig, []byte("[core]\n\thooksPath = "+theirs+"\n"), 0o644)
	res, err := Install(home, "/x/hook")
	if err != nil || res.ManualDir != theirs {
		t.Fatalf("%+v %v", res, err)
	}
	os.WriteFile(filepath.Join(theirs, "commit-msg"), []byte("#!/bin/sh\n'/x/hook' git commit-msg \"$@\" || exit $?\n"), 0o755)
	notes, err := UninstallNotes(home)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, filepath.Join(theirs, "commit-msg")) || strings.Contains(joined, "prepare-commit-msg") {
		t.Fatalf("notes: %s", joined)
	}
}

func TestStateFileAndConfigDirArePrivate(t *testing.T) {
	home, _ := setupHome(t)
	if _, err := Install(home, "/x/hook"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(home, ".config", "reright"):                     0o700,
		filepath.Join(home, ".config", "reright", "git-install.json"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %v", path, info, err, want)
		}
	}
}

func TestRepoHooksPath(t *testing.T) {
	r := newRepo(t)
	if got := RepoHooksPath(r.dir); got != "" {
		t.Fatalf("got %q", got)
	}
	r.mustGit("config", "core.hooksPath", ".husky/_")
	if got := RepoHooksPath(r.dir); got != ".husky/_" {
		t.Fatalf("got %q", got)
	}
	if got := RepoHooksPath(t.TempDir()); got != "" {
		t.Fatalf("outside a repo: %q", got)
	}
}
