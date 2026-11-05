package onboard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeOldGitLayer writes the 0.1 wrappers and git-install.json into home and sets the global core.hooksPath
// (in home/.gitconfig) to hooksPath, or to the wrappers when hooksPath is empty. With another hooksPath the
// record says, as 0.1 did, that the user was asked to add lines there by hand.
func writeOldGitLayer(t *testing.T, home, hooksPath string) string {
	t.Helper()
	dir := filepath.Join(home, ".config", "reright", "git-hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"commit-msg", "prepare-commit-msg", "pre-commit"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n: reright-git-hook\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := map[string]any{"hooks_dir": dir, "binary": filepath.Join(home, ".local/bin/reright-hook"), "activated": false}
	if hooksPath == "" {
		hooksPath = dir
	} else {
		st["manual_dir"] = hooksPath
	}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(home, ".config", "reright", "git-install.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "config", "--file", filepath.Join(home, ".gitconfig"), "core.hooksPath", hooksPath).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return dir
}
