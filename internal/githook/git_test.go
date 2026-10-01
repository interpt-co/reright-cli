package githook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/draft"
)

type repo struct {
	t        *testing.T
	home     string
	dir      string
	approved string
	extraEnv []string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	gitAvailable(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	r := &repo{t: t, home: home, dir: filepath.Join(home, "work"), approved: filepath.Join(home, "approved")}
	os.MkdirAll(r.dir, 0o755)
	os.WriteFile(r.approved, nil, 0o644)
	if _, err := Install(home, bin); err != nil {
		t.Fatal(err)
	}
	r.mustGit("init", "-q", "-b", "main")
	r.mustGit("config", "user.email", "a@example.com")
	r.mustGit("config", "user.name", "A")
	return r
}

func (r *repo) env() []string {
	var env []string
	drop := map[string]bool{"HOME": true, "XDG_CONFIG_HOME": true, "GIT_DIR": true, "GIT_EDITOR": true, "GIT_INDEX_FILE": true, "GIT_WORK_TREE": true}
	for _, m := range AgentMarkers {
		drop[m.Var] = true
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+r.home, "GIT_CONFIG_NOSYSTEM=1", "GITHOOK_TEST_BINARY=1", "GITHOOK_TEST_APPROVED="+r.approved, "GIT_EDITOR=true")
	return append(env, r.extraEnv...)
}

func (r *repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.env()
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

func (r *repo) approve(text string) {
	f, err := os.OpenFile(r.approved, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(draft.Hash(text) + "\n")
}

func (r *repo) write(name, content string) {
	p := filepath.Join(r.dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) stage(name, content string) {
	r.write(name, content)
	r.mustGit("add", name)
}

func (r *repo) subject() string {
	out, _ := r.git("log", "-1", "--format=%s")
	return strings.TrimSpace(out)
}

func (r *repo) seed() {
	r.approve("seed")
	r.stage("seed.txt", "seed")
	r.mustGit("commit", "-q", "-m", "seed")
}

func (r *repo) setupCommit(msg string) {
	saved := r.extraEnv
	r.extraEnv = append([]string{"RERIGHT_BYPASS=1"}, saved...)
	r.mustGit("commit", "-q", "-m", msg)
	r.extraEnv = saved
}

func (r *repo) editor(content string) string {
	p := filepath.Join(r.home, "editor.sh")
	os.WriteFile(p, []byte("#!/bin/sh\ncat > \"$1\" <<'EOF'\n"+content+"EOF\n"), 0o755)
	return p
}

func TestGitDashM(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	out, err := r.git("commit", "-m", "Add a")
	if err == nil {
		t.Fatal("unapproved commit went through")
	}
	if !strings.Contains(out, "submit_for_review") {
		t.Fatalf("no instructions in output: %s", out)
	}
	if _, err := r.git("rev-parse", "--verify", "HEAD"); err == nil {
		t.Fatal("a commit exists")
	}
	r.approve("Add a")
	r.mustGit("commit", "-m", "Add a")
	if r.subject() != "Add a" {
		t.Fatalf("subject %q", r.subject())
	}
}

func TestGitDashF(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	msg := filepath.Join(r.home, "msg.txt")
	os.WriteFile(msg, []byte("Add a\n\nWhy it is needed.\n"), 0o644)
	if _, err := r.git("commit", "-F", msg); err == nil {
		t.Fatal("unapproved -F commit went through")
	}
	r.approve("Add a\n\nWhy it is needed.")
	r.mustGit("commit", "-F", msg)
}

func TestGitEditorFlowStripsComments(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	r.extraEnv = []string{"GIT_EDITOR=" + r.editor("Add a\n\nBody line.\n")}
	if _, err := r.git("commit"); err == nil {
		t.Fatal("unapproved editor commit went through")
	}
	r.approve("Add a\n\nBody line.")
	r.mustGit("commit")
	if r.subject() != "Add a" {
		t.Fatalf("subject %q", r.subject())
	}
}

func TestGitAmend(t *testing.T) {
	r := newRepo(t)
	r.seed()
	r.approved = filepath.Join(r.home, "none")
	os.WriteFile(r.approved, nil, 0o644)
	r.mustGit("commit", "--amend", "--no-edit")
	if _, err := r.git("commit", "--amend", "-m", "Different words"); err == nil {
		t.Fatal("amend with a new unapproved message went through")
	}
	r.approve("Different words")
	r.mustGit("commit", "--amend", "-m", "Different words")
	if r.subject() != "Different words" {
		t.Fatalf("subject %q", r.subject())
	}
}

func TestGitSameMessageAsHeadIsNotAnAmend(t *testing.T) {
	r := newRepo(t)
	r.seed()
	r.approved = filepath.Join(r.home, "none")
	os.WriteFile(r.approved, nil, 0o644)
	r.stage("b", "2")
	if _, err := r.git("commit", "-m", "seed"); err == nil {
		t.Fatal("a new commit reusing the previous message skipped the check")
	}
}

func TestGitMergeRebaseCherryPickRevertAreSkipped(t *testing.T) {
	r := newRepo(t)
	r.seed()
	r.approved = filepath.Join(r.home, "none")
	os.WriteFile(r.approved, nil, 0o644)

	r.mustGit("checkout", "-q", "-b", "topic")
	r.stage("t", "topic")
	r.setupCommit("topic work")
	r.mustGit("checkout", "-q", "main")
	r.mustGit("checkout", "-q", "-b", "pick")
	r.stage("p", "pick")
	r.setupCommit("pick work")
	r.mustGit("checkout", "-q", "main")
	r.stage("m", "main")
	r.setupCommit("main work")

	r.mustGit("merge", "--no-ff", "-m", "Merge topic", "topic")
	if r.subject() != "Merge topic" {
		t.Fatalf("merge subject %q", r.subject())
	}

	r.mustGit("cherry-pick", "pick")
	r.mustGit("revert", "--no-edit", "HEAD")

	r.mustGit("checkout", "-q", "-b", "conflict", "HEAD~3")
	r.stage("c", "one")
	r.setupCommit("c one")
	r.mustGit("checkout", "-q", "main")
	r.stage("c", "two")
	r.setupCommit("c two")
	r.mustGit("checkout", "-q", "conflict")
	if _, err := r.git("rebase", "main"); err == nil {
		t.Fatal("expected a rebase conflict")
	}
	r.write("c", "resolved")
	r.mustGit("add", "c")
	r.extraEnv = []string{"GIT_EDITOR=true"}
	r.mustGit("rebase", "--continue")
}

func TestGitNoVerify(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	if _, err := r.git("commit", "--no-verify", "-m", "Sneaky"); err == nil {
		t.Fatal("prepare-commit-msg should catch --no-verify with -m")
	}
	r.extraEnv = []string{"GIT_EDITOR=" + r.editor("Sneaky\n")}
	r.mustGit("commit", "--no-verify")
	if r.subject() != "Sneaky" {
		t.Fatalf("expected the documented editor gap, subject %q", r.subject())
	}
}

func TestGitPlumbingIsNotCovered(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	tree := strings.TrimSpace(r.mustGit("write-tree"))
	r.mustGit("commit-tree", "-m", "Plumbing", tree)
}

func TestGitEmptyMessage(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	r.mustGit("commit", "--allow-empty-message", "-m", "")
	if r.subject() != "" {
		t.Fatalf("subject %q", r.subject())
	}
}

func TestGitHumanIsNotBlocked(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	r.extraEnv = []string{"GITHOOK_TEST_TTY=1"}
	r.mustGit("commit", "-m", "Human words")
	r.stage("b", "2")
	r.extraEnv = []string{"GITHOOK_TEST_TTY=1", "CLAUDECODE=1"}
	if _, err := r.git("commit", "-m", "Agent words"); err == nil {
		t.Fatal("agent marker on a terminal was not enforced")
	}
	r.extraEnv = []string{"GITHOOK_TEST_TTY=1", "RERIGHT_ENFORCE=1"}
	if _, err := r.git("commit", "-m", "Agent words"); err == nil {
		t.Fatal("RERIGHT_ENFORCE was not enforced")
	}
	r.extraEnv = []string{"CLAUDECODE=1", "RERIGHT_BYPASS=1"}
	r.mustGit("commit", "-m", "Bypassed")
}

func TestGitConfigModes(t *testing.T) {
	r := newRepo(t)
	cfgDir := filepath.Join(r.home, ".config", "reright")
	r.stage("a", "1")
	os.WriteFile(filepath.Join(cfgDir, "git.json"), []byte(`{"mode":"off"}`), 0o644)
	r.mustGit("commit", "-m", "Off mode")
	r.stage("b", "2")
	os.WriteFile(filepath.Join(cfgDir, "git.json"), []byte(`{"mode":"always"}`), 0o644)
	r.extraEnv = []string{"GITHOOK_TEST_TTY=1"}
	if _, err := r.git("commit", "-m", "Always mode"); err == nil {
		t.Fatal("always mode did not enforce on a terminal")
	}
}

func TestGitServerDown(t *testing.T) {
	r := newRepo(t)
	r.stage("a", "1")
	r.extraEnv = []string{"GITHOOK_TEST_DOWN=1", "CLAUDECODE=1"}
	out, err := r.git("commit", "-m", "Offline")
	if err == nil || !strings.Contains(out, "RERIGHT_OFFLINE=allow") {
		t.Fatalf("fail closed expected: %v %s", err, out)
	}
	r.extraEnv = []string{"GITHOOK_TEST_DOWN=1", "CLAUDECODE=1", "RERIGHT_OFFLINE=allow"}
	r.mustGit("commit", "-m", "Offline")
}

func chainHook(r *repo, path, logName string, exit int) {
	script := "#!/bin/sh\necho \"$0 $*\" >> " + filepath.Join(r.home, logName) + "\nexit " + itoa(exit) + "\n"
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(script), 0o755)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

func TestGitChainsToRepoHook(t *testing.T) {
	r := newRepo(t)
	chainHook(r, filepath.Join(r.dir, ".git", "hooks", "commit-msg"), "chain.log", 0)
	r.stage("a", "1")
	if _, err := r.git("commit", "-m", "Chained"); err == nil {
		t.Fatal("unapproved commit went through")
	}
	if b, _ := os.ReadFile(filepath.Join(r.home, "chain.log")); len(b) != 0 {
		t.Fatalf("repo hook ran before approval: %s", b)
	}
	r.approve("Chained")
	r.mustGit("commit", "-m", "Chained")
	b, _ := os.ReadFile(filepath.Join(r.home, "chain.log"))
	if !strings.Contains(string(b), ".git/hooks/commit-msg") || !strings.Contains(string(b), "COMMIT_EDITMSG") {
		t.Fatalf("repo hook not chained with the message file: %q", b)
	}
}

func TestGitChainsToLocalHooksPathAndHonoursItsFailure(t *testing.T) {
	r := newRepo(t)
	chainHook(r, filepath.Join(r.dir, ".husky", "commit-msg"), "husky.log", 1)
	r.mustGit("config", "core.hooksPath", ".husky")
	r.stage("a", "1")
	r.approve("Husky")
	if _, err := r.git("commit", "-m", "Husky"); err == nil {
		t.Fatal("the chained hook failed but the commit went through")
	}
	if b, _ := os.ReadFile(filepath.Join(r.home, "husky.log")); !strings.Contains(string(b), "commit-msg") {
		t.Fatalf("husky hook did not run: %q", b)
	}
}

func TestGitLocalHooksPathWithoutHookDoesNotBreakCommit(t *testing.T) {
	r := newRepo(t)
	r.mustGit("config", "core.hooksPath", ".husky/_")
	r.stage("a", "1")
	r.approve("Plain")
	r.mustGit("commit", "-m", "Plain")
}
