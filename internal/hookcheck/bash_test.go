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
	must(os.WriteFile(filepath.Join(dir, "plain.sh"), []byte("echo hello\n"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "clean.py"), []byte("import json, sys\nprint(json.dumps(sys.argv[1:]))\n"), 0o644))
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
		{"gh api graphql mutation", `gh api graphql -f query='mutation { addComment(input: {subjectId: "x", body: "hi"}) { clientMutationId } }'`, nil, true},
		{"gh api graphql query", `gh api graphql -f query='query { viewer { login } }'`, nil, false},
		{"command wrapper", `command git commit -m 'hi there'`, []string{"hi there"}, false},
		{"sudo wrapper", `sudo git commit -m 'hi there'`, []string{"hi there"}, false},
		{"sudo with user", `sudo -u bob git commit -m 'hi there'`, []string{"hi there"}, false},
		{"env wrapper", `env GIT_AUTHOR_NAME=x git commit -m 'hi there'`, []string{"hi there"}, false},
		{"env with option", `env -u FOO git commit -m 'hi there'`, []string{"hi there"}, false},
		{"nested wrappers", `sudo env A=b nohup git commit -m 'deep'`, []string{"deep"}, false},
		{"timeout wrapper", `timeout 10 gh pr comment 1 -b 'in time'`, []string{"in time"}, false},
		{"wrapper on harmless command", `sudo ls -la`, nil, false},
		{"xargs into git", `echo x | xargs -I{} git commit -m {}`, nil, true},
		{"xargs into harmless", `ls | xargs -n1 echo`, nil, false},
		{"eval git", `eval "git commit -m x"`, []string{"x"}, false},
		{"eval variable", `eval "$CMD"; git status`, nil, true},
		{"bash -c", `bash -c 'git commit -m x'`, []string{"x"}, false},
		{"sh -c gh", `sh -c "gh pr comment 1 --body 'via sh'"`, []string{"via sh"}, false},
		{"bash -lc", `bash -lc 'git commit -m login'`, []string{"login"}, false},
		{"bash -c nested", `bash -c "bash -c 'git commit -m twice'"`, []string{"twice"}, false},
		{"bash -c harmless", `bash -c 'echo hello'`, nil, false},
		{"bash script file", `bash deploy.sh`, nil, false},
		{"bash stdin git", `echo 'git commit -m x' | bash`, nil, true},
		{"bash stdin harmless", `echo 'echo hi' | bash`, nil, false},
		{"bash heredoc git", "bash <<'EOF'\ngit commit -m x\nEOF", nil, true},
		{"variable command", `G=git; $G commit -m x`, nil, true},
		{"variable command harmless", `PY=python3; $PY --version`, nil, false},
		{"variable command with git elsewhere", `$PY tool.py; git status`, nil, false},
		{"python subprocess", `python3 -c "import subprocess; subprocess.run(['git','commit','-m','x'])"`, nil, true},
		{"python harmless", `python3 -c "print('github')"`, nil, false},
		{"python dot git path", `python3 -c "open('.git/HEAD')"`, nil, false},
		{"python script", `python3 tools/sync.py git`, nil, false},
		{"node exec gh", `node -e "require('child_process').execSync('gh pr comment 1 -b hi')"`, nil, true},
		{"perl system", `perl -e 'system("git","commit","-m","x")'`, nil, true},
		{"python stdin git", "python3 - <<'EOF'\nimport os\nos.system('git commit -m x')\nEOF", nil, true},
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
		{"curl post remote", `curl -X POST -d hi https://api.github.com/repos/a/b/issues/1/comments`, nil, true},
		{"curl data remote", `curl -d 'a=b' https://example.com/hook`, nil, true},
		{"curl json remote", `curl --json '{"body":"x"}' https://example.com/hook`, nil, true},
		{"curl form remote", `curl -F file=@a.txt https://example.com/up`, nil, true},
		{"curl put no data", `curl -X PUT https://example.com/x`, nil, true},
		{"curl get", `curl -sS https://example.com/x`, nil, false},
		{"curl data local", `curl -d 'a=b' http://localhost:8095/hook`, nil, false},
		{"curl header with dot on local", `curl -H 'X-A: b.c' -d x http://127.0.0.1:8080/`, nil, false},
		{"wget post", `wget --post-data='a=b' https://example.com/hook`, nil, true},
		{"wget get", `wget https://example.com/file.tgz`, nil, false},
		{"mail here-string", `mail -s hi a@b.c <<< hello`, nil, true},
		{"sendmail", `sendmail a@b.c < msg.txt`, nil, true},
		{"unparseable curl", `curl -d "unterminated https://example.com`, nil, true},
		{"unparseable git", `git commit -m "unterminated`, nil, true},
		{"env -S", `env -S 'git commit -m x'`, []string{"x"}, false},
		{"env -S attached", `env -S"git commit -m x"`, []string{"x"}, false},
		{"env --split-string", `env --split-string 'git commit -m x'`, []string{"x"}, false},
		{"env --split-string=", `env --split-string='git commit -m x'`, []string{"x"}, false},
		{"env -iS", `env -iS 'git commit -m x'`, []string{"x"}, false},
		{"env -S then args", `env -S 'git commit' -m 'two words'`, []string{"two words"}, false},
		{"env -S variable", `G=git; env -S "$G commit -m x"`, nil, true},
		{"env -S harmless", `env -S 'echo hi'`, nil, false},
		{"commit option abbreviation", `git commit --allow-empty -m approved --mess=SMUGGLED`, []string{"approved\n\nSMUGGLED"}, false},
		{"commit abbreviation with value", `git commit --mess SMUGGLED`, []string{"SMUGGLED"}, false},
		{"commit file abbreviation", `git commit --fil=msg.txt`, []string{"From file\n\nBody\n"}, false},
		{"commit ambiguous abbreviation", `git commit --fi=msg.txt`, nil, true},
		{"tag message abbreviation", `git tag -a v9 --mess=hidden`, []string{"hidden"}, false},
		{"git alias to commit", `git -c alias.ci=commit ci -m x`, nil, true},
		{"git alias shell", `git -c alias.ci='!git commit -m x' ci`, nil, true},
		{"git alias attached", `git -calias.ci=commit ci -m x`, nil, true},
		{"git harmless config", `git -c core.pager=cat log`, nil, false},
		{"flock -c", `flock -w 5 /tmp/l -c 'git commit -m x'`, []string{"x"}, false},
		{"flock command", `flock /tmp/l git commit -m x`, []string{"x"}, false},
		{"find -exec sh -c", `find . -exec sh -c 'git commit -m x' \;`, []string{"x"}, false},
		{"find -exec git", `find . -name a -exec git commit -m x {} +`, []string{"x"}, false},
		{"find harmless", `find . -name '*.go' -exec gofmt -l {} +`, nil, false},
		{"watch", `watch git commit -m x`, []string{"x"}, false},
		{"watch string", `watch -n 5 'git commit -m x'`, []string{"x"}, false},
		{"script -c", `script -c 'git commit -m x' /dev/null`, []string{"x"}, false},
		{"su -c", `su -c 'git commit -m x' bob`, []string{"x"}, false},
		{"busybox sh", `busybox sh -c 'git commit -m x'`, []string{"x"}, false},
		{"runuser wrapper", `runuser -u bob -- git commit -m x`, []string{"x"}, false},
		{"bash script file with commit", `bash x.sh`, []string{"from-script"}, false},
		{"source script file", `source x.sh`, []string{"from-script"}, false},
		{"dot script file", `. ./x.sh`, []string{"from-script"}, false},
		{"exec script file", `./x.sh`, []string{"from-script"}, false},
		{"script harmless", `bash plain.sh`, nil, false},
		{"exec harmless script", `./plain.sh`, nil, false},
		{"python script file with git", `python3 tool.py`, nil, true},
		{"python clean script, unreadable arg, git on the line", `python3 clean.py "$1" && git status`, nil, false},
		{"python clean script, unreadable arg, curl on the line", `python3 clean.py "$(date)" | curl -s https://example.com/x`, nil, false},
		{"python clean script, flag then unreadable arg", `python3 -u clean.py "$X"; git log`, nil, false},
		{"python sensitive script, unreadable arg", `python3 tool.py "$1"`, nil, true},
		{"python script path from a variable, git in another statement", `python3 $H/x.py "$1"; git log`, nil, false},
		{"python script path from a variable, git in the same call", `python3 $H/x.py "$1" git`, nil, true},
		{"python heredoc clean, unreadable arg, git elsewhere", "python3 - \"$A\" <<'PY'\nprint(1)\nPY\ngit log", nil, false},
		{"python heredoc runs git, unreadable arg", "python3 - \"$A\" <<'PY'\nimport os\nos.system('git commit -m x')\nPY", nil, true},
		{"python missing script, unreadable arg", `python3 nope.py "$1"; git log`, nil, false},
		{"script written then run", "cat > y.sh <<'EOF'\ngit commit -m x\nEOF\nbash y.sh", nil, true},
		{"script chained after git add", `git add . && bash run.sh`, nil, false},
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
