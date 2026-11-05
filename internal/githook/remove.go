package githook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// scriptMarker is the second line of every wrapper reright 0.1 wrote.
const scriptMarker = ": reright-git-hook"

var hookNames = []string{"commit-msg", "prepare-commit-msg"}

var chainedHookNames = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit",
	"post-commit", "pre-rebase", "post-checkout", "post-merge", "pre-push", "pre-receive",
	"update", "post-receive", "post-update", "reference-transaction", "pre-auto-gc",
	"post-rewrite", "sendemail-validate", "post-index-change",
}

func allHookNames() []string {
	return append(append([]string(nil), hookNames...), chainedHookNames...)
}

// Info describes what is left of the git layer.
type Info struct {
	// Installed is true when git-install.json or a wrapper is still there.
	Installed bool
	// Active is true when the global core.hooksPath still points at the wrappers.
	Active          bool
	HooksDir        string
	Files           []string
	GlobalHooksPath string
}

// state is git-install.json as reright 0.1 wrote it.
type state struct {
	HooksDir      string   `json:"hooks_dir"`
	Binary        string   `json:"binary"`
	Files         []string `json:"files"`
	Activated     bool     `json:"activated"`
	ConfigFile    string   `json:"config_file,omitempty"`
	ConfigExisted bool     `json:"config_existed,omitempty"`
	ConfigBefore  []byte   `json:"config_before,omitempty"`
	ConfigAfter   string   `json:"config_after_sha256,omitempty"`
	ManualDir     string   `json:"manual_dir,omitempty"`
}

func paths(homeDir string) (cfgDir, hooksDir, stateFile string) {
	cfgDir = filepath.Join(homeDir, ".config", "reright")
	return cfgDir, filepath.Join(cfgDir, "git-hooks"), filepath.Join(cfgDir, "git-install.json")
}

func expandHome(dir, homeDir string) string {
	if strings.HasPrefix(dir, "~/") {
		return filepath.Join(homeDir, dir[2:])
	}
	return dir
}

// Present reports, without running git, whether anything of the git layer is left in homeDir.
func Present(homeDir string) bool {
	cfgDir, hooksDir, stateFile := paths(homeDir)
	for _, p := range []string{stateFile, hooksDir, filepath.Join(cfgDir, "git.json")} {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}

// Remove takes the git layer away. It first puts the global git config back: when the config file is
// as the layer left it, the copy taken before is restored, so an earlier core.hooksPath comes back; otherwise
// core.hooksPath is unset if it still points at the wrappers. A core.hooksPath that points somewhere else is
// left alone. Only then are the wrappers and git-install.json deleted, so git is never left pointing at a
// missing directory. The notes say what was done and list hook files of the user's that still call
// reright-hook git.
func Remove(homeDir string) ([]string, error) {
	cfgDir, hooksDir, stateFile := paths(homeDir)
	if _, err := exec.LookPath("git"); err != nil && !Present(homeDir) {
		return nil, nil // no git and nothing of the layer: nothing to do
	}
	var notes []string
	st, err := readState(stateFile)
	if err != nil {
		notes = append(notes, err.Error()+". reright is recovering from the real git config instead")
		st = nil
	}
	if st != nil && st.Activated && st.ConfigFile != "" {
		note, err := revertConfig(homeDir, st, hooksDir)
		if err != nil {
			return notes, err
		}
		if note != "" {
			notes = append(notes, note)
		}
	}
	cur, err := globalHooksPath(homeDir)
	if err != nil {
		return notes, err
	}
	if sameDir(cur, hooksDir, homeDir) {
		cmd := exec.Command("git", "config", "--global", "--unset", "core.hooksPath")
		cmd.Env = gitEnv(homeDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return notes, fmt.Errorf("githook: unsetting the global core.hooksPath: %v: %s. Run: git config --global --unset core.hooksPath", err, out)
		}
		notes = append(notes, "unset the global core.hooksPath, which still pointed at "+hooksDir)
		cur = ""
	}
	for _, name := range allHookNames() {
		p := filepath.Join(hooksDir, name)
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(scriptMarker)) {
			if err := os.Remove(p); err != nil {
				return notes, err
			}
		}
	}
	os.Remove(hooksDir)
	if err := os.Remove(stateFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return notes, err
	}
	os.Remove(filepath.Join(cfgDir, "git.json")) // the 0.1 mode settings, which nothing reads now
	seen := map[string]bool{}
	var dirs []string
	binary := ""
	if st != nil {
		binary = st.Binary
	}
	if st != nil && st.ManualDir != "" {
		dirs = append(dirs, st.ManualDir)
	}
	if cur != "" {
		dirs = append(dirs, expandHome(cur, homeDir))
	}
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		for _, name := range hookNames {
			p := filepath.Join(d, name)
			if b, err := os.ReadFile(p); err == nil && (bytes.Contains(b, []byte("reright-hook")) || (binary != "" && bytes.Contains(b, []byte(binary)))) {
				notes = append(notes, "remove the reright line from "+p+" (the one that runs reright-hook git "+name+"). The hook binary is gone, so that line now fails every commit")
			}
		}
	}
	return notes, nil
}

func revertConfig(homeDir string, st *state, hooksDir string) (string, error) {
	cur, err := globalHooksPath(homeDir)
	if err != nil {
		return "", err
	}
	if !sameDir(cur, hooksDir, homeDir) {
		return "", nil
	}
	now, readErr := os.ReadFile(st.ConfigFile)
	if readErr == nil && sum(now) == st.ConfigAfter {
		if st.ConfigExisted {
			return "put " + st.ConfigFile + " back as it was before reright set core.hooksPath", os.WriteFile(st.ConfigFile, st.ConfigBefore, 0o644)
		}
		return "removed " + st.ConfigFile + ", which reright had created only to set core.hooksPath", os.Remove(st.ConfigFile)
	}
	if out, err := exec.Command("git", "config", "--file", st.ConfigFile, "--unset", "core.hooksPath").CombinedOutput(); err != nil {
		return "", fmt.Errorf("githook: unsetting core.hooksPath: %v: %s", err, out)
	}
	return "unset core.hooksPath in " + st.ConfigFile, nil
}

// Status reports what is left of the git layer.
func Status(homeDir string) (Info, error) {
	_, hooksDir, stateFile := paths(homeDir)
	info := Info{HooksDir: hooksDir}
	if _, err := os.Lstat(stateFile); err == nil {
		info.Installed = true
	}
	for _, name := range allHookNames() {
		p := filepath.Join(hooksDir, name)
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(scriptMarker)) {
			info.Files = append(info.Files, p)
			info.Installed = true
		}
	}
	var err error
	info.GlobalHooksPath, err = globalHooksPath(homeDir)
	if err != nil {
		return info, err
	}
	info.Active = sameDir(info.GlobalHooksPath, hooksDir, homeDir)
	return info, nil
}

func gitEnv(homeDir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+homeDir)
}

func globalHooksPath(homeDir string) (string, error) {
	cmd := exec.Command("git", "config", "--global", "--get", "core.hooksPath")
	cmd.Env = gitEnv(homeDir)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("githook: reading global core.hooksPath: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func sameDir(value, dir, homeDir string) bool {
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "~/") {
		value = filepath.Join(homeDir, value[2:])
	}
	return filepath.Clean(value) == filepath.Clean(dir)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func readState(path string) (*state, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("githook: bad state file %s: %w", path, err)
	}
	return &st, nil
}
