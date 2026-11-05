package hookcheck

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func values(r Result) []string {
	var out []string
	for _, t := range r.Texts {
		out = append(out, t.Value)
	}
	return out
}

func TestBash(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "msg.txt"), []byte("From file\n\nBody\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "body.md"), []byte("PR body from file\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "x.sh"), []byte("#!/bin/sh\ngit commit -m from-script\n"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "tool.py"), []byte("import os\nos.system('git commit -m x')\n"), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "sub", "m.txt"), []byte("In sub file"), 0o644))

	cases := []struct {
		name, cmd string
		want      []string
		problem   bool
	}{
		{"not relevant", "ls -la && go test ./...", nil, false},
		{"git status", "git status", nil, false},
		{"git log mentions commit", "git log --oneline -5 commit", nil, false},
		{"commit -m", `git commit -m "Fix login redirect"`, []string{"Fix login redirect"}, false},
		{"commit two -m", `git commit -m 'Subject' -m 'Body line'`, []string{"Subject\n\nBody line"}, false},
		{"commit -am", `git commit -am "Subject"`, []string{"Subject"}, false},
		{"commit -mValue", `git commit -mAttached`, []string{"Attached"}, false},
		{"commit --message=", `git commit --message="Subject here"`, []string{"Subject here"}, false},
		{"commit --message value", `git commit --message 'Spaced'`, []string{"Spaced"}, false},
		{"chained", `cd /tmp && git add . && git commit -m "Chained"`, []string{"Chained"}, false},
		{"escaped quote in single", `git commit -m 'Don'\''t crash'`, []string{"Don't crash"}, false},
		{"escaped in double", `git commit -m "Say \"hi\" \$HOME"`, []string{`Say "hi" $HOME`}, false},
		{"unquoted escapes", `git commit -m Two\ words`, []string{"Two words"}, false},
		{"heredoc", "git commit -m \"$(cat <<'EOF'\nSubject line\n\nBody paragraph.\nEOF\n)\"", []string{"Subject line\n\nBody paragraph."}, false},
		{"heredoc with shell chars", "git commit -m \"$(cat <<'EOF'\nUse $HOME and `x`\nEOF\n)\"", []string{"Use $HOME and `x`"}, false},
		{"unquoted heredoc without expansion", "git commit -m \"$(cat <<EOF\nPlain\nEOF\n)\"", []string{"Plain"}, false},
		{"unquoted heredoc with var", "git commit -m \"$(cat <<EOF\nHi $USER\nEOF\n)\"", nil, true},
		{"variable", `git commit -m "$MSG"`, nil, true},
		{"command substitution", `git commit -m "$(git log -1 --format=%s)"`, nil, true},
		{"file relative", `git commit -F msg.txt`, []string{"From file\n\nBody\n"}, false},
		{"file long flag", `git commit --file=msg.txt`, []string{"From file\n\nBody\n"}, false},
		{"file stdin", `git commit -F -`, nil, true},
		{"missing file", `git commit -F nope.txt`, nil, true},
		{"no message opens editor", `git commit`, nil, true},
		{"amend without message", `git commit --amend`, nil, true},
		{"amend no-edit", `git commit --amend --no-edit`, nil, false},
		{"amend with message", `git commit --amend -m "Reworded"`, []string{"Reworded"}, false},
		{"fixup", `git commit --fixup HEAD~1`, nil, false},
		{"fixup attached", `git commit --fixup=abc123`, nil, false},
		{"reuse", `git commit -C HEAD`, nil, false},
		{"git -C", `git -C sub commit -m "In sub"`, []string{"In sub"}, false},
		{"git -C file", `git -C sub commit -F m.txt`, []string{"In sub file"}, false},
		{"git -C variable read only", `git -C "$ROOT" rev-parse --short HEAD`, nil, false},
		{"git -C variable in a substitution", `REV=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo nogit)`, nil, false},
		{"variable binary with a plain subcommand", `"$BIN" check-db "$DB"`, nil, false},
		{"variable git commit is let through", `"$G" commit -m "x"`, nil, false},
		{"git -C variable commit", `git -C "$ROOT" commit -m "Subject"`, nil, true},
		{"git -C variable tag", `git -C "$ROOT" tag -m "Release" v1`, nil, true},
		{"git -c config", `git -c user.name=x commit -m "Configured"`, []string{"Configured"}, false},
		{"git path binary", `/usr/bin/git commit -m "Abs"`, []string{"Abs"}, false},
		{"gh pr create", `gh pr create --title "Add X" --body "Adds X because Y."`, []string{"Add X\n\nAdds X because Y."}, false},
		{"gh pr create title only", `gh pr create -t "Only title"`, []string{"Only title"}, false},
		{"gh pr create body file", `gh pr create --title T --body-file body.md --base main`, []string{"T\n\nPR body from file\n"}, false},
		{"gh pr create fill", `gh pr create --fill`, nil, false},
		{"gh pr edit body", `gh pr edit 12 --body "New body"`, []string{"New body"}, false},
		{"gh pr edit title and body", `gh pr edit 12 --title "New title" --body "New body"`, []string{"New title", "New body"}, false},
		{"gh pr comment", `gh pr comment 12 -b "Thanks, fixed."`, []string{"Thanks, fixed."}, false},
		{"gh pr review", `gh pr review 12 --approve --body "Looks good"`, []string{"Looks good"}, false},
		{"gh issue comment", `gh issue comment 3 --body=Done`, []string{"Done"}, false},
		{"gh issue create", `gh issue create --title "Bug" --body "Steps"`, []string{"Bug\n\nSteps"}, false},
		{"gh repo flag first", `gh -R o/r pr comment 1 -b "x y"`, []string{"x y"}, false},
		{"gh variable in other flag", `gh pr create --head "$BRANCH" --title "T" --body "B"`, []string{"T\n\nB"}, false},
		{"gh pr list", `gh pr list --state open`, nil, false},
		{"gh pr view", `gh pr view 12 --json body`, nil, false},
		{"gh body var", `gh pr comment 1 --body "$BODY"`, nil, true},
		{"gh body stdin", `gh pr comment 1 --body-file -`, nil, true},
		{"gh api body field", `gh api -X PATCH repos/o/r/pulls/1 -f body="API body" -f state=open`, []string{"API body"}, false},
		{"gh api raw-field title", `gh api repos/o/r/issues --raw-field title='Issue title'`, []string{"Issue title"}, false},
		{"gh api field file", `gh api repos/o/r/pulls/1 -F body=@body.md`, []string{"PR body from file\n"}, false},
		{"gh api input", `gh api -X PATCH repos/o/r/pulls/1 --input payload.json`, nil, true},
		{"gh api input get", `gh api -X GET search/issues --input q.json`, nil, false},
		{"gh api get", `gh api repos/o/r/pulls/1`, nil, false},
		{"gh api field var", `gh api repos/o/r/issues -f body="$B"`, nil, true},
		{"gh body= from command output", `gh pr comment 12 --body="$(cat notes.md)"`, nil, true},
		{"gh title= variable", `gh pr create --title T --body="$BODY"`, nil, true},
		{"gh api raw-field= variable", `gh api repos/o/r/issues --raw-field="body=$X"`, nil, true},
		{"commit file written in same command", "cat > msg.txt <<'EOF'\nNew text\nEOF\ngit commit -F msg.txt", nil, true},
		{"commit file after cd", `cd sub && git commit -F m.txt`, nil, true},
		{"body file with redirect", `gh pr comment 1 --body-file body.md > /tmp/out`, nil, true},
		{"api file after other command", `echo hi && gh api repos/o/r/pulls/1 -F body=@body.md`, nil, true},
		{"gh pr close comment", `gh pr close 12 --comment "Superseded by #13"`, []string{"Superseded by #13"}, false},
		{"gh issue close comment short", `gh issue close 3 -c "Fixed in 1.2"`, []string{"Fixed in 1.2"}, false},
		{"gh pr merge subject and body", `gh pr merge 12 --squash --subject "Squash subject" --body "Squash body"`, []string{"Squash subject", "Squash body"}, false},
		{"gh release notes", `gh release create v1.0 --title "v1.0" --notes "First release"`, []string{"v1.0", "First release"}, false},
		{"gh api nested body key", `gh api repos/o/r/pulls/1/reviews -f event=COMMENT -f 'comments[][body]=Inline note'`, []string{"Inline note"}, false},
		{"gh api message key", `gh api -X PUT repos/o/r/contents/x -f message="Commit via API"`, []string{"Commit via API"}, false},
		{"gh api graphql query", `gh api graphql -f query='query { viewer { login } }'`, nil, false},
		{"command wrapper", `command git commit -m 'hi there'`, []string{"hi there"}, false},
		{"sudo wrapper", `sudo git commit -m 'hi there'`, []string{"hi there"}, false},
		{"sudo with user", `sudo -u bob git commit -m 'hi there'`, []string{"hi there"}, false},
		{"env wrapper", `env GIT_AUTHOR_NAME=x git commit -m 'hi there'`, []string{"hi there"}, false},
		{"env with option", `env -u FOO git commit -m 'hi there'`, []string{"hi there"}, false},
		{"nested wrappers", `sudo env A=b nohup git commit -m 'deep'`, []string{"deep"}, false},
		{"timeout wrapper", `timeout 10 gh pr comment 1 -b 'in time'`, []string{"in time"}, false},
		{"wrapper on harmless command", `sudo ls -la`, nil, false},
		{"xargs into git is let through", `echo x | xargs -I{} git commit -m {}`, nil, false},
		{"eval is let through", `eval "git commit -m x"`, nil, false},
		{"bash -c is let through", `bash -c 'git commit -m x'`, nil, false},
		{"bash stdin is let through", `echo 'git commit -m x' | bash`, nil, false},
		{"variable command is let through", `G=git; $G commit -m x`, nil, false},
		{"python inline code is let through", `python3 -c "import subprocess; subprocess.run(['git','commit','-m','x'])"`, nil, false},
		{"git tag annotated", `git tag -a v1 -m 'Release one'`, []string{"Release one"}, false},
		{"git tag cluster", `git tag -am 'Release two' v2`, []string{"Release two"}, false},
		{"git tag message long", `git tag --message='Release three' v3`, []string{"Release three"}, false},
		{"git tag file", `git tag -a v4 -F msg.txt`, []string{"From file\n\nBody\n"}, false},
		{"git tag annotated editor", `git tag -a v1`, nil, true},
		{"git tag lightweight", `git tag v1`, nil, false},
		{"git tag list", `git tag -l 'v*'`, nil, false},
		{"git merge message", `git merge --no-ff -m 'Merge feature' feature`, []string{"Merge feature"}, false},
		{"git merge file", `git merge -F msg.txt feature`, []string{"From file\n\nBody\n"}, false},
		{"git merge plain", `git merge feature`, nil, false},
		{"git merge variable message", `git merge -m "$M" feature`, nil, true},
		{"curl with a body is let through", `curl -X POST -d hi https://api.github.com/repos/a/b/issues/1/comments`, nil, false},
		{"unparseable curl is let through", `curl -d "unterminated https://example.com`, nil, false},
		{"unparseable git", `git commit -m "unterminated`, nil, true},
		{"env -S is let through", `env -S 'git commit -m x'`, nil, false},
		{"commit option abbreviation", `git commit --allow-empty -m approved --mess=SMUGGLED`, []string{"approved\n\nSMUGGLED"}, false},
		{"commit abbreviation with value", `git commit --mess SMUGGLED`, []string{"SMUGGLED"}, false},
		{"commit file abbreviation", `git commit --fil=msg.txt`, []string{"From file\n\nBody\n"}, false},
		{"commit ambiguous abbreviation", `git commit --fi=msg.txt`, nil, true},
		{"tag message abbreviation", `git tag -a v9 --mess=hidden`, []string{"hidden"}, false},
		{"git alias is let through", `git -c alias.ci=commit ci -m x`, nil, false},
		{"git harmless config", `git -c core.pager=cat log`, nil, false},
		{"flock -c is let through", `flock -w 5 /tmp/l -c 'git commit -m x'`, nil, false},
		{"find -exec is let through", `find . -exec sh -c 'git commit -m x' \;`, nil, false},
		{"shell script file is let through", `bash x.sh`, nil, false},
		{"python script that runs git is let through", `python3 tool.py`, nil, false},
		{"unparseable unrelated", `echo "unterminated`, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Bash(c.cmd, dir)
			if c.problem != (res.Problem != "") {
				t.Fatalf("problem = %q, want problem: %v (texts %q)", res.Problem, c.problem, values(res))
			}
			if !c.problem && !slices.Equal(values(res), c.want) {
				t.Fatalf("texts = %q, want %q", values(res), c.want)
			}
		})
	}
}
