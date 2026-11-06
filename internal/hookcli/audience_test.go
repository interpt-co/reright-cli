package hookcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/interpt-co/reright-cli/internal/audience"
	"github.com/interpt-co/reright-cli/internal/draft"
)

func isolateGit(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	global := filepath.Join(home, ".gitconfig")
	cfg := "[user]\n\tname = Me\n\temail = me@example.com\n[commit]\n\tgpgsign = false\n[core]\n\thooksPath = /dev/null\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(global, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repo makes a repository with one commit by author (empty means the user) and the given remote.
func repo(t *testing.T, author, remote string) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	args := []string{"commit", "-q", "--allow-empty", "-m", "start"}
	if author != "" {
		args = append(args, "--author", "Someone <"+author+">")
	}
	gitIn(t, dir, args...)
	if remote != "" {
		gitIn(t, dir, "remote", "add", "origin", remote)
	}
	return dir
}

func withAudience(f *fixture, public func(context.Context, audience.Remote) (bool, error)) {
	f.env.Audience = audience.Detector{Public: public}.Decide
}

func TestCommitInTheUsersOwnRepoPassesWithoutAServerCall(t *testing.T) {
	isolateGit(t)
	f := newFixture(t)
	withAudience(f, nil)
	dir := repo(t, "colleague@example.com", "") // no remote: nobody else reads it
	for _, cmd := range []string{`git commit -m "wip on the parser"`, `git tag -a v1 -m "first cut"`, `git commit`} {
		if d, reason := f.run(t, "pre", inDir(bash("s1", cmd), dir)); d != "" {
			t.Fatalf("%s: decision %q (%s)", cmd, d, reason)
		}
	}
	if f.checker.calls != 0 {
		t.Fatal("asked the server about a commit in the user's own repository")
	}
}

func TestCommitInASharedRepoIsCheckedAndSaysWhy(t *testing.T) {
	isolateGit(t)
	f := newFixture(t)
	withAudience(f, nil)
	dir := repo(t, "colleague@example.com", "git@github.com:team/app.git")
	d, reason := f.run(t, "pre", inDir(bash("s1", `git commit -m "Fix the parser"`), dir))
	if d != "deny" || !strings.Contains(reason, "colleague@example.com") || !strings.Contains(reason, "git config reright.commits never") {
		t.Fatalf("decision %q (%s)", d, reason)
	}
	f.checker.approved[draft.Hash("Fix the parser")] = true
	if d, reason := f.run(t, "pre", inDir(bash("s1", `git commit -m "Fix the parser"`), dir)); d != "" {
		t.Fatalf("approved text blocked: %s", reason)
	}
	// the user can say a repository is their own
	gitIn(t, dir, "config", "reright.commits", "never")
	if d, reason := f.run(t, "pre", inDir(bash("s1", `git commit -m "Something else"`), dir)); d != "" {
		t.Fatalf("decision %q (%s)", d, reason)
	}
}

// The public client repository has only Paulo's commits, and its release commits must stay checked.
func TestPublicRepoWithOnlyTheUsersCommitsIsChecked(t *testing.T) {
	isolateGit(t)
	f := newFixture(t)
	withAudience(f, func(_ context.Context, r audience.Remote) (bool, error) {
		return r.Host == "github.com" && r.Path == "interpt-co/reright-cli", nil
	})
	dir := repo(t, "", "git@github.com:interpt-co/reright-cli.git")
	d, reason := f.run(t, "pre", inDir(bash("s1", `git commit -m "Release the new hook"`), dir))
	if d != "deny" || !strings.Contains(reason, "is public") {
		t.Fatalf("decision %q (%s)", d, reason)
	}
	d, reason = f.run(t, "pre", inDir(bash("s1", `git tag -s v1.0.0 -m "Ship the new hook to everyone"`), dir))
	if d != "deny" {
		t.Fatalf("tag: decision %q (%s)", d, reason)
	}
}

func TestGhTextIsCheckedInTheUsersOwnRepo(t *testing.T) {
	isolateGit(t)
	f := newFixture(t)
	withAudience(f, nil)
	dir := repo(t, "", "")
	for _, cmd := range []string{`gh pr create --title "Fix" --body "Fixes the parser"`, `gh issue create --title "Parser bug" --body "It breaks on empty input files"`, `gh release create v1 --notes "First release of the new parser"`} {
		if d, _ := f.run(t, "pre", inDir(bash("s1", cmd), dir)); d != "deny" {
			t.Fatalf("%s: let through", cmd)
		}
	}
}

func TestAgentCannotMarkARepoAsItsOwn(t *testing.T) {
	isolateGit(t)
	f := newFixture(t)
	withAudience(f, nil)
	dir := repo(t, "colleague@example.com", "git@github.com:team/app.git")
	for _, cmd := range []string{`git config reright.commits never`, `git config --global --add reright.email colleague@example.com`} {
		d, reason := f.run(t, "pre", inDir(bash("s1", cmd), dir))
		if d != "deny" || !strings.Contains(reason, "reright.commits") {
			t.Fatalf("%s: decision %q (%s)", cmd, d, reason)
		}
	}
}

func TestCommitOutsideARepoIsStillChecked(t *testing.T) {
	isolateGit(t)
	f := newFixture(t)
	withAudience(f, nil)
	if d, _ := f.run(t, "pre", inDir(bash("s1", `git commit -m "Fix the parser"`), t.TempDir())); d != "deny" {
		t.Fatal("let through outside a repository")
	}
}
