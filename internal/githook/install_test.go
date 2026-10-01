package githook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupHome(t *testing.T) (home, gitconfig string) {
	t.Helper()
	gitAvailable(t)
	home = t.TempDir()
	gitconfig = filepath.Join(home, "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	t.Setenv("HOME", home)
	return home, gitconfig
}

func TestGitVerb(t *testing.T) {
	tests := map[string]string{
		"git commit -m x":                      "commit",
		"git -c core.hooksPath=/x cherry-pick": "cherry-pick",
		"git -C /repo revert HEAD":             "revert",
		"git --no-pager merge topic":           "merge",
		"git":                                  "",
	}
	for cmd, want := range tests {
		if got := gitVerb(strings.Fields(cmd)); got != want {
			t.Errorf("%q: got %q want %q", cmd, got, want)
		}
	}
}

func TestInstallSetsHooksPathAndUninstallRestoresNoFile(t *testing.T) {
	home, gitconfig := setupHome(t)
	res, err := Install(home, "/opt/reright/reright-hook")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Activated || res.Manual != "" || len(res.Files) != len(allHookNames()) {
		t.Fatalf("%+v", res)
	}
	out, _ := exec.Command("git", "config", "--file", gitconfig, "core.hooksPath").Output()
	if strings.TrimSpace(string(out)) != res.HooksDir {
		t.Fatalf("hooksPath %q", out)
	}
	for _, f := range res.Files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(f)
		if fi.Mode()&0o111 == 0 {
			t.Errorf("%s not executable", f)
		}
		name := filepath.Base(f)
		if name != "commit-msg" && name != "prepare-commit-msg" {
			continue
		}
		if !strings.Contains(string(b), "'/opt/reright/reright-hook' git "+name+` "$@"`) {
			t.Errorf("wrapper %s does not exec reright-hook: %s", name, b)
		}
	}
	info, err := Status(home)
	if err != nil || !info.Installed || !info.Active || len(info.Missing) != 0 || info.Mode != ModeAgents {
		t.Fatalf("%+v %v", info, err)
	}
	if err := Uninstall(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gitconfig); !os.IsNotExist(err) {
		t.Fatalf("global config was not removed: %v", err)
	}
	if _, err := os.Stat(res.HooksDir); !os.IsNotExist(err) {
		t.Fatal("hooks dir remains")
	}
	if info, _ := Status(home); info.Installed || info.Active {
		t.Fatalf("%+v", info)
	}
}

func TestUninstallRestoresExistingConfigByteForByte(t *testing.T) {
	home, gitconfig := setupHome(t)
	original := "[user]\n\tname = Someone\n# keep this comment\n[alias]\n\tco = checkout\n"
	os.WriteFile(gitconfig, []byte(original), 0o644)
	if _, err := Install(home, "/x/hook"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(gitconfig); string(b) == original {
		t.Fatal("config unchanged by install")
	}
	if err := Uninstall(home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(gitconfig); string(b) != original {
		t.Fatalf("not restored:\n%s", b)
	}
}

func TestUninstallAfterUserEditedConfigOnlyRemovesOurKey(t *testing.T) {
	home, gitconfig := setupHome(t)
	if _, err := Install(home, "/x/hook"); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(gitconfig, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("[user]\n\tname = Added Later\n")
	f.Close()
	if err := Uninstall(home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(gitconfig)
	if !strings.Contains(string(b), "Added Later") || strings.Contains(string(b), "hooksPath") {
		t.Fatalf("%s", b)
	}
}

func TestExistingHooksPathIsNotOverridden(t *testing.T) {
	home, gitconfig := setupHome(t)
	theirs := filepath.Join(home, "their-hooks")
	original := "[core]\n\thooksPath = " + theirs + "\n"
	os.WriteFile(gitconfig, []byte(original), 0o644)
	res, err := Install(home, "/x/hook")
	if err != nil {
		t.Fatal(err)
	}
	if res.Activated || res.ExistingHooksPath != theirs {
		t.Fatalf("%+v", res)
	}
	for _, want := range []string{theirs + "/commit-msg", theirs + "/prepare-commit-msg", "'/x/hook' git commit-msg \"$@\" || exit $?"} {
		if !strings.Contains(res.Manual, want) {
			t.Errorf("manual step lacks %q:\n%s", want, res.Manual)
		}
	}
	if b, _ := os.ReadFile(gitconfig); string(b) != original {
		t.Fatalf("config changed:\n%s", b)
	}
	info, _ := Status(home)
	if !info.Installed || info.Active || info.GlobalHooksPath != theirs {
		t.Fatalf("%+v", info)
	}
	if err := Uninstall(home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(gitconfig); string(b) != original {
		t.Fatalf("config changed by uninstall:\n%s", b)
	}
}

func TestInstallIsIdempotentAndKeepsOriginalState(t *testing.T) {
	home, gitconfig := setupHome(t)
	original := "[user]\n\tname = Someone\n"
	os.WriteFile(gitconfig, []byte(original), 0o644)
	if _, err := Install(home, "/x/hook"); err != nil {
		t.Fatal(err)
	}
	res, err := Install(home, "/y/hook")
	if err != nil || !res.AlreadyInstalled || !res.Activated {
		t.Fatalf("%+v %v", res, err)
	}
	if b, _ := os.ReadFile(res.Files[0]); !strings.Contains(string(b), "'/y/hook'") {
		t.Fatal("binary path not updated")
	}
	if err := Uninstall(home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(gitconfig); string(b) != original {
		t.Fatalf("not restored:\n%s", b)
	}
}

func TestInstallRefusesToOverwriteForeignHook(t *testing.T) {
	home, _ := setupHome(t)
	dir := filepath.Join(home, ".config", "reright", "git-hooks")
	os.MkdirAll(dir, 0o755)
	foreign := filepath.Join(dir, "commit-msg")
	os.WriteFile(foreign, []byte("#!/bin/sh\necho mine\n"), 0o755)
	if _, err := Install(home, "/x/hook"); err == nil {
		t.Fatal("overwrote a foreign hook")
	}
	if b, _ := os.ReadFile(foreign); !strings.Contains(string(b), "mine") {
		t.Fatal("foreign hook changed")
	}
}

func TestUninstallWithoutInstallIsANoOp(t *testing.T) {
	home, _ := setupHome(t)
	if err := Uninstall(home); err != nil {
		t.Fatal(err)
	}
}
