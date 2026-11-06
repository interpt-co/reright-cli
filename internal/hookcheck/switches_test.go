package hookcheck

import "testing"

func TestSwitchCommand(t *testing.T) {
	yes := []string{
		`reright disable commit`,
		`reright enable all`,
		`~/.local/bin/reright disable all --for 1h`,
		`cd /x && reright enable commit`,
		`reright --verbose disable commit`,
		`sudo reright disable commit`,
		`env X=1 reright enable commit`,
		`xargs reright disable commit`,
		`echo commit | xargs reright disable`,
		`flock /tmp/l reright disable commit`,
		`watch reright enable all`,
		`runuser -u me -- reright disable commit`,
		`timeout 5 reright disable commit`,
		`bash -c 'reright disable commit'`,
		`sh -c "cd /x; reright enable all"`,
		`eval "reright disable commit"`,
		`$R disable commit`,
		`reright "$SUB" commit`,
		`echo '{}' > ~/.config/reright/enforcement.json`,
		`cat ~/.config/reright/enforcement.json`,
		`sed -i 's/a/b/' ~/.config/reright/enforcement.json`,
		`python3 -c "open('/home/u/.config/reright/enforcement.json','w').write('{}')"`,
		"cat <<'EOF' > ~/.config/reright/enforcement.json\n{}\nEOF",
	}
	no := []string{
		`reright status`,
		`reright doctor`,
		`reright install --code rrs_x`,
		`echo disable the reright thing`,
		`echo "run reright disable commit to switch it off"`,
		`git commit -m "document reright disable and enable"`,
		"python3 - <<'PY'\ns = 'reright disable commit and enforcement.json'\nprint(s)\nPY",
		"cat > notes.md <<'EOT'\nUse reright disable commit --for 2h.\nEOT",
		`grep -n "reright enable" docs.md`,
		`ls ~/.config/reright`,
	}
	for _, c := range yes {
		if !SwitchCommand(c) {
			t.Errorf("should flag: %q", c)
		}
	}
	for _, c := range no {
		if SwitchCommand(c) {
			t.Errorf("should not flag: %q", c)
		}
	}
}

func TestGitConfigKeysAreTheUsersToSet(t *testing.T) {
	yes := []string{
		`git config reright.commits never`,
		`git config --global reright.commits never`,
		`git -C /x config --local reright.commits never`,
		`git config --add reright.email boss@example.com`,
		`git config set reright.commits never`,
		`git config --unset reright.commits`,
		`git config Reright.Commits always`,
		`K=reright.commits; git config "$K" never`,
		`bash -c 'git config reright.commits never'`,
		`cd /x && git config reright.commits never`,
		`sudo git config --system reright.commits never`,
		`python3 -c "import subprocess; subprocess.run(['git','config','reright.commits','never'])"`,
	}
	no := []string{
		`git config --get reright.commits`,
		`git config --get-all reright.email`,
		`git config get reright.commits`,
		`git config --list`,
		`git config user.email me@example.com`,
		`git -c reright.commits=never commit -m "x"`,
		`git commit -m "explain git config reright.commits never"`,
		`echo "run git config reright.commits never"`,
		`grep reright.commits docs.md`,
	}
	for _, c := range yes {
		if !SwitchCommand(c) {
			t.Errorf("should flag: %q", c)
		}
	}
	for _, c := range no {
		if SwitchCommand(c) {
			t.Errorf("should not flag: %q", c)
		}
	}
}
