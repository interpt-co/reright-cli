package audience

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const me = "me@example.com"

// isolate keeps the user's own git config (signing, hooks, identity) out of the tests.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	global := filepath.Join(home, ".gitconfig")
	cfg := "[user]\n\tname = Me\n\temail = " + me + "\n[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n[core]\n\thooksPath = /dev/null\n[init]\n\tdefaultBranch = main\n[protocol \"file\"]\n\tallow = always\n"
	if err := os.WriteFile(global, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL", "GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME", "GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return home
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	return dir
}

func commit(t *testing.T, dir, email string) {
	t.Helper()
	args := []string{"commit", "-q", "--allow-empty", "-m", "work"}
	if email != "" {
		args = append(args, "--author", "Someone <"+email+">")
	}
	run(t, dir, args...)
}

func decide(t *testing.T, dir string) Decision {
	t.Helper()
	return Detector{}.Decide(context.Background(), dir)
}

func wantShared(t *testing.T, d Decision, shared bool) {
	t.Helper()
	if d.Shared != shared {
		t.Fatalf("shared = %v (%s), want %v", d.Shared, d.Why, shared)
	}
	if d.Why == "" {
		t.Fatal("no reason given")
	}
}

func TestNoRemoteIsTheUsersOwnEvenWithOtherAuthors(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "colleague@example.com")
	wantShared(t, decide(t, dir), false)
}

func TestARemoteOnThisMachineIsNotAHostingService(t *testing.T) {
	isolate(t)
	backup := t.TempDir()
	run(t, backup, "init", "-q", "--bare")
	dir := newRepo(t)
	commit(t, dir, "colleague@example.com")
	run(t, dir, "remote", "add", "backup", backup)
	run(t, dir, "remote", "add", "mirror", "file://"+backup)
	run(t, dir, "remote", "add", "rel", "../elsewhere.git")
	wantShared(t, decide(t, dir), false)
}

func TestHostedRemoteWithOnlyTheUsersCommitsIsOwn(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, strings.ToUpper(me)) // the address is compared without case
	run(t, dir, "remote", "add", "origin", "git@github.com:me/tool.git")
	wantShared(t, decide(t, dir), false)
}

func TestHostedRemoteWithAnotherAuthorIsShared(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "colleague@example.com")
	run(t, dir, "remote", "add", "origin", "https://git.example.org/team/app.git")
	d := decide(t, dir)
	wantShared(t, d, true)
	if !strings.Contains(d.Why, "colleague@example.com") {
		t.Errorf("reason does not name the author: %s", d.Why)
	}
}

func TestAnotherAuthorOnAnotherBranchCounts(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	run(t, dir, "checkout", "-q", "-b", "feature")
	commit(t, dir, "colleague@example.com")
	run(t, dir, "checkout", "-q", "main")
	run(t, dir, "remote", "add", "origin", "git@github.com:team/app.git")
	wantShared(t, decide(t, dir), true)
}

func TestForkWithUpstreamIsShared(t *testing.T) {
	isolate(t)
	upstream := newRepo(t)
	commit(t, upstream, "maintainer@example.org")
	dir := t.TempDir()
	run(t, dir, "clone", "-q", upstream, ".")
	run(t, dir, "remote", "set-url", "origin", "git@github.com:me/project.git")
	run(t, dir, "remote", "add", "upstream", "https://github.com/maintainer/project.git")
	commit(t, dir, "")
	wantShared(t, decide(t, dir), true)
}

func TestBotsDoNotCountAsOtherAuthors(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "49699333+dependabot[bot]@users.noreply.github.com")
	run(t, dir, "remote", "add", "origin", "git@github.com:me/tool.git")
	wantShared(t, decide(t, dir), false)
}

func TestOtherIdentitiesOfTheUser(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "me@work.example")
	commit(t, dir, "old@laptop.example")
	run(t, dir, "remote", "add", "origin", "git@gitlab.com:me/tool.git")
	wantShared(t, decide(t, dir), true)
	// a repository user.email is the user too, next to the global one
	run(t, dir, "config", "user.email", "me@work.example")
	run(t, dir, "config", "--global", "--add", "reright.email", "Old@Laptop.example")
	wantShared(t, decide(t, dir), false)
}

func TestMailmapIsApplied(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "typo@exmaple.com")
	run(t, dir, "remote", "add", "origin", "git@github.com:me/tool.git")
	if err := os.WriteFile(filepath.Join(dir, ".mailmap"), []byte("Me <"+me+"> <typo@exmaple.com>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantShared(t, decide(t, dir), false)
}

// A noreply address is the user only when gh is signed in to that login or reright.email lists it. The
// owner of the remote proves nothing: a clone of a colleague's repository has the colleague as owner.
func TestGitHubNoreplyIsTheUserOnlyThroughGhOrRerightEmail(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "1234+Paro@users.noreply.github.com") // a commit made in the GitHub web editor
	run(t, dir, "remote", "add", "origin", "https://github.com/paro/tool")
	ctx := context.Background()
	// the remote owner's noreply is not enough, and without gh there is no match
	wantShared(t, Detector{}.Decide(ctx, dir), true)
	wantShared(t, Detector{GitHubLogins: func(context.Context) []string { return nil }}.Decide(ctx, dir), true)
	// gh signed in to another account: still shared
	other := Detector{GitHubLogins: func(context.Context) []string { return []string{"colleague"} }}
	wantShared(t, other.Decide(ctx, dir), true)
	// gh signed in to paro: the user's own
	asked := 0
	mine := Detector{GitHubLogins: func(context.Context) []string { asked++; return []string{"Paro"} }}
	wantShared(t, mine.Decide(ctx, dir), false)
	commit(t, dir, "999+someone@users.noreply.github.com")
	wantShared(t, mine.Decide(ctx, dir), true)
	if asked != 2 {
		t.Errorf("gh asked %d times for two decisions", asked)
	}
}

func TestGhIsNotAskedWithoutNoreplyCommits(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "colleague@example.com")
	run(t, dir, "remote", "add", "origin", "git@github.com:team/app.git")
	asked := false
	d := Detector{GitHubLogins: func(context.Context) []string { asked = true; return nil }}
	wantShared(t, d.Decide(context.Background(), dir), true)
	if asked {
		t.Error("asked gh about a history with no noreply address")
	}
}

func TestNoreplyListedInRerightEmailMatchesAnyIDForm(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	commit(t, dir, "1234+paro@users.noreply.github.com")
	commit(t, dir, "paro@users.noreply.github.com")
	run(t, dir, "remote", "add", "origin", "https://github.com/team/tool")
	run(t, dir, "config", "--global", "--add", "reright.email", "1234+paro@users.noreply.github.com")
	wantShared(t, decide(t, dir), false)
}

func TestOverride(t *testing.T) {
	isolate(t)
	solo := newRepo(t)
	commit(t, solo, "")
	run(t, solo, "config", "reright.commits", "always")
	wantShared(t, decide(t, solo), true)

	team := newRepo(t)
	commit(t, team, "colleague@example.com")
	run(t, team, "remote", "add", "origin", "git@github.com:team/app.git")
	run(t, team, "config", "reright.commits", "Never")
	wantShared(t, decide(t, team), false)
	run(t, team, "config", "reright.commits", "auto")
	wantShared(t, decide(t, team), true)
	run(t, team, "config", "reright.commits", "sometimes") // an unknown value means auto
	wantShared(t, decide(t, team), true)

	// a global value is the default for every repository, and a repository value wins over it
	run(t, team, "config", "--unset", "reright.commits")
	run(t, team, "config", "--global", "reright.commits", "never")
	wantShared(t, decide(t, team), false)
	run(t, team, "config", "reright.commits", "auto")
	wantShared(t, decide(t, team), true)
}

func TestShallowCloneIsShared(t *testing.T) {
	isolate(t)
	src := newRepo(t)
	commit(t, src, "colleague@example.com")
	commit(t, src, "")
	dir := t.TempDir()
	run(t, dir, "clone", "-q", "--depth", "1", "file://"+src, ".")
	run(t, dir, "remote", "set-url", "origin", "git@github.com:team/app.git")
	d := decide(t, dir)
	wantShared(t, d, true)
	if !strings.Contains(d.Why, "shallow") {
		t.Errorf("reason: %s", d.Why)
	}
}

func TestDetachedWorktreeUsesTheMainRepository(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	run(t, dir, "remote", "add", "origin", "git@github.com:me/tool.git")
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, dir, "worktree", "add", "-q", "--detach", wt)
	wantShared(t, decide(t, wt), false)
	commit(t, wt, "colleague@example.com")
	wantShared(t, decide(t, wt), true)
	run(t, dir, "config", "reright.commits", "never")
	wantShared(t, decide(t, wt), false)
}

func TestSubdirectoryOfARepository(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "colleague@example.com")
	run(t, dir, "remote", "add", "origin", "git@github.com:team/app.git")
	sub := filepath.Join(dir, "a", "b")
	os.MkdirAll(sub, 0o755)
	wantShared(t, decide(t, sub), true)
}

func TestEmptyRepositoryWithARemote(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	run(t, dir, "remote", "add", "origin", "git@github.com:me/new.git")
	wantShared(t, decide(t, dir), false)
}

func TestNotARepositoryIsChecked(t *testing.T) {
	isolate(t)
	wantShared(t, decide(t, t.TempDir()), true)
}

func TestPublicRemoteIsShared(t *testing.T) {
	isolate(t)
	dir := newRepo(t)
	commit(t, dir, "")
	run(t, dir, "remote", "add", "origin", "git@github.com:interpt-co/reright-cli.git")
	run(t, dir, "remote", "add", "backup", "https://git.example.org/me/backup.git")
	run(t, dir, "config", "remote.backup.mirror", "true")
	var asked []string
	det := Detector{Public: func(_ context.Context, r Remote) (bool, error) {
		asked = append(asked, r.Host+"/"+r.Path)
		return r.Path == "interpt-co/reright-cli", nil
	}}
	d := det.Decide(context.Background(), dir)
	wantShared(t, d, true)
	if !strings.Contains(d.Why, "public") {
		t.Errorf("reason: %s", d.Why)
	}
	det.Public = func(context.Context, Remote) (bool, error) { return false, nil }
	wantShared(t, det.Decide(context.Background(), dir), false)
	det.Public = func(context.Context, Remote) (bool, error) { return false, errors.New("offline") }
	wantShared(t, det.Decide(context.Background(), dir), true) // when in doubt, check
}

func TestParseRemote(t *testing.T) {
	cases := []struct {
		in, host, path string
		hosted         bool
	}{
		{"git@github.com:interpt-co/reright-cli.git", "github.com", "interpt-co/reright-cli", true},
		{"https://github.com/interpt-co/reright-cli", "github.com", "interpt-co/reright-cli", true},
		{"https://user:pw@GitLab.com/group/sub/repo.git/", "gitlab.com", "group/sub/repo", true},
		{"ssh://git@git.example.org:2222/team/app.git", "git.example.org", "team/app", true},
		{"example.org:repo", "example.org", "repo", true},
		{"/srv/git/app.git", "", "", false},
		{"../app.git", "", "", false},
		{"./app", "", "", false},
		{"~/mirrors/app.git", "", "", false},
		{"file:///srv/git/app.git", "", "", false},
		{"relative/dir:with-colon", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		r, hosted := ParseRemote(c.in)
		if hosted != c.hosted || (hosted && (r.Host != c.host || r.Path != c.path)) {
			t.Errorf("ParseRemote(%q) = %+v %v, want %s %s %v", c.in, r, hosted, c.host, c.path, c.hosted)
		}
	}
}

func TestHostingProbe(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/repos/open/repo", "/api/v4/projects/group/sub/repo":
			w.WriteHeader(200)
		case "/repos/closed/repo":
			w.WriteHeader(404)
		default:
			w.WriteHeader(403)
		}
	}))
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	p := HostingProbe{Dir: t.TempDir(), BaseGitHub: srv.URL, BaseGitLab: srv.URL, Now: func() time.Time { return now }}
	ctx := context.Background()
	check := func(host, path string, want bool, wantErr bool) {
		t.Helper()
		got, err := p.Public(ctx, Remote{Host: host, Path: path})
		if (err != nil) != wantErr || got != want {
			t.Fatalf("%s/%s: %v %v", host, path, got, err)
		}
	}
	check("github.com", "open/repo", true, false)
	check("github.com", "closed/repo", false, false)
	check("gitlab.com", "group/sub/repo", true, false)
	check("github.com", "limited/repo", false, true)
	if calls.Load() != 4 {
		t.Fatalf("calls = %d", calls.Load())
	}
	// answers are cached, errors are not
	check("github.com", "open/repo", true, false)
	check("github.com", "closed/repo", false, false)
	check("github.com", "limited/repo", false, true)
	if calls.Load() != 5 {
		t.Fatalf("calls after cache = %d", calls.Load())
	}
	now = now.Add(PublicTTL + time.Minute)
	check("github.com", "open/repo", true, false)
	if calls.Load() != 6 {
		t.Fatalf("expired answer was not asked again: %d", calls.Load())
	}
	// other hosts and paths that are not owner/repo are never asked
	check("git.example.org", "team/app", false, false)
	check("github.com", "just-owner", false, false)
	if calls.Load() != 6 {
		t.Fatalf("asked an unsupported remote: %d", calls.Load())
	}
}
