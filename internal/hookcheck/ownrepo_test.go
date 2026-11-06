package hookcheck

import (
	"path/filepath"
	"testing"
)

// ownRepos is an OwnRepo that knows two directories: /own is the user's own repository, everything
// else is shared. It records what it was asked.
type ownRepos struct{ asked []string }

func (o *ownRepos) own(dir string) bool {
	o.asked = append(o.asked, dir)
	return dir == "/own" || filepath.Dir(dir) == "/own"
}

func TestOwnRepoLetsCommitsThrough(t *testing.T) {
	cases := []struct {
		cmd, cwd string
		checked  bool // a commit text or problem is still reported
	}{
		{`git commit -m "work"`, "/own", false},
		{`git commit -m "work"`, "/team", true},
		{`git tag -a v1 -m "release"`, "/own", false},
		{`git merge --no-ff -m "merge" topic`, "/own", false},
		{`git -C /own commit -m "work"`, "/team", false},
		{`git -C /team commit -m "work"`, "/own", true},
		{`git -C sub commit -m "work"`, "/own", false},
		// an editor commit or an unreadable message is no problem in the user's own repository
		{`git commit`, "/own", false},
		{`git commit -m "$MSG"`, "/own", false},
		{`git commit`, "/team", true},
		// a -C the hook cannot read still blocks, wherever the session is
		{`git -C "$D" commit -m "work"`, "/own", true},
		// options and variables that point git elsewhere make the repository unknown
		{`git --git-dir=/team/.git commit -m "work"`, "/own", true},
		{`git --work-tree /team commit -m "work"`, "/own", true},
		{`GIT_DIR=/team/.git git commit -m "work"`, "/own", true},
		{`env GIT_WORK_TREE=/team git commit -m "work"`, "/own", true},
		{`export GIT_DIR=/team/.git; git commit -m "work"`, "/own", true},
		// a cd is followed when it is one literal directory, and both places must be the user's own
		{`cd /team && git commit -m "work"`, "/own", true},
		{`cd /own && git commit -m "work"`, "/team", true},
		{`cd /own && git commit -m "work"`, "/own", false},
		{`cd sub && git commit -m "work"`, "/own", false},
		{`cd "$D" && git commit -m "work"`, "/own", true},
		{`cd && git commit -m "work"`, "/own", true},
		{`cd ~/x && git commit -m "work"`, "/own", true},
		{`cd /own && cd /own && git commit -m "work"`, "/own", true},
		{`pushd /team && git commit -m "work"`, "/own", true},
		{`popd; git commit -m "work"`, "/own", true},
		// git -c does not change what the hook reads
		{`git -c reright.commits=never commit -m "work"`, "/team", true},
	}
	for _, c := range cases {
		o := &ownRepos{}
		r := BashWith(c.cmd, c.cwd, Options{OwnRepo: o.own})
		got := len(r.Texts) > 0 || r.Problem != ""
		if got != c.checked {
			t.Errorf("%s in %s: checked = %v, want %v (%+v, asked %v)", c.cmd, c.cwd, got, c.checked, r, o.asked)
		}
	}
}

func TestOwnRepoLeavesGhAndOtherTextAlone(t *testing.T) {
	o := &ownRepos{}
	r := BashWith(`gh pr create --title "Fix" --body "Body text" && git commit -m "work"`, "/own", Options{OwnRepo: o.own})
	if len(r.Texts) != 1 || r.Texts[0].Source != "gh pr create title and body" {
		t.Fatalf("got %+v", r)
	}
	r = BashWith(`gh release create v1 --notes "Notes here"`, "/own", Options{OwnRepo: o.own})
	if len(r.Texts) != 1 {
		t.Fatalf("release notes not checked: %+v", r)
	}
}

func TestGitTextsCarryTheirDirectory(t *testing.T) {
	r := Bash(`git -C sub commit -m "work"`, "/team")
	if len(r.Texts) != 1 || r.Texts[0].Dir != "/team/sub" {
		t.Fatalf("got %+v", r.Texts)
	}
	r = Bash(`gh issue create --title "T" --body "B"`, "/team")
	if len(r.Texts) != 1 || r.Texts[0].Dir != "" {
		t.Fatalf("got %+v", r.Texts)
	}
}

func TestWithoutOwnRepoEveryRepositoryIsChecked(t *testing.T) {
	r := Bash(`git commit -m "work"`, "/own")
	if len(r.Texts) != 1 {
		t.Fatalf("got %+v", r)
	}
}
