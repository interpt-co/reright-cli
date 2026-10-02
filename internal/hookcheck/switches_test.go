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
