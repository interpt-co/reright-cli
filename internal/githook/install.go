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

const scriptMarker = ": reright-git-hook"

var hookNames = []string{"commit-msg", "prepare-commit-msg"}

var chainedHookNames = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit",
	"post-commit", "pre-rebase", "post-checkout", "post-merge", "pre-push", "pre-receive",
	"update", "post-receive", "post-update", "reference-transaction", "pre-auto-gc",
	"post-rewrite", "sendemail-validate", "post-index-change",
}

var unchainedHookNames = []string{"fsmonitor-watchman", "push-to-checkout", "proc-receive", "p4-changelist", "p4-prepare-changelist", "p4-post-changelist", "p4-pre-submit"}

func UnchainedHooks() []string { return append([]string(nil), unchainedHookNames...) }

func allHookNames() []string {
	return append(append([]string(nil), hookNames...), chainedHookNames...)
}

type Result struct {
	HooksDir          string
	Files             []string
	Activated         bool
	AlreadyInstalled  bool
	ExistingHooksPath string
	Manual            string
	ManualDir         string
}

type Info struct {
	Installed       bool
	Active          bool
	HooksDir        string
	Binary          string
	Files           []string
	Missing         []string
	GlobalHooksPath string
	Mode            string
	TTYHeuristic    bool
}

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

func Install(homeDir, hookBinaryPath string) (Result, error) {
	cfgDir, hooksDir, stateFile := paths(homeDir)
	res := Result{HooksDir: hooksDir}
	if homeDir == "" || hookBinaryPath == "" {
		return res, errors.New("githook: home directory and hook binary path are required")
	}
	if !filepath.IsAbs(hookBinaryPath) {
		return res, fmt.Errorf("githook: the hook binary path %q must be absolute", hookBinaryPath)
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return res, err
	}
	st, err := readState(stateFile)
	if err != nil {
		return res, fmt.Errorf("%w. Run reright uninstall to clean up, then install again", err)
	}
	res.AlreadyInstalled = st != nil

	names := allHookNames()
	for _, name := range names {
		p := filepath.Join(hooksDir, name)
		if b, err := os.ReadFile(p); err == nil && !bytes.Contains(b, []byte(scriptMarker)) {
			return res, fmt.Errorf("githook: %s exists and was not written by reright, not overwriting it", p)
		}
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return res, err
	}
	cfgFileJSON := filepath.Join(cfgDir, "git.json")
	for _, name := range names {
		p := filepath.Join(hooksDir, name)
		if err := os.WriteFile(p, []byte(wrapperScript(name, hookBinaryPath, cfgFileJSON)), 0o755); err != nil {
			return res, err
		}
		if err := os.Chmod(p, 0o755); err != nil {
			return res, err
		}
		res.Files = append(res.Files, p)
	}

	if st == nil {
		st = &state{}
	}
	st.HooksDir, st.Binary, st.Files = hooksDir, hookBinaryPath, res.Files

	existing, err := globalHooksPath(homeDir)
	if err != nil {
		return res, err
	}
	res.ExistingHooksPath = existing
	switch {
	case existing == "":
		cfgFile := globalConfigFile(homeDir)
		before, readErr := os.ReadFile(cfgFile)
		st.ConfigFile, st.ConfigExisted, st.ConfigBefore = cfgFile, readErr == nil, before
		if err := os.MkdirAll(filepath.Dir(cfgFile), 0o755); err != nil {
			return res, err
		}
		if out, err := exec.Command("git", "config", "--file", cfgFile, "core.hooksPath", hooksDir).CombinedOutput(); err != nil {
			return res, fmt.Errorf("githook: setting core.hooksPath: %v: %s", err, out)
		}
		after, _ := os.ReadFile(cfgFile)
		st.ConfigAfter = sum(after)
		st.Activated = true
		res.Activated = true
	case sameDir(existing, hooksDir, homeDir):
		res.Activated = true
	default:
		res.ExistingHooksPath = existing
		res.Manual = manualStep(existing, homeDir, hookBinaryPath)
		res.ManualDir = expandHome(existing, homeDir)
		st.ManualDir = res.ManualDir
	}

	if err := writeState(stateFile, st); err != nil {
		return res, err
	}
	return res, nil
}

func expandHome(dir, homeDir string) string {
	if strings.HasPrefix(dir, "~/") {
		return filepath.Join(homeDir, dir[2:])
	}
	return dir
}

func Uninstall(homeDir string) error {
	_, err := UninstallNotes(homeDir)
	return err
}

func UninstallNotes(homeDir string) ([]string, error) {
	_, hooksDir, stateFile := paths(homeDir)
	var notes []string
	st, err := readState(stateFile)
	if err != nil {
		notes = append(notes, err.Error()+". reright is recovering from the real git config instead")
		st = nil
	}
	if st != nil && st.Activated && st.ConfigFile != "" {
		if err := revertConfig(homeDir, st, hooksDir); err != nil {
			return notes, err
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

func revertConfig(homeDir string, st *state, hooksDir string) error {
	cur, err := globalHooksPath(homeDir)
	if err != nil {
		return err
	}
	if !sameDir(cur, hooksDir, homeDir) {
		return nil
	}
	now, readErr := os.ReadFile(st.ConfigFile)
	if readErr == nil && sum(now) == st.ConfigAfter {
		if st.ConfigExisted {
			return os.WriteFile(st.ConfigFile, st.ConfigBefore, 0o644)
		}
		return os.Remove(st.ConfigFile)
	}
	if out, err := exec.Command("git", "config", "--file", st.ConfigFile, "--unset", "core.hooksPath").CombinedOutput(); err != nil {
		return fmt.Errorf("githook: unsetting core.hooksPath: %v: %s", err, out)
	}
	return nil
}

func Status(homeDir string) (Info, error) {
	cfgDir, hooksDir, stateFile := paths(homeDir)
	info := Info{HooksDir: hooksDir}
	st, err := readState(stateFile)
	if err != nil {
		return info, err
	}
	info.Installed = st != nil
	if st != nil {
		info.Binary = st.Binary
	}
	for _, name := range allHookNames() {
		p := filepath.Join(hooksDir, name)
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(scriptMarker)) {
			info.Files = append(info.Files, p)
		} else {
			info.Missing = append(info.Missing, p)
		}
	}
	info.GlobalHooksPath, err = globalHooksPath(homeDir)
	if err != nil {
		return info, err
	}
	info.Active = sameDir(info.GlobalHooksPath, hooksDir, homeDir)
	cfg, _ := LoadConfig(cfgDir)
	info.Mode = cfg.Mode
	info.TTYHeuristic = cfg.TTYHeuristic == nil || *cfg.TTYHeuristic
	return info, nil
}

func RepoHooksPath(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", dir, "config", "--local", "--get", "core.hooksPath")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func wrapperScript(name, bin, cfgFile string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n" + scriptMarker + "\n")
	if name == "commit-msg" || name == "prepare-commit-msg" {
		fmt.Fprintf(&b, `if [ "$RERIGHT_BYPASS" != 1 ] && ! grep -Eq '"mode"[[:space:]]*:[[:space:]]*"off"' %s 2>/dev/null; then
	if [ -x %s ]; then
		RERIGHT_GIT_PPID=$PPID %s git %s "$@" || exit $?
	else
		printf 'reright: %%s is missing or not executable, so this commit was not checked. Run reright install again, or reright uninstall.\n' %s >&2
	fi
fi
`, shellQuote(cfgFile), shellQuote(bin), shellQuote(bin), name, shellQuote(bin))
	}
	fmt.Fprintf(&b, `hooks=$(git config --local --get core.hooksPath 2>/dev/null)
case "$hooks" in
"~/"*) hooks="$HOME/${hooks#\~/}" ;;
esac
if [ -z "$hooks" ]; then
	hooks="$(git rev-parse --git-common-dir 2>/dev/null)/hooks"
fi
target="$hooks/%s"
if [ -f "$target" ] && [ -x "$target" ] && ! [ "$target" -ef "$0" ]; then
	exec "$target" "$@"
fi
exit 0
`, name)
	return b.String()
}

func manualStep(existing, homeDir, bin string) string {
	dir := expandHome(existing, homeDir)
	q := shellQuote(bin)
	var b strings.Builder
	fmt.Fprintf(&b, "core.hooksPath is already set to %s, so reright did not change it. Add these lines near the top of the hook files in that directory (create each file with a #!/bin/sh first line and chmod +x if it does not exist). Remove them again when you uninstall reright:\n", existing)
	for _, name := range hookNames {
		fmt.Fprintf(&b, "  %s/%s: if [ -x %s ]; then RERIGHT_GIT_PPID=$PPID %s git %s \"$@\" || exit $?; fi\n", dir, name, q, q, name)
	}
	return b.String()
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

func globalConfigFile(homeDir string) string {
	if p := os.Getenv("GIT_CONFIG_GLOBAL"); p != "" {
		return p
	}
	legacy := filepath.Join(homeDir, ".gitconfig")
	xdg := filepath.Join(homeDir, ".config", "git", "config")
	if _, err := os.Stat(legacy); err != nil {
		if _, err := os.Stat(xdg); err == nil {
			return xdg
		}
	}
	return legacy
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

func writeState(path string, st *state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
